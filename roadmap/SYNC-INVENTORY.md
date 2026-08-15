# What Actually Syncs

An audit of every piece of OS state against the standing directive:

> **EVERYTHING MUST SYNC, EACH INSTANCE IS ALMOST A DIRECT CLONE OF NEXT WITH FEW EXCEPTIONS**

A user's instances are **one OS**. What they install, configure, theme, arrange
or store on one appears on all of them. Under that directive a state that does
not sync is not a neutral fact — it is an **exception**, and an exception has to
be argued for. "We never got to it" is a gap.

Everything below is established from code, with the file named. `SYNC.md`
describes the CRDT engine and is accurate about it; this document is about the
much larger surface that engine does not cover. Where the two disagree, the
correction is noted here and in `SYNC.md`.

The inventory is also **code**, in `backend/internal/sqlcrdt/osstate.go`, with a
guard in `osstate_test.go`: 35 entries, each naming a real file and a string
that must literally appear in it. A document cannot fail a build; that one can.

---

## 1. The blocking question, answered

The App Hub expansion needs to know whether the installed app set is synced
state before designing what happens when a synced app cannot run on an
instance's architecture. The answer is:

> ## **No. The installed app set does not sync today, in any form, by any mechanism.**

Not partially, not over the LAN only, not when S3 is configured. There is no
path by which one instance learns what another has installed.

### Where the code is

**"Installed" is a filesystem fact, not a record.**

| | |
|---|---|
| `backend/services/appnet/store.go:521` | `func (s *AppStore) Installed()` — a **filesystem scan** (`ScanApps`) over `<root>/apps` and the bundled dirs. There is no query. |
| `backend/services/appnet/store.go:463` | `hasApp` is an `os.Stat` on `<root>/apps/<id>/app.json`. |
| `backend/services/appnet/store.go:199` | `Install` does `MkdirAll` + download + extract. It writes **no** database row anywhere. |
| `backend/services/appnet/store.go:434` | `Uninstall` does `RemoveAll` on the directory. Same — no row. |
| `backend/cmd/server/main.go:3776` | `POST /api/store/install` calls `appStore.Install` and nothing else. `POST /api/store/registry/install` (`main.go:3858`) is the same shape. |

So the installed set lives in `<root>/apps/`. The only replicator that watches
directories is `services/sync`, and it watches exactly two: `<root>/data` and
`<root>/db/browser-profiles` (`backend/services/sync/sync.go:109-111`).
`<root>/apps` is not one of them.

### The part that looks like it works

There **is** a purpose-built replicator for installed apps, and it is
substantial: signed changesets, Ed25519 roster verification, OR-set install/
uninstall semantics, distinct-origin uninstall quorum, generation epochs and a
temporal watermark against replay. It is wired end to end over the fabric
transport — `backend/internal/multiinstance/appsync.go`, merged by
`backend/internal/fabric/fabric.go:283`, constructed at
`backend/cmd/server/routes_newfeatures.go:491` and driven from
`backend/cmd/server/main.go:4413-4627`.

It replicates the `app_registry` table. **Nothing ever writes to that table.**

`AppSync.LocalInstall` (`appsync.go:359`) and `LocalUninstall` (`appsync.go:366`)
are the only local producers. Neither has a caller in any non-test file under
`backend/`. This is not an inference from reading — it is checked by
`sqlcrdt.TestInstalledAppSetHasNoLocalProducer`, which scans **512 non-test Go
files** and fails if one appears.

The only production writer of `app_registry` is `ApplyChangeset` — the path that
merges rows **received from a peer**. Every box will only ever write rows it was
sent, and no box ever originates one. The table is empty on every instance, and
the engine converges it perfectly.

### The circularity, which is the actual defect

`backend/internal/crdtsync/policy.go:135` **refuses** `sql:app_registry` from the
general CRDT engine, with this reason:

> "Already replicated by internal/multiinstance/appsync over the same fabric
> transport. Two engines converging the same table would fight."

