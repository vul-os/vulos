/**
 * shell-responsive.e2e.ts — the OS shell lays out at every device width, in both
 * orientations, and in a desktop window the user has dragged narrow.
 *
 * ── Why this file exists ────────────────────────────────────────────────────
 *
 * "FIX RESPONSIVENESS THERE ARE ISSUES ON TABLET AND MOBILE" was the whole
 * brief, and the shell had never been swept. What the sweep found is recorded
 * with its measurement in roadmap/SHELL-RESPONSIVE.md; this is the part that
 * stops it coming back.
 *
 * ── The two standards, and why they are measured and not read ───────────────
 *
 *   1. No horizontal overflow. Measured TWICE, because neither check subsumes
 *      the other: the document's own scroll extent, AND every element's box
 *      against the viewport. `.vmob-root` is `fixed inset-0` with
 *      `overflow: hidden`, so a control painted 69px off the right edge of a
 *      320px phone produced a document `scrollWidth` of exactly 320 and every
 *      existing overflow assertion in this suite passed. The content was gone;
 *      the scrollbar was the thing that was absent.
 *
 *   2. A 12px rendered type floor. Rendered, not declared: this shell's sizes
 *      come from Tailwind arbitrary values, CSS tokens and `clamp()`, and the
 *      only place they resolve to a number is the composited box.
 *
 * ── Scope, stated rather than implied ───────────────────────────────────────
 *
 * The type floor and the touch floor run over the SHELL's own chrome — the
 * surfaces in frontend/src/shell/**, src/mobile/** and layouts/MobileStack.tsx.
 * The overflow checks run over the WHOLE page, because horizontal overflow is
 * never anyone's local business.
 *
 * Two surfaces are deliberately outside the type floor's roots, both measured,
 * both reported in roadmap/SHELL-RESPONSIVE.md rather than folded in:
 * `src/widgets/host/widgets.css` (18 nodes at 10.5–11.5px on every desktop
 * width) and `layouts/DesktopCanvas.tsx`'s "alpha" wordmark (10px). Folding a
 * defect this file cannot fix into a gate this file owns is how a floor becomes
 * a thing people learn to route around.
 */
