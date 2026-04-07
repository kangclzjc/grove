// /*
// Copyright 2026 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

package volcano

import (
	"context"
	"encoding/json"
	"testing"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	apicommon "github.com/ai-dynamo/grove/operator/api/common"
	testutils "github.com/ai-dynamo/grove/operator/test/utils"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

func newBackend(t *testing.T, cfgJSON string, existingObjects ...client.Object) (*Backend, client.Client) {
	t.Helper()
	cl := testutils.CreateDefaultFakeClient(existingObjects)
	recorder := record.NewFakeRecorder(10)
	profile := configv1alpha1.SchedulerProfile{Name: configv1alpha1.SchedulerNameVolcano}
	if cfgJSON != "" {
		profile.Config = &runtime.RawExtension{Raw: []byte(cfgJSON)}
	}
	return New(cl, cl.Scheme(), recorder, profile), cl
}

// podRef is a shorthand for building a NamespacedName pod reference.
func podRef(name string) groveschedulerv1alpha1.NamespacedName {
	return groveschedulerv1alpha1.NamespacedName{Namespace: "default", Name: name}
}

// ----------------------------- Name / PreparePod -----------------------------

func TestBackend_Name(t *testing.T) {
	b, _ := newBackend(t, "")
	assert.Equal(t, "volcano", b.Name())
}

func TestBackend_PreparePod(t *testing.T) {
	b, _ := newBackend(t, "")
	pod := testutils.NewPodBuilder("test-pod", "default").
		WithLabels(map[string]string{apicommon.LabelPodGang: "my-podgang"}).
		Build()

	b.PreparePod(pod)

	assert.Equal(t, "volcano", pod.Spec.SchedulerName)
	assert.Equal(t, "my-podgang", pod.Annotations[volcanoschedulingv1beta1.VolcanoGroupNameAnnotationKey])
	assert.Equal(t, "my-podgang", pod.Annotations[volcanoschedulingv1beta1.KubeGroupNameAnnotationKey])
}

func TestBackend_PreparePod_NoLabel(t *testing.T) {
	b, _ := newBackend(t, "")
	pod := testutils.NewPodBuilder("test-pod", "default").Build()

	b.PreparePod(pod)

	assert.Equal(t, "volcano", pod.Spec.SchedulerName)
	assert.Empty(t, pod.Annotations[volcanoschedulingv1beta1.VolcanoGroupNameAnnotationKey])
	assert.Empty(t, pod.Annotations[volcanoschedulingv1beta1.KubeGroupNameAnnotationKey])
}

// ----------------------------- SyncPodGang basics -----------------------------

func TestBackend_SyncPodGang_Create(t *testing.T) {
	b, cl := newBackend(t, "")
	// WithPodGroup does not set PodReferences, so SubGroupPolicy is nil (no pods yet)
	podGang := testutils.NewPodGangBuilder("my-gang", "default").
		WithPodGroup("workers", 3).
		WithPodGroup("ps", 2).
		Build()
	podGang.Spec.PriorityClassName = "high-priority"

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &volcanoschedulingv1beta1.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-gang", Namespace: "default"}, got))
	assert.Equal(t, int32(5), got.Spec.MinMember)
	assert.Equal(t, volcanoschedulingv1beta1.DefaultQueue, got.Spec.Queue)
	assert.Equal(t, "high-priority", got.Spec.PriorityClassName)
	assert.Nil(t, got.Spec.NetworkTopology)
	assert.Nil(t, got.Spec.SubGroupPolicy) // no PodReferences → no SubGroupPolicy
	require.Len(t, got.OwnerReferences, 1)
	assert.Equal(t, "my-gang", got.OwnerReferences[0].Name)
}

func TestBackend_SyncPodGang_Update(t *testing.T) {
	existing := &volcanoschedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "my-gang", Namespace: "default"},
		Spec:       volcanoschedulingv1beta1.PodGroupSpec{MinMember: 1},
	}
	b, cl := newBackend(t, "", existing)
	podGang := testutils.NewPodGangBuilder("my-gang", "default").
		WithPodGroup("workers", 4).
		Build()

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &volcanoschedulingv1beta1.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-gang", Namespace: "default"}, got))
	assert.Equal(t, int32(4), got.Spec.MinMember)
}

