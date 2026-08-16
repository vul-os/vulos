// insets-validation.e2e.ts — MOB-12b, the safe-area inset boundary, in real
// Chromium against the real production build.
//
// WHY THIS SPEC EXISTS. The Android APK
// (clients/android/app/src/main/java/org/vulos/mobile/MainActivity.kt) pushes
// window insets into the shell by writing `--safe-top` / `--safe-bottom` /
// `--safe-left` / `--safe-right` as INLINE styles on <html>. An inline style
// beats the `:root { --safe-*: env(safe-area-inset-*) }` rule in src/index.css
// at any specificity, so a wrong native value does not degrade to the working
// web fallback — it REPLACES it. That Kotlin is parse-checked only; there is no
// Android SDK in this environment and it has never been compiled or run. So the
// defence is built on the side that CAN be executed, and this is where it is
// executed.
//
// THE TRICK THAT MAKES THIS TESTABLE. `env(safe-area-inset-*)` resolves to 0 in
// headless Chromium, so "fell back to env()" and "was applied as zero" would
// look identical — which is precisely the confusion the defect lives in. Each
// test therefore installs a SENTINEL stylesheet rule standing in for the env()
// declaration, at the same `:root` specificity but later in document order, so
// it is what the cascade lands on when an inline value is refused. A rejected
// push must produce the sentinel. Zero means the fix is a re-run of the bug.
//
// WHAT IS UNDER TEST. `installSafeAreaGuard` (src/mobile/safeAreaInsets.ts),
// installed at boot from src/main.tsx so it covers the desktop shell too. It is
// the ONLY mechanism, and that is a finding rather than an omission: a CSS-only
// defence was built, measured, and removed. `@property --safe-*: syntax
// '<length>'` does reject a malformed value — but it substitutes the registered
// `initial-value`, NOT the next declaration in the cascade (measured: with
// initial-value 7px and a :root rule of 21px, a pushed "34,00px" computed to
// 7px). With the only honest initial-value, 0px, that IS the defect. The
// `document the cascade` suite at the bottom of this file pins the measurements
// that led to removing it, so a future Chromium that changes them is noticed
// here and not on a phone.

import { test, expect, type Page } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'

const CALENDAR_EMPTY = { 'GET /api/pim/calendar/events': json({ events: [] }) }
const NAV = 'nav[aria-label="System navigation"]'

// Distinct per edge so a test cannot pass by reading the wrong one, and none of
// them 0 — the value this spec exists to tell apart from a fallback.
const SENTINEL = { top: '23px', bottom: '21px', left: '19px', right: '17px' }

async function installSentinelFallback(page: Page) {
  // Appended to <head> after index.css: same `:root` specificity, later in
  // document order, so it wins the cascade among stylesheet rules and stands in
  // for the env() declaration that is unobservably 0 in headless Chromium.
  await page.addStyleTag({
    content: `:root{--safe-top:${SENTINEL.top};--safe-bottom:${SENTINEL.bottom};` +
      `--safe-left:${SENTINEL.left};--safe-right:${SENTINEL.right}}`,
  })
}

/** Exactly what MainActivity.pushSafeAreaToShell does: a raw inline write, via
 *  no shell API at all. */
async function nativePush(page: Page, prop: string, value: string) {
  await page.evaluate(([p, v]) => {
    document.documentElement.style.setProperty(p, v)
  }, [prop, value])
}

const tokenValue = (page: Page, prop: string) =>
  page.evaluate(p => getComputedStyle(document.documentElement).getPropertyValue(p).trim(), prop)

const inlineValue = (page: Page, prop: string) =>
  page.evaluate(p => document.documentElement.style.getPropertyValue(p), prop)

async function bootMobile(page: Page) {
  const errors: string[] = []
  page.on('pageerror', e => errors.push(e.message))
  await installBackend(page, CALENDAR_EMPTY)
  await page.goto('/')
  await expect(page.locator(NAV)).toBeVisible({ timeout: 20_000 })
  await installSentinelFallback(page)
  return errors
}

