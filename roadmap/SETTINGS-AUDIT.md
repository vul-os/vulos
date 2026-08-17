# Every control in Settings, and whether the OS keeps its promise

`frontend/src/core/Settings.tsx` is 3,964 lines and had never been audited as a
whole. This document is the inventory, written **before** the fixes so the
findings are reviewable separately from the changes that act on them.

Scope: the 22 panels defined *in* `Settings.tsx`. The 16 panels it delegates to
`core/settings/*.tsx` are audited in §8 and are a separate surface.

Four verdicts, per the brief:

| verdict | meaning |
|---|---|
| **REAL** | writes something that takes effect, and the effect was verified |
| **HOLLOW** | renders a control that writes nowhere, writes a value nothing reads, **or performs a write whose outcome is invisible** |
| **STALE** | refers to a feature that moved, was retired, or never shipped |
| **DANGEROUS** | destructive with no confirmation, or misrepresents what it does |

**Counts — 84 controls: 76 REAL, 4 HOLLOW, 4 DANGEROUS. Plus 5 STALE claims and
12 fabricated readouts, tabled separately in §4 and §5 because they are not
controls.**

That ratio is the headline, and it is not the flattering reading it looks like.
Settings has been hardened twice already — `apiGet`/`apiSend`/`useApiError` exist
in this file precisely because a previous pass found that a dozen sections could
not tell a reply from a refusal. The *writes* are in good shape. What was never
audited is the other half: **what this screen asserts when it does not know.**
Eleven of the sixteen defects below are on the reading side, not the writing
side, and every one of them draws an absent value as a confident fact.

---

## 1. The four HOLLOW controls, named

A hollow control is worse than a missing one: it makes a promise the OS does not
keep, and the user has no way to tell. All four here are the same shape — the
write happens (or does not), and the screen looks identical either way.

### H1 — AI Assistant → "Save changes" (`AISettings`, ~line 590)

```ts
const save = () => updateProfile({ ai_provider: provider, ai_model: model, ai_api_key: apiKey || undefined })
```

The result is **discarded**. `updateProfile` is typed
`Promise<'ok' | 'rejected' | 'unreachable'>` and this call site keeps none of it.
There is no `saving`, no `saved`, no error slot — clicking "Save changes"
produces no change on screen whatsoever, whether the box accepted the patch,
refused it with a 403, or was never reached.

The offline case is the sharp one. `AuthProvider.updateProfile` opens with:

```ts
if (!user || offlineMode) return 'unreachable' // no server writes in an offline session
```

So in an offline session this button **writes nothing at all** and is
indistinguishable from a button that worked. The user has just typed an API key
into a field labelled "Stored on your box" and been told nothing.

Every other save in this file (`AccountSettings`, `NetworkSettings`,
`TURNSettingsSection`, `StorageModeSettings`, `NET9_ConnectionModeSettings`) has
a saving/saved/error cycle. This is the one that has none.

### H2 — AI Apps → "Delete" (`AIAppsSettings.remove`, ~line 2911)

```ts
const r = await fetch(`/api/ai-apps/${id}`, { method: 'DELETE' })
if (r.status === 503) { setEditDisabled(true); return }
if (versionsOpen === id) setVersionsOpen(null)
refresh()
```

Exactly one status is read. A 403, a 404 or a 500 falls through to `refresh()`,
which re-reads a list where the app is **still present** — and says nothing. The
user clicks Delete, watches the row stay put, and has no way to learn whether
the box refused or the click missed. This is the `fetch(…).then(refresh)` shape
that the rest of this file was already converted away from; this call site was
missed.

(It is also DANGEROUS — see D3.)

### H3 — AI Apps → "Restore" (`AIAppVersions`, ~line 2889)

```tsx
{msg && <p className="text-xs mt-2 text-success">{msg}</p>}
```

`msg` carries **both** outcomes — `'Rolled back successfully'`, `'Rollback
failed'`, `'Request failed'`, and the server's own error string. All four render
in `text-success` green. On the one panel whose entire purpose is recovering a
broken app, a failed rollback is drawn in the colour that means it worked.

