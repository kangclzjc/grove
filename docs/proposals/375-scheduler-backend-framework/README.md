# GREP-375: Scheduler Backend Framework

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Third-Party Scheduler Integration](#story-1-third-party-scheduler-integration)
    - [Story 2: Multi-Cluster Deployment with Different Schedulers](#story-2-multi-cluster-deployment-with-different-schedulers)
    - [Story 3: Scheduler Migration Path](#story-3-scheduler-migration-path)
  - [Limitations/Risks &amp; Mitigations](#limitationsrisks--mitigations)
    - [Backend API Stability](#backend-api-stability)
    - [Scheduler Capability Mismatch](#scheduler-capability-mismatch)
  - [Requirements](#requirements)
- [Design Details](#design-details)
  - [Architecture Overview](#architecture-overview)
    - [Layer 1: Configuration Layer](#layer-1-configuration-layer)
    - [Layer 2: Grove Control Plane](#layer-2-grove-control-plane)
    - [Layer 3: Scheduler Backend Layer](#layer-3-scheduler-backend-layer)
    - [Layer 4: Scheduler Layer](#layer-4-scheduler-layer)
  - [Scheduler Backend Interface](#scheduler-backend-interface)
  - [Backend Manager](#backend-manager)
  - [OperatorConfiguration Extension](#operatorconfiguration-extension)
  - [PodGang Lifecycle Changes](#podgang-lifecycle-changes)
    - [Previous Flow (Before Framework):](#previous-flow-before-framework)
    - [Revised PodGang Creation Flow](#revised-podgang-creation-flow)
      - [PodGang API enhancements](#podgang-api-enhancements)
      - [Creation Flow](#creation-flow)
  - [Backend Controller](#backend-controller)
  - [Monitoring](#monitoring)
    - [Status Conditions](#status-conditions)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [E2E Tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
  - [Alternative 1: Direct Scheduler Integration](#alternative-1-direct-scheduler-integration)
<!-- /toc -->

<!--
Include a table of contents as it helps to navigate easily in the document.

Ensure the TOC is wrapped with
   <code>&lt;!-- toc --&gt;&lt;!-- /toc --&gt;</code>
tags, and then generate by invoking the make target `update-toc`.

-->

## Summary

Grove's scheduler API is currently integrated with the KAI scheduler as the only advanced AI scheduler that supports hierarchical gang-scheduling and topology-aware scheduling. While any custom scheduler can integrate with Grove's PodGang scheduling API, the onus remains on the external scheduler to do the heavy lifting of the integration effort. It also becomes difficult to add any scheduler specific logic from the operator. This proposal introduces a Scheduler Backend Framework that standardizes and simplifies the process of adding new scheduler support directly into Grove, making it easier to handle scheduler specific logic in its own backend.

## Motivation
Grove's unified API for AI workloads *training, inference, agents, etc.* allows the Grove operator to realize a workload's scheduling constraints through the PodGang API. Since the PodGang API is specific to Grove, it needs to be translated to constraints that are specific to the backend scheduler. While it is possible for each scheduler to support Grove independently by implementing its own translation layer, it will likely introduce maintenance issues over time as Grove keep evolving its scheduler constraint generation capabilities.

Introducing a Scheduler Backend Framework addresses these challenges by providing:

* **Standardized Integration**: A well-defined interface and abstraction layer that makes adding a new scheduler backend easier and more standardized.
* **Reduced Invasiveness**: Minimal modifications to existing scheduler implementations, allowing schedulers to integrate with Grove through a plugin-like architecture.
* **Improved Scheduling Flow**: A more efficient and streamlined scheduling workflow that clearly separates concerns between Grove's workload management and scheduler-specific placement decisions.
* **Future-Proof Architecture**: A foundation that can adapt to emerging scheduling requirements and new scheduler implementations in the Kubernetes ecosystem.

In summary, refining Grove and introducing a Scheduler Backend Framework is both an urgent and inevitable improvement that will ensure Grove's long-term viability and extensibility in the evolving Kubernetes scheduling landscape.

### Goals

* **Define Scheduler Backend Interface**: Introduce a well-defined Go interface that abstracts scheduler-specific operations, enabling Grove to work with multiple scheduler backends through a standardized contract.
* **Refine PodGang Lifecycle Management**: Optimize the PodGang creation and update workflow to integrate with the Scheduler Backend Framework, allowing scheduler backends to customize pod specifications during reconciliation.
* **Enable Custom Resource Management**: Provide interfaces that allow Scheduler Backends to create, update, and delete their own custom resources in response to PodGang lifecycle events (create, update, delete, status changes).
* **Simplify Configuration and Selection**: Allow admins to configure scheduler backends during Grove installation via OperatorConfiguration. By default, the configured default backend is used; otherwise the workload can specify a scheduler backend (e.g. via `schedulerName` in the pod spec).
* **Multiple Backends and Dynamic Selection**: Provide clear mechanisms for backend registration and initialization, built-in support for multiple scheduler backends (e.g. Kubernetes kube-scheduler and kai-scheduler), and a clear path for third-party schedulers. At runtime, Grove selects the backend per workload from OperatorConfiguration and the workload's `schedulerName` in the pod spec.

### Non-Goals

* **Cluster topology and multi-cluster scheduling**: This GREP does not define or support cluster topology (e.g. multi-cluster topology) in the scheduler backend interface. Addressing those concerns may require additional extension points or interface methods in a future proposal and is out of scope here.
* **PodReferences definition and semantics**: Redefining or formally specifying the existing `PodReferences` field (e.g. structure, ownership, and how it is populated) is out of scope. This proposal relies on current behavior and may be updated in a follow-up if that is clarified elsewhere.

## Proposal

The Scheduler Backend Framework introduces a plugin-like architecture that decouples Grove's workload management from scheduler-specific implementations. The framework consists of four main components:

1. **Interface**: A Go interface defining the contract between Grove and scheduler backends.
2. **Registry**: Mechanism for scheduler backends to register themselves during initialization.
3. **Lifecycle Hooks**: Well-defined points in the PodGang lifecycle where backend schedulers can inject custom logic.
4. **Operator Configuration**: OperatorConfiguration allows enabling multiple scheduler backends simultaneously. 

The framework follows an architecture where:
- The operator configuration determines which schedulers are active at runtime and which scheduler is marked as default. Grove support multiple active schedulers.
- Grove manages the high-level workflow and `PodGang` lifecycle
- Scheduler backend(s) implement the interface to provide scheduler-specific behavior

### User Stories

#### Story 1: Third-Party Scheduler Integration

As a third-party scheduler developer, I want to integrate my custom gang scheduler with Grove without modifying Grove's core codebase. The Scheduler Backend Framework should provide clear interfaces and documentation that allow me to implement a backend plugin for my scheduler, register it with Grove, and have Grove automatically use my scheduler for workload placement decisions.

#### Story 2: Multi-Cluster Deployment with Different Schedulers

As a platform engineer managing multiple Kubernetes clusters, I want to deploy Grove across clusters that use different schedulers (e.g., kai-scheduler in production clusters, kube-scheduler in development clusters). The framework should allow me to configure the appropriate scheduler backend for each cluster through OperatorConfiguration without changing workload specifications or Grove's deployment manifests.

#### Story 3: Scheduler Migration Path

As a cluster administrator, I want to migrate from one scheduler to another (e.g., from a custom scheduler to kai-scheduler or vice versa) without significant disruption. The Scheduler Backend Framework should provide a clear migration path where I can update the OperatorConfiguration and restart Grove to use a different backend.

### Limitations/Risks & Mitigations

#### Backend API Stability

**Risk**: Changes to the backend interface in future Grove versions could break existing backend implementations.

**Mitigation**:
- Follow semantic versioning for backend interfaces
- Maintain backward compatibility within major versions
- Provide deprecation notices and migration guides for interface changes
- Consider versioned interfaces if breaking changes are necessary

#### Scheduler Capability Mismatch

**Limitation**:
Different schedulers have varying capabilities, which may not align with the uniform capability set exposed through the PodGang API.

**Mitigation**:
- **For Missing Capabilities**: The framework provides a clear contract so that each scheduler backend can decide how to handle requested features it does not support:
  - **Fail submit**: The scheduler backend can reject PodCliqueSet when the missing capability is required for correctness or safety.
  - **Pass through**: The backend allows the request and the workload proceeds; the backend should ensure PodCliqueSet status is updated with conditions indicating which features are not supported (e.g. "GangScheduling requested but not supported by this backend—workload scheduled without gang guarantees"). This gives users explicit feedback while allowing best-effort scheduling.

  The choice between fail vs pass-through is entirely up to each backend implementation, so that different schedulers can adopt the policy that fits their semantics
- **For Additional Capabilities**: Document and track scheduler-specific capabilities during the integration process. If a scheduler provides valuable additional features that require configuration, evaluate adding new fields to the PodCliqueSet and PodGang APIs. These API extensions can be implemented incrementally in phases as new schedulers are integrated.

### Requirements

* Support multiple active scheduler backends via OperatorConfiguration.
* **kube-scheduler backend is always enabled and active**.
* Have a provision in OperatorConfiguration to set the default backend. Default this field to `kube-scheduler` if not set (since it is always active).
* When the scheduler-related configuration in OperatorConfiguration is empty (i.e. no additional scheduler backends are configured), only admit PodCliqueSet (PCS) resources whose pod spec has `schedulerName` empty or set to `"default-scheduler"` (kube-scheduler remains the only active backend in that case).
* Provide a config in OperatorConfiguration for each scheduler profile to decide its internal behavior. For example, if it is the kube-scheduler and GangScheduling has been disabled then do not create Workload Objects. 

## Design Details

### Architecture Overview

The Scheduler Backend Framework introduces a clean separation between Grove's control plane logic and scheduler-specific implementations. The architecture is organized into four distinct layers:

<img src="assets/scheduler-backend-framework.excalidraw.png" alt="scheduler-backend-framework-architecture" style="zoom:50%;" />

#### Layer 1: Configuration Layer
User-facing configuration that defines scheduler profiles (which backends are active and which is the default):
- **OperatorConfiguration**: Add additional scheduler-related configuration fields
- **Helm Values**: Deployment-time configuration (`config.scheduler`)

#### Layer 2: Grove Control Plane
Core controllers and webhooks managing Grove workload lifecycle:
- **PodCliqueSet Controller**: Creates PodGang resources, fills PodReferences
- **PodCliqueSet Validation Webhook**: Admits create/update of PodCliqueSet by calling the resolved backend's `ValidatePodCliqueSet()`
- **Backend Controller**: Watches PodGang, calls `SchedulerBackend.SyncPodGang()` to creates scheduler specific CRs if needed.
- **PodClique Controller**: Creates Pods, calls `SchedulerBackend.PreparePod()` to inject scheduler specific configuration into the PodSpec.
- **Resources**: PodGang (with Initialized condition) and Pod (with schedulerName/gates)

#### Layer 3: Scheduler Backend Layer
Abstraction layer bridging Grove and specific schedulers:
- **KAI backend**: Implementation for the kai-scheduler (maps PodGang resources to PodGroup custom resource(s))
- **Kube backend**: Minimal implementation for the kube-scheduler (no custom CRs)

#### Layer 4: Scheduler Layer
Kubernetes schedulers that actually place pods. The backend schedulers in this layer (e.g. kai-scheduler, kube-scheduler) are responsible for providing supporting features such as gang scheduling, topology-aware packing, gang preemption, and the like; the exact feature set depends on each scheduler.

**Key Data Flow:**

The framework orchestrates workload scheduling through a coordinated flow between layers:
1. **Backend Initialization**: Operator startup initializes the configured scheduler backend via Backend Manager
2. **PCS Admission**: On PodCliqueSet create or update, the validation webhook resolves the backend and calls `ValidatePodCliqueSet()`; the request is admitted or rejected accordingly
3. **PodGang Creation**: PodCliqueSet Controller creates PodGang resources with `Initialized=False` condition, triggering Backend Controller to create scheduler-specific resources via `SyncPodGang()`
4. **Pod Configuration**: PodClique Controller creates Pods with scheduling gates, calling `PreparePod()` to inject scheduler-specific settings
5. **Scheduling Activation**: Once all pods exist and PodReferences are populated, the `Initialized` condition is set to `True`, gates are removed, and scheduling begins

For detailed lifecycle flow, see [PodGang Lifecycle Changes](#podgang-lifecycle-changes).

### Scheduler Backend Interface

The core of the framework is the `SchedulerBackend` interface, which defines all operations that a scheduler backend must implement. The interface is intentionally simple and focused:

```go

// SchedulerBackend defines the interface that different scheduler backends must implement.
//
// Architecture: Backend validates PodCliqueSet at admission, converts
// PodGang to scheduler-specific CR (PodGroup/Workload/etc), and prepares Pods with scheduler-specific configurations.
type SchedulerBackend interface {
	// Name is a unique name of the scheduler backend.
	// Used for logging and identification purposes.
	Name() string

	// Init provides a hook to initialize/setup one-time scheduler resources,
	// called at the startup of Grove operator.
	// Backends can perform tasks such as:
	// - Creating global custom resources required by the scheduler
	// - Validating scheduler availability and configuration
	// - Setting up any initial state
	Init() error

	// SyncPodGang synchronizes (creates/updates) scheduler specific resources for a PodGang
	// reacting to a creation or update of a PodGang resource.
	// This is called by the Backend Controller when PodGang spec changes.
	// Backends should:
	// - Create scheduler-specific custom resources (e.g., PodGroup, Workload)
	// - Update existing resources if the PodGang spec changed
	// - Use owner references to enable automatic cleanup
	SyncPodGang(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error

	// OnPodGangDelete cleans up scheduler specific resources for the given PodGang.
	// This is called when a PodGang is deleted.
	// Note: If using owner references, cleanup is automatic and this can be a no-op.
	OnPodGangDelete(ctx context.Context, podGang *groveschedulerv1alpha1.PodGang) error

	// PreparePod adds scheduler backend specific configuration to the given Pod object
	// prior to its creation. This includes setting schedulerName,
	// annotations, etc.
	// This is called during Pod creation in the PodClique controller.
	PreparePod(pod *corev1.Pod)

    // ValidatePodCliqueSet provides an ability to the scheduler backends to run additional
    // validations on the PodCliqueSet resource. For example - if a scheduler does not yet support
    // topology aware placements and if the PodCliqueSet has defined required topology pack constraints
    // then it can choose to reject the PodCliqueSet by returning an error. 
	ValidatePodCliqueSet(ctx context.Context, pcs *groveschedulerv1alpha1.PodCliqueSet) error
}
```

### Backend Manager

The manager initializes the **enabled** scheduler backends (as defined in OperatorConfiguration: the kube-scheduler backend is always enabled; other backends are enabled via profiles). The manager provides access by backend name and exposes whichever backend OperatorConfiguration designates as the default:

```go

// Initialize creates and registers backend instances: kube-scheduler is always registered;
// backends named in config.Profiles are also registered (profiles can include or omit kube-scheduler).
// Called once during operator startup before controllers start.
func Initialize(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, config SchedulerConfiguration) error

// Get returns the backend for the given name. kube-scheduler is always available; other backends return nil if not enabled via a profile.
func Get(name string) SchedulerBackend

// GetDefault returns the backend designated as default in OperatorConfiguration (the profile with default: true; if none, kube-scheduler). The manager does not define the default; it exposes the one from config.
func GetDefault() SchedulerBackend

```


### OperatorConfiguration Extension

The OperatorConfiguration is extended with a `scheduler` block: `profiles` lists scheduler profiles (each with `name`, optional `config`, and `default`). **The kube-scheduler backend is always enabled and active**, regardless of whether it appears in `profiles`. kube-scheduler may also be listed in `profiles` with custom configuration (e.g. per-backend options), same as other backends.

Profiles are used to enable other backends (e.g. kai-scheduler), to give kube-scheduler optional custom config when listed, and to choose which profile is the default (exactly one profile should have `default: true`). The profile with `default: true` is used when a workload does not specify a scheduler. If no profile has `default: true`, kube-scheduler is used as the default (since it is always active).

**When `profiles` is empty:** If `scheduler.profiles` is explicitly set to an empty list, only the kube-scheduler backend is active (it is always active). In this case, only admit PodCliqueSet resources whose pod spec has `schedulerName` empty or set to `"default-scheduler"`; reject (do not admit) PCS whose pod spec specifies any other scheduler name.

**API shape:**

```go
// OperatorConfiguration defines the configuration for the Grove operator.
type OperatorConfiguration struct {
	// ... existing fields (Controllers, LogLevel, etc.) ...

	// Scheduler configures which scheduler backends are active and their per-backend options.
	Scheduler SchedulerConfiguration `json:"scheduler,omitempty"`

	// TopologyAwareScheduling configures TAS (existing field)
	TopologyAwareScheduling TopologyAwareSchedulingConfiguration `json:"topologyAwareScheduling"`

	// ... other existing fields ...
}

// SchedulerConfiguration configures scheduler profiles and which is the default.
type SchedulerConfiguration struct {
	// Profiles is the list of scheduler profiles. Each profile has a backend name, optional config, and whether it is the default.
	// The kube-scheduler backend is always enabled and active even if not listed here. Listing "kube-scheduler" in profiles
	// only adds a profile (e.g. with config like GangScheduling: false) and allows marking it as default.
	// Valid backend names: "kube-scheduler", "kai-scheduler". Exactly one profile should have default: true; if none, kube-scheduler is the default.
	// +optional
	Profiles []SchedulerProfile `json:"profiles,omitempty"`
}

// SchedulerProfile defines a scheduler backend profile with optional backend-specific config and default flag.
type SchedulerProfile struct {
	// Name is the scheduler backend name. Valid values: "kube-scheduler", "kai-scheduler".
	// +kubebuilder:validation:Enum=kai-scheduler;kube-scheduler
	Name string `json:"name"`

	// Config holds backend-specific options. The operator unmarshals it into the config type for this backend (see backend config types below).
	// +optional
	Config *runtime.RawExtension `json:"config,omitempty"`

	// Default indicates this profile is the default backend when a workload does not specify one. Exactly one profile should have default: true.
	// +optional
	Default bool `json:"default,omitempty"`
}
```

The operator unmarshals `SchedulerProfile.Config` into the config type for that backend name (see below). New backends register their config type without changing `SchedulerProfile`.

When `GangScheduling` is enabled in `KubeSchedulerConfig`, the cluster's kube-scheduler is expected to support gang scheduling (e.g. via the Workload API). Grove then creates Workload objects so the Kube backend can use that capability.

**Backend config types (per-backend)**

```go
// KubeSchedulerConfig is the config type for the "kube-scheduler" backend.
type KubeSchedulerConfig struct {
	// GangScheduling enables or disables gang-scheduling behavior (e.g. Workload API). If false, the backend does not create Workload objects.
	// +optional
	GangScheduling *bool `json:"GangScheduling,omitempty"`
}
```

**How `profiles` and `config` relate**

Each item in `scheduler.profiles` has a `name` (which backend), an optional `config` (that backend's options), and `default` (whether it is the default). The operator deserializes `config` into the type for that backend: for `"kube-scheduler"` → `KubeSchedulerConfig`. The config shape is thus per-backend.

**Configuration options and ways to configure**

| Goal | How to configure |
|------|-------------------|
| Use kube-scheduler only (no extra options) | Omit `scheduler` or set `scheduler.profiles: []`. kube-scheduler is always active and is the default. |
| Use kube-scheduler and tune its behavior | One profile: `name: "kube-scheduler"`, `config: { GangScheduling: true }`, `default: true`. |
| Use kai-scheduler as default | `scheduler.profiles: [{ name: "kai-scheduler", config: {}, default: true }]`. kube-scheduler remains active; default is kai-scheduler. |
| Multiple backends, kube-scheduler default | Add profiles for each; e.g. kube-scheduler (with optional config) and kai-scheduler; set `default: true` on kube-scheduler profile. |
| Multiple backends, kai-scheduler default | Add profiles for each; e.g. kube-scheduler (with optional config) and kai-scheduler; set `default: true` on kai-scheduler profile. |

**Example YAML (OperatorConfiguration / Helm `config`):**

```yaml
# --- Style 1: Omit scheduler (all defaults) ---
# Same as profiles: [{ name: "kube-scheduler", default: true }]
# (scheduler block can be omitted entirely)
```

```yaml
# --- Style 2: Single backend, no backend-specific options ---
scheduler:
  profiles:
    - name: "kube-scheduler"
      default: true
```

```yaml
# --- Style 3: Single backend with backend-specific options (config = KubeSchedulerConfig) ---
scheduler:
  profiles:
    - name: "kube-scheduler"
      config:
        GangScheduling: true
      default: true
```

```yaml
# --- Style 4: Multiple backends; default is kube-scheduler ---
scheduler:
  profiles:
    - name: "kube-scheduler"
      config:
        GangScheduling: true
      default: true
    - name: "kai-scheduler"
      config: {}
```

```yaml
# --- Style 5: Multiple backends; default is kai-scheduler ---
scheduler:
  profiles:
    - name: "kube-scheduler"
      config:
        GangScheduling: true
    - name: "kai-scheduler"
      config: {}
      default: true
```

```yaml
# --- Style 6: Only kai-scheduler profile, but kube-scheduler is also active even if not set in profiles --- 
scheduler:
  profiles:
    - name: "kai-scheduler"
      config: {}
      default: true
```


**Phase 1 vs Phase 2:**

- **Phase 1** (Current): Establishes the framework infrastructure and interfaces. It includes:
  - **KAI Backend**: Full implementation—all PodGang-related management and kai-scheduler’s own CR (e.g. PodGroup) management are in the KAI backend. The backend implements the scheduler backend interface to create and update KAI CRs, providing clean separation and allowing kai-scheduler modifications to be minimized.
  - **Kube Backend**: Minimal no-op implementation. All backend interface methods (`Init`, `SyncPodGang`, `OnPodGangDelete`, `PreparePod`, `ValidatePodCliqueSet`) return immediately without creating any scheduler-specific resources.
- **Phase 2** (Future):
  - **Kube Backend**: Will support advanced community features as they become available, including `Workload` API for gang scheduling and other emerging Kubernetes scheduling capabilities. The backend will translate PodGang specifications to the appropriate Kubernetes-native scheduling primitives. 


### PodGang Lifecycle Changes

**Critical Design Change**: The PodGang creation flow has been fundamentally changed to support scheduler backends that require resources to exist before pods are created (e.g., Workload API):

#### Previous Flow (Before Framework):
1. Pods are created first
2. Wait for all pods to have back-references to PodGang
3. Create PodGang with complete PodReferences

#### Revised PodGang Creation Flow
To understand the new `PodGang` creation flow, we first introduce the enhancements made to the `PodGangStatus`.

##### PodGang API enhancements
A new `metav1.Condition` has been introduced for `PodGang`.

```go
const (
	// PodGangConditionTypeInitialized indicates that the PodGang has been populated
	// with pod references and pods can lift scheduling gates.
	PodGangConditionTypeInitialized PodGangConditionType = "Initialized"
)
```

A `PodGang` is considered as `Initialized` when:

* All constituent Pods are created.
* Pods back-reference to their PodGang via a grove.io/podgang label.
* PodGang.Spec.PodGroups have PodReferences fully populated.

> NOTE: Field PodReferences in PodGang.Spec.PodGroups is subject to change. If it does then this GREP will need to be updated accordingly.

##### Creation Flow
1. **Create PodGang early** with PodGroups having empty PodReferences and `Initialized=False`.
2. **Backend creates scheduler-specific CRs**: The Backend Controller reconciles the new PodGang and calls `SyncPodGang()` on the resolved backend. The backend creates or updates its scheduler-specific resources (e.g. PodGroup for kai-scheduler, Workload for kube-scheduler when supported). These CRs must exist before pods are allowed to be scheduled so the scheduler can enforce gang/topology semantics.
3. **Create Pods** (with scheduling gates to block scheduling). Pod creation may call `PreparePod()` to inject scheduler-specific settings (e.g. `schedulerName`, annotations).
4. **Update PodGang** with PodReferences once all pods are created, and set `Initialized=True`.
5. **Scheduling gates removed** to allow pods to be scheduled. The scheduler uses the backend-created CRs (PodGroup/Workload) when placing pods.

### Backend Controller

A dedicated Backend Controller watches PodGang resources and invokes backend hooks

```go
// Reconcile processes PodGang changes and synchronizes to backend-specific CRs.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error 
```

### Monitoring

#### Status Conditions

A new condition will be added to the Grove operator's status:

Condition: `Initialized`
Condition States:

| Status    | Reason            | Description                                                                 |
| --------- | ----------------- | --------------------------------------------------------------------------- |
| `True`    | `Ready`           | All constituent pods are created, pods back-reference this PodGang, and `PodGang.Spec.PodGroups` have `PodReferences` fully populated. |
| `False`   | `PodsPending`     | Not all constituent pods have been created yet.                             |
| `False`   | `RefsSyncing`     | Pods exist but references are not yet complete: either pods do not yet back-reference this PodGang, or `PodGang.Spec.PodGroups[].PodReferences` are not yet fully populated. |


### Test Plan

#### Unit Tests

Unit tests will cover the main framework areas: backend interface and registry behavior, each backend implementation’s contract (e.g. `PreparePod`, `ValidatePodCliqueSet`), controller integration with backend hooks (PodGang lifecycle, pod creation and scheduling gates), and OperatorConfiguration parsing and defaulting. 


#### E2E Tests

Today, E2E tests assume a single scheduler backend (e.g. KAI) and there is no way to configure which backend the test environment uses. The following changes are needed to align with this design and to run all E2E tests with the kai-scheduler:

1. **Make the scheduler backend configurable in E2E**
   The E2E harness (cluster bring-up, operator deployment) must supply [OperatorConfiguration](#operatorconfiguration-extension) so that the operator runs with the desired backend(s).

2. **Configure E2E to use kai-scheduler so all tests pass**
   To run the existing E2E suite (which assumes KAI behavior) and have all tests pass, the test environment must deploy the operator with OperatorConfiguration that enables kai-scheduler and sets it as the default.

3. **Scope of E2E**
   Once the backend is configurable, existing E2E tests should pass with the configured backend (kai-scheduler for the current suite). The test plan should also cover running against other supported backends where feasible.


### Graduation Criteria

The Scheduler Backend Framework will follow a staged rollout approach:

#### Alpha
- Core backend interface defined and implemented
- Backend registry functional
- Basic operator configuration support
- KAI backend and kube backend both supported and configurable via OperatorConfiguration

#### Beta 
- Backend interface stabilized (no breaking changes expected)
- Documentation for third-party backend development
- Kube backend support for advanced community features (e.g. Workload API for gang scheduling) as they become available in Kubernetes

#### GA
- Backend interface is stable and versioned
- Multiple production deployments using the framework
- Comprehensive documentation and examples
- All tests passing consistently
- Support for at least 2-3 different scheduler backends

## Implementation History
- **2026-01-27**: Initial GREP proposal created and submitted for review

## Alternatives

### Alternative 1: Direct Scheduler Integration

**Description**: Continue the current approach where scheduler-specific logic is embedded directly in Grove's controllers without abstraction.

**Pros**:
- No additional abstraction layer overhead
- Direct control over scheduler integration

**Cons**:
- High maintenance burden as schedulers evolve
- Adding new schedulers requires invasive changes to Grove core
- Difficult for third-party schedulers to integrate
- Code becomes increasingly complex with each scheduler addition

