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

> **This answer was true when written (2026-08-15) and is no longer true
> (2026-08-16). It is kept intact, not edited, because the rest of §1 is the
> record of HOW a system reaches this state with every test green — which is the
> part worth re-reading. §1a states what was built, what it does not do, and what
> is still unproven. Read §1 for the diagnosis and §1a for the answer.**

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

## 1a. What was built (2026-08-16): desire and realisation

The finding above is now out of date, and the correction belongs next to it
rather than replacing it — the shape of the mistake is the reusable part.

**The desired/realised split was adopted, as proposed.** Two sets, deliberately
in two tables with two different keys:

| | key | means |
|---|---|---|
| `app_desired` | `app_id` | fleet-level **desire**. What the user asked for. |
| `app_registry` | `(instance_ulid, app_id)` | per-instance **realisation**. What each box managed. |

`app_registry` was not the wrong table; it was the wrong *only* table. Its shape
is exactly right for realisation, which is why it kept its key and its merge and
gained two columns (`realise_state`, `realise_detail`) rather than being replaced.

### Which engine carries it, and why the other's refusal is now correct

**`multiinstance/appsync`**, and `crdtsync/policy.go` now refuses both
`sql:app_registry` and `sql:app_desired` **for the real reason, which is that the
deferral is finally true**. The old refusal said "already replicated by appsync"
and was correct in the abstract while false in fact; the correction is recorded
in the policy entry itself, dated, because a refusal whose premise silently
became true is indistinguishable from one whose premise silently became false.

§2 recommends retiring appsync and folding `app_registry` into `crdtsync`. That
is still the right target and was **not** done here, for reasons that are about
this change rather than about effort:

- `crdtsync` is **LAN-only**; appsync rides fabric's LAN **+ relay rendezvous**.
  Two of a user's boxes that are not on the same LAN is precisely the case "one
  OS" has to cover, and it is the case the app set most needs.
- appsync already carries per-instance Ed25519 identity, roster verification and
  — as of the eviction work an hour earlier — a **revocation check at admission**.
  The desired set inherits all of it rather than growing a parallel trust path.
- The desired set needs an **intent tombstone with an actor**. That is a third
  algebra, and neither crdtsync's per-column LWW nor its `GrowOnly` expresses it.
  Moving it into crdtsync's policy layer alongside `GrowOnly` is what §2's
  consolidation concretely means — and it is a larger change than wiring a
  producer, so it stays the target instead of being done halfway here.

**No third engine was built.** Both tables merge in one transaction off one
changeset, over the transport that already existed: a desire row travels as an
ordinary `AppRegistryEntry` whose `InstanceULID` is the reserved sentinel
`"@fleet"` (`@` is outside the Crockford base32 ULID alphabet, so nothing can
collide, and a test asserts that rather than the comment). Reusing the one wire
row meant `internal/fabric` needed no change at all — and avoided the failure
that actually mattered, which is a second slice that silently does not ride the
transport while every test still passes.

### How uninstall is expressed, and why nothing resurrects

`DesireRemove` writes `desired = 0` — an explicit **tombstone**, never a deleted
row, never vacuumed. Four things hold the line, and the first is the only one
that is about the data model rather than the code:

1. **A removal is a state, not an absence.** Delete the row instead and a peer
   that has not heard about the removal re-sends its copy, the removing box
   merges it back as news, and the app returns on **every sync, forever** — not
   once. Against a tombstone that re-send is an older write and loses. The test
   replays the stale changeset three times, because a design that survives one
   exchange and loses on the next is the bug.
2. **A tie does not resolve "install wins".** That is `app_registry`'s OR-set
   default and it is right there — for a straggler observation. For *intent* it
   is a resurrection bug: two boxes acting on one user action stamp the same
   removal instant, and install-wins means it never lands. Desire breaks ties on
   the actor id alone: deterministic, symmetric, no opinion about polarity.
3. **A local intent is stamped past everything the box has seen**, not at
   `now()`. Clocks between a user's own boxes disagree by more than the seconds
   between two of their actions, and a lagging box's removal would lose LWW to a
   row it had just merged — appearing to work, then silently undone by the next
   sync.
