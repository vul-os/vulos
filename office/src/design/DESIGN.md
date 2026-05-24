# Vulos Office — Design System

This document describes the design language for **Vulos Office** and should be
treated as the source of truth when extending the UI to new surfaces (Sheets,
Slides, Spaces, Status, Verify, Meetings, and any sibling apps under the Vulos
umbrella).

The system is **token-first**: every colour, font, spacing, radius, shadow,
and motion value lives in [`tokens.css`](./tokens.css) as a CSS custom property,
and is exposed to Tailwind through `tailwind.config.js`.  Do not introduce
new raw hex codes, pixel sizes, or shadow definitions in component code — add
or extend a token instead.

---

## 1. Direction

We are drawing from:

- **Linear** — information density without clutter; quiet active states; keyboard polish.
- **Mercury** (Banking UI) — warm neutrals, calm motion, restrained chrome.
- **Tana / Bear** — document-first; the editing surface is the protagonist.
- **Vercel** — typographic discipline; uppercase eyebrows; tight tracking on chrome.

We are explicitly **avoiding**:

- shadcn defaults, Material Design, default Tailwind UI starters.
- Glassmorphism, neumorphism, heavy gradients, bright purple-on-white.
- Inter as the only typeface, generic react-icons sets, Bootstrap blue (`#3b82f6`).

---

## 2. Colour

### 2.1 Palette

| Token        | Light                  | Dark                  | Purpose                                           |
| ------------ | ---------------------- | --------------------- | ------------------------------------------------- |
| `bg`         | `#fbf9f5` (oat-50)     | `#131110`             | App background                                    |
| `paper`      | `#ffffff`              | `#1c1a17`             | Document surface, cards                           |
| `clay`       | `#f5f1ea` (oat-100)    | `#25221d`             | Sidebar, sunken panels                            |
| `ink`        | `#1a1916`              | `#f1ece2`             | Primary text                                      |
| `ink-muted`  | `#4d4940`              | `#c8c0b0`             | Secondary text                                    |
| `ink-faint`  | `#8e8470`              | `#8e8674`             | Metadata, eyebrows, placeholders                  |
| `line`       | `#ece5da`              | `#2a2622`             | Hairline dividers, default borders                |
| `line-strong`| `#d8cebd`              | `#3a352e`             | Input borders, hover-emphasised dividers          |
| `accent`     | `#0f6a6c` (teal-600)   | `#3fad9b` (teal-400)  | The one accent. Primary buttons, focus, links.    |
| `accent-tint`| `#effaf8`              | `rgba(63,173,155,.10)`| Hover background on toolbars, comment anchors     |
| `success`    | `#4f7a4d` (sage)       | same                  | "Saved", resolved comments, accepted suggestion   |
| `warning`    | `#c08436` (honey)      | same                  | "Restore draft" banners, soft validations         |
| `danger`     | `#b8453a` (persimmon)  | same                  | Save errors, destructive actions                  |
| `info`       | `#4a6b8a` (dusty navy) | same                  | The ONLY blue we use, for "Spaces" category icon  |

### 2.2 The accent rule

**There is exactly one accent: deep teal.**  We picked it over terracotta to
read calm + trustworthy on a Docs/Signing surface.  Do not introduce a second
accent.  When you need a new category colour (e.g. for an app icon), use one
of the existing signal hues (`warning`, `danger`, `success`, `info`), or stay
in the warm-neutral scale.

### 2.3 Signal colours are warm

We do not use generic `green-500` / `red-500` / `blue-500`.  Our signal
palette skews warm — **sage**, **persimmon**, **honey**, **dusty navy** — to
sit comfortably alongside the oat neutrals.

---

## 3. Typography

### 3.1 The pair

| Role     | Token         | Stack                                                                                  |
| -------- | ------------- | -------------------------------------------------------------------------------------- |
| Chrome   | `--font-sans` | `ui-sans-serif`, then OS-native grotesques (SF Pro, Segoe UI Variable, Inter Display) |
| Document | `--font-serif`| `ui-serif`, then OS-native serifs (Iowan Old Style, Charter, Source Serif Pro, Cambria)|
| Code     | `--font-mono` | `ui-monospace`, JetBrains Mono, SF Mono, Menlo                                         |

We deliberately **do not ship Inter as a webfont**.  `ui-sans-serif` gives a
characterful, OS-native grotesque on every modern platform and keeps the
bundle tiny.  This is the same approach Vercel's new editorial sites use.

