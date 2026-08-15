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
