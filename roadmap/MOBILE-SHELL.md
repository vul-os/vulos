# Mobile shell — what a phone actually gets, and what it cannot have

> **Goal.** On a phone, Vulos should feel like a phone OS. On a desktop it should keep the
> desktop. One shell, two idioms, no second codebase.
> **Non-goals.** A native rewrite. A phone-as-instance (settled: `clients/android/DECISIONS.md`
> MOB-01). A second app registry, a second inset system, or a second notification store.
> **Status (2026-08-15).** Shipped: touch-tablet routing, a home screen with apps on it, a
> switcher that does not double memory, pull-to-refresh containment, `?open=` deep links,
> and native inset propagation in the APK. Open: the items in §5 and §6.

This note exists because the previous mobile work was correct about *layout* and wrong about
*metaphor*. The shell had a mobile stack, a dock and safe-area classes, and it still did not
behave like a phone: the home screen had no apps on it, the app switcher spawned a second
live copy of everything running, a downward drag reloaded the whole OS, and a touch tablet
got 12×12 px window controls. Every one of those was measured on the shipping build, not
inferred.

---

## 1. The four defects, and why the green tests missed them

| # | Defect | Measured | Why nothing caught it |
|---|---|---|---|
| MOBILE-07 | 768×1024 and 834×1194 touch profiles rendered **DesktopCanvas**, with 12×12 px `.vwin-light` window controls | `[data-shell="mobile"]` absent; 25 sub-44px targets | The e2e suite tested one phone viewport. A narrow *desktop* viewport cannot reproduce it — the bug needs `pointer: coarse`, and no test set it. |
| MOBILE-09 | The app switcher mounted a **second live instance of every running app** | `[data-calendar-app]` 1 → 2 on opening the switcher; `iframe` 1 → 2 | Every assertion asked "is the card visible?". A card rendering a whole live app is *maximally* visible. Visibility tests are blind to cost. |
| MOBILE-10 | The phone home screen contained **zero apps** | screenshot: wordmark, "What do you need?" twice, ~1400 px of nothing | Nothing was broken. The tests asserted the dock and the fullscreen app, both of which worked. An empty surface is only visible in pixels. |
| MOBILE-08 | Chrome **pull-to-refresh was armed on the whole OS** | `overscrollBehaviorY === 'auto'` on `html` and `body` | It is a browser behaviour, not application code. There was nothing to assert against because nobody had written the rule down. |

The pattern: **three of the four are invisible to any test that asks "is it there?"** They
need a test that asks "how many", "how big", or "what did the browser decide". The regression
guards in `frontend/e2e/mobile-native.e2e.ts` are written that way, and each was
mutation-verified — the mutation applied, **the build asserted to succeed**, the test re-run
and seen to fail, then reverted. One mutation initially "passed" because the injected JSX was
invalid, the build failed, and the test ran against a stale bundle. That is the failure mode
to watch for in this repo, not a flaky assertion.

---

## 2. The top edge — PWA vs APK vs impossible

The founder's question was whether the top edge can belong to Vulos: sliding down from the
top of the screen should show *Vulos's* shade, not Android's. Here is what is actually
available, tested where testable and marked where not.

| Gesture / behaviour | PWA (Chrome/Firefox Android) | APK (WebView) | Verified? |
|---|---|---|---|
| **Notification shade** (swipe down from the very top edge) | **Impossible.** Owned by SystemUI, outside the app's window. No web API sees the touch. | **Impossible while the bars are shown.** With `hide(systemBars())` + `BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE`, the first swipe reveals the bars *transiently* rather than opening the shade — the closest thing to owning the edge that exists. | Platform behaviour; **not device-verified here** |
| **Quick Settings** (second swipe / two-finger swipe) | Impossible | Same as above | Platform behaviour |
| **Browser pull-to-refresh** (drag down inside the page) | **Ours, and it was reloading the whole OS.** Fixed with `overscroll-behavior-y: contain` on the root scroller | N/A (no browser UI) | ✅ **Measured** `auto` → `contain`, e2e-guarded |
| **Back gesture** (edge swipe left/right) | Browser/system history | `OnBackPressedCallback`, already wired | Existing code |
| **Home / recents gesture** (bottom edge) | Impossible | Impossible unless the app holds the launcher role, which it does (TEL-01) | — |
| **Status-bar tap-to-scroll-top** | Not exposed | Not exposed | — |
| **Anything in the middle of the screen** | **Fully ours.** | **Fully ours.** | ✅ the switcher's swipe-up-to-close is built here for exactly this reason |