**Optional upgrade**: if a brand needs strict cross-OS consistency, self-host
`Inter Display` + `Source Serif Pro` and override the two CSS variables in
`tokens.css` — no other change is needed.

### 3.2 Where each font goes

- **Sans (chrome)**: the entire app shell, toolbars, sidebars, buttons,
  metadata, comment author names, table headers, code.
- **Serif (document)**: TipTap document body (`.tiptap p`, `.tiptap blockquote`,
  task list bodies), comment anchor quotes (`"…"`), SignView display
  headlines (`Welcome back.`, `Signed.`, `<Signer name>`).

If you're building a new surface and you're unsure: **default to sans**.
Use serif sparingly, only where the surface is itself a document or where
you want an editorial moment (e.g. "Signed." on the post-submit page).

### 3.3 Scale and tracking

The scale is `--text-2xs` (11px) → `--text-3xl` (36px), minor-third ratio
anchored at 14 px.  All chrome uses **tracking-tightish** (`-0.014em`);
uppercase eyebrows use **tracking-eyebrow** (`0.08em`).

Do not use Tailwind's default `tracking-tight` / `tracking-wider` — use the
token-backed `tracking-tightish` / `tracking-eyebrow` so refinement stays
consistent.

---

## 4. Spacing, Radii, Elevation

### 4.1 Spacing

4-px base.  Prefer `gap-2` (8 px) for chrome rows, `gap-4` (16 px) for cards.
Generous whitespace beats density — give every section room.

### 4.2 Radii

| Token        | Use                                                  |
| ------------ | ---------------------------------------------------- |
| `rounded-xs` (4 px) | Inline marks, small chips, suggestion highlights |
| `rounded-sm` (6 px) | Default buttons, inputs                          |
| `rounded-md` (8 px) | Cards, segmented controls                        |
| `rounded-lg` (12 px)| Modals, document canvas, large panels            |
| `rounded-xl` (16 px)| Hero shells (use sparingly)                      |
| `rounded-pill`      | Badges, pill counters, toggle dots               |

### 4.3 Elevation

Three steps only.  All shadows have a warm tint (`rgba(36, 28, 16, …)` in
light mode) and ride low — the UI should look **printed**, not floating.

| Token        | Use                                                       |
| ------------ | --------------------------------------------------------- |
| `shadow-e1`  | Primary buttons, document canvas, light raise on cards    |
| `shadow-e2`  | Popovers (toolbar dropdowns, font picker), tooltips       |
| `shadow-e3`  | Modals                                                    |
| `shadow-focus` | Focus ring — accent tint at 3 px, replaces OS default   |

Do NOT use heavy `shadow-2xl` material-style shadows.  If a surface needs to
feel "raised", it should be by a border + e1, not by a giant drop.

---

## 5. Motion

| Token              | Value                                  | Use                                |
| ------------------ | -------------------------------------- | ---------------------------------- |
| `duration-fast`    | 120 ms                                 | Hover / focus colour transitions   |
| `duration-base`    | 200 ms                                 | Default opens, tab switches        |
| `duration-slow`    | 320 ms                                 | Modals, page reveals, scrolls      |
| `ease-out`         | `cubic-bezier(.22, .61, .36, 1)`        | Default UI ease                    |
| `ease-spring`      | `cubic-bezier(.16, 1.0, .3, 1)`         | Soft springs for open / close      |
| `ease-in`          | `cubic-bezier(.4, 0, 1, 1)`             | Used only for "leave" anims        |

Keyframes provided: `animate-fade-in`, `animate-rise-in`, `animate-scale-in`,
`animate-slide-in-right` — use these for panel/modal mounts.  Prefer them
over inline JS / framer-motion.

`prefers-reduced-motion` is honoured: durations collapse to 0 ms automatically.

---

## 6. Components — patterns to follow

### 6.1 Buttons

Use `<Button>` from `src/components/ui`.  Variants:

- **primary** — exactly one per surface (the "yes" action: Save, Sign, Create).
- **secondary** — the default. Paper bg + line border.
- **ghost** — toolbars and tertiary actions.
- **destructive** — soft persimmon. Confirm dialogs only.
- **link** — underlined text-only. Use sparingly.

Do **not** stack two primary buttons next to each other — that loses the
hierarchy.

### 6.2 Inputs

