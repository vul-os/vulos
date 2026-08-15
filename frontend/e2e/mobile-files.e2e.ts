/**
 * mobile-files.e2e.ts — the file manager under a real finger.
 *
 * The gestures here are dispatched through CDP `Input.dispatchTouchEvent`, not
 * `page.mouse`. That matters more than it looks: the whole design decision this
 * suite is checking is that the touch path and the pointer path are chosen from
 * the POINTER TYPE of the event, so a test driven by synthetic mouse events
 * would exercise the desktop branch on a phone viewport and report a pass for
 * behaviour no finger can reach. Chromium turns dispatched touch events into
 * pointer events with `pointerType: 'touch'`, which is the real thing.
 *
 * The desktop half of the file is exercised too, at 1280×800 with a mouse, so
 * "made it work on a phone" cannot quietly mean "changed what a click does".
 */
import { test, expect, type Page } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'
import { mkdirSync } from 'node:fs'

const SHOTS = 'test-results/mobile-files'
mkdirSync(SHOTS, { recursive: true })

/** A deterministic `ls -lA` for two directories, so navigation is observable. */
const HOME_LS = [
  'drwxr-xr-x  4 ada ada  4096 Aug 15 10:01 Documents',
  'drwxr-xr-x  2 ada ada  4096 Aug 15 10:02 Downloads',
  '-rw-r--r--  1 ada ada  1024 Aug 15 10:03 notes.txt',
  '-rw-r--r--  1 ada ada  2048 Aug 15 10:04 budget.csv',
].join('\n')

const DOCS_LS = [
  '-rw-r--r--  1 ada ada  4096 Aug 15 11:00 thesis.md',
  '-rw-r--r--  1 ada ada  8192 Aug 15 11:01 receipts.pdf',
].join('\n')

function execOverride() {
  return {
    'POST /api/exec': (request: { postDataJSON: () => { command?: string } }) => {
      const cmd = request.postDataJSON()?.command ?? ''
      if (cmd.startsWith('echo $HOME')) return json({ output: '/home/ada' })
      if (cmd.includes('Documents')) return json({ output: DOCS_LS })
      if (cmd.startsWith('ls ')) return json({ output: HOME_LS })
      return json({ output: '' })
    },
  }
}

async function openFiles(page: Page) {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await installBackend(page, execOverride())
  await page.goto('/')
  // ⌘K is available on every shell and is the launch lane the dock uses too.
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1500 })
  }).toPass({ timeout: 25_000 })
  await input.fill('File Explorer')
  await expect(page.getByText('File Explorer', { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(page.locator('[data-file-row="Documents"]')).toBeVisible({ timeout: 20_000 })
  return errors
}

/** Centre of an element, in viewport coordinates. */
async function centre(page: Page, selector: string) {
  const box = await page.locator(selector).boundingBox()
  if (!box) throw new Error(`${selector} has no bounding box`)
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 }
}

/**
 * A real finger. `Input.dispatchTouchEvent` is what makes Chromium produce
 * pointer events with pointerType 'touch' — page.mouse would produce 'mouse'
 * and silently take the desktop branch.
 */
async function finger(page: Page) {
  const cdp = await page.context().newCDPSession(page)
  return {
    async down(x: number, y: number) {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] })
    },
    async move(x: number, y: number) {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y }] })
    },
    async up() {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    },
    async tap(x: number, y: number) {
      await this.down(x, y)
      await this.up()
    },
    async longPress(x: number, y: number, ms = 750) {
      await this.down(x, y)
      await page.waitForTimeout(ms)
      await this.up()
    },
  }
}

