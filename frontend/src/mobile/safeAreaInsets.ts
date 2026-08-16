// MOB-12b — the safe-area inset BOUNDARY.
//
// ── Why this file exists ────────────────────────────────────────────────────
//
// `src/index.css` defines the four inset tokens as the web fallback:
//
//     :root { --safe-top: env(safe-area-inset-top, 0px); ... }
//
// The Android APK (clients/android/.../MainActivity.kt) overrides them by
// running `document.documentElement.style.setProperty('--safe-top', '59.00px')`
// — an INLINE style, which beats that `:root` rule at every specificity. That
// is deliberate: `env(safe-area-inset-*)` inside a WebView only ever reports the
// display cutout, never the status bar or the navigation bar, so the native side
// has information CSS does not.
//
// The consequence nobody designed for is that a WRONG native value does not
// degrade to the working web fallback — it REPLACES it. Two failures were named
// by the author of the Kotlin, both of which review passes and hardware fails:
//
//   1. A wrong density divisor: physical pixels pushed where CSS pixels were
//      meant, so a 34px home-indicator inset arrives as ~134px at 390x844.
//      Syntactically perfect. Nothing in CSS can tell it from a real inset.
//
//   2. A lost `Locale.ROOT` in `String.format`: on a comma-decimal locale the
//      value is emitted as `"34,00px"`. `setProperty` ACCEPTS the string (an
//      unregistered custom property takes any token stream), `padding-bottom:
//      var(--safe-bottom)` is then invalid at computed-value time, and the
//      padding resolves to zero — the dock sits under the navigation bar. Every
//      Chromium screenshot looks perfect because Chromium is never in that
//      locale.
//
// The Kotlin cannot be compiled or run in this environment, so it cannot be the
// place this is fixed. This module makes a bad value impossible to APPLY, on the
// side that runs in Chromium today.
//
// ── The two layers ──────────────────────────────────────────────────────────
//
// LAYER 1 (always on, no JS): `src/index.css` registers the four tokens with
// `@property { syntax: '<length>' }`. A registered custom property is TYPED, so
// `setProperty('--safe-bottom', '34,00px')` is rejected by the CSSOM itself and
// the inline declaration never lands — the cascade falls through to the `:root`
// `env()` rule. That is the exact required semantics: reject means FALL BACK,
// not "apply zero". It covers every shell, including the desktop surfaces this
// module is not mounted on, and needs no JavaScript to be running.
//
// LAYER 2 (this file): `<length>` is a type, not a bound. `-40px` and `900px`
// are both perfectly good lengths and both are nonsense as insets, and a
// WebView older than Chrome 85 ignores `@property` entirely. So the guard
// re-reads the four inline properties on every mutation of the `<html>` style
// attribute, and REMOVES any that fails validation — removal being the one
// operation whose meaning is "fall back to the cascade", i.e. back to `env()`.
//
// The observer is a microtask; it runs at the end of the task that `setProperty`
// ran in, before the next paint, so a rejected value is never rendered.
//
// ── Where the boundary lives ────────────────────────────────────────────────
//
// One function — `checkInset` — decides what a legitimate inset is. Everything
// else in the shell that wants to set an inset goes through
// `applySafeAreaInsets`, which calls it. A second caller cannot bypass the
// check by writing the property directly either: the guard is watching the
// attribute, so a direct write is validated after the fact and reverted.

/** The four tokens, in the order the native bridge writes them. */
export const SAFE_AREA_PROPERTIES = ['--safe-top', '--safe-bottom', '--safe-left', '--safe-right'] as const
export type SafeAreaProperty = (typeof SAFE_AREA_PROPERTIES)[number]

/** Which viewport dimension bounds each edge. A top inset is a fraction of the
 *  viewport's HEIGHT; a landscape cutout inset is a fraction of its WIDTH.
 *  Bounding both against the same number was the obvious way to get this wrong. */
const AXIS: Record<SafeAreaProperty, 'height' | 'width'> = {
  '--safe-top': 'height',
  '--safe-bottom': 'height',
  '--safe-left': 'width',
  '--safe-right': 'width',
}