### H4 — Bluetooth → radio state (`BluetoothSettings`, ~lines 1390, 1398)

```tsx
actions={<Pill tone={status?.powered ? 'success' : 'neutral'}>{status?.powered ? 'On' : 'Off'}</Pill>}
…
control={<Toggle ariaLabel="Bluetooth" checked={status?.powered} onChange={setPower} />}
```

Neither is gated on a status having arrived. Before the first fetch resolves, and
after a failed one, the panel states flatly that Bluetooth is **Off** and draws
the switch in the off position. A radio whose state is unknown is not a radio
that is off, and the difference matters: the user's next action is to flip a
switch whose current position is fiction.

WiFi, twenty lines earlier, gets this right — `actions={status && (<Pill …/>)}`.
Bluetooth is the inconsistent one.

---

## 2. The four DANGEROUS controls

### D1 — Account → "Save" asserts a write that did not happen

`AccountSettings.save`, ~line 2206:

```ts
try {
  await updateProfile({ display_name: name, locale, timezone: tz })
  setSaved(true)
} catch (e) {
  setError(errorMessage(e) || 'Failed to save')
}
```

**`updateProfile` never throws.** Verified at
`frontend/src/auth/AuthProvider.tsx:219-239` — every failure path *returns* a
string: `'unreachable'` for an offline session or a rejected `fetch`,
`'rejected'` for a 4xx, `'unreachable'` for a 5xx. The `catch` block is dead
code and the `setError` slot below it can never be populated.

So the button flashes **"Saved"** for a patch the box refused with a 403, for a
patch sent to a box that is not answering, and for an offline session in which
no request was made at all.

This panel's own comment says the defect it was fixing was that Save "silently
did nothing … the one place in Settings you couldn't tell whether your click
landed." It was upgraded from silent-nothing to a **false success claim**, which
is strictly worse: the user now has positive evidence for something untrue, and
`NetworkSettings` twenty lines away carries a comment describing this exact
failure as the reason *it* was fixed.

### D2 — Storage Mode misreports where this box's bytes live

`StorageModeSettings`, ~line 3206:

```tsx
{cfg == null ? '…' : cfg.mode === 'local-minio-sync' ? 'Local MinIO + sync' : 'Central Tigris (default)'}
```

A two-state readout over a **three-state** selector. The selector immediately
below it offers `local-fs`, `local-minio-sync` and `central-tigris`, and
`local-fs` is the default:

```go
// backend/internal/storagemode/storagemode.go
const DefaultMode = ModeLocalFS         // "local-fs"
const LegacyDefaultMode = ModeCentralTigris
```

```go
// backend/cmd/server/routes_storagemode.go
// Since D-STORE-LOCAL-DEFAULT the default is local-fs (this box's own disk);
// local-minio-sync and the hosted central-tigris are both opt-ins.
```

So **a box in its default configuration is told its storage mode is "Central
Tigris (default)"** — hosted, third-party, S3. The box's bytes are on its own
disk. On an OS whose entire pitch is that your data stays on hardware you own,
this is the single worst thing this screen could say, and it says it to every
user who has never touched the setting.

The backend takes the opposite posture in the same breath: flipping to hosted is
logged as `"HOSTED third-party storage, selected explicitly by user"`, because
it is a posture change. The UI reports that posture as the resting state.

### D3 — AI Apps → "Delete" has no confirmation

`AIAppsSettings.remove`. Every other destructive action in this file confirms:
Device PIN removal, fingerprint removal, user removal and OS staging all call
`confirm()` first. Deleting an AI app — which destroys the app **and its entire
version history**, the thing the adjacent "Versions / Restore" UI exists to
protect — deletes on a single click, from a 12px text button sitting directly
beside "Open".

### D4 — Device PIN reports a PIN that may not exist

`DevicePINSettings.loadStatus`, ~line 2304:

```ts
setHasPIN(data.has_pin !== false)
```

An **absent** `has_pin` is not `false`, so it reads as `true`. The panel then
states "PIN status: **Set**", titles the form "Change PIN", and renders the
"Remove PIN" block — a destructive control offering to remove something the box
never said existed. A security state is the last place to default to the
reassuring answer.

---

## 3. Two things the brief asked me to check, which are clean

**The uploaded wallpaper does say it will not sync.** Verified end to end:
`core/useWallpaper.tsx:60` publishes `localOnly: exceedsSyncLimit(wallpaper)`,
and `WallpaperPicker` (Settings.tsx ~1052) renders the notice when it is true —
"This image is stored in this browser only … your other instances keep their
own." The claim in `USER-STATE-INVENTORY.md` §5 holds.

**Settings does not resurrect `vulos-theme`.** `Settings.tsx` contains **zero**
`localStorage` calls — every occurrence of the string is in a comment. Theme,
accent, night shift, schedule, density, dock, desktop layout, widget rail and
notification prefs all route through `useTheme()` / `syncedPrefs` /
`notificationStore` / the `desktop` store. There is no SYNC-class value being
written raw here. The `AppearanceSettings` heading text
`"Theme, accent, density, and wallpaper for this device."` is the only residue —
three of those four now follow the user, not the device.

**No telephony control claims a modem.** There is no telephony section in
Settings at all; the Phone app lives in `core/AppRegistry.ts` and its
no-modem degradation was settled by a separate pass today. Nothing to fix here,
recorded so it is not re-investigated.

**Modal focus management is real, unlike the Activity Monitor's.**
`SettingsModal` (~line 281) uses `useFocusTrap(true)`, and
`shell/useFocusTrap.ts` genuinely moves focus into the container on open, cycles
Tab/Shift+Tab within it, and restores focus to the opener on close. The
`aria-modal="true"` here is backed by the behaviour it advertises. The four
`confirm()` call sites are browser-modal by construction.

---

## 4. STALE — 5 claims that describe a world that moved

| # | where | says | actually |
|---|---|---|---|
| S1 | `StorageModeSettings` prose, ~3196 | "The default sends every read and write directly to hosted Tigris." | `DefaultMode = ModeLocalFS` since D-STORE-LOCAL-DEFAULT. The default sends them to this box's own disk. |
| S2 | `StorageModeSettings` "Current mode", ~3206 | a two-state readout | the selector below it has had three states since `local-fs` landed |
| S3 | `DensityPicker` comment, ~970 | "Applied eagerly on load in main.jsx" | `main.tsx`, and it is applied by the `density` pref group, not just at load |
| S4 | `NotificationsSettings` comment, ~3913 | "All prefs persist to localStorage" | they ride `Profile.Settings` under `shell.notifications.prefs` and follow the user |
| S5 | footer comment, ~3908 | "`./settings/ui.jsx`" | `ui.tsx` |

S1 and S2 are the same defect as D2 seen from the prose side, and are load-
bearing: a user reading this panel to decide whether to opt *out* of hosted
storage is told they are already in it.

---

## 5. Fabricated readouts — 12 places an absent value is drawn as a fact

> *A derived value is known only when every input is; otherwise show `—`.*

This is the class that today's Activity Monitor finding (`|| 0` rendering
`Free 0 B` — "out of memory" — precisely when the feed was down) belongs to, and
it is the dominant defect in this file. Eight of the twelve are reachable on a
**real** backend path, not merely on a dead one.

