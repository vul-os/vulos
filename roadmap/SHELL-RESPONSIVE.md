# The shell at every width — what the sweep found

> **Why this exists.** "FIX RESPONSIVENESS THERE ARE ISSUES ON TABLET AND MOBILE" was the
> whole brief. The shell had never been swept across widths; the mobile work that came before
> it (`roadmap/MOBILE-SHELL.md`) was a sweep of one idiom at four sizes, all portrait.
> **How everything below was obtained.** Chromium, driven at 23 device profiles through
> `frontend/scripts/e2e-isolated.mjs` — a private `--outDir`, a private port, and a
> content-hash provenance check that the served `index.html` names the bundle that run built.
> Provenance output is in §6.
> **What it is not.** Nobody here has a physical phone or a tablet. Every number below is an
> **emulated viewport**. §7 says exactly what that could not prove.

---

## 1. The inventory — width → symptom → state

Measured on the shipping build BEFORE any change in this pass. `shell` is which of the two
shells actually mounted, as the DOM reported it. `escaping` counts elements painted outside
the viewport; `tiny` counts rendered text nodes below the 12px floor; `small` counts
interactive controls below 44px inside the shell chrome; `chrome` is the fraction of viewport
HEIGHT the shell's own bars consume.

| # | Width × height | shell | Symptom | Measured | State |
|---|---|---|---|---|---|
| R-1 | **every width**, both orientations, all 3 tablets | mobile | The phone home grid draws **every app name at 11px** | 35 sub-floor text nodes per viewport | **FIXED** — 12px, `mobile/MobileHome.tsx` |
| R-2 | 360×800 | mobile | Status-bar cluster painted **29px past the right edge**; the clock is off screen | cluster 317px wide, right edge at 389px | **FIXED** — identity yields, `layouts/MobileStack.tsx` + `mobile/mobile.css` |
| R-3 | 320×568 | mobile | Same, **69px** | as above | **FIXED** — and read §3, because the first diagnosis of this one was wrong |
| R-4 | 1194×834, 1366×1024 (coarse pointer) | **desktop** | An iPad in landscape runs DesktopCanvas. `.vwin-light` window controls are **12×12px**, and their glyphs are `opacity: 0` until `.vwin-lights:hover` — three identical dots, one of which discards the window | 12×12, glyph opacity 0 | **FIXED** — 44px hit area + drawn glyphs under `(pointer: coarse)`, `shell/shell-chrome.css` |
| R-5 | 1194×834, 1366×1024 (coarse pointer) | desktop | **6 menu-bar controls under 44px** on a device with no mouse | System menu 69×32; Applications, Mission Control, Chat, Toggle fullscreen 28×28; Theme 24×24 | **FIXED** — one `--menubar-h` token read by all three sites that used to carry the bar's height as separate literals, plus a `.vshell-bar` rule reaching all six controls. See §4 |
| R-6 | 568×320 (phone landscape) | mobile | The shell's own chrome is **40.6% of the viewport height** — 130px of 320px | bar 44px + dock 86px | **BOUNDED, not redesigned.** §5 |
| R-7 | every desktop width 768–1440 | desktop | **19 sub-12px text nodes**: 18 in the widget rail (10.5–11.5px), 1 in the wallpaper wordmark ("alpha", 10px) | `src/widgets/host/widgets.css`, `layouts/DesktopCanvas.tsx:160` | **FIXED** — one `--vwidget-fs-min` token in `src/widgets/host/widgets.css` (18 nodes) and the wordmark raised 10px → 12px in `layouts/DesktopCanvas.tsx`. See §4 |
| R-8 | mobile chrome, all widths | mobile | Toast dismiss button **16×16 with a 10px glyph**; Toasts IS mounted in MobileStack | 16×16 | **FIXED** — 24px visible / 44px on coarse, 12px glyph |
| R-9 | mobile status bar | mobile | Notification count badge at **9px** — the only thing that says how many are waiting | 9px | **FIXED** — 12px |
| R-10 | notification panel | both | The row's dismiss control is `opacity-0` until `group-hover` — on a finger, an **invisible but still tappable** target | opacity 0 | **FIXED** — revealed on coarse pointers |
| R-11 | 1194×834 with a window open | desktop | The R-4 fix asked for 44px lights and the gate read **40×40** — twice. Not a layout defect: `.win-anim[data-win-anim="opening"]` is `scale(0.92)` (`index.css:778`) and `getBoundingClientRect` returns the **transformed** box. 44 × 0.92 = 40.5 | 40×40 while `getComputedStyle().width` said 44px | **NOT A DEFECT — a capture artifact.** The gate settles the open transform before measuring. Recorded because the first diagnosis was wrong (flex-shrink) and a fix credited to the wrong cause is worse than no fix |
| R-12 | 320×568 / 360×800, **app open** | mobile | The "Back to home" button — the labelled way out of a fullscreen app, and the only thing carrying the app's title — is **31.5 × 36**, under the floor in BOTH axes | 31.5×36 at 360; 26×44 at 320; 36px tall at every phone width | **FIXED** — `h-9`→`h-11` for the height (the row is already 44px, so the bar does not grow), and the clock's narrow tier for the width. §3 |