export type InsetReason =
  /** Accepted and within every bound. */
  | 'ok'
  /** Accepted and APPLIED, but larger than any inset a shipping device reports.
   *  See PLAUSIBLE_MAX_PX for why this is not a rejection. */
  | 'implausible'
  /** Nothing set (or explicitly cleared). Not an error — this is the state in
   *  which the `env()` fallback is supposed to be in charge. */
  | 'empty'
  /** Not a number followed by an optional unit at all: `"34,00px"`, `"calc(1px)"`,
   *  `"1e2px"`, `"Infinitypx"`, `"34px;"`. THE Locale.ROOT defect. */
  | 'malformed'
  /** A number with a unit this boundary does not accept — see UNIT below. */
  | 'unit'
  /** Below zero. An inset is a distance the content must stay OUT of; a negative
   *  one would pull content further under the system bar, and as a raw
   *  `padding: -20px` it is invalid CSS that poisons the whole declaration. */
  | 'negative'
  /** So large it cannot be a system inset on any device — it would hide more of
   *  the viewport than it could possibly be protecting. */
  | 'oversize'

export interface InsetCheck {
  /** May this value be applied? True for both 'ok' and 'implausible'. */
  readonly accepted: boolean
  /** The value in CSS pixels, or null when it could not be parsed / was rejected. */
  readonly px: number | null
  readonly reason: InsetReason
  /** The raw string as received, for diagnostics. */
  readonly raw: string
}

// ── The bounds, and where they come from ────────────────────────────────────
//
// UNIT. Only `px` (and a unitless `0`, which is a valid length everywhere).
// Deliberately narrow, because every other unit is either wrong for this job or
// unverifiable here:
//   · `%` on `padding-*` resolves against the containing block's INLINE size, so
//     `--safe-bottom: 5%` would produce a bottom padding derived from the
//     viewport WIDTH. It parses, it renders, and it is meaningless.
//   · `em` depends on the consuming element's font size, so the same token would
//     mean a different number of pixels in the dock than in the status bar.
//   · `vh`/`vw` express an inset as a fraction of the screen; a device inset is
//     a fixed physical distance and is not one.
//   · `rem`/`pt`/`cm` do resolve to fixed lengths, but nothing produces them —
//     the only writer is a bridge whose contract is CSS pixels — and accepting
//     them would mean re-implementing their conversion here just to bound-check
//     them.
// A stricter allowlist fails SAFE: an unrecognised unit falls back to `env()`,
// which is the correct value on any host that is not the Android WebView.
const UNIT = 'px'

/** Soft ceiling, absolute. The largest inset any shipping device reports is
 *  around 59 CSS px (the iPhone 14 Pro top cutout) and 48 CSS px (an Android
 *  3-button navigation bar); a 34px home indicator is typical. 96px is ~1.6x the
 *  largest known-good value, so nothing legitimate trips it, and it sits BELOW
 *  the values a 3x density-divisor bug produces (34 -> ~102, 59 -> ~177).
 *
 *  It is a FLAG, not a rejection, and that asymmetry is the whole point:
 *  rejecting means falling back to `env()`, which inside the Android WebView is
 *  0 for the navigation bar — the exact defect this workstream exists to stop.
 *  An inset that is too TALL is ugly and entirely usable; an inset that is
 *  wrongly ZERO puts the dock's touch targets under the navigation bar. So a
 *  merely-implausible value is applied and recorded, never substituted. */
export const PLAUSIBLE_MAX_PX = 96

/** Soft ceiling, relative — for small viewports, where 96px would be a large
 *  share of the screen. The worst real case is a landscape phone with a 48px
 *  3-button navigation bar on a 360px-tall viewport: 13.3%. 20% clears that. */
export const PLAUSIBLE_MAX_FRACTION = 0.2

/** Hard ceiling. An inset covering half the viewport is not an inset — there is
 *  no device on which the system chrome owns half the screen, and at that size
 *  the fallback (a possibly-zero `env()`) is unambiguously the better of two bad
 *  values, because the shell is unusable either way and only one of them is
 *  recoverable. */
export const REJECT_FRACTION = 0.5

// A number, then an optional unit. Split into two groups on purpose so a bad
// UNIT is reported differently from a value that is not a number at all — the
// distinction is what tells a device tester "your locale is wrong" apart from
// "your units are wrong".
//
// Scientific notation (`1e2px`) is not matched: CSS accepts it, no producer
// emits it, and a boundary should not accept a spelling only an attacker or a
// bug would choose. Leading `+` IS accepted; `String.format("%.2f")` never emits
// it, but it is unambiguously a non-negative number.
const NUMBER_THEN_UNIT = /^([+-]?(?:\d+(?:\.\d+)?|\.\d+))([a-z%]*)$/i

/**
 * THE boundary. Decide whether a raw custom-property value may be applied as a
 * safe-area inset, given the viewport extent (px) of the axis it applies to.
 *
 * Pure: no DOM, no globals. `extentPx <= 0` (an unmeasured or zero viewport, as
 * in a headless render) disables only the RELATIVE bounds; the absolute ones,
 * including the whole syntax check, still apply.
 */
