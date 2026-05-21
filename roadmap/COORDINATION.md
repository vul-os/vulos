# Coordination Primitives

How a leaderless, multi-instance Vulos cluster does **mutual exclusion and ownership** without a leader, a coordinator, or a dependency on any external service. One primitive — **bucket-backed leases with monotonic fencing tokens** — serves run-leases, singleton jobs, and snapshot/compaction ownership. Real-time collaboration/presence is a **separate**, latency-optimized mechanism (see CONCURRENCY.md).

For multi-instance data sync see CLUSTER.md and SYNC.md. For per-app concurrency declarations see CONCURRENCY.md.

> **Goal.** Provide exclusive ownership of a resource across N equal instances using only the shared object store, with correctness that **never depends on any external service**. No leader election, no consensus service.
> **Non-goals.** A general distributed lock service. Sub-second coordination (use the peering/relay hot path for that — CONCURRENCY.md). Relying on `If-None-Match: *` (MinIO doesn't support the wildcard — issue #20346, wontfix).
> **Status.** Design. Supersedes the advisory presence-lease note in CLUSTER.md (CLUSTER-09) for the *exclusion* use case — LEASE-* generalize it into a fencing-token primitive. Any external service is an optional accelerator only.

---

## The Primitive: Bucket-Backed Lease with Fencing Token

A lease is a single, **always-present** object in the shared bucket, mutated via **`If-Match <etag>` compare-and-swap (CAS)**. The object is created **once** at cluster init and thereafter only ever CAS-updated:

```
leases/<scope>.json
{
  "state":  "free" | "held",
  "holder": "<node-ulid>",
  "fence":  42,                 // monotonic fencing token, bumps on every acquire/renew
  "expires_at": "2026-05-20T10:00:30Z"
}
```

Operations are all `If-Match`-guarded CAS on the current etag:

| Op | Transition | Fence |
|---|---|---|
| **acquire** | `free → held` | bump |
| **renew** | `held → held` (same holder, before expiry) | bump |
| **release** | `held → free` | (unchanged) |

- **Acquire** reads the object + etag, and only succeeds if `state == free` (or the lease has expired) and the `If-Match` etag still matches — i.e. no one else mutated it first. The loser of a race gets a `412 Precondition Failed` and backs off.
- **Renew** keeps the lease alive with another `If-Match` CAS, bumping the fence and pushing `expires_at`.
- **Release** flips back to `free`.
- The object **always exists** after init, so every op is a plain `If-Match` CAS — we **never** use `If-None-Match: *` (unsupported on MinIO, #20346 wontfix).

### Fencing tokens

The monotonic `fence` integer is handed to whatever the lease guards. A stalled-then-resumed holder presents a **stale fence** and is rejected by the resource (the classic Kleppmann fencing pattern), so a process that paused past its lease expiry can't corrupt state after another node took over.

### Why `If-Match` (not `If-None-Match: *`)

`If-Match <etag>` CAS is the portable primitive: it works on **AWS S3 (SigV4)**, **MinIO**, and **Tigris**. `If-None-Match: *` (create-if-absent) is **not** supported by MinIO, so we avoid it entirely by pre-creating the object once and only ever doing `If-Match` updates.

**Tigris note:** strongly-consistent CAS on Tigris requires **Single-region or Multi-region** buckets — configure accordingly, or CAS guarantees don't hold.

#### Tigris bucket consistency guard (LEASE-03)

Tigris distinguishes four bucket replication classes:

| Class | Consistency | Safe for leasing? |
|---|---|---|
| Single-region | Strong | Yes |
| Multi-region | Strong | Yes |
| Global | Eventual | **No** |
| Dual-region | Eventual | **No** |

Global and Dual-region buckets replicate asynchronously.  Two nodes can each see stale ETags, both believe their CAS succeeded, and both think they hold the lease simultaneously — mutual exclusion is violated.

The lease package enforces this at `Manager` construction time via `CheckConsistency` (see `backend/services/lease/guard.go`):

- **Default (warn mode, `StrictConsistency=false`):** a loud `log.Printf` warning is emitted; the Manager is created and operations continue.  Use during initial migration when you cannot immediately change the bucket type.
- **Strict mode (`StrictConsistency=true`):** `New()` returns `ErrUnsafeBucket` and refuses to create the Manager.  Recommended for all production deployments.

Detection is endpoint-based: any endpoint containing `tigris.dev` triggers the check.  AWS S3, MinIO, and other S3-compatible backends are always considered safe (their CAS semantics are strongly consistent by default).

Set `S3Config.BucketType` to `"single-region"` or `"multi-region"` (from the `TIGRIS_BUCKET_TYPE` environment variable or equivalent) to declare a safe Tigris bucket explicitly and suppress the unknown-type warning.

---

## What This One Primitive Serves

| Use | Scope key | TTL |
|---|---|---|
| **Run-lease** (singleton apps — see CONCURRENCY.md) | `leases/run/<profile>/<app>.json` | ~15–30s |
| **Singleton jobs** (cron-style, run-once-per-cluster) | `leases/job/<job-id>.json` | per-job |
| **Snapshot / compaction ownership** (SYNC.md) | `leases/snapshot.json` | per-cycle |

One mechanism, one code path. **No leader election** anywhere — ownership is whoever currently holds the relevant lease.

---

## Two Mechanisms, Split by Latency

Coordination splits cleanly into two regimes, and we keep them **separate** on purpose:

| Need | Mechanism | Properties |
|---|---|---|
| **Exclusion / ownership** | bucket leases (this doc) | coarse, durable, slow (object-store round-trips), survives crashes |
| **Real-time collaboration / presence** | peering + relay hot path (CONCURRENCY.md) | ephemeral, fast, lossy-tolerant, never durable |

Don't conflate them: a bucket lease is wrong for cursor presence (too slow), and the hot path is wrong for "who owns this singleton app" (not durable). Each is sized for its job.

---

## Any External Service is an Accelerator Only

An optional external service may shave a round-trip, but **correctness must never depend on it**. The authoritative state is always the bucket object; a cluster with no external relationship coordinates perfectly using its own bucket. This preserves the self-hosting and forkability guarantees (SEED-TRUST.md) at the coordination layer.