import { test, expect, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import {
  PHONE_PORTRAIT, PHONE_LANDSCAPE, TABLET, DESKTOP, ALL_VIEWPORTS,
  MIN_FONT_PX, TOUCH_FLOOR, bootShell, shellKind, docSpill, spillingElements,
  tinyText, smallTargets, scanned, chromeShare, type Viewport,
} from './shell-responsive-harness'

const SHOTS = 'test-results/shell-responsive'
mkdirSync(SHOTS, { recursive: true })

/**
 * The shell's own chrome, in both idioms.
 *
 * `.vshell-bar` is present but its sub-floor targets are handled separately —
 * see KNOWN_BAR_TARGETS below.
 */
const SHELL_ROOTS = [
  '.vmob-bar',                            // phone status bar
  'nav[aria-label="System navigation"]',  // phone dock
  '[data-mobile-home]',                   // phone home screen
  '[data-mobile-switcher]',               // phone app switcher
  '.vshell-bar',                          // desktop menu bar
  '.vdock-layer',                         // desktop dock
  '.vwin-titlebar',                       // window chrome
]

/**
 * ── COVERAGE COUNTS ─────────────────────────────────────────────────────────
 *
 * Every guard in this repository that carried a coverage-count assertion
 * survived mutation testing; every one that did not was eventually found to be
 * measuring nothing. These are this file's.
 *
 * WIDTH_COUNT is asserted against the arrays the sweep actually iterates, so
 * deleting an awkward width — the standard way a responsive gate dies — fails
 * the suite instead of quietly shrinking it. The numbers are per-bucket rather
 * than one total, because "23 cases" would still be satisfied by 23 phones.
 */
const WIDTH_COUNT = { phonePortrait: 4, phoneLandscape: 4, tablet: 6, desktop: 7 } as const

/**
 * Per-case denominators. Below these, an empty finding list is not evidence.
 *
 * Split by shell, because the two draw very different amounts: the phone's
 * chrome roots include the home GRID (one label per installed app), the
 * desktop's are a menu bar and a dock whose tiles are marks with no captions.
 * One number would have to be the smaller of the two and would then be
 * satisfied trivially by the phone.
 */
interface Denominators { elements: number; textNodes: number; controls: number }
const MIN_SCANNED: Record<'mobile' | 'desktop', Denominators> = {
  mobile: { elements: 200, textNodes: 20, controls: 10 },
  desktop: { elements: 120, textNodes: 3, controls: 6 },
}

/**
 * The desktop menu bar's sub-44px controls on a coarse pointer, by accessible
 * name, as measured at 1194×834 and 1366×1024.
 *
 * These are NOT exempt — they are an open defect with a reason, and asserting
 * the set by NAME is what keeps that honest. A seventh offender fails. Fixing
 * one of these does NOT fail (the assertion is containment, not equality),
 * because a gate that goes red when someone repairs the thing it guards teaches
 * people to delete gates.
 *
 * Why they are open: `.vshell-btn` is 28px inside a 32px bar, and the bar's
 * height is hard-coded in THREE files that must agree — `h-8` in
 * shell/TopBar.tsx, `pt-8` in layouts/DesktopCanvas.tsx (the origin every window
 * is positioned against) and `MENU_BAR_H = 32` in shell/windowTiling.ts. Growing
 * the bar in the two this workstream owns and not the third would open every
 * window 12px UNDER the menu bar, on exactly the tablets the fix is for. See
 * roadmap/SHELL-RESPONSIVE.md.
 */
const KNOWN_BAR_TARGETS = new Set([
  'System menu',        // core/SystemPulse.tsx  — 69×32
  'Applications',       // shell/TopBar.tsx      — 28×28
  'Mission Control',    // shell/TopBar.tsx      — 28×28
  'Chat',               // shell/TopBar.tsx      — 28×28
  'Toggle fullscreen',  // shell/TopBar.tsx      — 28×28
  'Theme mode',         // core/ThemeToggle.tsx  — 24×24
])

/**
 * There is no longer a width at which the shell is allowed to overflow.
 *
 * This constant used to be 360, carrying a 74px budget for the status bar at
 * 320×568 with the note that the shell could not fix it: the compact cluster's
 * own MIN-CONTENT width was 317px — five controls at the 44px touch floor, a
 * 48px trust badge and a 72px clock — against a 320px viewport, with
 * MobileStack having already given back everything it had.
 *
 * It is 0 now because the clock gained a narrow tier in core/SystemPulse.tsx
 * (nothing below 360, glyph-only below 390, the time above it), which is the
 * one change that could close it. The constant survives rather than being
 * deleted so that the exemption's ABSENCE is explicit: `width < 0` is never
 * true, so every case takes the hard branch, and anyone reintroducing a
 * tolerance has to raise a number that is sitting here with its history
 * attached rather than quietly widening an `if`.
 */
const NARROW_SPILL_MAX_WIDTH = 0
/** Kept with the constant above; unreachable while it is 0. */
const NARROW_SPILL_BUDGET_PX = 74
const NARROW_SPILL_ROOT = '.vmob-bar'

/**
 * The shell's chrome may not eat the screen.
 *
 * A phone in LANDSCAPE is where this bites: at 568×320 the status bar and the
 * dock together are 130px of a 320px-tall viewport. The bound is generous on
 * purpose — it is a bound on absurdity, like the inset guard's 96px tripwire,
 * not a design target — and it is the only assertion here that a portrait-only
 * sweep could not have produced at all.
 */
const MAX_CHROME_FRACTION = 0.45

async function assertLaysOut(page: Page, vp: Viewport, override?: Partial<Denominators>) {
  const label = `${vp.label} (${vp.width}×${vp.height})`

  // ── denominators first ────────────────────────────────────────────────────
  const kind = await shellKind(page)
  const floor = { ...MIN_SCANNED[kind], ...override }
  const n = await scanned(page, SHELL_ROOTS)
  expect(n.elements, `${label}: only ${n.elements} elements in the document — the ${kind} shell did not render`)
    .toBeGreaterThanOrEqual(floor.elements)
  expect(n.textNodes, `${label}: the type floor measured ${n.textNodes} text nodes in the ${kind} shell chrome`)
    .toBeGreaterThanOrEqual(floor.textNodes)
  expect(n.controls, `${label}: found ${n.controls} controls in the ${kind} shell chrome`)
    .toBeGreaterThanOrEqual(floor.controls)

  // ── 1a. the document does not scroll sideways ─────────────────────────────
  const spill = await docSpill(page)
  expect(spill, `${label}: the OS scrolls horizontally by ${spill}px`).toBeLessThanOrEqual(1)

  // ── 1b. nothing is painted outside the viewport ───────────────────────────
  // The check the document-level one cannot make: `.vmob-root` is fixed and
  // overflow-hidden, so it CLIPS the evidence out of scrollWidth while leaving
  // the content unreachable.
  const escaping = await spillingElements(page)
  if (vp.width < NARROW_SPILL_MAX_WIDTH) {
    // The known-open narrow case. Still measured, against its known shape.
    const inBar = await spillingElements(page, NARROW_SPILL_ROOT)
    expect(
      escaping.length,
      `${label}: something OUTSIDE ${NARROW_SPILL_ROOT} is painted off screen — ` +
      `${JSON.stringify(escaping, null, 2)}\nOnly the status bar's known narrow overflow is accounted for.`,
    ).toBe(inBar.length)
    const worst = escaping.reduce((m, e) => Math.max(m, e.right, e.left), 0)
    expect(
      worst,
      `${label}: the status bar now overflows by ${worst}px, past the ${NARROW_SPILL_BUDGET_PX}px budget — ` +
      `${JSON.stringify(escaping, null, 2)}`,
    ).toBeLessThanOrEqual(NARROW_SPILL_BUDGET_PX)
  } else {
    expect(escaping, `${label}: painted outside the viewport — ${JSON.stringify(escaping, null, 2)}`).toEqual([])
  }

  // ── 2. the type floor ─────────────────────────────────────────────────────
  const tiny = await tinyText(page, MIN_FONT_PX, SHELL_ROOTS)
  expect(tiny, `${label}: shell chrome text below ${MIN_FONT_PX}px — ${JSON.stringify(tiny, null, 2)}`).toEqual([])

  // ── 3. the chrome leaves room for the OS ──────────────────────────────────
  const chrome = await chromeShare(page)
  expect(
    chrome.frac,
    `${label}: the shell's own chrome is ${chrome.px}px of ${vp.height}px (${JSON.stringify(chrome.parts)})`,
  ).toBeLessThanOrEqual(MAX_CHROME_FRACTION)
}

/** The touch floor, on the profiles where the pointer is a finger. */
async function assertTouchTargets(page: Page, vp: Viewport) {
  const label = `${vp.label} (${vp.width}×${vp.height})`
  const small = await smallTargets(page, SHELL_ROOTS, TOUCH_FLOOR)
  const barOnly = small.filter((s) => s.root === '.vshell-bar')
  const rest = small.filter((s) => s.root !== '.vshell-bar')

  expect(rest, `${label}: sub-${TOUCH_FLOOR}px targets in the shell chrome — ${JSON.stringify(rest, null, 2)}`)
    .toEqual([])

  // The menu bar's open set, by name. Anything not on the list is new.
  const unexpected = barOnly.filter((s) => ![...KNOWN_BAR_TARGETS].some((k) => s.label.startsWith(k)))
  expect(
    unexpected,
    `${label}: a NEW sub-${TOUCH_FLOOR}px control in the desktop menu bar — ${JSON.stringify(unexpected, null, 2)}\n` +
    `The known-open set is ${[...KNOWN_BAR_TARGETS].join(', ')}; see roadmap/SHELL-RESPONSIVE.md.`,
  ).toEqual([])
}

// ── the sweep is the size it says it is ─────────────────────────────────────
test('the sweep covers every bucket it claims to', () => {
  expect(PHONE_PORTRAIT.length, 'phone portrait widths were removed').toBe(WIDTH_COUNT.phonePortrait)
  expect(PHONE_LANDSCAPE.length, 'phone LANDSCAPE widths were removed').toBe(WIDTH_COUNT.phoneLandscape)
  expect(TABLET.length, 'tablet widths were removed').toBe(WIDTH_COUNT.tablet)
  expect(DESKTOP.length, 'desktop / dragged-window widths were removed').toBe(WIDTH_COUNT.desktop)
  expect(ALL_VIEWPORTS.length).toBe(
    WIDTH_COUNT.phonePortrait + WIDTH_COUNT.phoneLandscape + WIDTH_COUNT.tablet + WIDTH_COUNT.desktop,
  )
  // Both orientations of a phone are present, which is the half of this sweep
  // that no previous mobile spec in this repository had.
  expect(PHONE_LANDSCAPE.every((v) => v.width > v.height), 'a "landscape" case is not landscape').toBe(true)
  expect(PHONE_PORTRAIT.every((v) => v.height > v.width), 'a "portrait" case is not portrait').toBe(true)
  // And the narrowest phone and the widest tablet are actually in it.
  expect(Math.min(...PHONE_PORTRAIT.map((v) => v.width))).toBeLessThanOrEqual(360)
  expect(Math.max(...TABLET.map((v) => v.width))).toBeGreaterThanOrEqual(1366)
})

for (const vp of ALL_VIEWPORTS) {
  test.describe(vp.label, () => {
    test.use({
      viewport: { width: vp.width, height: vp.height },
      hasTouch: vp.touch, isMobile: vp.touch,
      deviceScaleFactor: vp.touch ? 2 : 1,
    })

    test(`lays out at ${vp.width}×${vp.height}`, async ({ page }) => {
      test.setTimeout(120_000)
      const errors = await bootShell(page, vp)
      await assertLaysOut(page, vp)
      if (vp.touch) await assertTouchTargets(page, vp)
      await page.screenshot({ path: `${SHOTS}/${vp.label.replace(/\s+/g, '-')}.png` })
      expect(errors, `${vp.label}: uncaught page errors — ${errors.join(' | ')}`).toEqual([])
    })
  })
}

/**
 * A tablet in LANDSCAPE runs the DESKTOP canvas, with real windows on it.
 *
 * TOUCH_STACK_MAX is 1024 (shell/viewportRule.ts), so an iPad-class tablet gets
 * the touch shell in portrait and DesktopCanvas in landscape. That is a
 * deliberate line — landscape is often a keyboard posture — but it means the
 * window chrome is operated by a finger, and roadmap/MOBILE-SHELL.md §6.5 left
 * exactly that open: 12×12px `.vwin-light` controls on a device with no mouse.
 *
 * Every other case in this file measures the EMPTY shell. This one opens a
 * window, because window chrome does not exist until there is a window and a
 * sweep of the empty desktop would have reported the traffic lights as fixed by
 * never rendering them.
 */
test.describe('an iPad-class tablet in landscape, with a window open', () => {
  test.use({ viewport: { width: 1194, height: 834 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 })

  test('the window controls are operable with a finger', async ({ page }) => {
    test.setTimeout(120_000)
    await bootShell(page, { label: 'tablet 1194 landscape', width: 1194, height: 834, touch: true })
    expect(await shellKind(page), 'this case is only meaningful on the desktop canvas').toBe('desktop')

    // Through the palette — the same lane the dock's launcher uses.
    await expect(async () => {
      await page.keyboard.press('Meta+k')
      await expect(page.getByPlaceholder(/Search apps/)).toBeVisible({ timeout: 1500 })
    }).toPass({ timeout: 20_000 })
    await page.getByPlaceholder(/Search apps/).fill('Calendar')
    await page.keyboard.press('Enter')
    await expect(page.locator('.vwin-titlebar').first()).toBeVisible({ timeout: 20_000 })

    /**
     * Wait for the OPEN ANIMATION to finish before measuring anything.
     *
     * `.win-anim` scales the window up from its dock origin, and
     * `getBoundingClientRect` returns the TRANSFORMED box — so a 44px control
     * inside a window still at scale(0.909) measures 40px. That is exactly what
     * this test reported twice while the CSS was already correct: three lights
     * at 40×40 against a rule asking for 44, on a build where
     * `getComputedStyle(...).width` said 44px. A capture artifact reported as a
     * layout defect is the failure mode this repository has already
     * misdiagnosed three times, so it is settled explicitly rather than with a
     * sleep that happens to be long enough.
     */
    await expect(async () => {
      const t = await page.locator('.vwin').first().evaluate((el) => getComputedStyle(el).transform)
      expect(t === 'none' || t === 'matrix(1, 0, 0, 1, 0, 0)', `window still animating: transform=${t}`).toBe(true)
    }).toPass({ timeout: 15_000 })

    const lights = await page.locator('.vwin-light').evaluateAll((els) =>
      els.map((el) => {
        const r = el.getBoundingClientRect()
        const svg = el.querySelector('svg')
        return {
          label: el.getAttribute('aria-label') || '?',
          w: Math.round(r.width), h: Math.round(r.height),
          glyphOpacity: svg ? Number(getComputedStyle(svg).opacity) : -1,
        }
      }),
    )
    // Denominator: three lights, or this proves nothing.
    expect(lights.length, 'no window controls were measured').toBeGreaterThanOrEqual(3)
    expect(
      lights.filter((l) => l.w < TOUCH_FLOOR || l.h < TOUCH_FLOOR),
      `window controls under ${TOUCH_FLOOR}px on a coarse pointer: ${JSON.stringify(lights)}`,
    ).toEqual([])

    // And they say which is which. The glyphs are `opacity: 0` until
    // `.vwin-lights:hover`, and there is no hover on a finger — three identical
    // dots where one of them discards the window.
    expect(
      lights.filter((l) => l.glyphOpacity <= 0),
      `window control glyphs invisible on a coarse pointer: ${JSON.stringify(lights)}`,
    ).toEqual([])

    // Opening a window must not have broken the page open sideways.
    expect(await docSpill(page)).toBeLessThanOrEqual(1)
    expect(await spillingElements(page)).toEqual([])
    await page.screenshot({ path: `${SHOTS}/tablet-1194-window-open.png` })
  })
})

/**
 * The phone shell, with an app fullscreen — the state a phone is actually in.
 *
 * Every viewport case above measures Home. The status bar swaps its identity
 * block for the app's title while an app is up, and that block is the one that
 * had to learn to give its width back (layouts/MobileStack.tsx), so the fix is
 * only half-tested until the other branch is on screen.
 */
test.describe('a phone with an app open', () => {
  // 320, the NARROWEST profile in the sweep, and the one this case could not be
  // run at before. The fullscreen branch is the hardest state in the shell: the
  // status cluster's min-content competes with a back chevron and the app's
  // title on a single 44px row, and it is where R-3's arithmetic bit hardest —
  // measured 31.5px wide for the back button at 360 before the clock gained its
  // narrow tier. Running it at the widest phone would have proved nothing.
  test.use({ viewport: { width: 320, height: 568 }, hasTouch: true, isMobile: true, deviceScaleFactor: 3 })

  test('the status bar still fits with an app title in it', async ({ page }) => {
    test.setTimeout(120_000)
    const vp: Viewport = { label: 'phone 320 portrait, app open', width: 320, height: 568, touch: true }
    await bootShell(page, vp)
    await page.locator('[data-home-tile="vulos-calendar"]').tap()
    await expect(page.getByRole('button', { name: 'Back to home' })).toBeVisible({ timeout: 20_000 })
    // The home grid is hidden behind the app, so the shell's own chrome is the
    // status bar and the dock and nothing else. Measured 6 text nodes in that
    // state (app title, clock, three dock captions, badge), against 20+ on Home
    // — the general mobile denominator would fail here for the right reason and
    // the wrong subject, so this case carries the number it actually has.
    await assertLaysOut(page, vp, { textNodes: 6 })
    await assertTouchTargets(page, vp)
    await page.screenshot({ path: `${SHOTS}/phone-360-app-open.png` })
  })
})
