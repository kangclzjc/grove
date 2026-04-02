// /*
// Copyright 2025 The Grove Authors.
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

package manager

import (
	"fmt"

	configv1alpha1 "github.com/ai-dynamo/grove/operator/api/config/v1alpha1"
	"github.com/ai-dynamo/grove/operator/internal/scheduler"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kai"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/kube"
	"github.com/ai-dynamo/grove/operator/internal/scheduler/volcano"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// registry holds initialized scheduler backends keyed by name.
type registry struct {
	backends       map[string]scheduler.Backend
	defaultBackend scheduler.Backend
}

// newBackendForProfile creates and initializes a Backend for the given profile.
// Add new scheduler backends by extending this switch (no global registry).
func newBackendForProfile(cl client.Client, scheme *runtime.Scheme, rec record.EventRecorder, p configv1alpha1.SchedulerProfile) (scheduler.Backend, error) {
	switch p.Name {
	case configv1alpha1.SchedulerNameKube:
		b := kube.New(cl, scheme, rec, p)
		if err := b.Init(); err != nil {
			return nil, err
		}
		return b, nil
	case configv1alpha1.SchedulerNameKai:
		b := kai.New(cl, scheme, rec, p)
		if err := b.Init(); err != nil {
			return nil, err
		}
		return b, nil
	case configv1alpha1.SchedulerNameVolcano:
		b := volcano.New(cl, scheme, rec, p)
		if err := b.Init(); err != nil {
			return nil, err
		}
		return b, nil
	default:
		return nil, fmt.Errorf("scheduler profile %q is not supported", p.Name)
	}
}

var (
	backends       map[string]scheduler.Backend
	defaultBackend scheduler.Backend
)

// Initialize creates and registers backend instances using package-level state.
// Prefer NewRegistry for dependency-injected usage.
func Initialize(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, cfg configv1alpha1.SchedulerConfiguration) error {
	backends = make(map[string]scheduler.Backend)

	for _, p := range cfg.Profiles {
		backend, err := newBackendForProfile(client, scheme, eventRecorder, p)
		if err != nil {
			return fmt.Errorf("failed to initialize %s backend: %w", p.Name, err)
		}
		backends[backend.Name()] = backend
		if string(p.Name) == cfg.DefaultProfileName {
			defaultBackend = backend
		}

	}
	return nil
}

// Get returns the backend for the given name. Empty string is valid and returns the default backend (e.g. when Pod.Spec.SchedulerName is unset).
// default-scheduler is always available; other backends return nil if not enabled via a profile.
func Get(name string) scheduler.Backend {
	if name == "" {
		return defaultBackend
	}
	return backends[name]
}

// GetDefault returns the backend designated as default in OperatorConfiguration (scheduler.defaultProfileName).
func GetDefault() scheduler.Backend {
	return defaultBackend
}

// NewRegistry creates a Registry with backend instances for each configured profile.
func NewRegistry(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, cfg configv1alpha1.SchedulerConfiguration) (scheduler.Registry, error) {
	r := &registry{
		backends: make(map[string]scheduler.Backend),
	}
	for _, p := range cfg.Profiles {
		backend, err := newBackendForProfile(client, scheme, eventRecorder, p)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize %s backend: %w", p.Name, err)
		}
		r.backends[backend.Name()] = backend
		if string(p.Name) == cfg.DefaultProfileName {
			r.defaultBackend = backend
		}
	}
	return r, nil
}

func (r *registry) Get(name string) scheduler.Backend {
	if name == "" {
		return r.defaultBackend
	}
	return r.backends[name]
}

func (r *registry) GetDefault() scheduler.Backend {
	return r.defaultBackend
}