### What the sweep did NOT find, and that is worth recording

- **No document-level horizontal scroll at any of the 23 profiles.** `docSpill` was 0
  everywhere, including at 320px where 69px of content was off screen. That is the finding:
  the shell's `overflow: hidden` root means `scrollWidth` **cannot** see this class of defect,
  and every horizontal-overflow assertion in this suite was reading it.
- **The mobile dock is clean at every width**, in both orientations, in every profile — 0
  sub-44px targets, 0 overflow. §8 of `MOBILE-SHELL.md` did that arithmetic and it holds.
- **A dragged/resized desktop window down to 768px is clean.** Below 768 the shell switches to
  the touch stack (measured at 700×440), which is the documented rule doing what it says.
- **`tablet 1024 landscape` (1024×768) mounts the MOBILE shell**, because `TOUCH_STACK_MAX` is
  1024 and the query is `max-width: 1024px`. 1024 is inclusive. That is not a defect, but it
  is a boundary nobody had written down: the iPad that gets the desktop canvas in landscape is
  the 11" and up, not the 10.2".

---

## 2. What changed

All of it in `frontend/src/shell/**`, `frontend/src/mobile/**` and
`frontend/src/layouts/MobileStack.tsx`.

1. **`mobile/MobileHome.tsx`** — tile labels 11px → 12px. And the grid's column count moves
   off `sm:grid-cols-6` (a 640px **viewport** query) onto a container query on the grid's own
   scroller. The two disagree wherever the surface is narrower than the screen, and on a phone
   that is not hypothetical: the scroller carries `safe-px-4`, so a landscape display cutout
   takes real width out of the grid while `sm:` keeps reading the untouched viewport. Column
   counts at every swept width are unchanged.
2. **`layouts/MobileStack.tsx` + `mobile/mobile.css`** — the status bar's identity block gains
   `flex-1 min-w-0` and truncation so it can give its width back; the row's padding drops from
   24px to 12px; the app mark beside the app *name* is hidden below 390px.
3. **`shell/shell-chrome.css`** — a `@media (pointer: coarse)` block for the desktop canvas.
   `.vwin-light` grows to a 44px box with a 20px painted dot (`background-clip: content-box`,
   so the padding is hit area and not paint) and its glyph is drawn rather than waiting for a
   hover that will never arrive. `.vwin-chrome-btn`, `.vshell-pip`, `.vshell-dock-item` and
   `.vshell-dock-glyph` take the same floor. `.vshell-touch` and `.vshell-reveal` are the two
   opt-in primitives the components use.
4. **`shell/Toasts.tsx`, `shell/NotificationCenter.tsx`** — the three items R-8, R-9, R-10.
5. **`layouts/MobileStack.tsx`** — the back-to-home button grows from `h-9` to `h-11` (R-12).
   The row is already 44px, so the bar does not get taller; the target does.

**Three of the twelve findings were produced by the gate itself, against fixes that had just
been written** — R-11 (a transform mistaken for a layout defect, twice), R-12 (the back button)
and the app-open denominator. That is the argument for measuring the fix rather than reading
it, and it is why the gate opens a window and opens an app rather than only sweeping the
resting shell.