func TestBackend_SyncPodGang_CustomQueue(t *testing.T) {
	cfgJSON := `{"queue": "my-queue"}`
	b, cl := newBackend(t, cfgJSON)
	podGang := testutils.NewPodGangBuilder("my-gang", "default").
		WithPodGroup("workers", 2).
		Build()

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &volcanoschedulingv1beta1.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-gang", Namespace: "default"}, got))
	assert.Equal(t, "my-queue", got.Spec.Queue)
}

// ----------------------------- SubGroupPolicy --------------------------------

// TestBackend_SyncPodGang_SubGroupPolicy verifies that each Grove PodGroup with
// PodReferences produces one SubGroupPolicySpec with the correct LabelSelector,
// SubGroupSize, and MinSubGroups.
func TestBackend_SyncPodGang_SubGroupPolicy(t *testing.T) {
	b, cl := newBackend(t, "")
	podGang := testutils.NewPodGangBuilder("my-gang", "default").Build()
	podGang.Spec.PodGroups = []groveschedulerv1alpha1.PodGroup{
		{
			Name:         "workers",
			MinReplicas:  3,
			PodReferences: []groveschedulerv1alpha1.NamespacedName{podRef("w-0"), podRef("w-1"), podRef("w-2")},
		},
		{
			Name:         "ps",
			MinReplicas:  2,
			PodReferences: []groveschedulerv1alpha1.NamespacedName{podRef("ps-0"), podRef("ps-1")},
		},
	}

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &volcanoschedulingv1beta1.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-gang", Namespace: "default"}, got))

	assert.Equal(t, int32(5), got.Spec.MinMember)
	require.Len(t, got.Spec.SubGroupPolicy, 2)

	workers := got.Spec.SubGroupPolicy[0]
	assert.Equal(t, "workers", workers.Name)
	assert.Equal(t, int32(3), *workers.SubGroupSize)
	assert.Equal(t, int32(1), *workers.MinSubGroups)
	require.NotNil(t, workers.LabelSelector)
	assert.Equal(t, "workers", workers.LabelSelector.MatchLabels[apicommon.LabelPodClique])
	assert.Equal(t, []string{apicommon.LabelPodClique}, workers.MatchLabelKeys)
	assert.Nil(t, workers.NetworkTopology)

	ps := got.Spec.SubGroupPolicy[1]
	assert.Equal(t, "ps", ps.Name)
	assert.Equal(t, int32(2), *ps.SubGroupSize)
	assert.Equal(t, int32(1), *ps.MinSubGroups)
	assert.Equal(t, "ps", ps.LabelSelector.MatchLabels[apicommon.LabelPodClique])
	assert.Equal(t, []string{apicommon.LabelPodClique}, ps.MatchLabelKeys)
}

// TestBackend_SyncPodGang_SubGroupPolicy_SkipsZeroMinReplicas verifies that PodGroups
// with MinReplicas=0 or no PodReferences are excluded from SubGroupPolicy.
func TestBackend_SyncPodGang_SubGroupPolicy_SkipsZeroMinReplicas(t *testing.T) {
	b, cl := newBackend(t, "")
	podGang := testutils.NewPodGangBuilder("my-gang", "default").Build()
	podGang.Spec.PodGroups = []groveschedulerv1alpha1.PodGroup{
		{Name: "no-refs", MinReplicas: 3},                                                        // no PodReferences → skip
		{Name: "zero-min", MinReplicas: 0, PodReferences: []groveschedulerv1alpha1.NamespacedName{podRef("p-0")}}, // MinReplicas=0 → skip
		{Name: "valid", MinReplicas: 2, PodReferences: []groveschedulerv1alpha1.NamespacedName{podRef("v-0"), podRef("v-1")}},
	}

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &volcanoschedulingv1beta1.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-gang", Namespace: "default"}, got))
	require.Len(t, got.Spec.SubGroupPolicy, 1)
	assert.Equal(t, "valid", got.Spec.SubGroupPolicy[0].Name)
	assert.Equal(t, []string{apicommon.LabelPodClique}, got.Spec.SubGroupPolicy[0].MatchLabelKeys)
}

