# Node Capability — store-only members (NODE-CAP-01)

**Status.** 🟢 Box side implemented + tested. 🟡 CP side (cloud control plane) is a contract for core, below.

A personal device — a laptop, a desktop — can join a user's cluster as a **connected member that syncs data but never serves routed traffic.** It replicates the account, participates in presence and CRDT sync, and shows up in the fleet as online — but the relay never routes client traffic to it, the CP never advertises it, and it is never billed or health-checked as an ingress endpoint.

This is "**connected member, not ingress target**" as a first-class node state.

---

## Why this didn't exist

The node model had no *serving-capability* axis. The typed axes were all orthogonal to it:

| Axis | Values | Meaning |
|---|---|---|
| `Role` | `owner` / `peer` | relationship to the account |
| `Kind` | `device` / `cloud` | origin (BYO vs Fly-provisioned) |
| `Status` | `online` / `offline` / `unknown` | reachability |
| `NodeMode` / `DomainMode` | `server`/`local`, `fabric`/`direct`/`own`/`local` | **process-env only — not on the instance row, not replicated** |

So the intent half-existed as `local` mode, but no peer, relay, or CP could ever *see* it. And "member ⟹ route target" was baked into the routing code, which advertised **every** registry instance, gated only by `Status==online && EndpointURL!=""`. **Endpoint presence *was* the serving signal** — a synced laptop that ever got an endpoint became routable.

---

## The state

One field on the instance row: **`StoreOnly bool`**.

Modeled as `StoreOnly` (not `Serves`) on purpose: **the zero value `false` means "serves normally."** Every existing code path that builds an `Instance` without touching the field keeps serving, and every existing DB row defaults to `0`. The safe default is structural, not something each call site has to remember. A box becomes store-only **only by explicit opt-in** — never derived from `local` mode, because a single-box local install is its own only server and must keep serving itself.

`StoreOnly` is deliberately **decoupled from presence**: a store-only member still goes `online` and still syncs. What changes is only whether it is a *route target*. That decoupling is the whole point — it did not exist before.

---

## Box side — implemented (this repo)

All in `backend/internal/multiinstance` unless noted. Field name `store_only` throughout.

| Change | File |
|---|---|
| `store_only INTEGER NOT NULL DEFAULT 0` (additive migration) | `migrations/0002_store_only.sql` |
| `Instance.StoreOnly` + Upsert / Get / List / `scanInstance` carry it | `registry.go` |
| `SetStoreOnly(ulid, bool)` — targeted UPDATE, returns row, `ErrNotFound` on unknown | `registry.go` |
| `storeOnlyEnv()` — reads `VULOS_STORE_ONLY` (explicit opt-in only) | `registry.go` |
| **Routing filter** — `BuildTable` skips store-only in both the pinned and expand-all branches | `router.go` |
| **Fan-out gate** — `fanOutNow` excludes store-only even when online+endpoint | `notifyfanout.go` |
| `CloudInstance.StoreOnly` wire field + `cloudInstanceToLocal` mapping | `cloudsync.go` |
| **Owner-authoritative preserve** — `Upsert`'s `ON CONFLICT` keeps an existing **owner** row's `store_only` in SQL (`CASE WHEN instances.role='owner' …`), so a sync/rotation/identity upsert can never clobber a locally-set owner store-only. Done in SQL, so it's atomic (no read-then-write TOCTOU vs a concurrent `SetStoreOnly`). Only `SetStoreOnly` changes an existing owner's value. | `registry.go` |
| Seed the flag from `VULOS_STORE_ONLY` on the box's **first** self-registration (later refreshes preserve the operator's Settings choice) | `appsync.go` |
| `PATCH /api/instances/{ulid}/store-only` (admin-gated) | `cmd/server/routes_instances_manage.go` |
| Per-instance toggle + "Sync-only" badge (Instances dashboard panel, admin/owner) | `src/builtin/dashboard/InstancesPanel.tsx` |

Tests: `store_only_test.go` — default-is-serving, persistence, setter, routing exclusion (both branches), fan-out exclusion, wire round-trip. The migration-fold equivalence proof (`migrate_equiv_test.go`) mirrors the new column.

**Two ways to set it:**
- **Instances dashboard toggle** (GUI) — a per-card "Make sync-only / Make serving" button (labelled **"Sync-only"** in the UI, not "store-only") → `PATCH /api/instances/{ulid}/store-only` → `SetStoreOnly`. Toggling **this box (owner)** takes effect immediately because the box builds its own routing table from the local registry. Toggling a **remote peer** is a local-view change until the CP round-trips the flag (see the contract below). It is a fleet-panel control, not an owner-only Settings pane.
- **`VULOS_STORE_ONLY=1`** (headless) → seeded on first self-registration. Documented in `docs/CONFIGURATION.md`.

> **UI vocabulary:** the wire/DB/Go identifier is `store_only`; the **user-facing label is "Sync-only."** Keep operator-facing copy (including the CP admin UI) on "Sync-only" so the OS dashboard and the CP don't show two names for one state.