Use `<Input>` from `src/components/ui` for any text field.  Pass `leading`
for an icon adornment (Search, Lock, …).  Errors go in the `error` prop;
hints in `hint`.  Heights line up with Button heights so rows align.

### 6.3 Tabs

Use `<Tabs>`.  Underline-only style.  Do **not** rebuild tabs with pill
backgrounds — that's the Tailwind-UI cliché.

### 6.4 Modals

Use `<Modal>`.  Backdrop is `rgba(26, 25, 22, 0.36)` (warm ink at 36 %), with
a 2 px blur.  Open animation is `scale-in` with `ease-spring`.

### 6.5 Sidebar

Use `<Sidebar>` primitives.  Active rows get a 2-px accent rail on the LEFT,
not a filled background.  Collapsed width is 56 px; expanded is 240 px.
App-category icons may carry a single warm tint each; the rail itself is
neutral.

### 6.6 Topbar

Use `<Topbar>`.  44 px tall, hairline bottom border.  Slot model:
`leading | title | meta | actions`.  Status indicators (saving / saved / unsaved)
go in `meta` as a discreet inline line — **never** a banner.

### 6.7 Tooltips

Use `<Tooltip>`.  300 ms hover delay so the chrome doesn't flicker.

---

## 7. Do / Don't

### Do

- Reach for tokens (`bg-paper`, `text-ink`, `border-line`, `bg-accent`).
- Pair sans chrome with serif document bodies.
- Use one accent button per surface.
- Use the `tracking-eyebrow` + uppercase 11-px label for section headings.
- Animate panel mounts with the provided keyframes.
- Use `paper-grain` on hero / signer-facing surfaces for letterpress tooth.
- Force `data-theme="light"` on public-facing surfaces (e.g. SignView) so a
  signer's OS dark mode doesn't make their signing page look threatening.

### Don't

- Don't write `bg-indigo-500`, `text-gray-700`, `border-gray-200`.  These
  bypass the token layer.  Use semantic tokens.
- Don't use `shadow-2xl` or `shadow-lg`.  Use `e1` / `e2` / `e3`.
- Don't use emerald / red-500 for success / error.  Use `success` / `danger`.
- Don't introduce a second accent — extend a category colour instead.
- Don't add Inter back as a webfont.  If you need a specific look, override
  the two CSS variables in `tokens.css`.
- Don't introduce framer-motion / motion-one / react-spring without a
  cross-team review — CSS keyframes + Tailwind transitions cover 95 % of
  what we need.

---

## 8. Dark mode

Dark mode is **first-class**, not an inversion.

- Triggered by `[data-theme="dark"]` on `<html>` OR by OS preference if no
  explicit override.
- Surfaces use warm-dark coffee tones (`#131110` base, `#1c1a17` paper),
  not slate (`#0f172a`).
- The accent shifts from `teal-600` (light) to `teal-400` (dark) for legibility.
- Signal backgrounds get a 14 % alpha overlay so they don't shout.
- `.paper-grain` reduces its opacity and switches to screen-blend in dark
  mode so the texture remains subtle.

The `useTheme()` hook (in `components/ui/useTheme.js`) provides explicit
light / dark / system cycling, persisted to `localStorage['vulos.theme']`.
The cycler IconButton lives in the sidebar footer.

---

## 9. Surfaces touched in this pass

