/**
 * The Vulos desktop-layout model — types, enumerations and the token allowlist.
 *
 * # The one design rule
 *
 * Everything a preset or a third-party pack can express is ENUMERATED or
 * RANGE-BOUNDED here. There is no free-form CSS anywhere in this model, and
 * there deliberately never will be (see roadmap/CUSTOMIZATION.md §"Why not
 * custom CSS"). A customization system that can express anything can also
 * reposition, cover or restyle the shell's trust surfaces — the TrustBadge, the
 * public-app banner, the shared-desktop notice — and a theme that can fake or
 * hide those is a security defect wearing a stylesheet.
 *
 * So the whole surface is: five dock geometry enums, three boolean affordances,
 * an ordered list of app ids, which side the window controls sit on, and five
 * namespaced custom properties with declared types and ranges.
 *
 * # Two form factors, not one responsive one
 *
 * A phone dock is not a narrow desktop dock. It holds fewer and often DIFFERENT
 * items, it needs a persistent app-drawer affordance because it cannot fit the
 * library, and it must honour the home-indicator inset. So `DesktopLayout.dock`
 * is keyed by form factor and the two profiles persist under separate keys —
 * rearranging your desktop dock must never clobber your phone's.
 */

/** Which screen edge the dock is anchored to. */
export type DockEdge = 'bottom' | 'top' | 'left' | 'right'

/** Tile footprint. Small/medium/large map to 36/44/56 px plates. */
export type DockSize = 'small' | 'medium' | 'large'

/**
 * `floating` — a rounded island inset from the edge (the Vulos/macOS habit).
 * `bar`      — a full-span, square-cornered strip flush with the edge (the
 *              Windows/Ubuntu habit).
 */
export type DockStyle = 'floating' | 'bar'

/** Where the strip sits along its edge. */
export type DockAlign = 'start' | 'center' | 'end'

/** Which side of the titlebar the close/minimise/maximise cluster sits on. */
export type WindowControlsSide = 'left' | 'right'

/**
 * The two shells Vulos actually ships. `mobile` is everything below
 * shell/useViewport.ts's 768px MOBILE_BREAKPOINT, where layouts/MobileStack
 * owns the screen; `desktop` is DesktopCanvas.
 */
export type FormFactor = 'desktop' | 'mobile'

export const FORM_FACTORS: readonly FormFactor[] = ['desktop', 'mobile'] as const

export const DOCK_EDGES: readonly DockEdge[] = ['bottom', 'top', 'left', 'right'] as const
export const DOCK_SIZES: readonly DockSize[] = ['small', 'medium', 'large'] as const
export const DOCK_STYLES: readonly DockStyle[] = ['floating', 'bar'] as const
export const DOCK_ALIGNS: readonly DockAlign[] = ['start', 'center', 'end'] as const
export const WINDOW_CONTROL_SIDES: readonly WindowControlsSide[] = ['left', 'right'] as const

/**
 * What one dock holds, and how it is drawn, for ONE form factor.
 *
 * `items` is the preset's DEFAULT keep-in-dock set. It is not the user's pin
 * list — the user's own pins live in shell/Dock.tsx's `vulos-dock-pins` and win
 * whenever they exist. A preset seeds; it does not overwrite.
 */
export interface DockProfile {
  edge: DockEdge
  size: DockSize
  style: DockStyle
  align: DockAlign
  /** Slide the strip off its edge until pointed at or focused. */
  autohide: boolean
  /** Show the search/launcher affordance (⌘Space) in the strip. */
  launcher: boolean
  /** Show the assistant panel toggle in the strip. */
  assistant: boolean
  /**
   * Show a persistent app-drawer affordance. Mandatory on a phone, where the
   * strip cannot hold the library and there is no menu bar to reach it from.
   */
  drawer: boolean
  /** Ordered app ids this dock offers by default. */
  items: string[]
}

/** The complete, validated customization state. */
export interface DesktopLayout {
  /** Id of the preset this state came from. */
  presetId: string
  dock: Record<FormFactor, DockProfile>
  windowControls: WindowControlsSide
  /** Allowlisted custom properties, already type- and range-checked. */
  tokens: Record<string, string>
}

/** A named, pickable arrangement. Presets are DATA, defined once. */
export interface LayoutPreset {
  id: string
  /** Named for what it DOES, never for whose desktop it resembles. */
  name: string
  /**
   * Which platform's muscle memory this matches, stated plainly. Ergonomics,
   * not imitation: what transfers is convention, never anyone's trade dress.
   */
  familiar: string
  /** One preview-safe sentence. Safe to render verbatim in onboarding. */
  summary: string
  /** `builtin` = declared in presets.ts; `pack` = parsed from the pack format. */
  source: 'builtin' | 'pack'
  layout: Omit<DesktopLayout, 'presetId'>
}