**Why the status-bar fix is not a container query.** It would be the right instrument if the
bar could ever be narrower than the screen, and it cannot — `.vmob-bar` is a flex child of a
`fixed inset-0` root. What a query would *cost* is real: `container-type` implies layout
containment, which makes the bar a stacking context, and SystemPulse's dropdowns are
`position: absolute; z-[100]` children of it. Capping them at the bar's own stacking level
would drop every status dropdown behind the app frame. The row gives its space back at every
width instead of at a breakpoint, which is the stronger property anyway. The one place a
container query *is* load-bearing — a surface whose own box shrinks independently of the
viewport — is the home grid, and that is where it went.

---

## 3. R-3 and R-12 at 320px — closed, and one of the two diagnoses here was wrong

At 320px the compact status cluster's own **min-content width was 317px**:

| Part | Width | Where it comes from |
|---|---|---|
| Trust badge hit area | 48px | `core/touch-chrome.css` `.vtrust-hit` |
| Wi-Fi | 44px | `core/touch-chrome.css` `.vsp-compact button` |
| Battery | 44px | same |
| Notifications | 44px | same |
| Theme | 44px | same |
| Clock | ~72px (56px of it text) | `core/SystemPulse.tsx` |
| gaps + divider | ~12px | |
| **total** | **~317px** | against a **320px** viewport |

### What actually fixed the home screen — and the claim that was wrong

This note previously said 320px "cannot be fixed by layout" and that only `core/` could close
it. **That was wrong about the home screen.** What closed 320×568 on Home was the shell's own
change: the identity block gaining `min-w-0` + `truncate` so flexbox would shrink it, plus the
row's padding dropping from 24px to 12px. The reason the error survived is worth more than the
correction: the gate was carrying 320 under a *known-open exemption*, so the case was being
asserted against its old broken shape and **never ran the hard branch that would have shown it
passing**. An exemption does not only hide regressions — it hides repairs, and it hid this one
for four runs.

Setting `NARROW_SPILL_MAX_WIDTH` to 0 is what surfaced it. The constant is still in the file,
at 0, precisely so that the exemption's absence is explicit rather than a deleted `if`.

### What the clock's narrow tier actually closed

The **fullscreen branch**. With an app open the row carries a back chevron and the app's title
as well, and there the 317px cluster left the back button at **31.5px wide at 360** and
**26px at 320** — under the 44px floor, with the app's title squeezed to nothing. That is
R-12's width, and no amount of shell-side yielding reaches it: the identity block is already
at its content minimum.

So the clock gained a narrow tier in `core/SystemPulse.tsx`, in the same idiom the battery
percentage has used since the 390px overflow was found (`hidden sm:inline`, line 568), one
tier further down:

| Viewport | What the clock draws | Width |
|---|---|---|
| `< 360px` | nothing | 0 |
| `< 390px` | the glyph only, still a 44px target that opens the calendar | 44px |
| `>= 390px` | the time | ~72px |
| `>= 640px` | the time and the date | unchanged |

The time string stays in the DOM at every width as `sr-only`, so the button's accessible name
is the time exactly as it always was and a screen-reader user loses nothing at any size.

**Why the clock and not something else.** It is the widest single control, and it is the only
one whose information the device *already shows*: `clients/android/DECISIONS.md` MOB-12
deliberately does **not** take immersive mode, precisely so the user keeps their own clock,
battery and notification icons in the system bar directly above this one. Vulos's clock is the
redundant one at a width where something has to go.

**Measured after:** 320×568 Home and 320×568 **with an app open** both clean — no overflow, no
sub-44px target, back button 44px in both axes. The app-open case in the gate runs at 320 now,
the narrowest profile in the sweep, rather than at the widest phone where it would prove
nothing.

---

## 4. R-5 and R-7 — closed in commit `ee1af016`

