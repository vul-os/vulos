# Multi-Instance Data Sync

How a leaderless, multi-location cluster keeps its data **redundant and load-balanced** across instances — a **two-tier** sync that combines a fast instance↔instance hot path with a durable bucket cold path, and adds **snapshot/compaction** so a fresh instance can bootstrap quickly.

For the underlying S3 model, schema, and conflict copies see CLUSTER.md. For the exclusion lease that guards compaction see COORDINATION.md. For per-app concurrency see CONCURRENCY.md.

> **Goal.** Make data redundant by construction (no failover election) and let a new or recovering instance join without replaying an unbounded changeset log: bootstrap from a recent snapshot + a short tail.
> **Non-goals.** A primary node. A central database. Hot-replicating the running OS (that's OS-DISTRIBUTION.md). Inventing a new CRDT merge engine ahead of the forward-plan Sync spec (see status below) — this doc is about *transport tiers* and *log compaction*, not merge logic.
> **Status — REALITY CHECK (2026-07-19).** This document originally described a "cr-sqlite cold-path changeset streaming" as ✅ SHIPPED. **That is not accurate: cr-sqlite is not integrated**, and cannot be under the pure-Go/no-CGO rule (`docs/decisions.md` D23, reaffirmed D94 item J). What is actually shipped:
> - **Bucket cold path (real):** `backend/services/sync/dbio.go` + `snapshot.go` + `bootstrap.go` — a compacted, SSE-C-encrypted, whole-database-file snapshot (via `VACUUM INTO`, pure-Go) written to S3 on a schedule, with new instances bootstrapping from `snapshot/latest` + a short tail. This works today, but it snapshots **one node's local SQLite file** — there is no cross-node CRDT merge happening before the snapshot is taken.
> - **Instance↔instance hot path (REMOVED 2026-07-20):** an implementation of the peering-mesh transport (SYNC-01/02) existed at `backend/services/sync/hotpath.go` with passing tests, but it streamed a `crsql_changes` virtual table that only exists if cr-sqlite loaded — which it never did under the no-CGO rule (`load_extension` returns "not authorized" against `modernc.org/sqlite`, verified empirically). It had zero callers and its inbound route was never registered on any mux, so it was deleted along with `backend/services/store/`. The hot path is unimplemented; re-introducing it needs a sync engine that does not depend on cr-sqlite.
> - **What actually provides leaderless CRDT sync today:** the app-registry-only CRDT in `backend/internal/multiinstance/appsync.go`, transported **same-LAN-only** via mDNS + authenticated HTTPS in `backend/internal/fabric/` (its own doc comment: *"Pure-Go: no CGO, no cr-sqlite."*). This does not cover auth/settings/chat/general SQL data, and has no WAN path.
> - **Forward plan (not shipped):** replace the cr-sqlite framing with the shared DMTAP-substrate **Sync** spec — a CRDT op algebra + version-vector/reconciliation wire protocol + first-class snapshots (`VULOS-PRODUCT-STANDARD.md` substrate capability 3) — with the **relay as the WAN rendezvous point** for the hot path (instead of/alongside the peering mesh), and the existing S3 snapshot/bootstrap machinery kept as the durable cold path underneath it. This is a design direction; no code has been written against it.
>
> Read the rest of this document (below) as the **original design intent** for the hot/cold two-tier split — the tiering idea and the snapshot/compaction mechanism are sound and largely already real (cold path); only the specific "cr-sqlite changesets" merge mechanism named in the hot path is not, and is what the forward plan above replaces.

---

## Redundancy Is Inherent (no failover) — design intent; today's real mechanism is narrower

The original design assumed cr-sqlite as a **leaderless CRDT**: every instance would hold a full, mergeable copy, with concurrent writes converging automatically, so redundancy needs no failover election and has no split-brain. That is still the right *shape* for the forward plan (see status above), but **today** the only working leaderless CRDT is the app-registry one (LAN-only, `multiinstance`/`fabric`) — general SQL data (auth/settings/sessions) is not cross-node merged; it is periodically snapshotted whole to S3 (durable, but single-writer-at-a-time in effect). "Load-balancing across locations" (NETWORK.md § session affinity) still routes users to a healthy instance, but today's redundancy story for structured data rests on the S3 snapshot, not a live CRDT merge.

---

## Two-Tier Sync

| Tier | Path | Latency | Durability | Built |
|---|---|---|---|---|
| **Hot** | instance ↔ instance changeset streaming over the **peering mesh** (relay fallback for NAT / cross-location) | low | none (transient) | **not implemented** — the cr-sqlite-dependent implementation was removed as dead code; see status above |
| **Cold** | periodic **durable checkpoint** to the shared S3 bucket | high | durable | **real** — whole-DB-file `VACUUM INTO` snapshot, not a cross-node merge |

- **Hot path (design intent, not live today):** the intent was for live instances to stream `crsql_changes` to each other directly over the **existing peering mesh** (PEERING.md), falling back to the **relay** when direct connectivity is blocked. The transport code and tests exist, but with no cr-sqlite loaded there is no real `crsql_changes` table to stream, so no data actually moves over this path today.
- **Cold path (real):** every instance periodically snapshots its local database file to S3 as the **durable** record. An instance that was offline catches up from the bucket — this part works as described.

The hot path is for *liveness between active peers*; the cold path is for *durability and catch-up*. They complement each other — the hot path doesn't replace the bucket, and the bucket isn't on the critical path for live convergence.

---

## Snapshot / Compaction (the new work)

**Problem:** the per-node changeset log in the bucket (`nodes/{id}/changes/{ver}.bin`, CLUSTER.md) **grows unbounded**. A brand-new instance that bootstraps by replaying the whole log pays for the cluster's entire history.

**Fix:** periodically write a **compacted snapshot** of the (compacting instance's) local database state to the bucket, and let new instances bootstrap from **snapshot + short changeset tail**. Note: since the hot-path changeset merge described above is not live in production, "the merged database state" is aspirational phrasing — today this snapshots whichever instance holds the compaction lease, not a cross-node merge:

```
cluster/
├── snapshot/
│   ├── latest.json            # points at the current snapshot + the changeset version it covers up to
│   └── <version>.db.enc       # compacted, encrypted DB state (SSE-C, per CLUSTER.md)
└── nodes/<id>/changes/        # only the TAIL after the snapshot version is needed going forward
```

- A new instance: download `snapshot/latest` → apply the short tail of changesets after its covered version → live. Bootstrap cost is bounded by snapshot age, not cluster age.
- Old per-node changesets **below** the snapshot's covered version can be pruned.

**Who runs compaction:** exactly one instance at a time, chosen by the **snapshot/compaction ownership lease** (COORDINATION.md, `leases/snapshot.json`) — no leader, just whoever holds the lease this cycle. The fencing token prevents a stalled compactor from clobbering a newer snapshot.

Encryption, SSE-C, and the Argon2id passphrase model are unchanged from CLUSTER.md — the snapshot is just another encrypted bucket object; the passphrase is held only locally.