// ───────────────────────────────────────────────────────────────────────────
// Phone 390×844 — the profile the Android APK actually runs at.
// ───────────────────────────────────────────────────────────────────────────

test.describe('phone 390×844 — safe-area inset boundary', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 3 })

  // THE named defect: a lost Locale.ROOT in the native String.format.
  test('a comma-decimal inset falls back to the cascade — it does NOT apply as zero', async ({ page }) => {
    const errors = await bootMobile(page)
    const dock = page.locator(NAV)

    // Baseline: nothing pushed, so the fallback is in charge.
    await expect(dock).toHaveCSS('padding-bottom', SENTINEL.bottom)

    // A correct push wins, which is the whole reason the bridge writes inline.
    await nativePush(page, '--safe-bottom', '34.00px')
    await expect(dock).toHaveCSS('padding-bottom', '34px')

    // Now the bug: the same number in a comma-decimal locale.
    await nativePush(page, '--safe-bottom', '34,00px')
    // Before this workstream: the inline value stuck, padding-bottom was invalid
    // at computed-value time, and the dock's touch targets sat under the
    // navigation bar. The assertion is the SENTINEL and not '0px' on purpose —
    // 0px would mean the boundary had reproduced the defect it exists to stop.
    await expect(dock).toHaveCSS('padding-bottom', SENTINEL.bottom)
    expect(await tokenValue(page, '--safe-bottom')).toBe(SENTINEL.bottom)
    expect(await inlineValue(page, '--safe-bottom')).toBe('')

    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('every edge rejects the comma form, and each falls back to its OWN token', async ({ page }) => {
    await bootMobile(page)
    for (const [prop, sentinel] of [
      ['--safe-top', SENTINEL.top],
      ['--safe-bottom', SENTINEL.bottom],
      ['--safe-left', SENTINEL.left],
      ['--safe-right', SENTINEL.right],
    ] as const) {
      await nativePush(page, prop, '59,00px')
      expect(await tokenValue(page, prop), prop).toBe(sentinel)
    }
  })

  test('a plausible inset survives on every edge', async ({ page }) => {
    await bootMobile(page)
    await page.evaluate(() => {
      const r = document.documentElement
      r.style.setProperty('--safe-top', '59.00px')
      r.style.setProperty('--safe-bottom', '34.00px')
      r.style.setProperty('--safe-left', '0.00px')
      r.style.setProperty('--safe-right', '0.00px')
    })
    await expect(page.locator('.vmob-bar')).toHaveCSS('padding-top', '59px')
    await expect(page.locator(NAV)).toHaveCSS('padding-bottom', '34px')
  })

  // `<length>` is a type, not a bound. These three are all valid lengths, all
  // impossible as insets, and all get past @property — the JS guard is what
  // catches them.
  for (const [label, value] of [
    ['negative', '-40px'],
    ['larger than half the viewport', '600px'],
    ['a percentage, which on padding resolves against the WIDTH', '10%'],
    ['unitless', '34'],
    ['a calc() the bridge could never emit', 'calc(34px + 1px)'],
  ] as const) {
    test(`an inset that is ${label} falls back to the cascade`, async ({ page }) => {
      await bootMobile(page)
      await nativePush(page, '--safe-bottom', value)
      await expect(page.locator(NAV)).toHaveCSS('padding-bottom', SENTINEL.bottom)
      expect(await inlineValue(page, '--safe-bottom')).toBe('')
    })
  }

  // The OTHER named risk: a wrong display-density divisor. 34px pushed as 134px
  // at 390×844. Syntactically perfect, and the shell CANNOT tell it from a real
  // inset — so it is applied, and flagged, and the flag is what the on-device
  // check reads. Rejecting it would substitute env() = 0 inside the WebView,
  // which is the failure this whole workstream exists to prevent.
  test('a density-bug inset is applied and FLAGGED, not silently accepted and not rejected', async ({ page }) => {
    await bootMobile(page)
    await nativePush(page, '--safe-bottom', '134.00px')
    await expect(page.locator(NAV)).toHaveCSS('padding-bottom', '134px')
    const diag = await page.evaluate(() => window.__vulosSafeArea)
    expect(diag?.flagged).toEqual(['--safe-bottom'])
    expect(diag?.rejected).toEqual([])
    expect(diag?.viewport).toEqual({ width: 390, height: 844 })
  })

  test('the diagnostics record names the property, the raw string and the reason', async ({ page }) => {
    await bootMobile(page)
    await nativePush(page, '--safe-bottom', '34,00px')
    const diag = await page.evaluate(() => window.__vulosSafeArea)
    expect(diag?.rejected).toEqual(['--safe-bottom'])
    // A device tester needs to SEE "34,00px" to know this is a locale bug and
    // not a units bug — that is the difference between two one-line Kotlin fixes.
    expect(diag?.checks['--safe-bottom'].raw).toBe('34,00px')
    expect(diag?.checks['--safe-bottom'].reason).toBe('malformed')
  })

  test('a refused inset is never painted — the revert lands before the next frame', async ({ page }) => {
    await bootMobile(page)
    // Write the bad value and read the rendered padding back in the SAME frame
    // callback, after a paint has been scheduled. The guard is a MutationObserver,
    // which runs at the microtask checkpoint at the end of the task that wrote
    // the value — i.e. before rendering — so nothing ever shows the bad state.
    const painted = await page.evaluate(async () => {
      const nav = document.querySelector('nav[aria-label="System navigation"]')!
      document.documentElement.style.setProperty('--safe-bottom', '900px')
      await new Promise(r => requestAnimationFrame(() => r(null)))
      return getComputedStyle(nav).paddingBottom
    })
    expect(painted).toBe(SENTINEL.bottom)
  })
})

