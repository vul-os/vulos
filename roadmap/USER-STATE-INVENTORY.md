# Where the user's own state lives, and whether it can follow them

Every `localStorage` / `sessionStorage` site in `frontend/src/**`, with a verdict
against the standing directive:

> **EVERYTHING MUST SYNC, EACH INSTANCE IS ALMOST A DIRECT CLONE OF NEXT WITH FEW EXCEPTIONS**

`roadmap/SYNC-INVENTORY.md` audits the OS's *server-side* state. This document
audits the half that never reaches a server at all, which is where every
customization feature built this week landed.

The verdicts are written down **before** the migration so the decisions are
reviewable separately from the code that acts on them. Three verdicts only:

| verdict | meaning |
|---|---|
| **SYNC** | follows the user to every instance. The default. Needs no argument. |
| **PER-BOX** | an **exception**, and an exception has to be argued for. "We never got to it" is a gap, not a justification. |
| **EPHEMERAL** | not state a user would notice losing — a recency list that rebuilds itself, an in-flight queue, a latch for a transient mode. |

**Counts: 15 SYNC, 5 PER-BOX, 4 EPHEMERAL — 24 entries.**

Of the 15 SYNC entries, **9 are migrated by this pass** and 6 are not; each
unmigrated one names what it is waiting on. Nothing here is claimed as syncing
because it *should*.

---

## 1. The defect, stated precisely

`localStorage` is not per-user and not per-box. It is **per browser profile**.
So the state below did not merely fail to reach a second instance:

> Open the **same box** in a second browser — or in a private window, or after
> clearing site data — and your wallpaper, theme, accent, dock pins, desktop
> arrangement and widget rail are gone. Not stale. Gone.

That is a strictly worse failure than "does not sync", and it applies to
everything in the SYNC table below.

---

## 2. SYNC — 15 entries

`bag key` is the key inside `Profile.Settings`, the free-form per-user
preference map that rides the replicated `profiles` row
(`backend/services/auth/profile_settings.go`).

| # | localStorage key(s) | written by | read by | bag key(s) | migrated |
|---|---|---|---|---|---|
| 1 | `vulos-theme` | `core/ThemeProvider.tsx` | `ThemeProvider`, `main.tsx` (pre-paint) | `profile.theme` — reserved, see §9 | **yes**, onto the `profiles.theme` COLUMN |
| 2 | `vulos-accent` | `core/ThemeProvider.tsx` | `ThemeProvider` | `shell.accent` | **yes** |
| 3 | `vulos-nightshift`, `-from`, `-to`, `-warmth` | `core/ThemeProvider.tsx` | `ThemeProvider` | `shell.nightshift`, `shell.nightshift.from`, `shell.nightshift.to`, `shell.nightshift.warmth` | **yes** |
| 4 | `vulos-schedule-dark`, `vulos-schedule-light` | `core/ThemeProvider.tsx` | `ThemeProvider` | `shell.schedule.dark`, `shell.schedule.light` | **yes** |
| 5 | `vulos-wallpaper` | `core/Settings.tsx` → `core/useWallpaper.tsx` | `layouts/DesktopCanvas.tsx`, `Settings.tsx` | `shell.wallpaper` | **partly** — see §5 |
| 6 | `vulos-dock-pins` | `shell/Dock.tsx` | `shell/Dock.tsx` | `shell.dock.pins` | **yes** |
| 7 | `vulos.desktop.layout` | `desktop/store.ts` | `desktop/store.ts`, `ThemeProvider` (accent token) | `shell.desktop.preset`, `shell.desktop.controls`, `shell.desktop.dock.desktop`, `shell.desktop.dock.mobile`, `shell.desktop.tokens` | **yes** — see §6 |
| 8 | `vulos.desktop.packs` | `desktop/store.ts` | `desktop/store.ts` | — | **no** — see §7 |
| 9 | `vulos.widgets.layout.v1` | `widgets/layout.ts` | `widgets/layout.ts`, `core/settings/WidgetsPanel.tsx` | `shell.widgets.count`, `shell.widgets.<i>` | **yes** — see §6 |
| 10 | `vulos.density` | `core/Settings.tsx` | `main.tsx` (pre-paint), `Settings.tsx` | `shell.density` | **yes** |
| 11 | `vulos.notifications.prefs.v1` | `core/notificationStore.ts` | `core/notificationStore.ts` | `shell.notifications.prefs` | **yes** |
| 12 | `vulos-ai-firstrun-done` | `core/AIFirstRun.tsx` | `core/AIFirstRun.tsx` | `shell.ai.firstrun` | **yes** |
| 13 | `terminal-prefs` | `builtin/terminal/Terminal.tsx` | same | — | **no** — see §7 |
| 14 | `vulos.appdata.<appId>::<key>` | `core/AppBridge.ts` (sandboxed apps) | same | — | **no** — see §7 |
| 15 | `vulos.widget.<instanceId>::<key>` | `widgets/storage.ts` (sandboxed widgets) | same | — | **no** — see §7 |

