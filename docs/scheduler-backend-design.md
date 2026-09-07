---
# Grove Scheduler Backend Design

## Overview

This document describes the scheduler backend framework introduced in PR #293 and the related changes to PodGang lifecycle and Pod preparation logic. The goal is to enable Grove to support multiple scheduler backends (e.g., KAI, kube-scheduler via Workload API, Volcano, etc.) while preserving existing behavior and gradually introducing Workload API support.

## Motivation

- Current Grove behavior: PodGang resources were created after pods were created and associated with the PodGang (pods have back references to PodGang).
- Problem: Workload API and kube-scheduler backend expect scheduler-specific CR (e.g., Workload) to be present so that kube-scheduler can run reservation/filter/scoring plugins before pod creation. Workload need to be create first and Pods need to reference Workload in their spec.
- Goal: Provide a pluggable backend framework that allows Grove to create PodGang early (with empty PodReferences) and let a scheduler backend create scheduler-specific resources (Workload/PodGroup). Later, when Pods are created / associated, operator populates PodGang podReferences and sets an Initialized condition. Pods should hold scheduling gates until PodGang is Initialized.

## High-level design

- New SchedulerBackend interface defines behavior backends must implement.
- Operator configuration adds a SchedulerName field to choose backend (kai-scheduler or grove-scheduler by default).
- PodGang creation flow changes:
  1. Create PodGang early with PodGroups whose PodReferences are empty and status condition Initialized=False.
  2. Backend controller (BackendReconciler) reacts to PodGang create/spec updates and invokes backend.SyncPodGang to create scheduler-specific resources (Workload/PodGroup).
  3. Pods are created (still blocked by scheduling gates); once operator detects all pods, it fills PodGang.Spec.PodGroups.PodReferences and (on first completion) sets PodGang.Status.Initialized=True.
  4. PodClique reconciler observes PodGang Initialized=True transition and removes scheduling gates from pods to allow scheduling.

## SchedulerBackend interface

The interface (operator/internal/schedulerbackend/types.go) contains:

- Name() string
- Init() error
- SyncPodGang(ctx context.Context, podGang *schedulerV1alpha1.PodGang) error
- OnPodGangDelete(ctx context.Context, podGang *schedulerV1alpha1.PodGang) error
- PreparePod(pod *corev1.Pod)

PreparePod is called right before creating Pod objects to let the backend inject schedulerName, scheduling gates, annotations, or any other scheduler-specific fields.

## Implementation notes in PR #293

- Files added/modified:
  - operator/internal/schedulerbackend/{types.go,manager.go}
  - operator/internal/schedulerbackend/kai/backend.go (KAI backend skeleton)
  - operator/internal/schedulerbackend/controller/{reconciler.go,register.go}
  - operator/internal/controller/podcliqueset/components/podgang/{podgang.go,syncflow.go}
  - operator/internal/controller/podclique/components/pod/pod.go (calls schedulerbackend.PreparePod)
  - operator/internal/controller/register.go (initialize backend and register backend controller)
  - operator/api/config/v1alpha1/types.go (SchedulerName added)
  - scheduler/api/core/v1alpha1/podgang.go (PodGangConditionTypeInitialized added)
  - charts/templates/clusterrole.yaml updated for podgangs/status

- PodGang creation changed to create empty PodGroups (no PodReferences). Post-creation, operator patches status with Initialized=False.
- New function updatePodGangsWithPodReferences populates PodGang.Spec.PodGroups.PodReferences once operator sees all pods for the PodCliqueSet are created. On the first successful update the operator sets Initialized=True in PodGang status.
- Backend controller watches PodGang resources and calls backend.SyncPodGang for create/spec-change events (predicate ignores status-only updates). Backend.SyncPodGang is expected to create scheduler-specific resources (Workload/PodGroup) or update/delete them.
- Kai backend currently implements PreparePod (sets schedulerName and annotations) but SyncPodGang and OnPodGangDelete are stubs (Phase 2). The schedulerbackend manager currently maps both "kai-scheduler" and "grove-scheduler" to the kai backend - this is an implementation detail / temporary mapping.

## API & RBAC changes

- New API condition type: PodGangConditionTypeInitialized - indicates that PodGang has been populated with pod references and pods can lift scheduling gates.
- OperatorConfiguration.SchedulerName (default: kai-scheduler) allows choosing a backend.
- clusterrole: granted podgangs/status permission because operator updates PodGang.status.

## Event flow and predicates

- PodGang create triggers backend controller (predicate processes create events).
- PodGang status-only updates (e.g., setting Initialized condition) are ignored by backend controller predicate; PodClique controller listens for Initialized transition to remove scheduling gates.
- PodClique controller's PodGang predicate: Create events do not trigger PodClique reconciliation; Update events trigger when Initialized transitions false -> true or when generation changes while Initialized is true (handles scale events).

## Testing guidance

1. Basic flow with default kai-scheduler:
   - Create a PodCliqueSet that results in PodGang creation. Confirm PodGang.Spec.PodGroups have empty PodReferences and PodGang.Status.Initialized is False.
   - Verify backend controller logs/behavior on PodGang creation.
   - Confirm Pods are created with scheduling gates and schedulerName set by PreparePod.
   - Once all pods for PodGroup exist, operator updates PodGang.PodReferences and sets Initialized=True.
   - Verify scheduling gates are removed and pods are scheduled.

2. Scale-out/scale-in scenarios:
   - Confirm operator updates PodGang.PodReferences after changes and does not flip Initialized for subsequent updates.

3. RBAC / chart validation:
   - Ensure clusterrole allows status patch on podgangs.

## Compatibility & migration notes

- Existing consumers of PodGang must treat Initialized=True as the signal that PodReferences are complete.
- If external systems read PodGang.Spec.PodGroups.PodReferences directly, they should tolerate empty PodReferences and check status.
- If implementing a new backend (e.g., kube-scheduler/Workload), implement backend.SyncPodGang to create Workload CRs so kube-scheduler can perform reservation/filters.

## Open items / next steps

- Implement kube-scheduler backend (Workload creation & mapping PodGang → Workload) in a follow-up PR.
- Implement KAI backend SyncPodGang and OnPodGangDelete.

## Review checklist

- [ ] Ensure schedulerbackend.Initialize is called early at operator startup and before controllers need PreparePod.
- [ ] Validate RBAC includes podgangs/status.
- [ ] Confirm predicate behavior for backend controller and PodClique controller matches intended triggers.
- [ ] Confirm the operator does not unintentionally clear existing PodReferences during updates.

---
