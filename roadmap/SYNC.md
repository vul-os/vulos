# Multi-Instance Data Sync

How a leaderless, multi-location cluster keeps its data **redundant and load-balanced** across instances — a **two-tier** sync that combines a fast instance↔instance hot path with a durable bucket cold path, and adds **snapshot/compaction** so a fresh instance can bootstrap quickly.

For the underlying S3 model, schema, and conflict copies see CLUSTER.md. For the exclusion lease that guards compaction see COORDINATION.md. For per-app concurrency see CONCURRENCY.md.

> **Scope — read this before reading the rest.** This document describes **one engine** and is accurate about it. It is **not** the answer to "does my OS sync". Measured against the standing directive that everything syncs and each instance is almost a direct clone of the next, **5 of 35 inventoried OS states sync**, 4 are partial, 12 are argued exceptions and 14 are gaps — and there are **twelve** distinct sync mechanisms in this repo, three of them independent CRDT implementations. The installed app set, the Drive, the password vault, the wallpaper and the dock are **not** carried by anything. That audit is **SYNC-INVENTORY.md**, and the inventory is enforced in code at `backend/internal/sqlcrdt/osstate.go`. Added 2026-08-15.

> **Goal.** Make data redundant by construction (no failover election) and let a new or recovering instance join without replaying an unbounded changeset log: bootstrap from a recent snapshot + a short tail.
> **Non-goals.** A primary node. A central database. Hot-replicating the running OS (that's OS-DISTRIBUTION.md).
>
> **Status — 2026-08-10.** A general, pure-Go, leaderless CRDT sync engine now exists, is proven to converge, and is wired end to end **for one domain over the LAN**. That is a real hot path, and it is also a narrow one — read the scope line before treating this as "sync is done".
>
> **What is shipped and proven**
> - **Merge engine — `backend/internal/crdtsync/`.** Per-column last-writer-wins over a hybrid logical clock with a node-id tie-break, plus PN-counters. Merge is commutative, associative and idempotent; each law is asserted by test against *conflicting* ops, not disjoint ones. Convergence is checked on a digest that includes each register's winning stamp, so two replicas showing the same value while disagreeing about who wrote it count as diverged. Coverage includes all 720 orderings of a six-op conflicting set, a randomised property test over shuffled/duplicated/batched delivery (seed reproducible via `VULOS_CRDT_SEED`), duplicate and out-of-order delivery, three-node relay where two nodes never talk, and offline catch-up both by ops and — past the compaction floor — by snapshot.
> - **Change capture — `backend/internal/sqlcrdt/`.** Arbitrary SQLite tables, captured in pure Go via SQLite's own **session extension** (`sqlite3session_diff` against a baseline copy). This is what replaces cr-sqlite: the session extension is compiled into the amalgamation `modernc.org/sqlite` ships, so no `load_extension` is involved. Because it diffs against a baseline it captures writes from **any** connection, and no existing write path had to change.
> - **Transport — `backend/internal/crdtsync/syncer.go`.** Pull-then-push rounds over the existing LAN fabric (mDNS + authenticated HTTPS), gated by the same `X-Fabric-Auth` shared secret, mounted on the same LAN-only mux.
> - **Wired end to end — `backend/cmd/server/crdtsync_wiring.go`,** called from `main.go` at the LAN mux site. **`users`, `profiles`, `reminders` and `acctsec_sensitive_actions`** replicate between boxes: an ordinary SQL `INSERT`/`UPDATE`/`DELETE` on one box appears on another, with concurrent edits to *different columns of the same row* both surviving.
> - **When it actually runs.** The engine shares fabric's LAN-only mux and shared secret, so it runs exactly where the LAN layer runs — i.e. behind **`VULOS_LAN_ENABLE=1`**, which is **on in the shipped systemd unit** (`build.sh`) and **off for a bare `vulos-server` process**. So it is live on a real box and dormant in a plain dev run. `TestCRDTSyncCallSiteIsReachable` pins that gate chain, so it cannot change silently.
>
> **Scope — what this is NOT yet**
> - **Four domains.** `users` (the account, password hash included — withholding it leaves the account present on your second box and unusable there), `profiles` (settings), `reminders`, and the security audit log as a GROW-ONLY set. Everything else is refused, on the record and with reasons, in `crdtsync/policy.go`: `sessions` and the key blobs are usable directly rather than needing a crack, push endpoints are per-device, and `storagemode`/`cgroup_slices` describe hardware a different machine does not have. This is an allow-list: a new table replicates only when someone writes down why.
> - **LAN only.** There is no WAN path. The seam for one is stated below.
> - **The bucket cold path is unchanged** and still snapshots one node's local file.
>
> **Anti-regression note.** The previous hot path (`backend/services/sync/hotpath.go`, removed 2026-07-20) had passing tests, **zero callers, and its route registered on no mux**. Its successor is guarded on both halves:
> - *Behavioural* — `TestStartCRDTSyncRegistersWorkingRoutes` runs the real wiring against a real mux and HTTP server and fails unless `/api/crdt/{pull,push,status}` are mounted **and** authenticated (401 without the secret, 200 with). `GET /api/crdt/sync-status` sits alongside them behind the same authorizer and reports the **loop's** health — round count, the peers the last round actually dialled, and the last error per peer. Mutation-tested by dropping the registration, and separately by registering with a permissive authorizer.
> - *Reachability* — `TestCRDTSyncCallSiteIsReachable` parses `main.go`'s syntax tree and pins the chain of conditions gating the call. A source-text grep cannot tell a call that **runs** from one that merely **exists**; this was verified by wrapping the call in `if false` and in a never-set env flag — both compiled, both left the call text intact, **both passed the textual guard**, and both were caught here.

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
| `profiles` | **yes** | Settings are what people most expect to follow them between their own boxes. Was refused while the row was one JSON blob mixing `AIAPIKey` and `PinHash` with `Theme` and `Locale` — column-level exclusion cannot reach inside a blob. Fixed at the STORAGE layer: credentials now live in `profile_secrets`, which is never bound, so the bytes in the replicated row do not contain them. |
| `users` | **yes** | The account, password hash included. Withholding the hash leaves the account present on your second box and impossible to log into, which pushes people into a second hand-made account with a different password — multiplying weak passwords instead of copies of one strong bcrypt hash, whose cost factor does not weaken with the number of copies. Residual, on the record: more machines a hash can be stolen from. **Needs a reload hook, unlike `reminders`:** `auth.Store` keeps its working set in memory and loads it once at startup, and `Login` iterates that map — so a replicated account sat in `auth.db` and was "not found" to every request until the box restarted. `Bridge.SetOnApplied` now fires when a cycle writes rows on a **peer's** behalf and the wiring calls `authStore.ReloadFromDB`. Covered by `TestTwoBoxes_AccountReachesTheSecondBox`, which separates *replicated* from *usable* and fails on the second claim if the reload is removed. |
| `acctsec_sensitive_actions` | **yes**, grow-only | The audit trail is worth most on the box an attacker did NOT compromise. Safe to replicate only because the algebra removes the edit primitive: tombstones refused locally, from a peer op and from a peer snapshot, and merge keeps the FIRST writer. Its key had to be rebuilt first — a per-box `AUTOINCREMENT` integer meant two machines gave the same key to different events, and a merge silently dropped one. |
| `sessions` | no | Per-**device** auth state. Replicating bearer tokens multiplies the blast radius of any one box, and revoking on one box could not be relied on to revoke elsewhere. A session is usable directly; a bcrypt hash still has to be cracked. |
| `recovery_blobs`, `master_key_blobs`, `local_api_keys` | no | Key material and credentials that are usable as-is. The point of an enveloped key is that it exists in few places. |
| `push_subscriptions` | no | Per-device endpoints and their keys. Meaningless on another box and credential-bearing. |
| `app_registry` | no — **and this refusal is now known to be wrong** | This said "already replicated by `multiinstance/appsync` over the same fabric", and the reasoning that two engines converging one table would fight is sound. The premise is not. **Nothing writes `app_registry`**: `AppSync.LocalInstall`/`LocalUninstall` have no non-test caller anywhere under `backend/`, and `POST /api/store/install` goes to `AppStore.Install`, which creates a directory and writes no row. So this engine declines to carry app state because a second engine already does, and the second engine converges an empty table — each defers to the other and neither moves anything. Corrected 2026-08-15; see SYNC-INVENTORY.md §1. The fix is still "retire the duplicate", but it must be paired with adding `app_registry` here **and** giving it a producer. |
| `storagemode`, `cgroup_slices` | no | Node-local hardware configuration. Boxes deliberately differ; replicating one box's storage mode describes hardware another does not have. This is a correctness refusal, not a security one. |

Approved domains are bound to their table and **explicit column list** in `sqlcrdt.ReplicatedTables()`. A test fails if the policy and the wiring disagree in either direction, and another fails if a real column of a replicated table is left undeclared — so a column added later cannot start leaving the box without someone deciding.

---

## Two-Tier Sync

| Tier | Path | Latency | Durability | Built |
|---|---|---|---|---|
| **Hot** | instance ↔ instance CRDT delta exchange over the **LAN fabric** (mDNS + authenticated HTTPS) | low | none (transient) | **real, four domains, LAN only** — `crdtsync` + `sqlcrdt`, wired for `users`, `profiles`, `reminders` and `acctsec_sensitive_actions` |
| **Hot (WAN)** | the same exchange via **relay rendezvous** for NAT / cross-location | low | none | **authenticated, not reachable** — signed per-instance transport is built and tested (see §1 below); no relay is operated and NAT traversal is not done, so two boxes in different places still cannot find each other |
| **Cold** | periodic **durable checkpoint** to the shared S3 bucket | high | durable | **real, but far narrower than "the database"** — a whole-DB-file `VACUUM INTO` snapshot of **one** file, defaulting to `auth.db`, and **off unless `VULOS_BACKUP_INTERVAL` is set** (`cmd/server/main.go:3711-3723`). Not a cross-node merge. |

- **Hot path (LAN, real):** each box pulls a delta from a peer, merges it, then pushes back the ops that peer is missing — using the sender's version vector returned on the pull, so the reverse direction costs no extra round trip. Rounds are bounded per peer; a truncated delta is never a correctness problem because merge is idempotent and order-independent.
- **Cold path (real, and narrower than this used to imply):** an instance snapshots **one** local database file to S3 — `backupDBPath`, defaulting to `auth.db` — and only when `VULOS_BACKUP_INTERVAL` is set, which it is not by default. "Its local database file" was literally true and read as "the database". `reminders.db`, `files.db`, `accountsecurity.db`, every loose JSON file in `<root>/db`, and the whole of `<root>/auth` (password vault, TOTP, passkeys) are **outside it**. A box restored from this comes back with accounts and settings and without reminders, Drive, audit history or any stored password. Corrected 2026-08-15.

The hot path is for *liveness between active peers*; the cold path is for *durability and catch-up*. They complement each other — the hot path doesn't replace the bucket, and the bucket isn't on the critical path for live convergence.

**Redundancy without failover.** For a replicated domain this is now literally true: every instance holds a full mergeable copy, concurrent writes converge with no leader, and there is no split-brain to resolve because there is no authority to lose.

For everything *not* on the allow-list, this used to say redundancy "still rests on the S3 snapshot below — durable, but single-writer-at-a-time in effect". That was too generous, and the generosity was load-bearing: it implied a safety net under the unreplicated majority. **There is no net under most of it.** The snapshot covers one database file and is off by default (see the cold-path row above), so for `files.db`, `reminders.db`, `<root>/db/*.json` and all of `<root>/auth`, unreplicated means **single-copy** — one disk, no redundancy of any kind. Corrected 2026-08-15.

---

## The WAN seam (authenticated, not reachable)

> Re-measured 2026-08-11. This section used to be titled "not implemented", which
> was true when written and is now too strong: the part everyone assumed was
> missing — per-instance authentication — is built, wired and tested. What is
> genuinely absent is the ability for two boxes in different locations to reach
> each other at all. Keeping the old title would have kept a solved problem on
> the list and hidden the unsolved one behind it.

The engine is transport-agnostic by construction. `crdtsync.PeerSource` is the entire discovery surface:

```go
type PeerSource interface { Peers(ctx context.Context) ([]SyncPeer, error) }
type SyncPeer struct { InstanceID, BaseURL string; WAN bool }
```

`internal/fabric`'s mDNS `Discoverer` is adapted to it in `cmd/server/crdtsync_wiring.go` via `PeerSourceFunc`. `fabric.RendezvousDiscoverer` — the relay-based WAN discoverer — **already satisfies the same `fabric.Discoverer` interface**, so pointing the engine at it is a wiring change, not an engine change.

What still has to be built before that is safe:

1. **Peer identity — RESOLVED, and this entry was stale.** It said "the CRDT endpoints do not use them yet". They do. `crdtsync/peerauth.go` implements per-instance Ed25519 identity end to end: requests are signed, responses are verified and attributed, and membership is checked against the multi-instance roster deny-by-default. `cmd/server/crdtsync_wiring.go` wires it — `NewPeerIdentity(signer)`, `NewPeerVerifier(pub, roster)`, `AnyOfAuthorizer(secret, PeerKeyAuthorizer(verifier))` — so a WAN peer is authenticated by key, not by the shared secret, which never leaves the LAN. It is covered by nine tests in `wansync_test.go` (signed convergence, unrostered refusal, revocation, unattributable response, replay, offline catch-up) and twelve in `peerauth_test.go`. Mutation-tested 2026-08-11: replacing the roster check with `if false` fails `TestWANSyncRefusesAnUnrosteredPeerAndNothingCrosses` AND `TestWANSyncStopsAtRevocation`, so the enforcement is real rather than merely present. Fail-closed throughout: with no `Identity` or no `WANHTTPClient` a WAN peer is SKIPPED, never downgraded to the LAN client (which skips certificate verification because link-local trust comes from the tunnel).

   What remains is REACHABILITY, not identity: getting two boxes in different locations to exchange packets at all — rendezvous relay operation and NAT traversal. That is a deployment and transport problem, and it is the honest content of "WAN is not delivered" everywhere else in this document.
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