4. **The reconciler removes what the fleet tombstoned**, so a box that was
   offline during the removal loses the app on reconnect rather than re-seeding it.

### What replaces the uninstall quorum for desire, and what it does not

The existing quorum stays exactly where it was, on the **realised** set. It is
**not** applied to desire, and that is a decision rather than an omission:
requiring a majority of a user's own boxes to agree before an uninstall took
effect would mean uninstalling an app three times. Intent is expressed once, by
a person, on one machine.

What guards it instead is **admission**: a desire row is merged only from an
origin that verified — rostered, not revoked, signature good against its
*rostered* key. An unverified changeset's desire rows are **dropped, not merely
uncounted**; with no quorum to withhold from, "does not count" would have meant
"applies". The signing message was extended to cover desire rows of **both**
polarities, because leaving `desired = 1` out — the natural mirror of "installs
are not quorum-gated" — would have left an unauthenticated fleet-wide **install**
primitive open, which is strictly worse than the remote uninstall the quorum was
built for. The extra section is appended only when desire rows exist, so any
changeset a box predating this could produce still signs byte-identically; an
internal test pins those bytes.

**Residual, stated rather than hidden:** a rostered box that is compromised and
not yet revoked can remove apps fleet-wide, because it can say anything the user
could say. That was already true of every intent the fabric carries. Quorum
would not have bounded it either — a compromised box can simply wait for the
user to remove something and corroborate it. It is bounded by revocation, which
now works and is enforced before merge; recorded, via `actor_ulid`; and one
action to undo.

### What a box that cannot realise an app reports

`realise_state = 'failed'` with `realise_detail` set to the installer's own
reason, on a row that **replicates** — a box reporting a failure only to itself
has reported nothing, because the user is standing at a different box.

**There is no architecture-specific branch anywhere in the sync layer, on
purpose.** `InstallFromRegistry` already refuses an unsupported arch before
touching the filesystem and already produces the sentence
(`ArchUnavailableReason`: *"requires amd64; this box is arm64"*). It travels the
same column as a failed download or a bad signature. The moment arch gets its own
path, every other reason an install can fail goes silent again — which is the
failure mode this whole split exists to remove.

### One broken instance cannot uninstall the fleet

Structurally, not by review: `mergeEntry` routes on the sentinel **first**, the
two merges never meet, and there is no code path from a realisation row into
`app_desired`. The `Realiser` interface says the same thing in the type system —
it can be asked what the box HAS and told to change it, and has no way to name
the desired set. A test merges five failure reports from a peer and requires the
fleet desire to be unchanged; the mutation that routes realisation rows into the
desired set kills it.

### What is proven, and where

| | |
|---|---|
| Desire algebra, tombstones, tie-break, clock bump, admission, signature coverage, transport union, reconcile planning | 19 tests in `internal/multiinstance`, **13 mutations killed** |
| Signing-message byte-compatibility with a pre-SYNC-APPS-01 box | internal test pinning the exact bytes — the round-trip test could not have caught this, since both sides run the same function |
| `*appnet.AppStore` satisfies `Realiser`, reads the disk not the table, and `Realise` routes through the **signed** registry path not the arbitrary-URL one | `appsync_realiser_test.go`, 3 mutations killed |
| The producers are **called**, not merely defined, and the reconcile loop is not dead code | `cmd/server/appsync_producers_test.go` (AST call sites), 4 mutations killed |
| The inventory cannot claim `syncs` without producers, or `gap` with them | `sqlcrdt.TestInstalledAppSetHasBothProducers`, inverted from the guard that caught the original defect |

**Not proven on this machine, and it matters:** a real two-box install over a
real fabric connection. `ip netns` is Linux-only and the install path downloads
from a signed registry over the network, so what has been shown is that the
merge, the algebra, the reconciler and the AppStore seam are correct in-process
and on Linux in a `golang:1.25` container — not that box A installing Steam
results in Steam running on box B. That run belongs on a Linux host with two
instances and a populated registry, and it is the remaining evidence gap.

**Two guards found by mutation rather than by reading**, both of the same family
as the defect this work fixes:

- The lagging-clock test read local state, where a local write always wins
  unconditionally. It passed with the property removed. The defect was never that
  the removal fails to apply — it is that it fails to *survive*, which is only
  visible after the peer re-sends.