**The conclusion, stated plainly: Vulos cannot own the top edge, in either tier.** The
notification shade is not a browser feature to override or an Android flag to set; it is a
separate window owned by SystemUI. The only lever is hiding the bars entirely (immersive),
which converts the *first* swipe into "show the bars" instead of "open the shade" — and that
is a trade, not a win.

**So the design rule is: never put anything Vulos needs at the top edge.** Concretely —

- Vulos's own notifications and quick settings must be reachable from the **bottom dock**,
  never a top-edge pull. The mobile shell has no top-edge gesture today; it must stay that
  way, and that is a rule worth a test rather than a convention.
- The mobile status bar is **identity and status only** — app name, trust badge, clock. It
  is not a control surface, because half of it is under a gesture the user will trigger by
  accident.
- Every gesture Vulos invents lives in the middle of the screen. The switcher's
  swipe-up-to-close is the first one and sets the precedent.

**Immersive mode is available and deliberately not taken.** See `clients/android/DECISIONS.md`
MOB-12: the APK ships as an enabled launcher, so hiding the status bar deletes the user's own
clock, battery and notification icons from their phone's home screen. It reverses for a
kiosk/signage profile, where there is no user's own phone to impose on.

**`display: "fullscreen"` in the manifest was considered and rejected.** It would hide the
Android status bar for the installed PWA. But `display_override` is honoured on **desktop**
installs too, where it opens a fullscreen window with no way out. Fullscreen belongs in the
APK, where it can be scoped to the phone. The manifest stays `standalone`.

---

## 3. App drawer vs app switcher — they are different surfaces, and both are needed

