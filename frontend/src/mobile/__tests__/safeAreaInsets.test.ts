// MOB-12b — the safe-area inset boundary.
//
// These tests stand in for a device the repo does not have. The Android bridge
// writes `--safe-*` as INLINE styles on <html>, which outrank the
// `env(safe-area-inset-*)` fallback in src/index.css, so a wrong native value
// replaces the working fallback instead of degrading to it. The two failures the
// Kotlin's author named — a lost `Locale.ROOT` producing "34,00px", and a wrong
// density divisor producing "134px" where "34px" was meant — are both exercised
// here by name.
//
// jsdom is deliberately the right harness for the JS half: it stores custom
// properties WITHOUT validation, exactly like a browser that has not registered
// them with `@property` (an old Android WebView, i.e. the worst case). The
// `@property` half is a CSSOM behaviour and is covered in
// e2e/insets-validation.e2e.ts, in real Chromium.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  checkInset,
  sanitizeSafeArea,
  applySafeAreaInsets,
  installSafeAreaGuard,
  SAFE_AREA_PROPERTIES,
  PLAUSIBLE_MAX_PX,
  type SafeAreaProperty,
} from '../safeAreaInsets'

// jsdom's window is 1024 x 768 unless told otherwise, so:
//   top/bottom bounds are against 768 → hard reject > 384, soft flag > 96
//   left/right bounds are against 1024 → hard reject > 512, soft flag > 96
const VIEWPORT_H = 768
const VIEWPORT_W = 1024

let warn: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  for (const p of SAFE_AREA_PROPERTIES) document.documentElement.style.removeProperty(p)
  delete window.__vulosSafeArea
  warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
})
afterEach(() => {
  warn.mockRestore()
  for (const p of SAFE_AREA_PROPERTIES) document.documentElement.style.removeProperty(p)
})

const read = (p: SafeAreaProperty) => document.documentElement.style.getPropertyValue(p)
const flush = () => new Promise(r => setTimeout(r, 0))

// ───────────────────────────────────────────────────────────────────────────
// checkInset — the one place that decides what a legitimate inset is.
// ───────────────────────────────────────────────────────────────────────────