The reasoning is correct in the abstract and the conclusion is wrong in fact.
The general engine declines to carry app state because a second engine already
does; the second engine carries a table nobody fills. **Each mechanism defers to
the other and neither moves anything.** That is why this gap survived review —
every individual component is real, wired, and tested, and each one's tests pass.

### What a user experiences

Install an app on your laptop instance and your other instance never learns
about it. Nothing is queued, nothing is retried, no status endpoint reports a
difference. The second box simply does not have the app, and never will.

### What this means for the App Hub design

The architecture-mismatch question — *what happens when a synced app cannot run
on this instance's architecture* — **cannot arise today**, because no app
reaches a second instance to mismatch against. That design is correct to
anticipate the problem, but it is downstream of work that has not been done.

The order is: give the installed set a **record** (a row written on install,
which is the missing half-day of work), then decide **arch-aware placement**.
Doing the placement design first means specifying behaviour for a state
transition the system cannot currently reach. `registry.json` already carries
per-app `arch` (`registry.json`, e.g. `"arch": ["amd64"]`), so the data the
placement design needs exists; what does not exist is the event that would
consult it.

There is a real design choice underneath, and it should be made deliberately
rather than fallen into: **does an installed app set sync as one set, or as a
set-per-instance?** `app_registry` is keyed `(instance_ulid, app_id)`, i.e. a
per-instance inventory that is *visible* fleet-wide — which is the right shape
for "this box has Steam, that one can't", and the wrong shape for "I installed
Steam, put it everywhere". The directive says the latter is the default. The
resolution is a fleet-level **desired set** plus a per-instance **realised set**,
with the arch mismatch reported as a realisation failure against a desired
entry. That distinction is what the parallel agent's design actually needs.

---

## 2. How many sync engines exist

The project's memory records a decentralisation finding: **one shared sync
engine competing with roughly five hand-rolled ones**. That finding still holds
and is **understated**. Counting only mechanisms that move *durable state
between a user's own instances* — excluding transport, discovery, presence,
telemetry and cloud billing — there are **twelve**, in three tiers.

### Tier 1 — general-purpose replicators (4)

These could, in principle, carry arbitrary OS state. They are the candidates for
"the one engine".

| # | Engine | Carries | Granularity / merge | Reach |
|---|---|---|---|---|
| 1 | `internal/crdtsync` + `internal/sqlcrdt` | 4 SQL tables | **CRDT**: per-column LWW over a hybrid logical clock, actor tie-break | LAN only |
| 2 | `internal/multiinstance/appsync` + `internal/fabric` | 1 SQL table (`app_registry`) | **CRDT**: LWW + OR-set + distinct-origin uninstall quorum + generation epochs | LAN + relay rendezvous |
| 3 | `services/sync` | 2 directories (`<root>/data`, browser profiles) | **files**: hash compare, remote wins, loser kept as `.conflict-<node>-<ts>` | S3 |
| 4 | `services/peering/collab` | Yjs documents under `<peering>/collab` | **CRDT**: Yjs binary update blobs over WebSocket | peer-to-peer |

**There are three independent CRDT implementations in this repository**
(crdtsync, appsync, Yjs), sharing no merge code, no peer roster, no clock, and
no authentication model. That is the precise form of the "five hand-rolled"
finding, and it is the number that matters: three separate answers to "who wins"
will diverge in three ways, and the divergences will present as unrelated bugs.

### Tier 2 — single-purpose replicators with their own loops (4)

Each replicates exactly one record type and cannot be pointed at anything else.

| # | Engine | Carries | Wired at |
|---|---|---|---|
| 5 | `services/devicekey/revsync` | device-key revocations | `cmd/server/main.go:3191` |
| 6 | `services/peering/feeds` | signed append-only feeds | `cmd/server/main.go:2862` |
| 7 | `services/files/peershare` | individually shared files | `cmd/server/routes_files_peer.go` |
| 8 | `internal/multiinstance/notifyfanout` | `seen_notifications` dedup | `cmd/server/routes_newfeatures.go:474` |