/* ── The token allowlist ──────────────────────────────────────────────────────
   Five namespaced custom properties. Namespaced `--vd-` so they can never
   collide with, or shadow, the design tokens in index.css that the trust chrome
   is drawn from: nothing a pack sets is READ by TrustBadge, PublicAppBanner,
   SharedDesktopNotice or TransparencyPanel, by construction.

   Every entry declares a type AND a range, because the range is the security
   control. `--vd-dock-opacity` floors at 0.6 for exactly one reason: at 0 the
   dock is invisible while still occupying its edge, which is a UI that cannot
   be navigated back out of. There is no value in this table that can make a
   control disappear. */

export type TokenKind = 'length' | 'number' | 'color'

export interface TokenRule {
  kind: TokenKind
  /** px for `length`, unitless for `number`. Ignored for `color`. */
  min?: number
  max?: number
  doc: string
}

export const TOKEN_ALLOWLIST: Readonly<Record<string, TokenRule>> = Object.freeze({
  '--vd-dock-radius': { kind: 'length', min: 0, max: 28, doc: 'Corner radius of the dock strip.' },
  '--vd-dock-opacity': { kind: 'number', min: 0.6, max: 1, doc: 'Dock surface opacity. Floors at 0.6 so the dock can never be made invisible.' },
  '--vd-window-radius': { kind: 'length', min: 0, max: 24, doc: 'Corner radius of a desktop window frame.' },
  '--vd-accent': { kind: 'color', doc: 'Accent colour, as #rgb or #rrggbb. Legibility-corrected by ThemeProvider before it is drawn as text.' },
})

/* Tile SIZE is deliberately NOT a token. It is the `size` enum on the dock
   profile, so there is exactly one control for it. A `--vd-dock-icon` token
   would have been a second source of truth for the same pixel value, and the
   two would have had to agree in CSS (the plate) and in JS (the icon's `size`
   prop) — mock-vs-core divergence, invented on purpose. */

export const ALLOWED_TOKENS: readonly string[] = Object.freeze(Object.keys(TOKEN_ALLOWLIST))

/* ── Form-factor limits ───────────────────────────────────────────────────────
   These are not taste. They are the arithmetic that keeps a preset usable. */

/**
 * A phone dock may only sit on a horizontal edge.
 *
 * A left-vertical dock at 390px wide costs ~17% of the screen WIDTH permanently,
 * on the axis a phone has least of, and puts every target outside the thumb arc.
 * It works beautifully at 1440px, which is exactly why it has to be rejected
 * rather than merely discouraged.
 */
export const MOBILE_EDGES: readonly DockEdge[] = ['bottom', 'top'] as const

/**
 * A phone dock may not use the small tile.
 *
 * `small` draws a 36px plate. That clears WCAG 2.5.8's 24px floor but misses the
 * 44px platform target every touch guideline agrees on, and a phone dock is the
 * one surface where every target is hit with a thumb. Desktop keeps `small`
 * because a pointer is precise and a dense taskbar is a legitimate preference.
 */
export const MOBILE_SIZES: readonly DockSize[] = ['medium', 'large'] as const

/**
 * Five items on a phone dock.
 *
 * 390px (iPhone-class, the narrowest viewport this OS claims to support) minus
 * a 2×8px gutter, divided by 5 items plus the mandatory drawer affordance = 6
 * slots of ~62px. WCAG 2.5.8 wants 24px minimum and the platform guidance is
 * 44px; 62px clears both. A sixth item drops every slot to ~53px, which still
 * passes — but leaves no room for the safe-area inset on a notched device in
 * landscape, so the cap sits where the geometry is honest at every rotation.
 */
export const MOBILE_MAX_ITEMS = 5

/**
 * Twelve items on a desktop dock.
 *
 * At 768px — the narrowest viewport that renders DesktopCanvas at all — twelve
 * 44px tiles plus the launcher, the assistant and 1px gaps come to ~700px,
 * inside the viewport with the safe-area gutter to spare. Beyond that the strip
 * would rely on its overflow scroll to be usable on first paint, and a dock you
 * have to scroll before you can see your apps is not a dock.
 */
export const DESKTOP_MAX_ITEMS = 12

/**
 * App ids are lowercase slugs. Deliberately not a free string: an id ends up in
 * a DOM id, an aria-describedby and a registry lookup, and the day someone
 * writes `"../../etc"` or `"https://evil/x"` into a pack manifest it should be
 * a validation error and not a rendered element.
 */
export const APP_ID_RE = /^[a-z0-9][a-z0-9-]{0,63}$/

/** Preset and pack ids use the same grammar as app ids. */
export const PRESET_ID_RE = APP_ID_RE

/** The pack envelope's fixed discriminator and the only version we accept. */
export const PACK_FORMAT = 'vulos.desktop.pack'
export const PACK_VERSION = 1

/** A pack manifest, as it appears on disk / on the wire. */
export interface LayoutPack {
  format: typeof PACK_FORMAT
  version: typeof PACK_VERSION
  id: string
  name: string
  familiar: string
  summary: string
  layout: {
    dock: Record<FormFactor, DockProfile>
    windowControls: WindowControlsSide
    tokens?: Record<string, string>
  }
}

export type ValidationResult<T> =
  | { ok: true; value: T; errors: [] }
  | { ok: false; value: null; errors: string[] }
