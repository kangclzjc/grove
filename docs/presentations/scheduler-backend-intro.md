---
# Slide 1: Title

Title: Grove — Scheduler Backend & PodGang Init Flow
Subtitle: Design overview and implementation notes (PR #293)
Speaker notes: Introduce the motivation and scope: framework + PodGang lifecycle changes. Workload/kube-scheduler implementation is out-of-scope for this PR.

---
# Slide 2: Background & Motivation

- Existing flow: PodGang created after pods exist.
- Problem: Workload API / kube-scheduler requires scheduler-specific CRs to be present earlier.
- Goal: Support multiple scheduler backends without breaking current workflows.
Speaker notes: Explain reservation/filtering requirement for kube-scheduler and why PodGang must be created earlier.

---
# Slide 3: High-level Design

- New SchedulerBackend interface
- PodGang created early with empty PodReferences and Initialized=False
- Backend controller converts PodGang → scheduler-specific CR
- Operator fills pod references and sets Initialized=True when ready
Speaker notes: Summarize responsibilities of operator vs backend.

---
# Slide 4: SchedulerBackend Interface

- Name(), Init(), SyncPodGang(ctx, podGang), OnPodGangDelete(ctx, podGang), PreparePod(pod)
- PreparePod injects schedulerName, scheduling gates, annotations
Speaker notes: Emphasize PreparePod is called during Pod construction.

---
# Slide 5: PodGang Lifecycle (flow diagram)

- PodClique -> Pods (blocked by scheduling gates)
- Operator creates PodGang (empty podRefs, Initialized=False)
- Backend controller SyncPodGang -> create Workload/PodGroup
- Pods get created and backref updated
- Operator updates PodGang podRefs and sets Initialized=True
- PodClique removes scheduling gates
Speaker notes: Walk through each step and which component is responsible.

---
# Slide 6: Implementation status (PR #293)

- Added schedulerbackend package and manager
- Backend controller implemented
- Kai backend: PreparePod implemented; Sync/OnDelete skeleton
- PodGang flow updated: create empty PodGroups and update podRefs
Speaker notes: Clarify what is complete and what is intentionally left for next PR.

---
# Slide 7: Event predicates & triggers

- Backend controller: processes create and spec-change (generation) events, ignores status-only updates
- PodClique controller: triggers on PodGang Initialized False→True and on generation changes after initialized
Speaker notes: Explain why predicates are chosen (avoid duplicate/loop triggers).

---
# Slide 8: Risks & Considerations

- grove-scheduler currently mapped to kai backend in Initialize (temporary)
- Ensure schedulerbackend initialization order (PreparePod caller must not run before Initialize)
- RBAC: podgangs/status permission required
Speaker notes: Call out ops considerations for deployment.

---
# Slide 9: Next steps

- Implement kube-scheduler backend (Workload creation)
- Complete Kai backend Sync/OnDelete
- Add tests (unit/e2e) for timing-sensitive flows
- Update documentation & migration notes
Speaker notes: Roadmap and recommended priorities.

---
# Slide 10: Q&A

- Discussion points: backend naming, mapping policy, timeline for Workload implementation
Speaker notes: Invite reviewers to discuss open items.

---