`services/peering` also propagates group definitions, federation profiles and
proximity drops, but those are **request-driven fan-out** rather than autonomous
replicators — only `feeds.go` among them carries a pull loop — so they are not
counted as engines.

### Tier 3 — bulk copy and backup (4)

One-way, not convergent. These are durability, not sync, and the distinction
matters because three of them are frequently mistaken for sync.

| # | Path | Copies | Note |
|---|---|---|---|
| 9 | `services/sync` Compactor/Restorer | **one** DB file, default `auth.db` | off unless `VULOS_BACKUP_INTERVAL` is set |
| 10 | `services/snapshot` | S3 object snapshots | |
| 11 | `services/vault` | restic backup of `dataDir` | requires `restic` on PATH |
| 12 | `services/joinsync` | nothing — verification only | see §1 correction; installs no data |

`services/kitbackup` is **not** an engine: it assembles a recovery-kit JSON of
static identity and storage credentials. Listed here only because the name
suggests otherwise.

### The number that actually matters

Engine count is a symptom. The measurement that states the problem is
**coverage against the directive**, from the 35-entry inventory in
`backend/internal/sqlcrdt/osstate.go`:

| Status | Count |
|---|---|
| **Syncs** | **5** |
| Partial | 4 |
| Exception (deliberate, argued) | 12 |
| **Gap** (should sync, does not) | **14** |

**Five of thirty-five states sync.** Twelve engines exist and four of the five
successes come from one of them. The problem is not that there are too few
engines; it is that eleven engines carry almost nothing while the general one
is allow-listed down to four tables.

### Which engine should survive

**`crdtsync` + `sqlcrdt`.** It is the only one that is general by construction,
proven convergent against conflicting operations rather than disjoint ones,
fail-closed by allow-list, and already carries per-instance Ed25519 identity.
Every other tier-1 engine is a special case of it:

- **Engine 2 (appsync) should be retired, not fixed.** It duplicates engine 1's
  job for one table, it is the reason engine 1 refuses `app_registry`
  (`crdtsync/policy.go:135`), and it has no producer. Retiring it and adding
  `app_registry` to the allow-list is strictly less work than wiring a producer
  into it — and it removes a whole CRDT implementation. Its **uninstall quorum**
  is the one idea worth keeping and should move into crdtsync's policy layer as
  a domain algebra, alongside the existing `GrowOnly`.
- **Engine 3 (services/sync) should stay**, scoped to bytes. Blobs do not belong
  in a per-column register store. But its *directory list* is the bug: two
  hard-coded directories is why `<root>/apps`, `<root>/auth` and `<root>/db`
  are unreachable.
- **Engine 4 (Yjs) should stay** for live multi-writer documents, where
  character-level intent preservation is worth a second CRDT. It should not be
  extended to OS state.

The consolidation target is therefore **three** engines with clean boundaries —
rows, bytes, live documents — not one, and not twelve.

### A defect found while counting, outside my files

`backend/services/peering/collab.go` and its clients disagree on the wire: the
transport defines a JSON message envelope (`collab:update` carrying a
base64 blob, `collab.go:30`) while a y-websocket client speaks the binary
y-protocol. Two clients on the same `/sync` WebSocket that pick different
encodings will not merge. Reported, not touched — `services/peering` is not
mine.

---

## 3. Evicting a compromised instance

The directive is one robust engine, secure, able to kick out a compromised
instance. This section is the design. The most useful finding first:

> **The machinery is largely built and it is good. The sync layer does not
> honour it, and the admission credential cannot be revoked at all.**

### 3.1 What eviction is not

**Eviction is not an ACL edit.** Three things follow, and every one of them is a
place this system currently gets it wrong or has not decided:

1. **An evicted instance keeps everything it already read.** Nothing can undo
   that — not re-keying, not tombstones, not quorum. If it held a full replica
   of `users`, it holds those bcrypt hashes forever. Eviction bounds the
   *future*, never the past. Any design that implies otherwise is lying to the
   user, and the UI must say "this box can no longer receive or change your
   data" and never "your data has been removed from it".