// TestBackend_SyncPodGang_SubGroupPolicy_WithPerGroupTopology verifies that per-PodGroup
// TopologyConstraint is mapped to SubGroupPolicySpec.NetworkTopology independently
// from the top-level PodGang topology.
func TestBackend_SyncPodGang_SubGroupPolicy_WithPerGroupTopology(t *testing.T) {
	keyToTier := map[string]int{
		"kubernetes.io/hostname":      1,
		"topology.kubernetes.io/rack": 2,
	}
	cfgBytes, _ := json.Marshal(configv1alpha1.VolcanoSchedulerConfiguration{TopologyKeyToTier: keyToTier})
	b, cl := newBackend(t, string(cfgBytes))

	rackKey := "topology.kubernetes.io/rack"
	hostKey := "kubernetes.io/hostname"
	podGang := testutils.NewPodGangBuilder("my-gang", "default").Build()
	podGang.Spec.PodGroups = []groveschedulerv1alpha1.PodGroup{
		{
			Name:        "workers",
			MinReplicas: 4,
			PodReferences: []groveschedulerv1alpha1.NamespacedName{podRef("w-0"), podRef("w-1"), podRef("w-2"), podRef("w-3")},
			TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{
					Required: &hostKey, // workers must be on same host
				},
			},
		},
		{
			Name:        "ps",
			MinReplicas: 2,
			PodReferences: []groveschedulerv1alpha1.NamespacedName{podRef("ps-0"), podRef("ps-1")},
			TopologyConstraint: &groveschedulerv1alpha1.TopologyConstraint{
				PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{
					Preferred: &rackKey, // ps prefers same rack
				},
			},
		},
	}
	// gang-wide: must be within same rack
	podGang.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rackKey},
	}

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &volcanoschedulingv1beta1.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-gang", Namespace: "default"}, got))

	// top-level gang topology: hard, rack=tier2
	require.NotNil(t, got.Spec.NetworkTopology)
	assert.Equal(t, volcanoschedulingv1beta1.HardNetworkTopologyMode, got.Spec.NetworkTopology.Mode)
	assert.Equal(t, 2, *got.Spec.NetworkTopology.HighestTierAllowed)

	require.Len(t, got.Spec.SubGroupPolicy, 2)

	// workers: hard, host=tier1
	wnt := got.Spec.SubGroupPolicy[0].NetworkTopology
	require.NotNil(t, wnt)
	assert.Equal(t, volcanoschedulingv1beta1.HardNetworkTopologyMode, wnt.Mode)
	assert.Equal(t, 1, *wnt.HighestTierAllowed)

	// ps: soft, rack=tier2
	psnt := got.Spec.SubGroupPolicy[1].NetworkTopology
	require.NotNil(t, psnt)
	assert.Equal(t, volcanoschedulingv1beta1.SoftNetworkTopologyMode, psnt.Mode)
	assert.Equal(t, 2, *psnt.HighestTierAllowed)

	assert.Equal(t, []string{apicommon.LabelPodClique}, got.Spec.SubGroupPolicy[0].MatchLabelKeys)
	assert.Equal(t, []string{apicommon.LabelPodClique}, got.Spec.SubGroupPolicy[1].MatchLabelKeys)
}

// ----------------------------- Top-level topology ----------------------------