**Both were reported rather than fixed for as long as this section said so.** That was the
right call at the time: the bar's own height lived in three files that could not see each
other, and growing it in two without the third would have opened every window 12px underneath
the menu bar on exactly the devices the fix was for. `ee1af016` ("The bar's height lived in
three files that could not see each other, so the touch floor was reported instead of fixed")
made the coordinated change and closed both. What follows is HOW, not a plan for how it could
be done.

**R-5, the desktop menu bar on a coarse pointer.** `.vshell-btn` was 28px inside a 32px bar.
The bar's height was hard-coded in **three files with no way of knowing about each other**:

| File | Was | Now | What it controls |
|---|---|---|---|
| `shell/TopBar.tsx` | `h-8` | `h-[var(--menubar-h)]` | the bar itself |
| `layouts/DesktopCanvas.tsx` | `pt-8` | `pt-[var(--menubar-h)]` | the origin every window is positioned against |
| `shell/windowTiling.ts` | `MENU_BAR_H = 32` | `MENU_BAR_H = resolveMenuBarHeight()`, reading `--menubar-h` from the DOM at import, falling back to 32 on any failure | the geometry a window OPENS with |

All three now read the one `--menubar-h` token that already existed in `src/index.css` (two
unrelated rules in `shell-chrome.css` read it before; nothing else did). A
`@media (pointer: coarse) { :root:root { --menubar-h: 44px; } }` block in `shell-chrome.css`
raises it to 44px on a coarse pointer — the selector repeats (`:root:root`, not `:root`) so the
override wins on specificity rather than on CSS bundle order, which is a function of import
order this rule does not control and this repo has been bitten by that assumption before.

The six controls themselves are covered by one rule on the bar rather than six edits across
three components, two of which are outside this workstream (`core/SystemPulse.tsx`,
`core/ThemeToggle.tsx`, `shell/TopBar.tsx`):

```css
.vshell-bar button,
.vshell-bar [role='button'] {
  min-width: var(--touch-min, 44px);
  min-height: var(--touch-min, 44px);
}
```

**R-7, sub-12px type on the desktop.** 18 of the 19 nodes were the widget rail
(`src/widgets/host/widgets.css`: `.vwidget-title` 10.5px, `.vwidget-clock-when` 10.5px,
`.vwidget-label` 11.5px, `.vwidget-railbtn` 11px and eleven more declarations) and the 19th was
`layouts/DesktopCanvas.tsx`, the wallpaper's "alpha" wordmark at 10px (now `text-[12px]`, up
from `text-[10px]`). The widget-rail fifteen declarations became one token,
`--vwidget-fs-min: 12px`, set on `.vwidget-rail, .vwidget-gallery, .vwidget-config` and read by
every affected rule — a scale consolidation rather than fifteen separate numbers, which is what
lets the guard count text nodes rather than distinct sizes without a scale cleanup reading as a
regression.

**The gate's own exemption sets are now empty, and that emptiness is asserted.**
`KNOWN_BAR_TARGETS` in `frontend/e2e/shell-responsive.e2e.ts` used to name the six open
controls by accessible name (`System menu`, `Applications`, `Mission Control`, `Chat`, `Toggle
fullscreen`, `Theme mode`); it is now `new Set<string>([])`, and a coverage-count assertion
(`expect(KNOWN_BAR_TARGETS.size, ...).toBe(0)`) fails the suite if anyone reintroduces an
exemption without adding it back as a defended name. `.vwidget-rail` was added to `SHELL_ROOTS`
so its own text is swept rather than exempted.

**The gate itself was the finding this pass turned up.** Its first version passed 24/24 with
the half-done fix planted (`--menubar-h` reverted to `2rem` while the controls kept their 44px
floor) — the old check only asked whether a *control* cleared 44px, never whether the *bar*
could hold it. A control 44px tall inside a 32px bar is still "44px" by that measurement while
painted 6px above and below the bar's own box, over the window underneath it. The gate now
carries a `barFit()` check (`frontend/e2e/shell-responsive-harness.ts`) that measures the bar's
own height against its tallest control and asserts nothing escapes it, plus a check that the
window layer's inset (`windowInsetTop`) equals the bar's painted height — the two numbers that
used to live in separate files and could silently disagree. Reverting `--menubar-h` to `2rem`
under the strengthened gate now fails 2/24 (the two coarse-pointer viewports) with "menu-bar
control(s) painted outside the bar — bar 32px, tallest control 44px".

---

## 5. R-6 — the chrome share in landscape, bounded rather than redesigned

A phone in **landscape** is where a bottom dock hurts: at 568×320 the status bar (44px) and
the dock (86px) are **130px of a 320px viewport — 40.6%**. It falls to 33% at 844×390 and 30%
at 932×430, and it is 15% or less in every portrait profile.

Nothing is unreachable — the home grid scrolls — so this is a **bound on absurdity, not a
design target**, in the same spirit as the 96px tripwire in
`roadmap/MOBILE-SAFE-AREA-DEVICE-TEST.md`. The gate asserts ≤45%. Compacting the dock in
landscape (dropping the caption row buys ~16px) is a design decision with a real cost —
`MOBILE-SHELL.md` §4.1 is explicit that the dock's words matter — and is left open rather than
taken unilaterally under a brief about responsiveness.