| Surface                | Status                                                              |
| ---------------------- | ------------------------------------------------------------------- |
| `tokens.css`           | **New**. Source of truth.                                           |
| `tailwind.config.js`   | Rebuilt against tokens.                                             |
| `index.css`            | Rebuilt: token-driven base, TipTap prose in serif, scrollbar, marks.|
| `index.html`           | Inter removed; theme-color tracks light/dark; comments documented.  |
| `components/ui/*`      | **New** primitives: Button, IconButton, Input, Card, Tabs, Modal, Tooltip, Sidebar, Topbar, useTheme. |
| `components/Layout.jsx`| Rewritten against design-system Sidebar.                            |
| `components/LoginScreen.jsx` | Warm paper, serif headline, single accent button.              |
| `components/CommentsPanel.jsx` | Clean side rail, design-system Tabs, anchor quoted in serif. |
| `apps/docs/DocsEditor.jsx` | New Topbar + meta-line save status + paper canvas + grain.     |
| `apps/docs/DocsToolbar.jsx`| Token colours, eyebrow group labels, quiet Export dropdown.     |
| `apps/pdf/SignView.jsx`| Public signer page: warm paper, serif name, single-accent fields, progress bar, sage-success completion screen. |
| `apps/sheets/SheetsEditor.jsx` | Design-system `Topbar` + meta-line save status + quiet Export dropdown; Fortune Sheet kept intact, selection / header / tab chrome retinted via `.sheets-themed` rules through tokens (accent + warm neutrals). |
| `apps/slides/SlidesEditor.jsx` | Design-system `Topbar` + meta-line save status; thumbnail rail rebuilt against `bg-clay` + 2-px accent rail for active slide; quiet token-driven mini toolbar (uses `IconButton` + `Tooltip`); paper-grain slide canvas; speaker notes use the warm `warning-bg` strip. DOMPurify `sanitize()` retained on every `slide.content` rendering path. |
| `apps/slides/SlidePreview.jsx` | Light-touch retint — reveal.js owns the deck rendering; only the close affordance is design-system (focus ring + token colours). `sanitize()` + DOMPurify wrapper preserved. |
| `apps/spaces/SpacesApp.jsx` | Design-system `Sidebar` for channel/DM/thread tree, `Input` search, `Modal`-based Create Channel / New DM flows; presence footer + StatusPicker on tokens. |
| `apps/spaces/ChannelView.jsx` | `Topbar` with roster pills + `PresenceDot`s, paper compose lane with single primary Send, slide-in right-rail thread context panel (mirrors `CommentsPanel`). |
| `apps/spaces/MessageList.jsx` | Serif italic small-caps date separators, comfortable density, accent-tint own-message rail, edit/delete menu via `Card`-like popover; warm-signal status indicators. |
| `apps/spaces/CallView.jsx` | Warm-ink call surface, quiet 2-px accent outline on active speaker, accent-tint P2P / warning Relay transport pill, IconButton dock cluster, 55%-height screen-share preserved; WebRTC + signaling untouched. |
| `apps/spaces/Room.jsx` | Lobby `Card` with serif headline, warmly-framed camera preview, invitee tint-pill roster, single accent Join button. |
| `apps/spaces/Meetings.jsx` | `Topbar` dashboard, meeting `Card`s with warm-signal status pills, `Modal` create flow, `Tooltip`-driven "Link copied" confirmation; `credentials: 'include'` preserved on every fetch. |
| `apps/spaces/InCallChat.jsx` | Themed against the call surface (warm-ink bg, accent own-bubble, slide-in animation). |
| `components/PresenceBar.jsx` | Warm-signal `PresenceDot` palette (sage / honey / persimmon / accent), serif-italic small-caps tooltip name labels, token-driven `StatusPicker`. |
| `components/RemoteCursors.jsx` | Caret + cell-selection name labels rendered in serif italic with the warm tracking; aligned with the PresenceBar tooltip treatment. |
| `apps/pdf/PDFEditor.jsx` | Design-system `Topbar` with quiet meta-line, distinguished primary "Prepare to Sign" `Button`, warm-paper canvas + `paper-grain`, page thumbnails sidebar with 2-px accent rail on the selected slot, signature modal ported to design-system `Tabs` + `IconButton`, single-accent annotation overlays (no rainbow). |
| `apps/pdf/SigningSetup.jsx` | Field type chips as `IconButton`s with serif-italic labels, signer roster as `Card`s with a per-signer colour stripe (name + email in serif italic), required toggle as a clear box-check control, signing-order picker (sequential / parallel) via underline `Tabs`. Drag-place + persist logic untouched. |
| `components/EnvelopeDashboard.jsx` | Card-per-envelope with warm signal-hue status badges (sage / honey / persimmon), quiet horizontal accent progress bar, expandable per-signer rows in serif italic, remind/cancel as `IconButton`s + `Tooltip`s. |
| `components/Verify.jsx` | Public verification page: warm paper drop-zone, serif "Verify a Vulos-signed document" headline, calm verdict reveal with sage `ShieldCheck` / persimmon `ShieldAlert`, collapsible per-signer rows in serif body, `Powered by Vulos Office` provenance footer. |

## 10. Surfaces deferred to the next pass

All previously deferred PDF / Signing surfaces — `PDFEditor`, `SigningSetup`,
`EnvelopeDashboard`, `Verify` — have been migrated to the token layer in this
pass and are documented in §9 above.  No surfaces remain on the deferred list.