2. **Shared group keys make eviction meaningless without re-keying.** A member
   evicted from a group whose key it still holds is not evicted; it is
   inconvenienced. Real eviction requires generating a new group key and
   re-wrapping every shared secret for the *remaining* members.
3. **Revocation must be monotonic and enforced at admission.** A CRDT converges
   on whatever peers write. If revocation state is itself replicated through a
   CRDT with an edit primitive, a compromised peer writes its own un-revocation
   and the fleet faithfully converges on it. Revocation must be a grow-only set,
   and it must be checked *before* a peer's bytes are merged, not after.

### 3.2 What exists, and it is better than expected

`services/devicekey` gets all three right, and should be the model:

| Property | Where | Verdict |
|---|---|---|
| Monotonic — no un-revoke path, the set only grows | `services/devicekey/revocation.go:3-6` | correct |
| Persistent store keyed by fingerprint | `revocation.go:206` `RevocationStore` | correct |
| Certs are quorum-signed or self-signed, verified on merge | `revocation.go:146` `Verify(roster, threshold, now)` | correct |
| Propagates between boxes by pull loop, merging only what verifies | `services/devicekey/revsync.go`, wired `cmd/server/main.go:3191` | correct |
| Fail-closed with no roster: entries skipped, never merged | `revsync.go:75` | correct |
| Break-glass rotate/revoke requires fleet quorum | `cmd/server/routes_devicekey_lifecycle.go:22` | correct |

`services/fleetid` supplies the roster, `VerifyQuorum`, and a **default-deny**
policy (`policy.go:91` `DenyAllPolicy`). This is a genuinely well-built
revocation subsystem. The gap is not here.

### 3.3 Where it breaks

**(a) The sync engines do not consult it.** `internal/crdtsync`,
`internal/multiinstance` and `internal/fabric` contain **no reference to
`devicekey`** — the grep is empty. The one revocation system that is monotonic
and propagates is invisible to every replicator.

**(b) There is a second, weaker revocation system, and that is the one the sync
layer uses.** `multiinstance.Instance.Revoked` is a plain bool column
(`internal/multiinstance/registry.go:86`). The roster check does honour it, is
re-read per call, and fails closed (`cmd/server/crdtsync_wiring.go:54-81`) —
that part is right. But:

- **It is never set.** `RevokePeer` and `RestoreFromRevocation`
  (`internal/multiinstance/rotation.go:121,145`) have **zero non-test callers**.
  Enforcement is real; nothing triggers it. There is no operator path to evict
  a box.
- **It is not monotonic.** `RestoreFromRevocation` is an un-revoke primitive.
  It exists in the same codebase as a package whose doc says "revoked forever…
  no un-revoke path". Two revocation systems with opposite disciplines.
- **It does not propagate.** The instances table is deliberately not replicated
  (correctly — `crdtsync/peerauth.go:168` refuses the roster so no peer-facing
  handler can write it). The consequence is that a revocation must be entered
  by hand on every box, and there is no mechanism to do it once.

**(c) The admission credential cannot be revoked. This is the hole.**

`VULOS_FABRIC_SECRET` is a **single bearer secret, identical on every box**
(`internal/multiinstance/appsync.go:26` says so in as many words). And:

- `internal/fabric/handlers.go:43,57,99` gates **every** changeset endpoint on
  that secret **alone**. No roster check at the door.
- `cmd/server/crdtsync_wiring.go:186` composes
  `AnyOfAuthorizer(sharedSecret, PeerKeyAuthorizer(verifier))` — an **OR**. The
  per-peer signature path is sound, but a caller that presents the shared secret
  satisfies a whole scheme on its own and never reaches the roster.