Entry 12 deserves a sentence, because it is the smallest one and the most
directly on the directive's nose: a one-time "here is your AI assistant"
introduction stored per browser means a user with two instances is introduced to
their assistant twice, and a user who clears site data is introduced to it
again. One key fixes it.

---

## 3. PER-BOX — 5 entries, each with its argument

| # | key | owner | why it is an exception |
|---|---|---|---|
| 16 | `vulos-shell-state` | `providers/ShellProvider.tsx` | **Window geometry is a statement about a particular screen.** Replicating a 2560×1440 arrangement onto a phone — which this OS explicitly targets as a thin client to the same box — puts windows partly or wholly off-screen. The right shape is per-device-class geometry keyed off a synced identity; until that exists, not syncing is correct behaviour rather than a missing feature. Already argued at `backend/internal/sqlcrdt/osstate.go` as `StatusException`. **Narrow:** the *geometry* is excepted; the *set of open windows* is not obviously so, and is left as specified work rather than claimed as decided. |
| 17 | `vulos.biometric.unlock` | `core/settings/DevicePanel.tsx`, `auth/LockScreen.tsx`, `auth/OfflineLockScreen.tsx` | An enrolment is a property of **this device's sensor**. Syncing "biometric unlock is on" to a box with no reader turns an unlock preference into a lockout, and syncing it to a box with a *different person's* finger enrolled is worse than that. |
| 18 | `vulos.location.share` | `core/settings/LocationPanel.tsx`, `App.tsx` | Consent to use **this device's** geolocation. Consent granted to a laptop in a house is not consent granted to a box in an office; a synced "on" would start a sensor the user never authorised on that machine. |
| 19 | `vulos.os.endpoints.v1` | `lib/net/endpoints.ts`, `components/OfflineIndicator.tsx` | A reachability cache — the last-known-good cloud↔LAN address pair **for this client's network position**. Another box's LAN address is not merely useless here, it is a failover target that will always fail. |
| 20 | `vulos.notifications.log.v1` | `core/notificationStore.ts` | A *log*, not a preference. It is unbounded-ish (`MAX_ITEMS`), it is not something a user configures, and its correct home is the box's own `<root>/db/notifications.json` — which `SYNC-INVENTORY.md` already records as a **gap**. Putting a notification history into the profile CRDT register would make every notification a rewrite of the register that carries the user's whole profile. **This is an exception to the mechanism, not to the directive:** the history should sync, via the backend store, not via here. |

---

## 4. EPHEMERAL — 4 entries

| # | key | owner | why |
|---|---|---|---|
| 21 | `vulos-spotlight-recent` | `shell/Spotlight.tsx` | Eight app ids, derived from usage, capped, and fully rebuilt within a few minutes of use. Losing it costs a user nothing they can name. |
| 22 | `vulos-cmdk-recent` | `shell/CommandPalette.tsx` | Same shape, same argument. |
| 23 | `vulos.driving.muted` | `core/useDrivingMode.ts` | A latch recording that driving mode muted notifications, so leaving driving mode can unmute them. It describes a transient mode on the device that is *moving*. Syncing it would mute a stationary box. |
| 24 | `vulos.os.offlineQueue.v1` | `lib/offlineQueue.ts` | In-flight writes waiting to be flushed. Replicating a queue of pending writes to a second box would **apply them twice**. |

Two sites are deliberately *not* in this table:

- `lib/masterKey.ts` — comments about storage only; it states that the key is
  never placed in `localStorage`/`sessionStorage`, and it isn't. Nothing to fix.
- `lib/offline/index.js` `SNAP_KEY` — IndexedDB, not web storage.