describe('checkInset — what is a legitimate inset', () => {
  it.each([
    // The real numbers off real hardware, all of which must survive.
    ['34px', 34],       // iPhone home indicator
    ['34.00px', 34],    // exactly what String.format("%.2f") emits
    ['59px', 59],       // iPhone 14 Pro top cutout — the largest real inset
    ['48px', 48],       // Android 3-button navigation bar
    ['24px', 24],       // Android status bar
    ['0px', 0],
    ['0', 0],           // a unitless zero is a valid CSS length
    ['+34.00px', 34],   // signed but non-negative
    ['.5px', 0.5],
    ['  34px  ', 34],   // CSSOM round-trips can leave whitespace
    ['-0px', -0],       // negative zero is zero
  ])('accepts %j', (raw, px) => {
    const c = checkInset(raw, VIEWPORT_H)
    expect(c.accepted).toBe(true)
    expect(c.reason).toBe('ok')
    expect(c.px).toBe(px)
  })

  it('reports an unset property as empty, which is not an error', () => {
    // 'empty' is the state in which env() is supposed to be in charge.
    expect(checkInset('', VIEWPORT_H).reason).toBe('empty')
    expect(checkInset('   ', VIEWPORT_H).reason).toBe('empty')
    expect(checkInset(null, VIEWPORT_H).reason).toBe('empty')
    expect(checkInset(undefined, VIEWPORT_H).reason).toBe('empty')
  })

  // THE named defect: a lost Locale.ROOT in the native String.format.
  it.each(['34,00px', '34,0px', '0,00px', '1 234px'])(
    'rejects the comma-decimal value %j as malformed', raw => {
      const c = checkInset(raw, VIEWPORT_H)
      expect(c.accepted).toBe(false)
      expect(c.reason).toBe('malformed')
      // Not zero. Rejection has to mean "no value", so the cascade can supply
      // one — if this ever reported px: 0 the boundary would be re-committing
      // the original bug.
      expect(c.px).toBeNull()
    },
  )

  it.each([
    'calc(34px)',
    'env(safe-area-inset-bottom)',
    'var(--x)',
    '34px;',
    '34px !important',
    '1e2px',       // CSS accepts scientific notation; no producer emits it
    'Infinitypx',
    'NaN',
    'undefined',
    'null',
    '',
    '34px 0px',
    'expression(1)',
  ].filter(v => v !== ''))('rejects %j', raw => {
    expect(checkInset(raw, VIEWPORT_H).accepted).toBe(false)
  })

  it.each([
    ['34', 'unit'],     // unitless non-zero: `padding: 34` is invalid CSS
    ['5%', 'unit'],     // % on padding resolves against the INLINE size — width
    ['2rem', 'unit'],
    ['2em', 'unit'],
    ['10vh', 'unit'],
    ['10vw', 'unit'],
    ['20pt', 'unit'],
    ['1cm', 'unit'],
  ] as const)('rejects %j because of its unit', (raw, reason) => {
    const c = checkInset(raw, VIEWPORT_H)
    expect(c.accepted).toBe(false)
    expect(c.reason).toBe(reason)
  })

  it.each(['-1px', '-20px', '-34.00px', '-0.5px'])('rejects the negative inset %j', raw => {
    const c = checkInset(raw, VIEWPORT_H)
    expect(c.accepted).toBe(false)
    expect(c.reason).toBe('negative')
  })

  it('rejects an inset larger than half its axis as oversize', () => {
    // 385 > 768 * 0.5. An inset that hides half the viewport is not an inset.
    expect(checkInset('385px', VIEWPORT_H).reason).toBe('oversize')
    expect(checkInset('384px', VIEWPORT_H).accepted).toBe(true)
    // The axis matters: the same 500px is oversize against a 768 height and
    // merely implausible against a 1024 width. Bounding both edges against one
    // dimension was the obvious way to get this wrong.
    expect(checkInset('500px', VIEWPORT_H).reason).toBe('oversize')
    expect(checkInset('500px', VIEWPORT_W).reason).toBe('implausible')
  })

  // THE other named defect: a wrong density divisor. 390x844 phone, real bottom
  // inset 34px, pushed as ~134px.
  it('applies a density-bug value but flags it, because zero would be worse', () => {
    const c = checkInset('134px', 844)
    expect(c.accepted).toBe(true)      // APPLIED — a too-tall dock is usable
    expect(c.px).toBe(134)
    expect(c.reason).toBe('implausible')
  })

  it.each([
    ['96px', 'ok'],           // the largest value no device exceeds
    ['96.01px', 'implausible'],
    ['102px', 'implausible'], // 34 at 3x density
    ['177px', 'implausible'], // 59 at 3x density
  ] as const)('%j is %s against the absolute plausibility ceiling', (raw, reason) => {
    expect(checkInset(raw, 844).reason).toBe(reason)
    expect(PLAUSIBLE_MAX_PX).toBe(96)
  })

  it('tightens the plausibility ceiling on a short viewport, but not below a real nav bar', () => {
    // Landscape phone: 360px tall, 48px 3-button navigation bar = 13.3%.
    expect(checkInset('48px', 360).reason).toBe('ok')
    // 20% of 360 is 72px, so the relative ceiling binds before the 96px one.
    expect(checkInset('73px', 360).reason).toBe('implausible')
  })

  it('keeps the absolute bounds when the viewport cannot be measured', () => {
    // extent 0 (headless/unattached document) disables only the RELATIVE bounds.
    expect(checkInset('34,00px', 0).reason).toBe('malformed')
    expect(checkInset('-20px', 0).reason).toBe('negative')
    expect(checkInset('34px', 0).reason).toBe('ok')
    expect(checkInset('5000px', 0).reason).toBe('implausible')
  })
})

// ───────────────────────────────────────────────────────────────────────────
// sanitizeSafeArea — rejection means REMOVE, so the cascade takes over.
// ───────────────────────────────────────────────────────────────────────────