- The producers are wrapped in closures, so deleting every handler call left the
  producer calls inside the closure bodies where the source scan still found
  them. **Collection is not execution.** The AST test that fixed it then failed
  the same way itself: it delegated to a helper matching only `*ast.Ident`, so
  for `fabricAppSync.Reconcile` it found nothing, iterated an empty gate list and
  passed a loop wrapped in `if false`. A check that iterates nothing passes
  everything.

### What was NOT done

- **Version skew is not reconciled.** A desired version newer than the installed
  one produces no action. Upgrades need their own ordering and rollback story,
  and reinstalling on every skew would make each one a download storm.
- **Apps already on disk are not adopted.** An app the desired set has never
  heard of is left alone — un-adopted, not undesired. Deleting a user's
  pre-existing apps because a table is new would be the worst available reading
  of the directive. Adoption should be an explicit act, and is unbuilt.
- **The desired set is not backfilled.** It starts empty on every box, so an
  upgraded instance does nothing until someone installs or removes something.
  That is the safe default and it also means the feature is invisible until used.
- **The fabric changeset transport is still gated on the shared secret alone**
  (§3.6). Desire rows inherit that door, so the admission argument above is only
  as strong as that transport — the per-peer signature check inside appsync is a
  second gate, not the only one. Closing §3.6's remaining item strengthens this.
- **No UI.** `realise_state`/`realise_detail` replicate and nothing renders them,
  so a user cannot yet see "your arm64 box cannot install this". The data a UI
  needs exists; the UI does not.

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

| Status | Count (2026-08-15 audit) | Count (2026-08-16, after §1a) |
|---|---|---|
| **Syncs** | **5** | **6** |
| Partial | 4 | 5 |
| Exception (deliberate, argued) | 12 | 12 |
| **Gap** (should sync, does not) | **14** | **12** |

**Five of thirty-five states synced at the time of the audit; six do now**, and
the two that moved are the installed app set (§1a) — one to `syncs`, one to
`partial`. Twelve engines exist and four of the original five successes come
from one of them. The problem is not that there are too few engines; it is that
eleven engines carry almost nothing while the general one is allow-listed down
to four tables. **That is unchanged**: §1a fixed a gap by giving an existing
engine a producer, which is not consolidation and should not be mistaken for it.

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
  **Correction (2026-08-16):** the producer was wired into appsync instead, and
  the "strictly less work" claim was wrong on the merits — see §1a. Retiring
  appsync means first giving crdtsync a WAN/rendezvous reach it does not have,
  and the desired set turned out to need a **third** algebra (an intent tombstone
  with an actor) that neither per-column LWW nor `GrowOnly` expresses. So the
  list of things that must move into crdtsync's policy layer is now **two**:
  the uninstall quorum *and* the desire algebra. Retiring appsync remains the
  target; it is a larger change than this paragraph assumed.
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

### 3.6 What was built (2026-08-16), and what it does not do

Steps 2, 3 and 4 above are done for the **crdtsync** transport. Step 1 is done
for that transport and **not** for fabric's. Step 5 is untouched. Details,
because "eviction shipped" would be a larger claim than the code supports.

**The `OR` is gone.** `crdtsync_wiring.go` now composes
`SignedOrBootstrapSecretAuthorizer(signature, secret, roster)` instead of
`AnyOfAuthorizer(secret, signature)`. The old comment argued AnyOf was not a
weakening because a signature is strictly stronger than the secret; that is true
of *granting* access and says nothing about *revoking* it, which was the only
case that mattered. Both the code and the comment were corrected.

**The secret was narrowed, not deleted, and here is what it was load-bearing
for.** Deleting it outright would have been an outage, for two reasons found
before changing anything:

1. `crdtsync/syncer.go`'s `post` signs **WAN requests only** — a LAN peer is
   dialled with `X-Fabric-Auth` and nothing else. Requiring signatures at the
   door without changing the dialling side refuses every legitimate LAN peer.
2. Worse, **nothing in this codebase writes a peer's public key into a local
   roster.** `appsync.SetIdentity` publishes *self's* key; `CloudSyncer` publishes
   peers' keys from the control plane. So a box with no control plane can
   attribute nobody, its roster arm authorises nobody, and the secret was
   carrying 100% of LAN sync.