Now concrete: bundled apps launch on demand (15/15 verified launching and serving in a Linux
container this session, with a killed app's namespace reclaimed and re-activated). So "what
is running" is real state with a real cost behind it, not a list of open tabs.

They answer different questions and must not be merged:

| | **Library** (drawer) | **Apps** (switcher) |
|---|---|---|
| Question | "What *can* I run?" | "What *is* running, and what can I stop?" |
| Contents | The whole registry | Only live windows |
| Primary action | Launch | Switch / close |
| Cost of an entry | Zero | A container, memory, a namespace |
| Empty state | Never empty | Usually empty, and that is healthy |

The dock keeps three destinations — **Home · Apps · Library** — and that is the right count.
Merging the drawer into the switcher would put ~40 launchable apps next to 2 running ones and
lose the only surface that shows cost. Merging the switcher into Home would hide running apps
behind a scroll.

The switcher's cards are **identity cards, not previews**. The web platform cannot snapshot a
cross-origin iframe, and rendering the app live is what caused MOBILE-09. Showing the app's
mark, its title and its state is honest; a fake preview is not.

---

## 4. Mobile dock profile — spec for the customization workstream

The dock is currently hard-coded to Home · Apps · Library in `layouts/MobileStack.tsx`. The
founder asked whether it should carry different items on mobile. It should be *configurable*,
and the customization workstream owns the mechanism (`shell/Dock.tsx`, the accent tokens,
`index.css`). This is the contract the mobile shell needs from it.

**Requirements, in priority order:**

1. **Three to five slots, never more.** At 390 px, five 44 px targets plus labels is the
   ceiling. Six is where labels start truncating, and a truncated dock label is worse than no
   label.
2. **Home and Apps are not removable.** Home is the only way back from a fullscreen app on a
   device with no window chrome; Apps is the only way to close a running app without opening
   it first. Removing either strands the user. Library *is* removable — the home grid now
   shows every app, so the drawer is a convenience.
3. **A slot is `{ kind: 'system' | 'app', id }`.** `system` covers home/apps/library/
   notifications; `app` pins a launchable app id straight to the dock.
4. **Per-device, not per-account.** The dock a user wants on their phone is not the dock they
   want on the box's own screen. Key the stored profile by device profile
   (`roadmap/DEVICE-PROFILES.md`), not by user alone.
5. **The badge must clear 4.5:1.** See §7 — the current one does not.
6. **Reuse the shell's existing `touch-target` floor.** `.touch-target` is defined in
   `index.css` and, before this workstream, was used in exactly two places. It is the right
   primitive; it just has almost no adoption.

**What mobile does NOT need from it:** drag-to-reorder. On a phone, dock editing belongs in
Settings as a list, not as a long-press-and-drag on the dock itself — a long press on a
3-slot dock is a gesture with nowhere to go and it competes with the system's own long-press.

---

## 5. Close / not-responding — API spec for the force-quit + Activity Monitor workstream

The switcher deliberately does **not** implement app health. It needs three things, and
specifying them here is cheaper than the switcher growing a parallel poller that disagrees
with Activity Monitor.

**5.1 — Health, per running window.** Something the shell can read synchronously for a window
it already knows about:

```
GET /api/apps/health            ->  { apps: [ { app_id, state, since_ms, rss_bytes } ] }
     state: "running" | "starting" | "unresponsive" | "stopped"
```

- `unresponsive` must be **determined on the box**, not inferred in the browser from a missed
  frame. A slow relay link is not a hung app, and a shell that guesses will accuse healthy
  apps on bad networks — the exact population Vulos targets.
- `rss_bytes` is what makes the switcher able to say *why* it is being opened. It is optional;
  the switcher must render correctly without it, and must show nothing rather than "0 MB".
- Polling is the switcher's job only **while the switcher is open**. It must not poll on Home.

**5.2 — Force quit, distinct from close.**

```
POST /api/apps/{id}/stop     -> graceful; the app gets to save
POST /api/apps/{id}/kill     -> immediate; only offered for state == "unresponsive"
```

The switcher's ✕ and swipe-up both mean **stop**. `kill` must never be the primary action and
must never be reachable by the same gesture as `stop` — a gesture that sometimes discards
unsaved work is a gesture people stop trusting.

**5.3 — What the switcher will render when this lands.** A card gains: a `Not responding`
line in the danger token, and a `Force quit` action that appears *only* in that state. No new
surface, no new poll loop, no second source of truth. If the endpoint is absent or errors, the
card renders exactly as it does today — an absent capability is not an error state, and an
outage must not be reported as "everything is fine" *or* as "everything is broken".

---

## 6. Open, with the reason each is open

1. **The APK inset work is parse-checked only.** There is no Android SDK in the build
   environment. `MainActivity.applyEdgeToEdgeInsets()` is not compiled, not run on a device.
   The **density division** and the **`Locale.ROOT` number formatting** are the two things
   that look right in review and fail on real hardware — verify both on a device before
   relying on the fix.
2. **`android:windowLayoutInDisplayCutoutMode=shortEdges`** is not set. It is API 28+ against
   `minSdk 26`, so it needs a `values-v28/` override, and there is no SDK here to compile or
   lint it. Without it the shell is letterboxed beside the notch in landscape.
3. **Maskable icons.** Every entry in `manifest.json` is `purpose: "any"`. Android's adaptive
   icon then shows the Vulos mark in a white badge instead of filling the launcher's shape —
   visible on every Android home screen that installs the PWA. Needs a 512 px maskable asset
   with the mark inside the 80% safe zone; it is an **asset** problem, not a code one.
4. **`useNarrow` vs `useViewport`.** Two breakpoint hooks with different values (640 and 768)
   and different meanings. `useNarrow` is per-*viewport*, so a builtin in a narrow desktop
   window keeps two panes — deliberate and documented. But `AssistantPanel.tsx:22` already
   notes that its `useNarrow(640)` "can never actually be true in production". Worth a pass to
   find the other dead branches.
5. **Tablet landscape.** `TOUCH_STACK_MAX` is 1024, so an iPad in portrait gets the touch
   shell and in landscape gets the desktop canvas — including its 12 px window controls. That
   is a deliberate line (landscape is often a keyboard posture) but it is a *line*, and the
   canvas's own targets should come up regardless.

---

## 7. Reported, not fixed — defects in files this workstream does not own

Each of these is real and measured. They are listed rather than patched because patching
someone else's file locally hides the systemic version of the problem.

**7.1 — The accent-on-white contrast floor — reported here, FIXED at the token level
(`ef46ca1b`).** The dock's window-count badge in `MobileStack.tsx` uses `.accent-bg` with
`var(--accent-contrast)` on top. Both tokens live in `src/index.css`, so this was never a
badge problem. Measured white-on-raw-accent at **3.68:1 on both themes** with the default
blue (App Hub workstream, running build); the shipped dark default `#5b6cff` computes to
**4.17:1** on its own, i.e. below the 4.5:1 gate *before* any user accent is applied.

This was deliberately **not** patched in `MobileStack.tsx`. The same pair appears in the
Settings nav rail's active pip and the window-chrome button hover fill — a local patch on the
badge would have left the others and hidden the systemic version.

The customization workstream fixed it correctly: `src/core/accentContrast.ts` derives
`--accent-solid` (the accent moved until white clears AA on it) and `--accent-text`
separately, because the two move in *opposite* directions on a dark theme. `.accent-bg` now
resolves to `var(--accent-solid, var(--accent))`. **This is the outcome the reporting
convention is for** — an owner fixing it once, at the token, rather than three workstreams
each patching their own instance.

**7.2 — Touch leaks in builtins.** From a sweep of `src/builtin/**`, `src/core/**`,
`src/apps/**`. Two of the five items below have since been fixed by other workstreams —
marked inline rather than silently dropped, per the reporting convention §7.1 sets.

- **`builtin/files/FileManager.tsx` is the worst file in the tree on touch — FIXED
  (`4c54227c`, "On a phone you could not open a folder, and two file actions did not
  exist").** `onDoubleClick` used to be the only trigger for entering a folder (the row's
  `onClick` merely selected it) and `onContextMenu` the only path to "Ask AI about this" and
  "Share to peer…", with no long-press handler anywhere in `src/`. Now: a single tap opens (a
  folder navigates, a file previews), a long press opens the same context menu at the finger's
  position, and the pointer path is untouched (click still selects, double-click still opens,
  right-click still opens the menu) — which one applies is decided by the POINTER that
  produced the event, not a `(pointer: coarse)` media query. New primitive:
  `frontend/src/mobile/useLongPress.ts` (`useLongPress`, `usePointerKind`), wired into
  `FileManager.tsx`'s own header comment, which now tells this history itself.
- **`builtin/packages/Packages.tsx:490`** — still open. The Remove button is `opacity-0` with
  only `group-hover`/`focus-visible` to reveal it. On touch it is an invisible but
  still-tappable target.
- **`shell/Toasts.tsx:77` — FIXED, by the SHELL-RESPONSIVE workstream (R-8), not this one.**
  The dismiss button is `w-6 h-6` (24px) now, not 16×16; `Toasts.tsx`'s own comment records the
  old defect in the past tense. **`core/SystemPulse.tsx:251/253`** — still open: 24px month
  arrows, mounted on mobile, still under the 44px floor. So of the original five sub-44px
  targets in the mobile chrome, one (Toasts) is fixed and `shell/TrustBadge.tsx` was not
  independently re-checked here.
- **`window.confirm()` call sites — count and citation both stale.** This said "12," naming
  Drive, Settings, Authenticator, WebhooksPanel, CDNPanel, and a phone-dialer file "gating an
  actual phone call." That file (the former `phone/CallsTab.tsx:58`) no longer exists — it was
  renamed to `RecentsTab.tsx` in `e7bec3f6` ("Phone was an Android app wearing a box app's
  name") — and the confirm-before-calling `window.confirm()` no longer exists anywhere in the
  phone/contacts code either: the worst offender in the original list (blocking a phone call
  with unstyled OS chrome) is gone, not just moved. Current count across `frontend/src`
  (excluding tests): **7**, in
  `core/settings/WebhooksPanel.tsx` (2), `core/settings/CDNPanel.tsx` (1),
  `apps/Authenticator/Authenticator.tsx` (1), `builtin/drive/Drive.tsx` (3). Still unstyled OS
  chrome breaking the fullscreen shell; still open, just not 12 sites and not the phone dialer.
- **`builtin/stream/StreamViewer.tsx`** — still open. Driven entirely by `onMouseMove`/
  `onMouseDown`/`onWheel` + `requestPointerLock()`, which does not exist on mobile browsers.
  The streamed desktop is view-only on touch.

**7.3 — `frontend/src/layouts/DesktopCanvas.tsx` had an uncommitted `tsc` error** during this
session (`OpenWindowOptions` vs a `component?: unknown` structural slice). It cleared before
this note was written; flagging it in case it returns.

---

## 8. The mobile dock is data now — what §4 asked for, and what it actually got

§4 specified a contract for the customization workstream. That workstream shipped a
*different* model, and the phone dock is built on the one that exists rather than the one that
was requested. Both are recorded here because the difference is the interesting part.

**What §4 asked for:** slots of `{ kind: 'system' | 'app', id }`, keyed by device profile.

**What shipped** (`src/desktop/types.ts`): a `DockProfile` per FORM FACTOR — `edge`, `size`,
`style`, `align`, `autohide`, `launcher`, `assistant`, `drawer`, and `items: string[]` of app
ids — with `desktop` and `mobile` persisting under separate keys, and a validator that already
rejects a vertical phone dock, a `small` tile, a sixth item and a drawerless phone dock.

The two are close enough. `items` is the founder's "different items on the mobile dock";
`system` slots are not expressible in `items`, which is *better* than §4.2 asked for — Home and
Apps are emitted unconditionally by `src/mobile/mobileDock.ts` and there is no representable
state in which either is missing, rather than a rule someone has to remember to enforce.

**Consumed and observable in the rendered dock:** `items`, `edge`, `size`, `style`, `align`
(which end a *floating* island hugs — a full-span bar has no alignment to have), `drawer`, and
`assistant`, which lands on the phone home screen's ask bar because on a phone the assistant is
a full-screen surface and not a side panel.

**Refused, with the reason stated in code rather than silently dropped:**

- `autohide` — there is no hover on touch to bring a hidden dock back, and this is the phone's
  ONLY navigation surface. An autohidden phone dock is a UI you cannot navigate out of, which
  is the same failure `--vd-dock-opacity`'s 0.6 floor exists to prevent.
  `mobileDockAutohide()` returns false for every profile, so the refusal has a call site a test
  can point at.
- `launcher` — on a phone the launcher and the drawer are ONE surface. The Library slot opens
  the Launchpad, which is itself the searchable app list, and the validator makes `drawer`
  mandatory, so a launcher slot would be a second control for a surface that is always present.
  It is subsumed: the library slot is emitted for `launcher || drawer`.

**Two things the model could not have known:**

1. **The profile is read as `useDockProfile('mobile')`, never the active form factor.**
   `desktop/store.ts`'s `activeFormFactor()` is a pure 768px width test, but `useViewport.ts`
   mounts this shell on a coarse-pointer tablet up to 1024px. **Between 768 and 1024 the touch
   shell is up while the store reports `desktop`** — an iPad would have got the desktop dock's
   twelve-item, `small`-tile geometry. The same disagreement means `<html>`'s `data-dock-*`
   attributes carry the DESKTOP profile in that band, so the mobile dock stamps its geometry on
   its own `<nav>` instead of reading the root. Worth fixing in `store.ts` (which this
   workstream does not own) so the two agree: `activeFormFactor()` should consult the same
   predicate `useViewport.ts` does, not width alone.

2. **The stock mobile profile's `items` are `['home', 'lilmail', 'messages']`.** A docked
   `home` folds into the system Home slot: two adjacent dock targets both labelled "Home" that
   go to different places is a navigation defect, and two buttons with one accessible name in
   one toolbar. The Home *app* stays launchable from the home grid and the Library.

**Eight slots is the ceiling** — Home + Apps + `MOBILE_MAX_ITEMS` + Library. At 390px with the
8px safe-area gutter that is 48.4px per column, over the 44px floor; a ninth would be 42.4px.
It is also why app tiles carry no caption at that density (§4.1: a truncated dock label is
worse than none) and why the plate is clamped to the MEASURED column minus an 8px gap —
screenshotted before the clamp, five 56px marks in 48.7px columns drew as one continuous strip
of colour, with every assertion correctly green because the touch target is the button.

---

## 9. The APK insets are still parse-checked only, and this shell now depends on them

§6.1 recorded that `MainActivity.applyEdgeToEdgeInsets()` is not compiled and has never run on
a device. That is unchanged — there is still no Android SDK in this environment, and nothing in
this pass verified it. What has changed is that **more of the phone shell now rests on it**, so
the consequence is worth stating precisely rather than leaving as a general caveat.

**What depends on it.** `--safe-top`, `--safe-bottom`, `--safe-left` and `--safe-right` are
defined in `src/index.css` as `env(safe-area-inset-*)`, and inside the APK `pushSafeAreaToShell()`
**overwrites all four as INLINE styles on `<html>`**. Everything below consumes them:

- the mobile status bar's notch padding (`.safe-pt`);
- the dock's home-indicator padding — now edge-aware, so a `bottom` dock takes
  `--safe-bottom` and a `top` dock does not;
- the dock's horizontal gutter (`--safe-left` / `--safe-right`), which is the 8px in the
  48.4px-per-slot arithmetic above;
- the home grid's and the switcher's gutters.

**Why the inline write makes it worse than an absent fix.** An inline style on the root element
wins over the `:root { … env(…) }` declaration. So a wrong injected value does not degrade to
the correct web fallback — it *replaces* it. Both of the two risks §6.1 named are handled in
the source as written (the density division is present; the format uses `Locale.ROOT`), but
"handled in source" and "verified on hardware" are different claims, and only the first one can
be made here:

- **Density.** `bars.top / density` is invisible on a 1× emulator and three times too large on
  a real 3× phone. Wrong here means a dock inset roughly 100px too tall at the bottom of a
  390×844 screen, eating the bottom row of the home grid.
- **`Locale.ROOT`.** Without it, `%.2f` in a comma-decimal locale emits `"34,00px"`.
  `setProperty` accepts it (custom properties take almost any token sequence), then
  `padding-bottom: var(--safe-bottom)` is invalid at computed-value time and resolves to
  **zero** — so the dock sits under the navigation bar on exactly the devices that cannot be
  tested from here, while every screenshot taken in Chromium looks perfect.

**What was verified, and where.** Everything in this pass was driven in Chromium at 390×844,
768×1024, 834×1194 and 1280×800, where `env(safe-area-inset-*)` resolves to 0 and the injection
does not exist. That is a complete check of the LAYOUT and no check at all of the INSET. The
first person with a device should open the APK on a notched phone and on a phone with gesture
navigation, and read `getComputedStyle(document.documentElement).getPropertyValue('--safe-bottom')`
in the WebView inspector — a plausible CSS length in CSS pixels is the whole test, and it takes
a minute.

**`clients/android/**` was not touched by this pass.**

---

## Related

- `clients/android/DECISIONS.md` — MOB-01…07, TEL-01, and **MOB-12** (edge-to-edge + insets,
  immersive not taken)
- `clients/android/ANDROID-COMPAT.md` — what Android apps a box can and cannot run
- `roadmap/DEVICE-PROFILES.md` — the profile the dock spec in §4 keys off
- `roadmap/OFFLINE.md` — the offline model both delivery tiers share
- `frontend/e2e/mobile-native.e2e.ts` — the regression guards for §1, at four device profiles
- `frontend/src/shell/useViewport.ts` — MOBILE-07, the layout predicate and why it needs
  `pointer: coarse` *and* `hover: none`
- `frontend/src/mobile/mobileDock.ts` — §8, what the phone dock contains and what it refuses
- `frontend/src/mobile/useLongPress.ts` — the touch primitive §7.2 said did not exist anywhere
- `frontend/src/core/touch-chrome.css` — the 44px floor for the status cluster, and why it is
  a rule on the cluster rather than on five components
- `frontend/e2e/mobile-dock.e2e.ts`, `mobile-files.e2e.ts`, `mobile-touch-targets.e2e.ts` —
  the gates for §8, §7.2 and the 44px floor, all on real touch device profiles