---

## 5. Wallpaper: what syncs and what cannot

`vulos-wallpaper` holds **whatever `setWallpaper` was handed**, and today the
only producer is `core/Settings.tsx`'s file picker, which calls
`FileReader.readAsDataURL`. So in practice the stored value is a **`data:` URI
of the whole image** — megabytes.

That value cannot ride the preference bag, and the limit is not the reason:

- `MaxSettingValueLen` is 512 bytes (`profile_settings.go`).
- Raising it would not fix anything. The bag is **one CRDT register**
  (`profiles.data`). Every wallpaper change would rewrite the register that
  carries the user's entire profile, and ship a multi-megabyte payload to every
  instance on every change.

So the honest split, and what the code now does:

| the wallpaper is… | behaviour |
|---|---|
| a **reference** ≤512 bytes — a path like `/vulos.png`, a URL, a gradient | **syncs**, as `shell.wallpaper` |
| an **uploaded image** (`data:` URI) | stays in this browser, and the shell **deletes** `shell.wallpaper` server-side rather than leaving a stale value that would overwrite it on the next load |

That deletion is load-bearing. Without it the sequence is: user uploads a photo
(local only) → reload → hydrate applies the older *server* value → the photo the
user just chose is silently replaced. Deleting the key instead makes the state
consistent and true: *the OS-wide wallpaper is unset; this browser has a local
one.*

**The remedy this is waiting on** is a replicated byte store reachable from the
shell. There is not one: `<root>/data` is watched by the file syncer but has no
HTTP write path from the shell and is gated on S3; `appfs` (`/api/appdata`) sits
outside every watched directory and is itself a gap; Drive metadata does not
replicate at all. Building that store is backend work outside this pass. Until
it exists, an uploaded wallpaper is per-browser, and the OS says so rather than
pretending.

---

## 6. Why composite state is decomposed rather than stored as a blob

Measured (`JSON.stringify` of realistic values, against the 512-byte cap):

| value | bytes |
|---|---|
| `DesktopLayout`, 12 desktop dock items, 4 tokens | **611** |
| widget rail, 5 instances | **827** |
| widget rail, 12 instances | **1947** |
| widget rail, 24 instances (`MAX_INSTANCES`) | **3867** |
| dock pins, 10 ids | 101 |

Neither composite fits. Two ways out; only one is right.

**Rejected: chunk the blob across `shell.x.0`, `shell.x.1`, …** It invents a
framing format nobody documented, and a reassembly bug produces a *plausible but
wrong* layout rather than an obvious failure.

**Chosen: decompose along the seams the data already has.**

- **Desktop layout** → 5 keys: `preset`, `windowControls`, `dock.desktop`,
  `dock.mobile`, `tokens`. These are the model's own fields. Each is well under
  the cap, and reassembly is fed straight back through the **existing**
  `validateLayout()` — so a tampered or truncated value degrades to stock
  exactly as a tampered `localStorage` value already does. No new trust boundary.
- **Widget rail** → `shell.widgets.count` plus one key per placement. Each
  placement is re-checked by the **existing** `reconcileInstance()`, which
  already drops unknown widgets, clamps sizes and intersects grants against the
  manifest. A placement that arrives from another box gets exactly the same
  treatment as one read back from disk.

Key budget against `MaxSettingKeys` = 64:

```
theme family      8
wallpaper         1
dock pins         1
density           1
notification prefs 1
AI first-run      1
desktop layout    5
widget rail       1 + up to 24
                 ---
worst case       44 of 64
```

---

## 7. SYNC entries this pass does not migrate, and what each waits on

| entry | waiting on |
|---|---|
| 8 — `vulos.desktop.packs` | Third-party layout packs are **install artifacts**, not preferences: a pack manifest is a document, several of them are a list of documents, and their peer is the installed-app set (`app_desired`/`app_registry`), not a string map. Syncing them belongs with app install, which another agent owns. |
| 13 — `terminal-prefs` | An app-owned preference in a *builtin* app that does not go through `AppBridge`. It wants the same fix as entry 14, and inventing a second private path for one app would be the wrong shape. |
| 14 — `vulos.appdata.<appId>::<key>` | Sandboxed-app storage, quota'd at 256 KB per app. This is app **data**, and `SYNC-INVENTORY.md` already tracks its server-side counterparts (`appfs` = gap, `<root>/data` = partial). It needs a replicated app-data store, not a preference bag. |
| 15 — `vulos.widget.<instanceId>::<key>` | Same class as 14, quota'd at 64 KB per placement. Note the ordering consequence: this pass syncs *which widgets are in your rail*, so a widget arrives on the second box **empty** rather than absent. That is a strict improvement and it is not the finished state. |

