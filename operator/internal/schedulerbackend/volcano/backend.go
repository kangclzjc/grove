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
	"fmt"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	grovecorev1alpha1 "github.com/ai-dynamo/grove/operator/api/core/v1alpha1"
	apicommon "github.com/ai-dynamo/grove/operator/api/common"

	groveschedulerv1alpha1 "github.com/ai-dynamo/grove/scheduler/api/core/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

const (
	// schedulerName is the value set on Pod.Spec.SchedulerName for Volcano.
	schedulerName = "volcano"
)

// Backend implements the scheduler backend interface (SchedBackend in schedulerbackend package) for Volcano scheduler.
// It converts Grove PodGang resources into Volcano PodGroup CRs and annotates Pods for Volcano gang scheduling.
type Backend struct {
	client        client.Client
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
	profile       configv1alpha1.SchedulerProfile
	queue         string
	keyToTier     map[string]int
}

// New creates a new Volcano backend instance.
// It parses VolcanoSchedulerConfiguration from profile.Config if provided.
func New(cl client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, profile configv1alpha1.SchedulerProfile) *Backend {
	b := &Backend{
		client:        cl,
		scheme:        scheme,
		eventRecorder: eventRecorder,
		profile:       profile,
		queue:         volcanoschedulingv1beta1.DefaultQueue,
	}
	if profile.Config != nil && len(profile.Config.Raw) > 0 {
		var cfg configv1alpha1.VolcanoSchedulerConfiguration
		if err := json.Unmarshal(profile.Config.Raw, &cfg); err == nil {
			if cfg.Queue != "" {
				b.queue = cfg.Queue
			}
			if len(cfg.TopologyKeyToTier) > 0 {
				b.keyToTier = cfg.TopologyKeyToTier
			}
		}
	}
	return b
}

// Name returns the pod-facing scheduler name ("volcano").
func (b *Backend) Name() string {
	return schedulerName
}

// Init initializes the Volcano backend. No one-time cluster setup is required.
func (b *Backend) Init() error {
	return nil
}

// SyncPodGang creates or updates a Volcano PodGroup for the given PodGang.
//
// The Volcano PodGroup is configured with:
//   - MinMember: sum of all PodGroup.MinReplicas (overall gang constraint)
//   - NetworkTopology: from PodGang top-level TopologyConstraint (gang-wide topology)
//   - SubGroupPolicy: one entry per Grove PodGroup, each enforcing per-PodClique
//     gang scheduling (SubGroupSize = MinReplicas) and topology (per-PodGroup TopologyConstraint)
func (b *Backend) SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	volcanoGroup := &volcanoschedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podGang.Name,
			Namespace: podGang.Namespace,
		},
	}
	_, err := controllerutil.CreateOrPatch(ctx, b.client, volcanoGroup, func() error {
		if err := controllerutil.SetControllerReference(podGang, volcanoGroup, b.scheme); err != nil {
			return fmt.Errorf("failed to set controller reference on Volcano PodGroup: %w", err)
		}
		var minMember int32
		for _, pg := range podGang.Spec.PodGroups {
			minMember += pg.MinReplicas
		}
		volcanoGroup.Spec.MinMember = minMember
		volcanoGroup.Spec.Queue = b.queue
		volcanoGroup.Spec.PriorityClassName = podGang.Spec.PriorityClassName
		volcanoGroup.Spec.NetworkTopology = buildNetworkTopology(podGang.Spec.TopologyConstraint, b.keyToTier)
		volcanoGroup.Spec.SubGroupPolicy = buildSubGroupPolicy(podGang.Spec.PodGroups, b.keyToTier)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to sync Volcano PodGroup for PodGang %s/%s: %w", podGang.Namespace, podGang.Name, err)
	}
	return nil
}

// OnPodGangDelete removes the Volcano PodGroup owned by the given PodGang.
func (b *Backend) OnPodGangDelete(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error {
	volcanoGroup := &volcanoschedulingv1beta1.PodGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podGang.Name,
			Namespace: podGang.Namespace,
		},
	}
	if err := b.client.Delete(ctx, volcanoGroup); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to delete Volcano PodGroup for PodGang %s/%s: %w", podGang.Namespace, podGang.Name, err)
	}
	return nil
}