So: **an evicted instance that still holds the fabric secret retains full CRDT
pull and push and full fabric changeset access, no matter what any roster says.**
Marking it `Revoked` changes nothing on the LAN path. And there is **no re-key
path** — grep finds no rotation for `VULOS_FABRIC_SECRET` or for the cluster
passphrase whose Argon2id derivation produces the SSE-C key protecting every S3
object. Re-keying today means editing an environment variable on every surviving
box by hand and re-encrypting the bucket, with no tooling and no coordination.

This is the single most important security finding in this audit: **the system
has a correct revocation subsystem, a correct roster check, and a group bearer
secret that bypasses both.**

### 3.4 The design

**One revocation authority.** Retire `multiinstance.Instance.Revoked` and
`RestoreFromRevocation`; make `devicekey`'s monotonic `RevocationStore` the only
one, and derive the multiinstance roster's revoked flag from it. Deleting the
un-revoke primitive is the important half — an un-revoke that exists will
eventually be called, and quorum-gated re-admission of a *new* key is the safe
way to express "that box is clean now".

**Admission before merge, always.** Both sync transports must consult the
revocation oracle in the handler, before any bytes are parsed or merged. The
oracle already exists as an interface (`fleetid.RevocationOracle`, implemented
at `cmd/server/routes_devicekey_lifecycle.go:79`); it is simply not wired into
`fabric/handlers.go` or the crdtsync authorizer chain.

**Replace the group bearer secret with per-peer identity.** The fabric secret
should become a *bootstrap* credential only — good for joining, never for
ongoing authorisation. Steady-state admission is the Ed25519 per-peer signature
path that already exists and is already tested. Then `AnyOfAuthorizer` becomes
`AllOf` for the roster dimension: a peer must present a valid signature **and**
be rostered **and** not be revoked. That single change turns eviction from
advisory into enforced.

**Epoch the group keys, because some must remain shared.** SSE-C bucket
encryption cannot be per-peer. So:

1. Group keys carry an **epoch number**, monotonically increasing.
2. Eviction increments the epoch and generates a fresh key.
3. The new key is **wrapped once per remaining member** to that member's device
   public key — the wrapping infrastructure this needs is the same one the
   password vault needs (§4), which is why they should be built together.
4. New writes use the new epoch; old objects stay readable at their old epoch
   until re-encrypted lazily.
5. The epoch floor is **monotonic and grow-only**, exactly like the revocation
   set, so a compromised peer cannot roll the fleet back to an epoch it holds
   the key for. `services/signing/epoch.go` already implements a monotonic epoch
   floor and is the precedent to follow.

**Quorum, with the 2-box case named.** Eviction should require the same
`fleetid.VerifyQuorum` threshold as break-glass revocation, so one compromised
box cannot evict the others. But a two-instance fleet cannot form a majority —
`appsync` already special-cases `≤ 2` instances for uninstall quorum. For
eviction the safe resolution is different from uninstall: fall back to
**explicit owner authorisation with a step-up challenge** (`services/stepup`
exists), not to unanimity-minus-one, because "the other box agrees" is exactly
what a compromised other box will say.

**What eviction can never undo, stated in the product.** The evicted box keeps
every byte it already held. Therefore eviction must be *accompanied* by
credential rotation for anything it could read: user passwords, API keys, and
any app token in `profile_secrets`. The eviction flow should present that list —
generated from the sync inventory, which is exactly what an inventory in code is
useful for — and drive the rotations, rather than leaving the user to guess.

### 3.5 Ordering

1. Wire the existing `RevocationOracle` into `fabric/handlers.go` and the
   crdtsync authorizer. Cheap, and it makes the roster mean something.
2. Demote the fabric secret to bootstrap-only; require signature + roster for
   steady state. This closes the hole.
3. Give eviction an operator path at all (`RevokePeer` has no caller today).
4. Unify on the monotonic store; delete `RestoreFromRevocation`.
5. Group-key epochs and re-wrapping. Largest piece, and shares its machinery
   with the password vault.

Steps 1–3 are small and remove the ability of an evicted box to keep syncing.
Steps 4–5 are what make the claim honest.

---