| # | panel | line | draws | when |
|---|---|---|---|---|
| F1 | Storage Mode | 3147 | "Central Tigris (default)" | `GET /api/storagemode` has no `res.ok`; a 5xx JSON error body narrows to `{}`, which is non-null, so the panel reports hosted storage **because the box failed to answer** |
| F2 | About — Graphics | 3783 | "Tier 0 — Software", "Capture: X11 SHM", "Vendor: None" | `GET /api/system/info` has no `res.ok`. The error body narrows to an all-`undefined` `SysInfo` that is **non-null**, so `{sys && …}` renders the whole card. Three concrete hardware claims, none of them measured. |
| F3 | About — System | 3828 | "OS: Debian Linux" | `os_version` absent — a version-less fallback string where `—` was available |
| F4 | Device PIN | 2398 | "5 attempts remaining" | `attempts_left ?? 5`. The real handler returns **only** `{"has_pin": false}` when `h.DevicePIN == nil` (`services/auth/handlers.go:1401`), so this is a live path, not a hypothetical. |
| F5 | Device PIN | 2304 | "PIN status: Set" | see D4 |
| F6 | Fingerprint | 2650 | "undefined of 3" | `{status.failures_left} of 3` with no guard |
| F7 | Battery & Energy | 1728-1730 | "CPU Governor: " / "Idle: " over nothing | ungated interpolation. **The identical shape was already fixed twice in this file** — Sound's "Backend:" (~1499) and Display's "Compositor:" (~1629) both carry comments explaining it. Energy was missed. The mock backend proves it is live: `GET /api/energy/status` returns `{screen_on, screen_dimmed}` and nothing else. |
| F8 | Connection Mode | 1873 | "External listener: enabled" | `blocked` is `false` before the first load and after a failed one. "Not known to be blocked" is drawn as "enabled". |
| F9 | Remote Access | 1963 | `http://localhost:8080` in the Access URL box | a hard-coded seed presented as the box's configuration; the `GET`'s failure is swallowed by `.catch(() => {})` with nothing shown |
| F10 | Backup & Sync | 3024 | "Snapshots: 0" | `sync?.total_snapshots || 0` — a failed `/api/vault/sync` reads as a vault holding zero snapshots |
| F11 | Search & Index | 3091-3094 | "Files indexed: 0", "Status: Ready" | `|| 0`, and `indexing` absent falls to the green "Ready" |
| F12 | Bluetooth | 1390 | "Off" | see H4 |

F1 and F2 share a root cause worth stating on its own: **`toX()` narrowers make a
5xx error body look like a valid empty answer.** `{"error": "..."}` passes
`isRecord`, keeps none of the fields it is asked for, and returns a well-formed
record of `undefined`s that is *not null* — so every `{data && …}` gate opens.
`apiGet` exists in this file specifically to prevent this, with a 17-line comment
saying so. Three call sites never adopted it.

---

## 6. What gets fixed, in priority order

1. **D2 + S1 + S2 + F1** — Storage Mode tells the truth about where bytes live.
2. **D1** — Account stops claiming "Saved" for writes that did not land.
3. **D4 + F5** — Device PIN stops asserting a PIN it was not told about.
4. **D3 + H2 + H3** — AI app deletion confirms, reports refusal, and stops
   painting failures green.
5. **H1** — AI Assistant's Save reports what happened.
6. **H4 + F12** — Bluetooth stops reporting a radio it has not heard from.
7. **F2-F11** — every remaining readout degrades to `—` rather than to a
   plausible number.
8. **S3-S5** — comment corrections.

## 7. What is deliberately left

- **`LANPairingPanel` / `LANRootCertPanel`** — landed today, owned elsewhere.
  Audited (§8) but not restructured.
- **Bluetooth "Remove"** (forget a pairing) has no confirmation. Left: it is
  non-destructive of data and fully undone by re-pairing, and it carries a
  correct accessible name (`Forget {device}`). Recorded rather than fixed so the
  decision is visible.
- **Log Out** has no confirmation. Same reasoning — ends a session, destroys
  nothing.
- **`AppearanceSettings`'s "for this device" description** is now wrong for
  three of the four things it lists, but rewording it is a copy decision that
  touches the synced-prefs story another agent owns.

## 8. Delegated panels

The 16 `core/settings/*.tsx` panels (5,653 lines) were scanned separately for the
same four verdicts. See the notes appended below.
