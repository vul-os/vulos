# Multi-Instance Data Sync

How a leaderless, multi-location cluster keeps its data **redundant and load-balanced** across instances — a **two-tier** sync that combines a fast instance↔instance hot path with a durable bucket cold path, and adds **snapshot/compaction** so a fresh instance can bootstrap quickly.

For the underlying cr-sqlite/S3 model, schema, and conflict copies see CLUSTER.md. For the exclusion lease that guards compaction see COORDINATION.md. For per-app concurrency see CONCURRENCY.md.

> **Goal.** Make data redundant by construction (no failover election) and let a new or recovering instance join without replaying an unbounded changeset log: bootstrap from a recent snapshot + a short tail.
> **Non-goals.** A primary node. A central database. Hot-replicating the running OS (that's OS-DISTRIBUTION.md). Changing cr-sqlite's CRDT semantics — this is about *transport tiers* and *log compaction*, not merge logic.
> **Status.** ✅ SHIPPED. The cr-sqlite cold-path changeset streaming (CLUSTER-05), the instance↔instance hot-path relay over the peering mesh (SYNC-01/02), and bucket-side snapshot/compaction (SYNC-03) are all implemented in `backend/services/sync/` (hotpath, bootstrap, snapshot packages). The sync engine starts alongside the cluster service at boot. Snapshot compaction runs under the COORDINATION.md exclusion lease.

---

## Redundancy Is Inherent (no failover)

cr-sqlite is a **leaderless CRDT** (CLUSTER.md): every instance holds a full, mergeable copy, and concurrent writes converge automatically. So redundancy needs **no failover election and has no split-brain** — there is no leader to lose. "Load-balancing across locations" is therefore just routing users to a healthy instance (NETWORK.md § session affinity); any instance can serve any read/write and the changes merge.

---

## Two-Tier Sync

| Tier | Path | Latency | Durability | Built |
|---|---|---|---|---|
| **Hot** | instance ↔ instance changeset streaming over the **peering mesh** (relay fallback for NAT / cross-location) | low | none (transient) | new (SYNC) |
| **Cold** | periodic **durable checkpoint** to the shared S3 bucket | high | durable | exists (CLUSTER-05) |

- **Hot path:** live instances stream `crsql_changes` to each other directly over the **existing peering mesh** (PEERING.md). When direct connectivity is blocked (NAT, cross-location), it falls back to the **relay** — the same transport peering already uses. This gives near-real-time convergence between active instances without waiting on a bucket round-trip.
- **Cold path:** every instance still periodically writes its changesets to S3 (the existing CLUSTER-05 loop) as the **durable** record and the substrate for snapshots. An instance that was offline catches up from the bucket.

The hot path is for *liveness between active peers*; the cold path is for *durability and catch-up*. They complement each other — the hot path doesn't replace the bucket, and the bucket isn't on the critical path for live convergence.

---

## Snapshot / Compaction (the new work)

**Problem:** the per-node changeset log in the bucket (`nodes/{id}/changes/{ver}.bin`, CLUSTER.md) **grows unbounded**. A brand-new instance that bootstraps by replaying the whole log pays for the cluster's entire history.

**Fix:** periodically write a **compacted snapshot** of the merged database state to the bucket, and let new instances bootstrap from **snapshot + short changeset tail**:

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