describe('sanitizeSafeArea', () => {
  it('leaves a plausible set of insets exactly as pushed', () => {
    const root = document.documentElement
    root.style.setProperty('--safe-top', '59.00px')
    root.style.setProperty('--safe-bottom', '34.00px')
    root.style.setProperty('--safe-left', '0.00px')
    root.style.setProperty('--safe-right', '0.00px')
    const d = sanitizeSafeArea(root)
    expect(d.rejected).toEqual([])
    expect(d.flagged).toEqual([])
    expect(read('--safe-top')).toBe('59.00px')
    expect(read('--safe-bottom')).toBe('34.00px')
  })

  it('REMOVES a comma-decimal value rather than zeroing it', () => {
    const root = document.documentElement
    root.style.setProperty('--safe-bottom', '34,00px')
    const d = sanitizeSafeArea(root)
    expect(d.rejected).toEqual(['--safe-bottom'])
    // The whole point: the inline declaration is gone, so `:root { --safe-bottom:
    // env(safe-area-inset-bottom, 0px) }` is what the dock reads again. If this
    // were set to '0px' instead, the boundary would be applying the very value
    // the defect produced.
    expect(read('--safe-bottom')).toBe('')
    expect(root.getAttribute('style') ?? '').not.toContain('--safe-bottom')
  })

  it('removes only the bad property and keeps the good ones', () => {
    const root = document.documentElement
    root.style.setProperty('--safe-top', '59.00px')
    root.style.setProperty('--safe-bottom', '34,00px')
    const d = sanitizeSafeArea(root)
    expect(d.rejected).toEqual(['--safe-bottom'])
    expect(read('--safe-top')).toBe('59.00px')
    expect(read('--safe-bottom')).toBe('')
  })

  it.each(['-20px', '900px', '34', '5%', 'calc(20px)'])('removes %j', raw => {
    const root = document.documentElement
    root.style.setProperty('--safe-top', raw)
    sanitizeSafeArea(root)
    expect(read('--safe-top')).toBe('')
  })

  it('keeps a density-bug value and records it as flagged', () => {
    const root = document.documentElement
    root.style.setProperty('--safe-bottom', '134px')
    const d = sanitizeSafeArea(root)
    expect(d.rejected).toEqual([])
    expect(d.flagged).toEqual(['--safe-bottom'])
    expect(read('--safe-bottom')).toBe('134px')
  })

  it('is idempotent', () => {
    const root = document.documentElement
    root.style.setProperty('--safe-top', '34px')
    root.style.setProperty('--safe-bottom', '34,00px')
    const first = sanitizeSafeArea(root)
    const second = sanitizeSafeArea(root)
    expect(first.rejected).toEqual(['--safe-bottom'])
    expect(second.rejected).toEqual([])  // already gone
    expect(read('--safe-top')).toBe('34px')
  })

  it('publishes diagnostics on window for the on-device check', () => {
    const root = document.documentElement
    root.style.setProperty('--safe-bottom', '34,00px')
    root.style.setProperty('--safe-top', '134px')
    sanitizeSafeArea(root)
    const d = window.__vulosSafeArea
    expect(d).toBeDefined()
    expect(d!.rejected).toEqual(['--safe-bottom'])
    expect(d!.flagged).toEqual(['--safe-top'])
    // The raw string survives into diagnostics — the tester needs to SEE
    // "34,00px" to know it is a locale bug and not a units bug.
    expect(d!.checks['--safe-bottom'].raw).toBe('34,00px')
    expect(d!.checks['--safe-bottom'].reason).toBe('malformed')
    expect(d!.viewport).toEqual({ width: VIEWPORT_W, height: VIEWPORT_H })
  })

  it('says on the console which property it refused and why', () => {
    const root = document.documentElement
    root.style.setProperty('--safe-bottom', '34,00px')
    sanitizeSafeArea(root)
    const msg = warn.mock.calls.map(c => String(c[0])).join('\n')
    expect(msg).toContain('--safe-bottom')
    expect(msg).toContain('34,00px')
    expect(msg).toContain('env(safe-area-inset-*)')
  })
})

// ───────────────────────────────────────────────────────────────────────────
// applySafeAreaInsets — the sanctioned setter.
// ───────────────────────────────────────────────────────────────────────────