// ───────────────────────────────────────────────────────────────────────────
// Desktop 1440×900. shell/MissionControl.tsx and shell/Launchpad.tsx read
// var(--safe-top) / var(--safe-bottom) on this shell, and the mobile stack is
// not mounted — so this suite is what proves the guard is installed at BOOT
// (src/main.tsx) and not from the phone shell. If someone moves the install
// into a mobile-only component, these go red and the phone suite stays green.
// ───────────────────────────────────────────────────────────────────────────

async function bootDesktop(page: Page) {
  const errors: string[] = []
  page.on('pageerror', e => errors.push(e.message))
  await installBackend(page, CALENDAR_EMPTY)
  await page.goto('/')
  await expect(page.locator('[data-shell="mobile"]')).toHaveCount(0, { timeout: 20_000 })
  await installSentinelFallback(page)
  return errors
}

test.describe('desktop 1440×900 — the guard is installed at boot, not by the phone shell', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('a malformed inset is refused on the desktop shell too', async ({ page }) => {
    const errors = await bootDesktop(page)
    for (const [prop, sentinel] of [
      ['--safe-top', SENTINEL.top],
      ['--safe-bottom', SENTINEL.bottom],
      ['--safe-left', SENTINEL.left],
      ['--safe-right', SENTINEL.right],
    ] as const) {
      await nativePush(page, prop, '34,00px')
      expect(await inlineValue(page, prop), `${prop} inline`).toBe('')
      expect(await tokenValue(page, prop), `${prop} computed`).toBe(sentinel)
    }
    const diag = await page.evaluate(() => window.__vulosSafeArea)
    expect(diag).toBeDefined()   // the guard ran on a shell with no mobile stack
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('a valid inset still wins over the fallback, as the bridge intends', async ({ page }) => {
    await bootDesktop(page)
    // Rendered through the same max() shape MissionControl and Launchpad use,
    // because the token alone would not show that it still RESOLVES as a length
    // — these properties are deliberately unregistered, so getPropertyValue
    // hands back the raw token stream either way.
    await page.evaluate(() => {
      const s = document.createElement('style')
      s.textContent = '#safe-probe { padding-top: max(2.5rem, var(--safe-top)); }'
      document.head.append(s)
      const d = document.createElement('div'); d.id = 'safe-probe'; document.body.append(d)
    })
    const probe = page.locator('#safe-probe')
    await expect(probe).toHaveCSS('padding-top', '40px')   // sentinel 23px < 2.5rem floor
    await nativePush(page, '--safe-top', '59.00px')
    expect(await tokenValue(page, '--safe-top')).toBe('59.00px')
    await expect(probe).toHaveCSS('padding-top', '59px')
    await page.evaluate(() => { document.documentElement.style.removeProperty('--safe-top') })
    expect(await tokenValue(page, '--safe-top')).toBe(SENTINEL.top)
    await expect(probe).toHaveCSS('padding-top', '40px')
  })
})

// ───────────────────────────────────────────────────────────────────────────
// The measurements the design rests on.
//
// These use an UNGUARDED probe property, so they record what the browser does
// to the shell's four consumption shapes when a bad value gets through. They
// are the reason the guard exists, the reason `max()` is not a substitute for
// it, and the reason @property was removed. If a future Chromium changes any of
// them, that should be discovered here rather than inferred from a phone.
// ───────────────────────────────────────────────────────────────────────────

test.describe('what the browser actually does with a bad inset', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('max() defends against a small inset and NOT against a malformed one', async ({ page }) => {
    await installBackend(page, CALENDAR_EMPTY)
    await page.goto('/')
    const table = await page.evaluate(() => {
      const style = document.createElement('style')
      style.textContent = `
        :root { --probe-inset: 0px; }
        #probe-max  { padding-top: max(40px, var(--probe-inset)); }
        #probe-bare { padding-bottom: var(--probe-inset); }
        #probe-calc { padding-bottom: calc(var(--probe-inset) + 6px); }`
      document.head.append(style)
      for (const id of ['probe-max', 'probe-bare', 'probe-calc']) {
        const d = document.createElement('div'); d.id = id; document.body.append(d)
      }
      const out: Record<string, [string, string, string]> = {}
      for (const v of ['0px', '34px', '134px', '34,00px', '-40px']) {
        document.documentElement.style.setProperty('--probe-inset', v)
        out[v] = [
          getComputedStyle(document.getElementById('probe-max')!).paddingTop,
          getComputedStyle(document.getElementById('probe-bare')!).paddingBottom,
          getComputedStyle(document.getElementById('probe-calc')!).paddingBottom,
        ]
      }
      return out
    })

    expect(table).toEqual({
      // [ max(40px, var)  |  bare var  |  calc(var + 6px) ]
      '0px':     ['40px', '0px', '6px'],     // max() earns its keep against zero
      '34px':    ['40px', '34px', '40px'],
      '134px':   ['134px', '134px', '140px'], // …and is no help against a density bug
      // THE row. Invalid at computed-value time poisons the WHOLE declaration,
      // so max(40px, <invalid>) is 0px — not 40px. Every consumer collapses,
      // including the two that look defended. This is why the fix has to remove
      // the declaration instead of relying on the consumers.
      '34,00px': ['0px', '0px', '0px'],
      '-40px':   ['40px', '0px', '0px'],
    })
  })

  test('@property would substitute its own initial-value, not the cascade — which is why it was removed', async ({ page }) => {
    await installBackend(page, CALENDAR_EMPTY)
    await page.goto('/')
    const result = await page.evaluate(() => {
      const style = document.createElement('style')
      style.textContent = `
        @property --probe-registered { syntax: '<length>'; inherits: true; initial-value: 7px; }
        :root { --probe-registered: 21px; }`
      document.head.append(style)
      const read = () => getComputedStyle(document.documentElement).getPropertyValue('--probe-registered').trim()
      const out: Record<string, string> = { cascade: read() }
      document.documentElement.style.setProperty('--probe-registered', '34px')
      out.valid = read()
      document.documentElement.style.setProperty('--probe-registered', '34,00px')
      out.malformed = read()
      return out
    })
    expect(result.cascade).toBe('21px')
    expect(result.valid).toBe('34px')
    // 7px — the registered initial-value — and NOT 21px, the cascade. A
    // registration whose initial-value were the honest 0px would therefore
    // answer a malformed push with a hard zero at the root: the original defect
    // with a type annotation on it.
    expect(result.malformed).toBe('7px')
  })
})