// PreparePod adds Volcano-specific configuration to the Pod prior to creation.
// Sets Pod.Spec.SchedulerName and the two group-name annotations required by Volcano:
//   - scheduling.volcano.sh/group-name: used by the Volcano scheduler to find the PodGroup
//   - scheduling.k8s.io/group-name: pre-set to prevent Volcano's pg_controller from creating
//     a per-pod fallback PodGroup (pg_controller skips pods that already have this annotation)
func (b *Backend) PreparePod(pod *corev1.Pod) {
	pod.Spec.SchedulerName = schedulerName
	podGangName := pod.Labels[apicommon.LabelPodGang]
	if podGangName == "" {
		return
	}
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[volcanoschedulingv1beta1.VolcanoGroupNameAnnotationKey] = podGangName
	pod.Annotations[volcanoschedulingv1beta1.KubeGroupNameAnnotationKey] = podGangName
}

// ValidatePodCliqueSet runs Volcano-specific validations on the PodCliqueSet.
func (b *Backend) ValidatePodCliqueSet(_ context.Context, _ *grovecorev1alpha1.PodCliqueSet) error {
	return nil
}

// buildSubGroupPolicy converts Grove PodGroups into Volcano SubGroupPolicy entries.
// Each Grove PodGroup (backed by a PodClique) becomes one SubGroupPolicySpec:
//   - LabelSelector selects pods via the grove.io/podclique label (= PodGroup.Name = pclqFQN)
//   - SubGroupSize = PodGroup.MinReplicas (minimum pods that must be co-scheduled)
//   - MinSubGroups = 1 (at least one group of MinReplicas must be satisfiable)
//   - NetworkTopology from PodGroup.TopologyConstraint (per-PodClique topology)
//
// A PodGroup is skipped if MinReplicas == 0, has no PodReferences, or has no
// NetworkTopology (subGroupPolicy is only needed when topology constraints are present;
// basic gang scheduling is handled by minMember alone).
func buildSubGroupPolicy(podGroups []groveschedulerv1alpha1.PodGroup, keyToTier map[string]int) []volcanoschedulingv1beta1.SubGroupPolicySpec {
	if len(podGroups) == 0 {
		return nil
	}
	policies := make([]volcanoschedulingv1beta1.SubGroupPolicySpec, 0, len(podGroups))
	for _, pg := range podGroups {
		if pg.MinReplicas <= 0 || len(pg.PodReferences) == 0 {
			continue
		}
		nt := buildNetworkTopology(pg.TopologyConstraint, keyToTier)
		if nt == nil {
			// Without topology constraints, subGroupPolicy adds no value over minMember
			// and can cause scheduling failures in some Volcano configurations.
			continue
		}
		policies = append(policies, volcanoschedulingv1beta1.SubGroupPolicySpec{
			Name: pg.Name,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					apicommon.LabelPodClique: pg.Name,
				},
			},
			SubGroupSize:    ptr.To(pg.MinReplicas),
			MinSubGroups:    ptr.To(int32(1)),
			NetworkTopology: nt,
		})
	}
	if len(policies) == 0 {
		return nil
	}
	return policies
}

// buildNetworkTopology converts a TopologyConstraint into a Volcano NetworkTopologySpec
// using the provided key-to-tier mapping. Returns nil if no topology constraint is set,
// keyToTier is empty, or the relevant key is not found in the mapping.
func buildNetworkTopology(tc *groveschedulerv1alpha1.TopologyConstraint, keyToTier map[string]int) *volcanoschedulingv1beta1.NetworkTopologySpec {
	if len(keyToTier) == 0 || tc == nil || tc.PackConstraint == nil {
		return nil
	}
	pc := tc.PackConstraint
	if pc.Required != nil {
		if tier, ok := keyToTier[*pc.Required]; ok {
			return &volcanoschedulingv1beta1.NetworkTopologySpec{
				Mode:               volcanoschedulingv1beta1.HardNetworkTopologyMode,
				HighestTierAllowed: &tier,
			}
		}
	}
	if pc.Preferred != nil {
		if tier, ok := keyToTier[*pc.Preferred]; ok {
			return &volcanoschedulingv1beta1.NetworkTopologySpec{
				Mode:               volcanoschedulingv1beta1.SoftNetworkTopologyMode,
				HighestTierAllowed: &tier,
			}
		}
	}
	return nil
}