describe('applySafeAreaInsets', () => {
  it('formats numbers without a locale, so it cannot reproduce the Kotlin bug', () => {
    applySafeAreaInsets({ '--safe-top': 59, '--safe-bottom': 34 })
    expect(read('--safe-top')).toBe('59.00px')
    expect(read('--safe-bottom')).toBe('34.00px')
  })

  it('refuses a bad value AND does not leave the previous one looking current', () => {
    applySafeAreaInsets({ '--safe-bottom': 34 })
    expect(read('--safe-bottom')).toBe('34.00px')
    const d = applySafeAreaInsets({ '--safe-bottom': '34,00px' })
    expect(d.rejected).toEqual(['--safe-bottom'])
    // A stale 34.00px would be a lie about the CURRENT inset, and would survive
    // a rotation that genuinely changed it.
    expect(read('--safe-bottom')).toBe('')
  })

  it.each([NaN, Infinity, -1])('refuses the number %s', n => {
    const d = applySafeAreaInsets({ '--safe-top': n })
    expect(d.rejected).toEqual(['--safe-top'])
    expect(read('--safe-top')).toBe('')
  })

  it('leaves properties it was not given alone', () => {
    applySafeAreaInsets({ '--safe-top': 59 })
    applySafeAreaInsets({ '--safe-bottom': 34 })
    expect(read('--safe-top')).toBe('59.00px')
    expect(read('--safe-bottom')).toBe('34.00px')
  })

  it('clears a property when handed an empty string', () => {
    applySafeAreaInsets({ '--safe-top': 59 })
    applySafeAreaInsets({ '--safe-top': '' })
    expect(read('--safe-top')).toBe('')
  })
})

// ───────────────────────────────────────────────────────────────────────────
// installSafeAreaGuard — the boundary a second caller cannot walk around.
// ───────────────────────────────────────────────────────────────────────────

describe('installSafeAreaGuard', () => {
  let uninstall: () => void
  beforeEach(() => { uninstall = installSafeAreaGuard() })
  afterEach(() => { uninstall() })

  // This is the Android bridge's exact call. It does not go through any shell
  // API, and it must still be validated.
  it('reverts a direct native-style write of a comma-decimal value', async () => {
    document.documentElement.style.setProperty('--safe-bottom', '34,00px')
    expect(read('--safe-bottom')).toBe('34,00px')   // it landed
    await flush()
    expect(read('--safe-bottom')).toBe('')          // and was taken back out
    expect(window.__vulosSafeArea!.rejected).toEqual(['--safe-bottom'])
  })

  it('leaves a correct native push alone', async () => {
    const root = document.documentElement
    root.style.setProperty('--safe-top', '59.00px')
    root.style.setProperty('--safe-bottom', '34.00px')
    await flush()
    expect(read('--safe-top')).toBe('59.00px')
    expect(read('--safe-bottom')).toBe('34.00px')
  })

  it('does not loop on its own removals', async () => {
    const root = document.documentElement
    root.style.setProperty('--safe-bottom', '-40px')
    await flush()
    await flush()
    expect(read('--safe-bottom')).toBe('')
    // One removal, one warning — not a warning per observer turn.
    expect(warn.mock.calls.filter(c => String(c[0]).includes('-40px')).length).toBe(1)
  })

  it('ignores unrelated style writes on the same element', async () => {
    const root = document.documentElement
    root.style.setProperty('--vd-window-radius', '8px')   // desktop/store.ts does this
    root.style.setProperty('--safe-top', '34px')
    await flush()
    expect(read('--vd-window-radius')).toBe('8px')
    expect(read('--safe-top')).toBe('34px')
  })

  it('sanitizes what is already there at install time', async () => {
    uninstall()
    document.documentElement.style.setProperty('--safe-left', '34,00px')
    uninstall = installSafeAreaGuard()
    expect(read('--safe-left')).toBe('')
  })

  it('re-checks on resize, because the bounds are relative to the viewport', async () => {
    const root = document.documentElement
    root.style.setProperty('--safe-top', '300px')  // under 384, so it stands
    await flush()
    expect(read('--safe-top')).toBe('300px')
    // Rotate into a short viewport: the same value now hides more than half.
    const original = window.innerHeight
    Object.defineProperty(window, 'innerHeight', { value: 400, configurable: true })
    try {
      window.dispatchEvent(new Event('resize'))
      expect(read('--safe-top')).toBe('')
    } finally {
      Object.defineProperty(window, 'innerHeight', { value: original, configurable: true })
    }
  })

  it('stops guarding once uninstalled', async () => {
    uninstall()
    document.documentElement.style.setProperty('--safe-bottom', '34,00px')
    await flush()
    expect(read('--safe-bottom')).toBe('34,00px')
    uninstall = installSafeAreaGuard()   // for the afterEach
  })
})