export function checkInset(raw: string | null | undefined, extentPx: number): InsetCheck {
  const value = typeof raw === 'string' ? raw.trim() : ''
  if (value === '') return { accepted: false, px: null, reason: 'empty', raw: value }

  const m = NUMBER_THEN_UNIT.exec(value)
  if (!m) return { accepted: false, px: null, reason: 'malformed', raw: value }

  const n = Number(m[1])
  if (!Number.isFinite(n)) return { accepted: false, px: null, reason: 'malformed', raw: value }

  const unit = m[2].toLowerCase()
  // A unitless zero is a valid CSS length; a unitless anything-else is not, and
  // `padding: 34` would be dropped by the parser and take the declaration with it.
  if (unit !== UNIT && !(unit === '' && n === 0)) {
    return { accepted: false, px: null, reason: 'unit', raw: value }
  }

  if (n < 0) return { accepted: false, px: null, reason: 'negative', raw: value }

  const bounded = extentPx > 0 && Number.isFinite(extentPx)
  if (bounded && n > extentPx * REJECT_FRACTION) {
    return { accepted: false, px: null, reason: 'oversize', raw: value }
  }

  const softMax = bounded
    ? Math.min(PLAUSIBLE_MAX_PX, extentPx * PLAUSIBLE_MAX_FRACTION)
    : PLAUSIBLE_MAX_PX
  if (n > softMax) return { accepted: true, px: n, reason: 'implausible', raw: value }

  return { accepted: true, px: n, reason: 'ok', raw: value }
}

// ── Diagnostics ─────────────────────────────────────────────────────────────
//
// The guard cannot fix the Kotlin; it can only make the Kotlin's mistakes
// legible. This record is what the one-minute on-device check reads — see
// roadmap/MOBILE-SAFE-AREA-DEVICE-TEST.md. It is kept on `window` deliberately:
// a tester holding a phone has a remote-debugging console and nothing else.

export interface SafeAreaDiagnostics {
  /** Per-property verdict from the last sanitize pass. */
  readonly checks: Record<SafeAreaProperty, InsetCheck>
  /** Properties removed from the inline style because they failed validation. */
  readonly rejected: SafeAreaProperty[]
  /** Properties applied but flagged as larger than any real device reports —
   *  the density-divisor tripwire. */
  readonly flagged: SafeAreaProperty[]
  /** Viewport the bounds were computed against. */
  readonly viewport: { width: number; height: number }
}

declare global {
  interface Window {
    /** Populated by the safe-area guard. Read this on-device. */
    __vulosSafeArea?: SafeAreaDiagnostics
  }
}

function viewportOf(root: HTMLElement): { width: number; height: number } {
  const view = root.ownerDocument?.defaultView
  // visualViewport is the right number under an on-screen keyboard, but it is
  // not the number the CSS viewport units or a fixed-position dock use, and it
  // shrinks while typing — bounding an inset against it would flip a value from
  // valid to oversize mid-keystroke. innerWidth/innerHeight it is.
  return { width: view?.innerWidth ?? 0, height: view?.innerHeight ?? 0 }
}

let sanitizing = false

/**
 * The one commit path. `rawFor` supplies the candidate value for each property;
 * every candidate goes through `checkInset`, accepted ones are written and
 * rejected ones are REMOVED — removal being the operation that means "fall back
 * to the cascade", i.e. back to `:root { env(safe-area-inset-*) }`.
 *
 * Validation happens BEFORE the write, not after, so the outcome does not depend
 * on whether the browser supports `@property` (where an invalid `setProperty` is
 * a silent no-op that would leave a stale value looking current).
 */
function commit(root: HTMLElement, rawFor: (prop: SafeAreaProperty) => string | undefined): SafeAreaDiagnostics {
  const viewport = viewportOf(root)
  const checks = {} as Record<SafeAreaProperty, InsetCheck>
  const rejected: SafeAreaProperty[] = []
  const flagged: SafeAreaProperty[] = []

  sanitizing = true
  try {
    for (const prop of SAFE_AREA_PROPERTIES) {
      const raw = rawFor(prop)
      const extent = AXIS[prop] === 'height' ? viewport.height : viewport.width
      if (raw === undefined) {
        // Not part of this update: leave whatever is there, and report it.
        checks[prop] = checkInset(root.style.getPropertyValue(prop), extent)
        continue
      }
      const check = checkInset(raw, extent)
      checks[prop] = check
      if (check.reason === 'empty') {
        root.style.removeProperty(prop)
        continue
      }
      if (!check.accepted) {
        root.style.removeProperty(prop)
        rejected.push(prop)
        continue
      }
      if (check.reason === 'implausible') flagged.push(prop)
      if (root.style.getPropertyValue(prop) !== check.raw) root.style.setProperty(prop, check.raw)
    }
  } finally {
    sanitizing = false
  }

  const diagnostics: SafeAreaDiagnostics = { checks, rejected, flagged, viewport }
  const view = root.ownerDocument?.defaultView
  if (view) view.__vulosSafeArea = diagnostics
  report(diagnostics)
  return diagnostics
}

