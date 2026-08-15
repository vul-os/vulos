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

**7.1 — The accent-on-white contrast floor fails by default.** The dock's window-count badge
in `MobileStack.tsx` uses `.accent-bg` + `var(--accent-contrast)`. Both come from
`src/index.css` (`--accent-contrast: #ffffff` at :46, `.accent-bg { background: var(--accent) }`
at :841). Computed against the **shipped defaults**:

| Theme | `--accent` | vs `#ffffff` | 4.5:1? |
|---|---|---|---|
| dark | `#5b6cff` | **4.17:1** | ✗ |
| light | `#4d5efb` | 4.87:1 | ✓ (thin) |

The App Hub workstream independently measured **3.68:1 on both themes** on a running build,
i.e. with a user accent applied — worse than the defaults. It appears for Settings and Files
too, and ~12 files pair these two tokens. **This is a token problem, not a badge problem, and
it belongs to whoever owns `index.css`.** The fix is one of: derive `--accent-contrast` per
accent by luminance (white or near-black, whichever wins) rather than hard-coding white; or
constrain the accent presets to values that clear 4.5:1 against white. Patching the badge
locally would leave the other eleven.

**7.2 — Touch leaks in builtins.** From a sweep of `src/builtin/**`, `src/core/**`,
`src/apps/**`:

- **`builtin/files/FileManager.tsx` is the worst file in the tree on touch.** `:866`
  `onDoubleClick` is the *only* trigger for entering a folder (the row's `onClick` merely
  selects), and `:867` `onContextMenu` is the only path to "Ask AI about this" (`:1039`) and
  "Share to peer…" (`:1045`). There is **no long-press handler anywhere in `src/`**. On a
  phone you cannot open a folder and cannot reach the file actions.
- **`builtin/packages/Packages.tsx:490`** — the Remove button is `opacity-0` with only
  `group-hover`/`focus-visible` to reveal it. On touch it is an invisible but still-tappable
  target.
- **`shell/Toasts.tsx:77`** — a **16 px** toast dismiss button, and Toasts *is* mounted in
  `MobileStack`. **`core/SystemPulse.tsx:251/253`** — 24 px month arrows, also mounted on
  mobile. These two plus `shell/TrustBadge.tsx` are the only sub-44px targets left in the
  mobile chrome (5 of them, measured).
- **12 `window.confirm()` call sites** (Drive, Settings, Authenticator, WebhooksPanel,
  CDNPanel, `phone/CallsTab.tsx:58` gating an actual phone call). Unstyled OS chrome that
  breaks the fullscreen shell.
- **`builtin/stream/StreamViewer.tsx`** is driven entirely by `onMouseMove`/`onMouseDown`/
  `onWheel` + `requestPointerLock()`, which does not exist on mobile browsers. The streamed
  desktop is view-only on touch.

**7.3 — `frontend/src/layouts/DesktopCanvas.tsx` had an uncommitted `tsc` error** during this
session (`OpenWindowOptions` vs a `component?: unknown` structural slice). It cleared before
this note was written; flagging it in case it returns.

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
