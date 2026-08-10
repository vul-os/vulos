# Multi-Instance Data Sync

How a leaderless, multi-location cluster keeps its data **redundant and load-balanced** across instances — a **two-tier** sync that combines a fast instance↔instance hot path with a durable bucket cold path, and adds **snapshot/compaction** so a fresh instance can bootstrap quickly.

For the underlying S3 model, schema, and conflict copies see CLUSTER.md. For the exclusion lease that guards compaction see COORDINATION.md. For per-app concurrency see CONCURRENCY.md.

> **Goal.** Make data redundant by construction (no failover election) and let a new or recovering instance join without replaying an unbounded changeset log: bootstrap from a recent snapshot + a short tail.
> **Non-goals.** A primary node. A central database. Hot-replicating the running OS (that's OS-DISTRIBUTION.md).
>
> **Status — 2026-08-10.** A general, pure-Go, leaderless CRDT sync engine now exists, is proven to converge, and is wired end to end **for one domain over the LAN**. That is a real hot path, and it is also a narrow one — read the scope line before treating this as "sync is done".
>
> **What is shipped and proven**
> - **Merge engine — `backend/internal/crdtsync/`.** Per-column last-writer-wins over a hybrid logical clock with a node-id tie-break, plus PN-counters. Merge is commutative, associative and idempotent; each law is asserted by test against *conflicting* ops, not disjoint ones. Convergence is checked on a digest that includes each register's winning stamp, so two replicas showing the same value while disagreeing about who wrote it count as diverged. Coverage includes all 720 orderings of a six-op conflicting set, a randomised property test over shuffled/duplicated/batched delivery (seed reproducible via `VULOS_CRDT_SEED`), duplicate and out-of-order delivery, three-node relay where two nodes never talk, and offline catch-up both by ops and — past the compaction floor — by snapshot.
> - **Change capture — `backend/internal/sqlcrdt/`.** Arbitrary SQLite tables, captured in pure Go via SQLite's own **session extension** (`sqlite3session_diff` against a baseline copy). This is what replaces cr-sqlite: the session extension is compiled into the amalgamation `modernc.org/sqlite` ships, so no `load_extension` is involved. Because it diffs against a baseline it captures writes from **any** connection, and no existing write path had to change.
> - **Transport — `backend/internal/crdtsync/syncer.go`.** Pull-then-push rounds over the existing LAN fabric (mDNS + authenticated HTTPS), gated by the same `X-Fabric-Auth` shared secret, mounted on the same LAN-only mux.
> - **Wired end to end — `backend/cmd/server/crdtsync_wiring.go`,** called from `main.go` at the LAN mux site. The **`reminders`** table replicates between boxes today: an ordinary SQL `INSERT`/`UPDATE`/`DELETE` on one box appears on another, with concurrent edits to *different columns of the same row* both surviving.
>
> **Scope — what this is NOT yet**
> - **One domain.** `reminders` only. Every other table is refused, on the record and with reasons, in `crdtsync/policy.go`. Auth material (`users`, `sessions`, `recovery_blobs`, `master_key_blobs`, `local_api_keys`) is refused permanently; `profiles` is refused *pending* a field-level bridge (see below). This is an allow-list: a new table replicates only when someone writes down why.
> - **LAN only.** There is no WAN path. The seam for one is stated below.
> - **The bucket cold path is unchanged** and still snapshots one node's local file.
>
> **Anti-regression note.** The previous hot path (`backend/services/sync/hotpath.go`, removed 2026-07-20) had passing tests, **zero callers, and its route registered on no mux**. Its successor is therefore guarded by tests that assert the routes are mounted and authenticated, and that `main.go` still calls the wiring — mutation-tested by removing the call and confirming the guard goes red.

---

## The merge: per-column LWW over a hybrid logical clock

Change capture tells you **what changed**. It does not tell you **who wins** when two boxes write the same row. That is the merge policy, and it is what turns capture into sync.

Granularity is **per column, not per row**. Two boxes editing different fields of the same record must both survive — that is the entire reason for doing this at column granularity, and it is asserted end to end (`done=1` on one box, new `text` on the other, both kept).

Each write is stamped `(wall, logical, actor)`:

- **wall** — a hybrid logical clock's wall component, in unix milliseconds. It never goes backwards for a given actor and jumps forward when a remote stamp is observed, so a box with a slow or backward-jumping clock still emits stamps that sort after everything it has already seen. The clock is re-seeded from persisted state at open, so a restart cannot emit a stamp that loses to what the same box wrote before the restart.
- **logical** — disambiguates two events in the same millisecond.
- **actor** — the instance ULID. This is the **tie-break**, and because every box compares the same triple, two boxes handed the same pair of concurrent writes pick the *same* winner regardless of arrival order.

Merge is then literally "take the max" under the total order `(wall, logical, actor, liveness, value)`, which is trivially commutative, associative and idempotent. The two trailing components only ever decide an exact-stamp collision — which a well-behaved actor cannot produce but a byzantine one can. Live beats deleted; larger value bytes break the final tie.

A **delete is a tombstone with a stamp**, not an absence, so delete-vs-concurrent-update resolves by stamp rather than by arrival order: a delete that loses does not erase a newer write, and one that wins is not undone by a late-arriving older write.

**Version vectors decide only what to transmit, never who wins.** The merge outcome is a pure function of the stamps, so a delta computed from a stale, empty or actively lying version vector costs bandwidth and nothing else. That separation is deliberate — a peer cannot influence the merge by misreporting what it has seen. Relatedly, the version vector only advances across a **contiguous** run actually present in the log, so a peer that hands over op 7 while op 6 is still missing cannot make this box stop asking for 6.

**Bounded history.** The materialised state is the authority for reads and is bounded by live key/field count. The op log exists only as the reconciliation index and is pruned by `Compact`; a peer that has fallen behind the pruned floor is served a **snapshot** instead of a delta. Applying a snapshot is a *merge*, not a replace — every register goes through the same max-by-stamp rule — so handing one to an already-running node does not cost it local writes the snapshot's author never saw.

---

## Which data syncs

The engine replicates whatever it is handed, so **what it is handed is the security boundary**. That decision is an allow-list in `backend/internal/crdtsync/policy.go`, enforced rather than documented: `Open` requires a non-empty allow-list — there is no permissive default — and local writes, pushed deltas, applied snapshots and served deltas all check it.

A deny-list would be the wrong shape because it fails **open**: every table added later would replicate by default, and the first person to add a credential column would ship it to every box on the LAN without noticing.

| Domain | Syncs | Why |
|---|---|---|
| `reminders` | **yes** | User-authored content whose whole value is being the same everywhere. No column is a credential, it has a stable `TEXT` primary key, and `RemindersStore` holds no in-memory cache — merged rows are visible to the next read with no reload hook. |
| `profiles` | **not yet** | *Wanted.* The obvious settings domain, but the row is a single JSON `data` blob holding `AIAPIKey` and `PinHash` alongside `Theme` and `Locale`, and column-level exclusion cannot reach inside a blob. Needs a field-level bridge projecting the safe subset. Refused on those grounds, not on principle. |
| `sessions` | no | Per-**device** auth state. Replicating bearer tokens multiplies the blast radius of any one box, and revoking on one box could not be relied on to revoke elsewhere. |
| `users`, `recovery_blobs`, `master_key_blobs`, `local_api_keys`, `push_subscriptions` | no | Auth and key material. A merge that resolved a password change the wrong way is an authentication bug, not a lost edit. Needs a deliberate, audited propagation path, not a general CRDT. |
| `app_registry` | no | Already replicated by `multiinstance/appsync` over the same fabric. Two engines converging one table would observe each other's writes as local edits, restamp them, and never settle. |
| `storagemode`, `cgroup_slices` | no | Node-local hardware configuration. Boxes deliberately differ; replicating one box's storage mode describes hardware another does not have. |
| `acctsec_sensitive_actions` | no | A security audit trail's value depends on being a local append-only record. A mergeable audit log is one an attacker can edit from a second box. |

Approved domains are bound to their table and **explicit column list** in `sqlcrdt.ReplicatedTables()`. A test fails if the policy and the wiring disagree in either direction, and another fails if a real column of a replicated table is left undeclared — so a column added later cannot start leaving the box without someone deciding.

---

## Two-Tier Sync

| Tier | Path | Latency | Durability | Built |
|---|---|---|---|---|
| **Hot** | instance ↔ instance CRDT delta exchange over the **LAN fabric** (mDNS + authenticated HTTPS) | low | none (transient) | **real, one domain, LAN only** — `crdtsync` + `sqlcrdt`, wired for `reminders` |
| **Hot (WAN)** | the same exchange via **relay rendezvous** for NAT / cross-location | low | none | **not implemented** — seam stated below |
| **Cold** | periodic **durable checkpoint** to the shared S3 bucket | high | durable | **real** — whole-DB-file `VACUUM INTO` snapshot, not a cross-node merge |

- **Hot path (LAN, real):** each box pulls a delta from a peer, merges it, then pushes back the ops that peer is missing — using the sender's version vector returned on the pull, so the reverse direction costs no extra round trip. Rounds are bounded per peer; a truncated delta is never a correctness problem because merge is idempotent and order-independent.
- **Cold path (real, unchanged):** every instance periodically snapshots its local database file to S3 as the durable record.

The hot path is for *liveness between active peers*; the cold path is for *durability and catch-up*. They complement each other — the hot path doesn't replace the bucket, and the bucket isn't on the critical path for live convergence.

**Redundancy without failover.** For a replicated domain this is now literally true: every instance holds a full mergeable copy, concurrent writes converge with no leader, and there is no split-brain to resolve because there is no authority to lose. For everything *not* on the allow-list, redundancy still rests on the S3 snapshot below — durable, but single-writer-at-a-time in effect.

---

## The WAN seam (not implemented)

The engine is transport-agnostic by construction. `crdtsync.PeerSource` is the entire discovery surface:

```go
type PeerSource interface { Peers(ctx context.Context) ([]SyncPeer, error) }
type SyncPeer struct { InstanceID, BaseURL string; WAN bool }
```

`internal/fabric`'s mDNS `Discoverer` is adapted to it in `cmd/server/crdtsync_wiring.go` via `PeerSourceFunc`. `fabric.RendezvousDiscoverer` — the relay-based WAN discoverer — **already satisfies the same `fabric.Discoverer` interface**, so pointing the engine at it is a wiring change, not an engine change.

What still has to be built before that is safe:

1. **Peer identity.** LAN auth is a single shared `X-Fabric-Auth` secret, which is defensible inside a link-local tunnel and is not a peer identity. A WAN path needs per-instance authentication (fabric already has Ed25519 per-instance keys and a roster; the CRDT endpoints do not use them yet). Until then the engine treats a WAN peer as reachable but not trusted, which is why it does not run over one.
2. **Transport safety, already enforced.** The syncer **fails closed** on WAN peers: with no WAN client configured they are *skipped*, never dialled with the LAN client — which skips certificate verification because it points at link-local addresses. A WAN peer must also be `https`. This mirrors `fabric`'s FABRIC-SSRF-01 reasoning.
3. **NAT traversal / rendezvous liveness** — the relay work tracked separately.

Nothing in the merge, the op algebra, the snapshot path or the allow-list changes for WAN. The convergence properties are transport-independent and already proven.

---

## Snapshot / Compaction (the new work)

**Problem:** the per-node changeset log in the bucket (`nodes/{id}/changes/{ver}.bin`, CLUSTER.md) **grows unbounded**. A brand-new instance that bootstraps by replaying the whole log pays for the cluster's entire history.

**Fix:** periodically write a **compacted snapshot** of the (compacting instance's) local database state to the bucket, and let new instances bootstrap from **snapshot + short changeset tail**. Note this is the **S3 cold path**, which is separate from — and unchanged by — the CRDT hot path above. It still snapshots whichever instance holds the compaction lease rather than a cross-node merge, so "the merged database state" remains aspirational phrasing here for any table outside the replication allow-list. (`crdtsync` has its own, unrelated, in-engine snapshot/compaction for the op log; see the merge section.)

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