func TestBackend_SyncPodGang_WithTopology_Required(t *testing.T) {
	keyToTier := map[string]int{
		"kubernetes.io/hostname":      1,
		"topology.kubernetes.io/rack": 2,
	}
	cfgBytes, _ := json.Marshal(configv1alpha1.VolcanoSchedulerConfiguration{TopologyKeyToTier: keyToTier})
	b, cl := newBackend(t, string(cfgBytes))

	rackKey := "topology.kubernetes.io/rack"
	podGang := testutils.NewPodGangBuilder("my-gang", "default").
		WithPodGroup("workers", 2).
		Build()
	podGang.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rackKey},
	}

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &volcanoschedulingv1beta1.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-gang", Namespace: "default"}, got))
	require.NotNil(t, got.Spec.NetworkTopology)
	assert.Equal(t, volcanoschedulingv1beta1.HardNetworkTopologyMode, got.Spec.NetworkTopology.Mode)
	assert.Equal(t, 2, *got.Spec.NetworkTopology.HighestTierAllowed)
}

func TestBackend_SyncPodGang_WithTopology_PreferredOnly(t *testing.T) {
	keyToTier := map[string]int{
		"kubernetes.io/hostname":      1,
		"topology.kubernetes.io/rack": 2,
	}
	cfgBytes, _ := json.Marshal(configv1alpha1.VolcanoSchedulerConfiguration{TopologyKeyToTier: keyToTier})
	b, cl := newBackend(t, string(cfgBytes))

	hostKey := "kubernetes.io/hostname"
	podGang := testutils.NewPodGangBuilder("my-gang", "default").
		WithPodGroup("workers", 2).
		Build()
	podGang.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Preferred: &hostKey},
	}

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &volcanoschedulingv1beta1.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-gang", Namespace: "default"}, got))
	require.NotNil(t, got.Spec.NetworkTopology)
	assert.Equal(t, volcanoschedulingv1beta1.SoftNetworkTopologyMode, got.Spec.NetworkTopology.Mode)
	assert.Equal(t, 1, *got.Spec.NetworkTopology.HighestTierAllowed)
}

func TestBackend_SyncPodGang_NoTopologyMapping(t *testing.T) {
	b, cl := newBackend(t, "")
	rackKey := "topology.kubernetes.io/rack"
	podGang := testutils.NewPodGangBuilder("my-gang", "default").
		WithPodGroup("workers", 2).
		Build()
	podGang.Spec.TopologyConstraint = &groveschedulerv1alpha1.TopologyConstraint{
		PackConstraint: &groveschedulerv1alpha1.TopologyPackConstraint{Required: &rackKey},
	}

	require.NoError(t, b.SyncPodGang(context.Background(), podGang))

	got := &volcanoschedulingv1beta1.PodGroup{}
	require.NoError(t, cl.Get(context.Background(), client.ObjectKey{Name: "my-gang", Namespace: "default"}, got))
	assert.Nil(t, got.Spec.NetworkTopology)
}

// ----------------------------- OnPodGangDelete --------------------------------

func TestBackend_OnPodGangDelete(t *testing.T) {
	existing := &volcanoschedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "my-gang", Namespace: "default"},
	}
	b, cl := newBackend(t, "", existing)
	podGang := testutils.NewPodGangBuilder("my-gang", "default").Build()

	require.NoError(t, b.OnPodGangDelete(context.Background(), podGang))

	got := &volcanoschedulingv1beta1.PodGroup{}
	err := cl.Get(context.Background(), client.ObjectKey{Name: "my-gang", Namespace: "default"}, got)
	assert.True(t, client.IgnoreNotFound(err) == nil && err != nil, "expected NotFound error")
}

func TestBackend_OnPodGangDelete_AlreadyGone(t *testing.T) {
	b, _ := newBackend(t, "")
	podGang := testutils.NewPodGangBuilder("my-gang", "default").Build()
	require.NoError(t, b.OnPodGangDelete(context.Background(), podGang))
}
