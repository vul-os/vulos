# Concurrency Model

How a single profile that is **legitimately live in many locations at once** runs apps safely across a leaderless cluster. Conflict resolution is **per data type**; an app **opts into** concurrency via its manifest; live collaboration (collaborative CRDTs + presence) is **in scope**.

For the exclusion primitive (run-leases, fencing) see COORDINATION.md. For changeset streaming + snapshots see SYNC.md and CLUSTER.md. For the manifest field itself see APP-MANIFEST.md. For routing & session affinity of a profile see NETWORK.md.

> **Goal.** Let the same profile be active in several places without data loss or split-brain: pick the right conflict strategy per data type, make every app **safe by default**, and let apps explicitly opt into active-active / collaborative behavior.
> **Non-goals.** Forcing every app to be replication-aware (default is safe single-owner). A global lock service (COORDINATION.md handles exclusion). Treating multi-location as anomalous — it is **intended** (see NETWORK.md's security caveat).
> **Status.** Design. CONC-*/COLLAB-* add the manifest `concurrency` field (extends APP-MANIFEST.md / `backend/services/appnet/manifest.go`), the infra-enforced run-lease for singletons, and the presence/awareness channel for collaborative apps.
> **Reality check (2026-07-19).** This doc assumes cr-sqlite provides the leaderless CRDT merge for structured data (see the "Conflict Policy Per Data Type" table below, and CLUSTER.md/SYNC.md). **cr-sqlite is not integrated** — it conflicts with the pure-Go/no-CGO rule (`docs/decisions.md` D23/D94-J) and is not shippable as-is; see the reality checks in CLUSTER.md and SYNC.md for the full picture. The only leaderless CRDT that actually merges data across instances today is the pure-Go app-registry CRDT (`backend/internal/multiinstance/appsync.go`, LAN-only via `backend/internal/fabric/`), which is narrower than "structured store" in general. The forward plan is the shared DMTAP-substrate Sync spec (CRDT op algebra + version-vectors + snapshots) — see CLUSTER.md's reality check — not cr-sqlite. This document's *policy* framework (LWW / CRDT-counter / sequence-CRDT / lease, per data kind) is still the intended design; only the "cr-sqlite already does this" assumption is wrong today.

---

## Conflict Policy Per Data Type

Different data wants different resolution (this extends CLUSTER.md's per-data-type table to the *concurrency* dimension):

| Data kind | Policy | Why |
|---|---|---|
| Settings / preferences / most state | **LWW** (last-write-wins per field) | matches user expectation; cr-sqlite field-level merge |
| Counters / quotas | **CRDT counter** | additive, commutative — never lost across merges |
| Co-edited documents | **sequence / collaborative CRDT** (Automerge/Yjs-style) | preserves concurrent edits + intent |
| Exclusive resources | **lease** (COORDINATION.md fencing lease) | only one owner at a time |

The forward-plan Sync spec (see CLUSTER.md's reality check) is intended to provide leaderless CRDT merge for the structured store; this table says *which* discipline applies to *which* data once that lands. Today, only the app-registry has a working leaderless CRDT (pure-Go, LAN-only — see CLUSTER.md/SYNC.md).

---

## Manifest-Declared Concurrency (opt-in)

An app declares its concurrency posture in its manifest (APP-MANIFEST.md), and the value is **signed with the manifest** so it can't be tampered with post-publish:

```json
{ "concurrency": "singleton" | "replicated" | "collaborative" }
```

| Mode | Topology | Infra provides | Default? |
|---|---|---|---|
| **singleton** | active-passive | infra-enforced **run-lease**; fails over to another instance on holder loss | **yes (default — safe)** |
| **replicated** | active-active | CRDT merge across live instances | no |
| **collaborative** | active-active + live co-editing | CRDT merge **plus** an infra-provided **presence/awareness channel** on the peering/relay hot path | no |

- **Default = `singleton` = safe.** An app that says nothing gets active-passive with a run-lease: exactly one instance runs it for a given profile, and if that instance dies, the lease expires and another picks it up (no split-brain). The run-lease is the bucket-backed fencing lease from COORDINATION.md (`leases/run/<profile>/<app>.json`, ~15–30s TTL).
- **`replicated`** is an explicit opt-in to active-active with CRDT merge — for apps whose state is genuinely commutative/mergeable.
- **`collaborative`** is `replicated` plus a real-time **presence/awareness** channel (who's here, cursors, selections) carried on the **peering + relay hot path** (COORDINATION.md's fast/ephemeral mechanism) — *not* the bucket.

Apps must **opt INTO** concurrency. We never silently make an app active-active; getting it wrong loses data, so the safe mode is the default and concurrency is a deliberate, signed declaration.

---

## Live Collaboration Is In Scope

Real-time co-editing is a first-class target, not a someday: collaborative CRDTs (Automerge/Yjs-style — Yjs transport already exists in PEERING.md, PEER-30..33) plus a **presence/awareness** channel. The presence channel rides the peering/relay hot path because it must be **fast and ephemeral**; the document state merges via CRDT and persists via the normal sync/snapshot path (SYNC.md). The two coordination regimes from COORDINATION.md map directly:

- **Exclusion** (does this singleton already run somewhere?) → bucket lease.
- **Presence** (where is everyone's cursor right now?) → peering/relay hot path.

---

## Interaction With Routing & Session Affinity (NETWORK.md)

An external router may route a user to the nearest healthy instance with sticky-until-failure affinity, using the OS's health and session-affinity hooks (NETWORK.md). Because a profile can be live in many places at once **by design**, the concurrency model must tolerate it: `singleton` fails over cleanly via the run-lease, `replicated`/`collaborative` merge. Crucially, **multi-location liveness must not be treated as a compromise signal** — impossible-travel / geo-velocity heuristics are explicitly *not* applicable here (see NETWORK.md's security caveat).