---

## CP side — contract for core (cloud control plane)

The box already honors its own flag **locally**. For a store-only choice to be honored **cluster-wide** — so *other* members, the relay, and DNS stop treating the laptop as reachable — the flag must round-trip through the control plane.

**What exists box-side today:** only the **pull** direction — `cloudInstanceToLocal` reads `store_only` from `GET /api/instances`, so the box learns a *peer's* store-only state once the CP echoes it. **What does NOT exist yet (either repo):** the **push** direction — nothing reports a *local* `store_only` change (owner toggle / `VULOS_STORE_ONLY`) up to the CP. Until push exists, a box's own store-only choice never reaches the CP, so peers can't learn it. Closing this needs work on **both** sides:

- **Box side (this repo, still TODO):** on `SetStoreOnly` for the owner, POST/PATCH the change to a CP endpoint (there is no outbound instance-state write in `cloudsync.go` today — it is GET-only).
- **CP side (core):**
  1. **Receive + persist** `store_only` per instance (define the box→CP write endpoint the box will call above).
  2. **Echo** it in `GET /api/instances` (the pull wire field already exists box-side; a CP that omits it yields `false` = serving, the safe default — additive, not breaking).
  3. **Exclude** store-only instances from any CP-built routing table / DNS advertisement (the CP's analogue of `router.go`'s filter).
  4. **Skip ingress health-checks** for store-only members — do not alarm on a member never meant to accept traffic.
  5. **Do not bill** a store-only member as a serving/hosting instance. (Open: what, if anything, is still metered — sync bandwidth / storage — is a CP decision.)
  6. **Admin UI:** surface the state **read-only**, labelled **"Sync-only"** to match the OS dashboard. The box owns the truth (see below); the CP reflects it.

Relay (core's, out of this repo's scope) needs no new logic if it routes purely from the CP-advertised table — dropping store-only members upstream is sufficient. If the relay has an independent target list, it needs the same filter.

---

## Source-of-truth rule

**The box owns the truth; the CP respects and displays it.** A personal laptop authoritatively decides "I'm private, don't route to me" — the CP cannot make a laptop serve traffic it refuses to serve, and this matches the existing model where a node's `local` posture is self-declared. Flow: **box (dashboard toggle / `VULOS_STORE_ONLY`) → [box→CP push, still TODO] → CP (persist + echo) → routing / DNS / billing honor it.**

v1 is **box-writes / CP-reflects.** A CP-initiated "please go private" that the box confirms on next sync is a clean v2 if a fleet admin ever needs to set it remotely — but avoid two writers without a reconciliation rule.

Because the owner row is box-authoritative, CP sync (`upsertFromCloud`) preserves the owner's local `store_only` rather than overwriting it — otherwise a CP that hasn't implemented its side would silently revert the owner's choice on the next resync. Peer rows stay CP-authoritative; a peer marked store-only from another box's dashboard is local-view-only until the CP round-trips the flag (the acknowledged gap above).

---

## Rollout safety

- Additive column, default `0`. Existing rows and existing code = serving. **Zero behavior change on upgrade.**
- A CP that predates the wire field omits it → `false` → serving. **No coordinated deploy required**; box and CP can ship in either order.
- The only way to reach the new state is explicit opt-in, so nothing becomes store-only by accident.

---

## Known gaps / follow-ups

Recorded so they aren't mistaken for "done."

- **box→CP push is unbuilt** (both repos) — see the contract above. This is the single biggest hole: a box's own store-only choice does not propagate cluster-wide until it exists.
- **Last-serving-box footgun** — nothing warns when the owner marks its *only* serving box sync-only, which zeroes `/api/routing/apps` for every app account-wide (remote reach to this box's apps stops). `Delete` has an `ErrIsOwner` guard for the analogous case; `SetStoreOnly` / the toggle have none. Add a confirm (and/or a "no serving instance left" warning) before this ships to real multi-box users.
- **No self-serving-state on Box Health** — `BoxHealthPanel` doesn't show this box's own serving/sync-only state; an operator has to open the Instances panel and find their own card.
- **Setup wizard is silent** — the "Join an existing system" step is the natural place to ask "serve traffic, or just sync?"; today the only pre-boot path is `VULOS_STORE_ONLY`.
- **Pre-existing (not NODE-CAP-01):** `router.go`'s `BuildTable` advertises an FQDN for every non-store-only instance regardless of `Status`/`EndpointURL` — an offline or endpoint-less instance still appears in `/api/routing/apps`. The `Status==online && EndpointURL!=""` gate lives only in `notifyfanout.go`. Out of scope here; noted so it isn't attributed to this change.
- **v2** — a CP-initiated "please go private" (fleet admin sets a peer remotely) would reuse `PATCH /api/instances/{ulid}/store-only` semantics but needs the box to confirm on next sync; specify before building.