---

## 8. Source of truth, cache, and what happens offline

This is the distinction the defect was really about, so it is stated flatly.

**Before:** `localStorage` was the **source of truth**. Nothing else held the
value; there was nothing to reconcile against.

**After:** the **box** is the source of truth — `Profile.Settings` on the
replicated `profiles` row. `localStorage` is a **cache**, and it is not
optional: `main.tsx` stamps `data-theme` and `data-density` onto `<html>`
*before React mounts*, long before a profile can be fetched. Without a
synchronous local copy every reload would flash the wrong theme.

The reconciliation rule, in `core/syncedPrefs.ts`:

1. **First hydrate for a user session** — for each key: if the server has a
   value it wins; if the server has **nothing** and the cache does, the cache's
   value is **adopted** (applied locally *and* pushed up). This is the one-time
   migration for every box that already has real values today.
2. **Every hydrate after that** — the server wins, unconditionally. An absent
   server key means *unset*, and clears the cache. Adoption does **not** re-run,
   because if it did, deleting a preference on box A would be resurrected by box
   B's stale cache on its next load.
3. Adoption is idempotent by construction rather than by a flag: after step 1 the
   server *has* the key, so step 1 produces an empty patch on every subsequent
   boot. Hydrating does not write to the server unless something is genuinely
   missing, so it does not fight the sync engine on reload.

**An offline write:** `AuthProvider.updateProfile` performs no server write when
the session is offline. A preference changed then is applied to the cache
immediately (the UI is correct at once) and the patch is appended to a **pending
queue**, itself mirrored to `localStorage` under `vulos.prefs.pending.v1` so it
survives a reload. At the next hydrate the pending queue is replayed **over** the
server's values — a write the user made later beats a value the server has held
since before — and is cleared once the server accepts it. The failure mode this
avoids is the obvious one: changing your theme on a plane and finding it reverted
on landing.

**What is still not solved:** a second box learns of a change when it next loads
or refetches the profile. There is no push. Two boxes both online do not converge
in real time; they converge on next load. That is the profile row's existing
behaviour and this pass does not change it.

---

## 9. The two themes

`SYNC-INVENTORY.md` recorded that the theme which syncs is not the theme the
shell reads:

- `profiles.Theme` — a real column on the replicated profile row, documented
  `"dark" | "light" | "auto"`, decoded by `handleUpdateProfile`. Nothing in
  `frontend/src/` read it or wrote it.
- `vulos-theme` — a `localStorage` key that `ThemeProvider` reads and writes,
  and that `main.tsx` applies before first paint. This is the one that governed
  what a user saw.

**Resolved in favour of `profiles.Theme`.** It already exists, already
replicates, already has a wire field and a typed decode. `vulos-theme` is
**demoted to the pre-paint cache** — still written, still read by `main.tsx`, no
longer authoritative.

Theme is nonetheless routed through the same engine as everything else, under
the **reserved key `profile.theme`**. It is not a `Settings` entry and never
reaches the map — `SyncedPrefsBridge` splits it back out into the PUT's
top-level `theme` field at the wire. The reason for the indirection is that
adoption, the offline queue and the retry rule are worth having for theme too,
and a second mechanism beside them would be a second set of bugs.

**Who lost, and what was checked before demoting it:** `vulos-theme` has exactly
two readers, `core/ThemeProvider.tsx` and `main.tsx`
(`getInitialResolvedTheme`), and one writer, `ThemeProvider`. The key is not
deleted — deleting it would reintroduce the flash-of-wrong-theme on every reload
that it was added to prevent.

One widening: the shell's theme has a fourth value, `'schedule'`, that the
column's comment does not list. The column is a free string with no server-side
validation, so it carries it. The comment on `Profile.Theme` is updated to say
so rather than left to describe three of four cases.

The rest of the theme family — accent, night shift, the schedule times — has no
column and goes in the bag.