/**
 * Read the four inline safe-area properties off `root`, validate each, and
 * REMOVE the ones that fail — restoring the `:root { env(...) }` declaration
 * underneath. Idempotent; safe to call as often as you like.
 */
export function sanitizeSafeArea(root: HTMLElement = document.documentElement): SafeAreaDiagnostics {
  return commit(root, prop => root.style.getPropertyValue(prop))
}

// Warn once per distinct verdict. A native bridge re-pushes its insets on every
// inset change and on every navigation, so warning per pass would bury the
// console — the thing a tester is reading — under its own output.
let lastReport = ''
function report(d: SafeAreaDiagnostics): void {
  if (d.rejected.length === 0 && d.flagged.length === 0) { lastReport = ''; return }
  const line = [...d.rejected, ...d.flagged].map(p => `${p}=${d.checks[p].raw}(${d.checks[p].reason})`).join(' ')
  if (line === lastReport) return
  lastReport = line
  for (const prop of d.rejected) {
    console.warn(
      `[safe-area] refused ${prop}: ${JSON.stringify(d.checks[prop].raw)} (${d.checks[prop].reason}). ` +
      'Falling back to env(safe-area-inset-*). If this is the Android APK, the native inset push is producing invalid CSS.',
    )
  }
  for (const prop of d.flagged) {
    console.warn(
      `[safe-area] ${prop}=${JSON.stringify(d.checks[prop].raw)} is larger than any device reports ` +
      `(>${PLAUSIBLE_MAX_PX}px). Applied anyway, because a too-large inset is usable and a wrongly-zero one is not. ` +
      'A wrong display-density divisor in the native bridge looks exactly like this.',
    )
  }
}

/**
 * The sanctioned way for shell code to set safe-area insets. Values may be
 * numbers (CSS px) or strings; anything that fails `checkInset` is REMOVED
 * rather than written, so the `env()` fallback takes over — a rejected update
 * never leaves a stale value behind pretending to be the current inset.
 */
export function applySafeAreaInsets(
  insets: Partial<Record<SafeAreaProperty, string | number>>,
  root: HTMLElement = document.documentElement,
): SafeAreaDiagnostics {
  return commit(root, prop => {
    const v = insets[prop]
    if (v === undefined) return undefined
    // Numbers are formatted here, in one place, so no caller can reproduce the
    // locale bug: Number.prototype.toFixed is locale-INDEPENDENT by spec, unlike
    // toLocaleString and unlike Java's String.format without an explicit Locale.
    if (typeof v === 'number') return Number.isFinite(v) ? `${v.toFixed(2)}px` : String(v)
    return String(v)
  })
}

/**
 * Install the guard: sanitize now, then re-sanitize on every mutation of the
 * `<html>` style attribute (which is how the native bridge delivers insets) and
 * on resize/orientation change (which moves the relative bounds).
 *
 * Idempotent per root — a second install replaces the first rather than stacking
 * observers. Returns a teardown.
 */
export function installSafeAreaGuard(root: HTMLElement = document.documentElement): () => void {
  const view = root.ownerDocument?.defaultView
  if (!view || typeof view.MutationObserver !== 'function') {
    // No observer (a non-DOM host); the one-shot sanitize is still worth doing.
    sanitizeSafeArea(root)
    return () => {}
  }

  const observer = new view.MutationObserver(() => {
    // Our own removeProperty/setProperty calls mutate the same attribute. The
    // pass is idempotent, so re-entering would terminate anyway — but it would
    // double every console warning, so it is skipped explicitly.
    if (sanitizing) return
    sanitizeSafeArea(root)
    // Drop the records our own removals just queued, so the next real push is
    // not preceded by a redundant pass.
    observer.takeRecords()
  })
  observer.observe(root, { attributes: true, attributeFilter: ['style'] })

  const onViewportChange = () => { sanitizeSafeArea(root) }
  view.addEventListener('resize', onViewportChange)
  view.addEventListener('orientationchange', onViewportChange)

  sanitizeSafeArea(root)

  return () => {
    observer.disconnect()
    view.removeEventListener('resize', onViewportChange)
    view.removeEventListener('orientationchange', onViewportChange)
  }
}