So the secret now survives exactly one window: **until this box first learns any
peer identity.** The window closes permanently on first enrolment, without a
restart, and a **revoked** peer still counts as an enrolment — otherwise evicting
your only peer would reopen the one credential it still holds. Every error path
answers "window closed", i.e. requires a signature.

**LAN requests are now signed too** (`lanSigningClient`), in addition to the
secret, whenever this box can name the peer it is dialling. That is what lets a
fleet upgrade one box at a time instead of the first upgraded box refusing all
the others.

**Revocation reaches the sync layer, and is now monotonic.** Three un-revoke
paths were live and are closed:

| Path | Was | Now |
|---|---|---|
| `RestoreFromRevocation` | cleared the flag; no production caller | deleted |
| `RotateIdentity` | set `Revoked = false` on self every rotation — a one-call self-pardon, run by the box being revoked | refuses to rotate while revoked |
| `Registry.Upsert` | `revoked = excluded.revoked`, so any writer that did not carry the flag cleared it — **including `CloudSyncer`, whose wire type has no revoked field, on every poll** | latched in SQL, like `store_only` |

The third was the decisive one: without it, revoking a box survived until the
next cloud-sync round and then silently undid itself.

**Eviction now has a trigger.** `Instance.Revoked` was enforced in three places
and set in none — `RevokePeer` had no production caller and `CloudInstance` has
no revoked field, so the control plane could not set it either. An enforcement
path with no trigger is not a control. `VULOS_FABRIC_REVOKED_PEERS` (instance
ULIDs and/or peer keys) is that trigger; it needs no control plane, it is
persisted into the roster at startup so it outlives the variable and reaches
appsync's quorum check, and the in-memory list is what the door consults so a
failed write cannot fail open. `VULOS_FABRIC_PEER_KEYS` is its counterpart, and
exists only because of finding (2) above: a peer nobody can name cannot be
evicted, so an operator with no control plane needs a way to name one.

**Deny dominates allow, structurally.** `TrustedPeer` sweeps the whole roster for
a reason to refuse — both key slots, ignoring the rotation-overlap expiry —
before any allow arm runs. The previous `continue`-on-revoked was correct only
while the roster was the only allow arm; the pin list is a second one.

**Eviction covers the pull direction too.** A revoked instance is dropped from
the dial list. Refusing its requests stops it writing to us and does nothing
about the ops we would otherwise pull from it and merge.

#### What this does NOT do

- **No group re-key.** Stated in §3.1 as the thing that makes eviction
  meaningful, and it is not built. An evicted instance keeps every byte it
  already read, and any shared key material it holds — the fabric secret itself,
  the SSE-C bucket key — remains valid for the rest of the fleet. What shipped
  bounds the **future**: the evicted box can no longer read from or write to a
  peer's replicated domains. It is not retroactive and the UI must not imply it
  is. §3.4's epoch design is still the plan.
- **`VULOS_FABRIC_SECRET` can now be rotated** (FABRIC-SECRET-ROT-01, added
  2026-08-16 — see `roadmap/GROUP-SECRET-ROTATION-AND-REKEY.md`). It was an
  environment variable read once at startup into a single acceptance slot, so
  the only available mechanic was "change it and restart", which partitions a
  fleet that updates one box at a time. It now has a second, inbound-only
  acceptance slot (`VULOS_FABRIC_SECRET_ALSO`) bounded by an absolute deadline
  (`..._ALSO_UNTIL`), which closes **per request against the wall clock** — so
  the old value stops being accepted with no restart and no operator action.
  A two-phase roll (prepare, then commit) keeps every pair of boxes talking in
  both directions throughout.

  Scope, stated so this is not read as more than it is: this rotates the
  **fabric secret only**. The **SSE-C bucket key is still not rotatable at all**
  (`Argon2id(passphrase, salt)` with one salt object inside the bucket it
  protects, no epoch, no per-object key id), and per-instance Ed25519 identities
  are deliberately untouched — `multiinstance.RotateIdentity` and
  `services/devicekey` already own those correctly, and sweeping them into a
  fleet-wide operation would destroy the per-box asymmetry eviction depends on.
  **The group re-key of §3.4 is specified but still not built.**