**This assertion is the one thing in this file a portrait-only sweep could not have produced
at all.** Every mobile spec in this repository before it set portrait viewports only.

---

## 6. The gate

`frontend/e2e/shell-responsive.e2e.ts`, over `frontend/e2e/shell-responsive-harness.ts`.

**What it holds:**

1. **No horizontal overflow**, measured *twice* — the document's scroll extent AND every
   element's box against the viewport. Neither subsumes the other, and R-2/R-3 are the proof:
   69px of content off screen with a document `scrollWidth` of exactly 320.
2. **A 12px rendered type floor** over the shell's own chrome.
3. **A 44px touch floor** on every coarse-pointer profile, plus a `barFit()` check that the
   bar contains its tallest control and the window layer's inset agrees with the bar's painted
   height (added when R-5 closed — see §4).
4. **Chrome ≤ 45% of viewport height.**
5. **Window controls operable with a finger**, in a *tablet-landscape case that opens a
   window* — because window chrome does not exist until there is a window, and a sweep of the
   empty desktop would have reported the traffic lights as fixed by never rendering them.
6. **The phone status bar with an app fullscreen**, which is the branch of the identity block
   the fix actually changed.

**Coverage-count assertions.** Every guard in this repository that carried one survived
mutation testing; every one that lacked one did not.

- `WIDTH_COUNT` is asserted against the arrays the sweep iterates, **per bucket** — 4 phone
  portrait, 4 phone landscape, 6 tablet, 7 desktop. One total would still be satisfied by 23
  phones. Deleting an awkward width, which is how a responsive gate normally dies, fails the
  suite instead of quietly shrinking it.
- Both orientations are asserted to *be* the orientation they claim (`width > height` and the
  converse), the narrowest phone is asserted ≤360 and the widest tablet ≥1366.
- Per case, three denominators from `scanned()`: elements in the document (≥120), text nodes
  the type floor measured inside the shell roots (≥12), controls the touch floor found (≥6).
  An empty finding list against a surface that never rendered is the failure mode this repo
  keeps finding in its own guards.
- The window-controls case asserts it measured **≥3 lights** before asserting anything about
  their size.

### The gate was mutation-tested

Three mutations, each applied to the **thing** the gate guards (never to the check), each
applied and reverted by exact-string replacement rather than `git checkout` — other agents
have uncommitted work in this tree. Every run went through `e2e-isolated.mjs` and every one
printed `PROOF 1 ok` and `PROOF 2 ok — marker absent` in its own log, which is what says the
mutated build is the build the browser ran. That matters here specifically: `MOBILE-SHELL.md`
§1 records a mutation in this repo that "passed" because the build had failed and the test ran
against a stale bundle.

| Mutation | The thing broken | Result |
|---|---|---|
| **A** | `MobileHome.tsx` tile label 12px → 11px | **13 failed / 11 passed.** Every mobile-shell case red with *"shell chrome text below 12px"*; the desktop cases correctly stayed green, because the phone home grid is not mounted there |
| **B** | `.vwin-light` 44px → 0.75rem in the coarse block | **1 failed / 23 passed.** Exactly the tablet-landscape window case: *"window controls under 44px on a coarse pointer: [{"label":"Close window","w":24,"h":24}, …]"* |
| **C** | the status bar's identity block loses `flex-1 min-w-0` | **1 failed / 23 passed.** Exactly 360×800: *"painted outside the viewport — `div.vmob-bar-status` right: 21"*. 320 stayed green inside its known budget and 390+ stayed green, which is the discrimination the fix was for |

After all three reverts, `git status --porcelain` shows none of this workstream's files, and
the suite is **24 passed** again.

### Nothing neighbouring broke

`mobile-touch-targets`, `mobile-native`, `mobile-dock`, `mobile-shell`, `mobile-files`,
`insets-validation`, `mobile-contrast`, `desktop-layout`, `desktop-windows`, `shell-contrast`
— **106 passed**, one run, same provenance. Those suites are where the changes here could have
gone wrong quietly: the 44px floor on the phone chrome, the safe-area inset padding on
`.vmob-bar`, the home grid's column count at 768 and 834 (which the container query had to
reproduce exactly), and the desktop's deliberately dense mouse chrome, which the
`(pointer: coarse)` block must not have leaked into. Unit tests: 135 passed across
`src/mobile`, `src/layouts`, `src/shell`.