test.describe('phone 390×844 (touch)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 3 })

  test('one tap opens a folder', async ({ page }) => {
    // Before: onDoubleClick was the ONLY trigger for entering a folder, so a tap
    // selected and nothing else. There is no double-tap idiom on a phone.
    const errors = await openFiles(page)
    const f = await finger(page)
    const p = await centre(page, '[data-file-row="Documents"]')
    await f.tap(p.x, p.y)
    await expect(page.locator('[data-file-row="thesis.md"]')).toBeVisible({ timeout: 10_000 })
    await expect(page.locator('[data-file-row="Documents"]')).toHaveCount(0)
    await page.screenshot({ path: `${SHOTS}/phone-390-folder-opened.png` })
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('a long press opens the actions, fully on screen', async ({ page }) => {
    // Before: onContextMenu was the only path to these two, and there was no
    // long-press handler anywhere in src/. On a phone they did not exist.
    await openFiles(page)
    const f = await finger(page)
    const p = await centre(page, '[data-file-row="notes.txt"]')
    await f.longPress(p.x, p.y)

    const menu = page.locator('[data-file-menu]')
    await expect(menu).toBeVisible()
    await expect(menu.getByRole('button', { name: 'Ask AI about this' })).toBeVisible()
    await expect(menu.getByRole('button', { name: 'Share to peer…' })).toBeVisible()

    // Clamped into the viewport. Before the clamp, a press on the right-hand
    // half of a 390px screen put a 200px-wide menu off the edge — open, in the
    // DOM, and unreachable.
    const box = await menu.boundingBox()
    if (!box) throw new Error('the menu has no bounding box')
    expect(box.x).toBeGreaterThanOrEqual(0)
    expect(box.y).toBeGreaterThanOrEqual(0)
    expect(box.x + box.width, 'menu runs off the right edge').toBeLessThanOrEqual(390)
    expect(box.y + box.height, 'menu runs off the bottom edge').toBeLessThanOrEqual(844)
    await page.screenshot({ path: `${SHOTS}/phone-390-longpress-menu.png` })
  })

  test('a long press near the right edge is still fully on screen', async ({ page }) => {
    await openFiles(page)
    const f = await finger(page)
    const box = await page.locator('[data-file-row="budget.csv"]').boundingBox()
    if (!box) throw new Error('no row')
    await f.longPress(box.x + box.width - 6, box.y + box.height / 2)
    const menu = page.locator('[data-file-menu]')
    await expect(menu).toBeVisible()
    const m = await menu.boundingBox()
    if (!m) throw new Error('no menu box')
    expect(m.x + m.width).toBeLessThanOrEqual(390)
    await page.screenshot({ path: `${SHOTS}/phone-390-longpress-right-edge.png` })
  })

  test('a press that opened the menu does not also open the folder', async ({ page }) => {
    await openFiles(page)
    const f = await finger(page)
    const p = await centre(page, '[data-file-row="Documents"]')
    await f.longPress(p.x, p.y)
    await expect(page.locator('[data-file-menu]')).toBeVisible()
    // Still in the same directory — the synthesised click was swallowed.
    await expect(page.locator('[data-file-row="Documents"]')).toBeVisible()
    await expect(page.locator('[data-file-row="thesis.md"]')).toHaveCount(0)
  })

  test('a scroll is not a long press', async ({ page }) => {
    // The single most common way a hand-rolled long press turns a scrollable
    // list into a minefield.
    await openFiles(page)
    const f = await finger(page)
    const p = await centre(page, '[data-file-row="notes.txt"]')
    await f.down(p.x, p.y)
    await f.move(p.x, p.y - 60)
    await page.waitForTimeout(800)
    await f.up()
    await expect(page.locator('[data-file-menu]')).toHaveCount(0)
  })

  test('rows and menu actions clear 44px', async ({ page }) => {
    await openFiles(page)
    const rows = await page.locator('[data-file-row]').evaluateAll((els) =>
      els.map((el) => ({ n: el.getAttribute('data-file-row'), h: Math.round(el.getBoundingClientRect().height) })))
    expect(rows.length).toBeGreaterThan(0)
    expect(rows.filter((r) => r.h < 44), `sub-44px rows: ${JSON.stringify(rows)}`).toEqual([])

    const f = await finger(page)
    const p = await centre(page, '[data-file-row="notes.txt"]')
    await f.longPress(p.x, p.y)
    const items = await page.locator('[data-file-menu] button').evaluateAll((els) =>
      els.map((el) => ({ t: el.textContent?.trim(), h: Math.round(el.getBoundingClientRect().height) })))
    expect(items).toHaveLength(2)
    expect(items.filter((i) => i.h < 44), `sub-44px menu items: ${JSON.stringify(items)}`).toEqual([])
  })
})

test.describe('desktop 1280×800 (mouse) — no regression', () => {
  test.use({ viewport: { width: 1280, height: 800 }, hasTouch: false, isMobile: false })

  test('a single click still selects and does NOT navigate', async ({ page }) => {
    await openFiles(page)
    await page.locator('[data-file-row="Documents"]').click()
    await expect(page.locator('[data-file-row="Documents"]')).toHaveAttribute('aria-current', 'true')
    await expect(page.locator('[data-file-row="thesis.md"]')).toHaveCount(0)
  })

  test('a double click still opens the folder', async ({ page }) => {
    await openFiles(page)
    await page.locator('[data-file-row="Documents"]').dblclick()
    await expect(page.locator('[data-file-row="thesis.md"]')).toBeVisible({ timeout: 10_000 })
  })

  test('right-click still opens the actions', async ({ page }) => {
    await openFiles(page)
    await page.locator('[data-file-row="notes.txt"]').click({ button: 'right' })
    const menu = page.locator('[data-file-menu]')
    await expect(menu).toBeVisible()
    await expect(menu.getByRole('button', { name: 'Ask AI about this' })).toBeVisible()
    await page.screenshot({ path: `${SHOTS}/desktop-1280-rightclick-menu.png` })
  })

  test('a held mouse button does NOT open the actions', async ({ page }) => {
    // A mouse user pressing and holding is not asking for anything, and a long
    // press that fired on a mouse would race the real contextmenu.
    await openFiles(page)
    const p = await centre(page, '[data-file-row="notes.txt"]')
    await page.mouse.move(p.x, p.y)
    await page.mouse.down()
    await page.waitForTimeout(900)
    await page.mouse.up()
    await expect(page.locator('[data-file-menu]')).toHaveCount(0)
  })

  test('the row is a real button, reachable and announced', async ({ page }) => {
    // It was a div: the primary control of the app, never focusable, never
    // keyboard-reachable, never announced as actionable.
    await openFiles(page)
    const row = page.locator('[data-file-row="Documents"]')
    await expect(row).toHaveJSProperty('tagName', 'BUTTON')
    await expect(row).toHaveAttribute('aria-label', 'Folder Documents')
    await row.focus()
    await expect(row).toBeFocused()
  })
})