- **The fabric changeset transport is untouched.** `/api/fabric/changeset` is
  still gated on the shared secret alone (`fabric/handlers.go:43,57,99`). A
  ULID-based revocation check was deliberately **not** added there: the
  transport is unattributable, so a revoked box would simply claim a different
  `OriginULID` and the check would report PASS while stopping nobody. That door
  needs the same treatment crdtsync just got — per-peer signatures on both the
  handler and `fabric/client.go` — and it is a separate change, not a line.
- **Revocation still does not propagate.** It must be entered on each box. The
  roster is deliberately not replicated, so this is by design until the
  `devicekey` monotonic store becomes the single authority (step 4 of §3.5,
  which this work makes possible rather than performs).
- **`services/devicekey` is still not consulted.** The unification in §3.4 —
  one revocation authority instead of two — remains open. What changed is that
  the weaker of the two systems is no longer *wrong*: it is monotonic, it has a
  trigger, and it is enforced at admission.

The test that pins all of it is
`cmd/server.TestRevokedInstanceHoldingTheFabricSecretIsRefused`: restore the
`OR` and it fails with "a caller presenting only VULOS_FABRIC_SECRET got 200,
want 401".

---

## 4. The legitimate exceptions

The directive allows "few exceptions". These are them, decided rather than
inherited. The bar: **an exception is state where syncing would be *wrong*** — a
security defect, or a description of hardware the other box does not have. "We
never got to it" is a gap and appears in §5 instead.

Twelve of thirty-five entries. `TestGapsAreNotQuietlyReclassified` fails if
exceptions ever become the majority, because an inventory where everything is
excepted has stopped applying the directive.

### Category A — identity, which must differ or the fleet cannot count itself

| State | Why |
|---|---|
| Per-instance signing key, `instance.json`, peering identity key | An instance's identity is the one thing that *must* differ, or quorum, attribution and eviction have nothing to distinguish. The strongest exception here. |
| Device key (`<root>/auth/tpm/device_key.priv`, or TPM-sealed) | A device key on two machines stops being a device key. Boxes vouch for one another on the strength of it. Its **revocation record** is the opposite case and correctly does propagate. |

*Caveat found while auditing:* `fabric_instance_key` lives under `<root>/data`,
which the file syncer watches. It is sealed under `VULOS_FABRIC_KEY_HEX`, so
what would cross is ciphertext — but a per-instance key inside a replicated
directory is a placement that should be changed rather than a seal to rely on.

### Category B — credentials whose value is being in few places

| State | Why |
|---|---|
| Login sessions | A bearer token is usable directly; replicating one multiplies the blast radius of any single box, and revoking on one box could not be relied on elsewhere. |
| Recovery blobs, master-key envelopes, API-key hashes | The entire point of an enveloped key is that it exists in few places. |
| `profile_secrets` (AI API key, device PIN hash) | This table exists *so that* `profiles` can replicate. A PIN belongs to a machine someone is standing at; an API key is a bearer secret that can be spent. |
| Passkeys (the private half) | Bound to the authenticator holding them. A syncing passkey is a security defect. **Note the split:** TOTP seeds in the same directory are *not* device-bound and are a gap, not an exception. |

### Category C — descriptions of hardware the other box does not have

| State | Why |
|---|---|
| Storage mode, cgroup slices | Replicating one box's storage mode or CPU/memory limits describes hardware another machine does not have. A correctness refusal, not a security one. |
| WiFi credentials | Written straight to `/etc/wpa_supplicant`, with no Vulos-side record. Also correct on the merits: networks in range differ by location. |

### Category D — already identical, so replication would add risk not value

| State | Why |
|---|---|
| App catalogue (`registry.json`) | Ships with the release and is signed by the release key. Replicating it would let one box's copy overwrite another's — a downgrade path. |
| Calendar and mail | Not on the box: both instances are clients of the same remote service and already see the same data. |
| Contacts (unified view) | Derived in memory from external sources. Replicating a projection creates a second, staler authority for data whose home is elsewhere. |

### Category E — the arguable ones, decided