---

## 6b. Two things about the harness, for whoever runs this next

**`frontend/src/main.tsx` is shared state and `e2e-isolated.mjs` writes to it.** The script
isolates the `--outDir` and the port, but its `--plant-mutation` control plants its marker in
a source file every agent shares. During this pass another agent's plant runs held that marker
for most of an hour, and `e2e-isolated` correctly refuses to run against a tree carrying a
marker it did not plant. Two consequences worth recording: a **stranded** marker from a killed
run fails an unrelated agent's clean run as "the tree is dirty" (that happened here, once,
and was removed by exact-content match — never `git checkout`), and a **concurrent unplant**
can strip a live marker so a `--plant-mutation` control reports "marker absent" and proves
nothing. Runs in this pass were serialised and every result below was confirmed against
`PROOF 2 ok — marker absent` in the same run's own log.

**The content-hash check has a false negative.** `e2e-isolated.mjs` requires the served entry
to match `-[A-Za-z0-9_]{8,}\.js$`. Vite hashes with **base64URL**, whose alphabet includes
`-`, so a hash containing a hyphen matches only from that hyphen onward. Measured here: entry
`index-C-4eASDL.js` — an ordinary 8-character content hash — was rejected as *"carries no
content hash"*. It is not a flake: the hash is a function of the source, so **every** run of
that tree fails identically, and the failure reads as a build problem rather than as a guard
misfiring. The character class wants `-` in it. **Left unfixed at the coordinator's
instruction** (the harness is shared and is being changed for the plant-file contention
above); reported here so the two changes land together.

---

## 7. What an emulated viewport could not prove

Stated plainly, because "measured in Chromium at 390×844" and "works on a phone" are different
claims and only the first one can be made from here.

1. **`env(safe-area-inset-*)` is 0 in every measurement above.** Every number in §1 is a check
   of the LAYOUT and no check at all of the INSET. `roadmap/MOBILE-SAFE-AREA-DEVICE-TEST.md`
   is the device procedure and it is still OPEN.
2. **The container-query conversion in §2.1 is REASONED, not measured, for the case it was
   made for.** Its whole advantage over `sm:` appears when a landscape display cutout makes
   the grid narrower than the viewport, and there is no notched device here to produce a
   non-zero `--safe-left`. At every width in the sweep the two rules agree by construction.
3. **`pointer: coarse` / `hover: none` is a Playwright context flag, not a finger.** It drives
   the right media queries; it does not tell you whether a 44px target is *comfortable*, and
   it cannot exercise a long press, a two-finger gesture, or the on-screen keyboard shoving
   the viewport.
4. **The on-screen keyboard does not exist in these runs.** On a real phone in landscape it
   takes roughly half the remaining height, and R-6's 40.6% chrome share is measured *without*
   it. Any surface with a text field in landscape is unmeasured here.
5. **Text is Chromium's font stack on macOS.** The status-bar arithmetic in §3 turns on a
   ~56px clock and a ~317px cluster; both are font-metric dependent, and an Android WebView
   with a different default face will land on different numbers. The direction of the finding
   does not change; the exact pixel budget might.
6. **No real scrollbar.** macOS overlay scrollbars take 0 layout width. A platform with
   classic scrollbars removes ~15px from every width in this sweep, which is more than the
   slack the 360px case now has.
7. **Nothing here ran on hardware, in the APK, or over a Relay link.**

---

## Related

- `roadmap/MOBILE-SHELL.md` — the mobile idiom, §6.5 (which R-4 closes) and §7 (the reporting
  convention §3 and §4 follow)
- `roadmap/MOBILE-SAFE-AREA-DEVICE-TEST.md` — the inset check only a device can do
- `frontend/e2e/shell-responsive.e2e.ts` — the gate
- `frontend/e2e/shell-responsive-harness.ts` — the measurement primitives, and the widths
- `frontend/e2e/mobile-touch-targets.e2e.ts` — the 44px floor on the phone chrome, which this
  sweep extends to landscape and to the desktop canvas on a coarse pointer
- `frontend/scripts/e2e-isolated.mjs` — why a green run here is about this build's bytes
