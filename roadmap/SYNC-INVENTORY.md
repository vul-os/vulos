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