**Window geometry: exception.** A window rectangle is a statement about a
particular screen. This OS explicitly targets phones as thin clients to the same
box, so replicating a 2560×1440 layout onto a phone produces windows partly or
wholly off-screen. The right shape is per-device-class geometry keyed off a
synced identity, not one global rectangle. Until that exists, not syncing is
correct behaviour rather than a missing feature.

*But the exception is narrow and must stay narrow.* Only the **geometry** is
excepted. Which windows were **open** is not obviously device-specific and is
left as specified work, not claimed as decided.

**Dock arrangement: NOT an exception — it is a gap.** This is the call worth
recording, because dock pins and window geometry look like the same class and
are not. A pinned app is a **choice about what you use**; a window rectangle is
a **fact about a screen**. The mobile dock being deliberately different is an
argument for a *presentation* that differs per device class, not for the
underlying set of pinned apps to differ. Same for wallpaper, theme, desktop
layout and the widget rail: all are choices, none describes hardware, and all
are gaps.

**Password vault: NOT an exception.** It is the one gap that cannot be resolved
by declaring it one. A password manager that exists on one machine is a password
manager people stop using. The **contents** should follow the user; what must
not follow is `vault.key`, the per-device wrapping key. The correct shape is
replicated ciphertext with per-device key wrapping — the same shape the eviction
re-keying in §3.4 needs, which is why they should be built together.

---

## 5. The gaps, ranked by user impact

Fourteen gaps and four partials as audited; **twelve and five** after §1a.
Ranked by what a user loses, not by implementation cost. Each is pinned by name
in `sqlcrdt.TestTheKnownGapsAreStillRecorded`, so removing one requires either
fixing it or arguing it down in the same commit — which is how row 3 below came
to be struck through rather than deleted.

| # | Gap | What the user experiences |
|---|---|---|
| 1 | **Password vault** does not sync, and `<root>/auth` is in **no backup path** | Save a password on one box, it is not on the other. Lose that box and every stored password is gone permanently. |
| 2 | **Joining a cluster installs nothing** (§1 correction, `joinsync/backend.go`) | The wizard reaches 100% "complete" against an empty machine. The progress bar is measuring a readability check. |
| 3 | ~~**Installed app set** does not sync~~ — **FIXED 2026-08-16, see §1a** | Was: install on the laptop box; the other never learns, nothing queued or retried. Now: an install writes a fleet **desire** and a per-box **realisation**, both replicate, and a reconcile loop makes each box match. What remains is narrower and listed in §1a: version skew, adoption of pre-existing installs, and no UI for the failure reason. |
| 4 | **Drive metadata and bytes** do not sync; `files.db` is in no policy at all | Your Drive is a different Drive on each box. A file is not merely unopenable on the other — it is not in the tree. |
| 5 | **Durable backup covers one DB file**, `auth.db`, and is off by default | A restored box comes back with accounts and settings and without reminders, Drive, audit history or passwords. |
| 6 | **TOTP keychain** does not sync and is in no backup | Second-factor codes work at one desk only, and vanish with the box. |
| 7 | **Wallpaper, theme, desktop layout, dock pins, widget rail** are browser localStorage | None of it follows you — and it is not even per-box: open the *same* box in another browser and it is gone. |
| 8 | **Two themes exist**; the replicated one is not the one in use | `profiles.Theme` syncs and the shell neither reads nor writes it. The setting that syncs is not the setting that governs. |
| 9 | **Notification history and DND** do not sync | Dismiss on one box, it still waits on the other. Set DND before bed; the other box notifies anyway. |
| 10 | **appfs sandbox storage** is outside the synced directory | What an app saves for you is on one box or both, depending on which of two storage APIs its author used. |
| 11 | **Launcher visibility and suite selection** do not sync | Hide an app on one box, it is still there on the other. |
| 12 | **Relay and TURN config** are per-box | Point one box at your relay; the others do not know. |
| 13 | **Per-app data syncs while the app does not** | An app's saved data can arrive on a box that cannot open it. |

The top two were the sharpest because both present as *success*: the join wizard
reports completion, and the app-registry replicator reported healthy convergence
of an empty table. Neither surfaced as an error anywhere. The second is fixed
(§1a); **the join wizard is not**, and it is now the sharpest remaining one.

---
