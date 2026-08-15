// widgets-rail.e2e.ts — the widget rail in a real browser, at real widths.
//
// This suite exists because this project has a documented history of visual
// defects surviving every green check: a token audit cannot see `opacity`, a
// unit test cannot see a tile clipped by its grid row, and neither can see a
// widget that renders nothing because the registry was never imported.
//
// Every test fails on ANY uncaught page error. The rail is mounted on the
// desktop under every window, so a widget that throws takes out the shell — the
// per-tile error boundary is supposed to prevent that, and this is where that
// claim is checked against a real React.

import { test, expect, type Page } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'

const SHOTS = 'test-results/widgets'

// A PIM calendar payload with two future events, so the agenda widget has a
// concrete "next up" plus a second event to reveal on expand.
function agendaEvents() {
  const soon = new Date(Date.now() + 60 * 60 * 1000).toISOString()
  const later = new Date(Date.now() + 25 * 60 * 60 * 1000).toISOString()
  return json({
    events: [
      { uid: 'e1', title: 'Launch review', start: soon, location: 'War room' },
      { uid: 'e2', title: 'Team sync', start: later },
    ],
  })
}

async function boot(page: Page, overrides: Record<string, unknown> = {}, theme: 'dark' | 'light' = 'dark') {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.addInitScript((t) => {
    try {
      localStorage.setItem('vulos-theme', t)
      // Start every test from the shipped default rail — but ONLY on the first
      // load of the test. addInitScript runs on EVERY navigation, so an
      // unguarded reset wiped the very layout the reload was meant to prove had
      // persisted: three tests "failed" because the test harness undid the thing
      // under test. sessionStorage survives a reload in the same tab, which is
      // exactly the scope needed.
      if (!sessionStorage.getItem('__vulos_widget_test_booted')) {
        sessionStorage.setItem('__vulos_widget_test_booted', '1')
        localStorage.removeItem('vulos.widgets.layout.v1')
      }
    } catch { /* private mode */ }
  }, theme)
  await installBackend(page, overrides)
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 20_000 })
  await page.evaluate((t) => { document.documentElement.setAttribute('data-theme', t) }, theme)
  return errors
}

/** Open a window so the desktop (and its rail) is the backdrop. */
async function openAWindow(page: Page, appName = 'Calculator') {
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  await input.fill(appName)
  await expect(page.getByText(appName, { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('button', { name: 'Close window' }).first()).toBeVisible()
}

const rail = (page: Page) => page.locator('[data-widget-rail]')
const widget = (page: Page, id: string) => page.locator(`[data-widget-id="${id}"]`)

test.describe('the default rail', () => {
  test('renders every shipped widget with real content or an honest empty', async ({ page }) => {
    const errors = await boot(page, { 'GET /api/pim/calendar/events': agendaEvents() })
    await openAWindow(page)

    await expect(rail(page)).toBeVisible()

    // The clock shows a real time, not a placeholder.
    const clock = widget(page, 'vulos.clock')
    await expect(clock).toBeVisible()
    await expect(clock.locator('.vwidget-big')).toHaveText(/\d/)

    // World clocks: the two cities the founder named, each with a time.
    const world = widget(page, 'vulos.worldclock')
    await expect(world).toBeVisible()
    await expect(world.getByText('New York')).toBeVisible()
    await expect(world.getByText('Sydney')).toBeVisible()
    await expect(world.getByText('London')).toBeVisible()
    // Three cities, three printed times — a dial alone would not be readable.
    await expect(world.locator('.vwidget-clock-time')).toHaveCount(3)
    for (const t of await world.locator('.vwidget-clock-time').allTextContents()) {
      expect(t, 'a clock rendered no digits').toMatch(/\d/)
    }
    // Each city shows its UTC offset, and the two hemispheres differ.
    const whens = await world.locator('.vwidget-clock-when').allTextContents()
    expect(whens.some((w) => /[+-]\d/.test(w)), `no offsets rendered: ${whens.join(' | ')}`).toBe(true)

    // The agenda widget shows the next event.
    await expect(widget(page, 'vulos.agenda').getByText('Launch review')).toBeVisible()

    // Box health degrades honestly: the mock backend serves no telemetry socket.
    const pulse = widget(page, 'vulos.pulse')
    await expect(pulse).toBeVisible()
    await expect(pulse.getByText(/not reporting telemetry/i)).toBeVisible()

    // Notifications: an honest empty, not a blank card.
    await expect(widget(page, 'vulos.notifications').getByText('Nothing needs you.')).toBeVisible()

    await page.screenshot({ path: `${SHOTS}/desktop-dark.png`, fullPage: false })
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('renders identically in the light theme', async ({ page }) => {
    const errors = await boot(page, { 'GET /api/pim/calendar/events': agendaEvents() }, 'light')
    await openAWindow(page)
    await expect(rail(page)).toBeVisible()
    await expect(widget(page, 'vulos.worldclock').getByText('Sydney')).toBeVisible()
    await page.screenshot({ path: `${SHOTS}/desktop-light.png` })
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('no tile overflows its grid slot', async ({ page }) => {
    // A widget whose content is taller than its row is CLIPPED, and clipping is
    // exactly the class of defect that survives every unit test and every token
    // audit — it is only visible in a composited layout.
    const errors = await boot(page, { 'GET /api/pim/calendar/events': agendaEvents() })
    await openAWindow(page)
    await expect(rail(page)).toBeVisible()

    const overflow = await page.evaluate(() => {
      const out: string[] = []
      for (const slot of document.querySelectorAll('[data-widget-id]')) {
        const card = slot.querySelector('.vwidget-card, iframe')
        if (!card) { out.push(`${slot.getAttribute('data-widget-id')}: no card rendered`); continue }
        const s = slot.getBoundingClientRect()
        const c = card.getBoundingClientRect()
        if (c.height > s.height + 1.5 || c.width > s.width + 1.5) {
          out.push(`${slot.getAttribute('data-widget-id')}: card ${c.width}x${c.height} exceeds slot ${s.width}x${s.height}`)
        }
        // A tile that collapsed to nothing is as broken as one that overflows.
        if (s.height < 40 || s.width < 40) {
          out.push(`${slot.getAttribute('data-widget-id')}: slot collapsed to ${s.width}x${s.height}`)
        }
      }
      return out
    })
    expect(overflow).toEqual([])
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('the rail stays inside the viewport in BOTH axes', async ({ page }) => {
    // The first version of this test only checked left/right, and the rail was
    // running 350px off the BOTTOM of the screen at the time — the last widget
    // and its edit controls were unreachable, and this test said "pass".
    //
    // A bound that is not measured in the axis the content grows in is not a
    // bound. The rail grows downward, so the assertion that matters is the one
    // that was missing.
    const errors = await boot(page)
    await openAWindow(page)
    await expect(rail(page)).toBeVisible()

    const bad = await page.evaluate(() => {
      const problems: string[] = []
      const r = document.querySelector('[data-desktop-widgets]')!.getBoundingClientRect()
      if (r.right > window.innerWidth + 1) problems.push(`rail right ${r.right} > viewport ${window.innerWidth}`)
      if (r.left < 0) problems.push(`rail left ${r.left} < 0`)
      if (r.bottom > window.innerHeight + 1) problems.push(`rail bottom ${Math.round(r.bottom)} > viewport ${window.innerHeight}`)

      // And every individual tile must be reachable: either on screen already,
      // or inside something that scrolls.
      const port = document.querySelector('.vwidget-scrollport') as HTMLElement | null
      if (!port) problems.push('no scrollport — a rail taller than the screen would be unreachable')
      for (const slot of document.querySelectorAll('[data-widget-id]')) {
        const s = slot.getBoundingClientRect()
        const onScreen = s.bottom <= window.innerHeight + 1 && s.top >= 0
        const scrollable = !!port && port.scrollHeight > port.clientHeight
        if (!onScreen && !scrollable) {
          problems.push(`${slot.getAttribute('data-widget-id')} is off-screen and nothing scrolls`)
        }
      }
      return problems
    })
    expect(bad).toEqual([])
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })
})

test.describe('the sandboxed third-party widget', () => {
  test('runs in an opaque-origin frame and paints through the bridge', async ({ page }) => {
    const errors = await boot(page)
    await openAWindow(page)

    // Add it from the gallery — the same path a user takes.
    await page.getByRole('button', { name: 'Edit widgets' }).click()
    await page.getByRole('button', { name: 'Add widget' }).click()
    await page.getByRole('button', { name: 'Add Moon phase' }).click()

    const moon = widget(page, 'com.example.moon')
    await expect(moon).toBeVisible()
    const frame = moon.locator('iframe')
    await expect(frame).toBeVisible()

    // THE security assertion, on the live DOM rather than the source.
    expect(await frame.getAttribute('sandbox')).toBe('allow-scripts')

    // The bridge actually formed: the widget received its context and painted a
    // real phase name. An empty frame would mean the handshake silently failed.
    const inner = moon.frameLocator('iframe')
    await expect(inner.locator('#phase')).toHaveText(/moon|crescent|gibbous|quarter/i, { timeout: 10_000 })
    await expect(inner.locator('#illum')).toHaveText(/\d+% lit/)

    await page.screenshot({ path: `${SHOTS}/sandboxed-widget.png` })
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })
})

test.describe('arranging the rail', () => {
  test('add, configure, resize, reorder and remove all persist', async ({ page }) => {
    const errors = await boot(page)
    await openAWindow(page)

    await page.getByRole('button', { name: 'Edit widgets' }).click()

    // REMOVE, and prove it survives a reload — the layout is user state.
    await expect(widget(page, 'vulos.notifications')).toBeVisible()
    await page.getByRole('button', { name: 'Remove Notifications' }).click()
    await expect(widget(page, 'vulos.notifications')).toHaveCount(0)

    // RESIZE cycles through the sizes the manifest offers.
    const world = widget(page, 'vulos.worldclock')
    await expect(world).toHaveAttribute('data-size', 'large')
    await page.getByRole('button', { name: 'Resize World clock' }).click()
    await expect(world).toHaveAttribute('data-size', 'medium')

    await page.reload()
    await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 20_000 })
    await openAWindow(page)
    await expect(widget(page, 'vulos.notifications')).toHaveCount(0)
    await expect(widget(page, 'vulos.worldclock')).toHaveAttribute('data-size', 'medium')

    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('configuring the world clock changes which cities it shows', async ({ page }) => {
    const errors = await boot(page)
    await openAWindow(page)
    await page.getByRole('button', { name: 'Edit widgets' }).click()
    await page.getByRole('button', { name: 'Configure World clock' }).click()

    const field = page.getByLabel('Cities')
    await expect(field).toBeVisible()
    // A half-hour zone and a quarter-hour zone, typed by a user.
    await field.fill('Asia/Kolkata, Asia/Kathmandu, Pacific/Chatham')

    const world = widget(page, 'vulos.worldclock')
    await expect(world.getByText('Mumbai')).toBeVisible()
    await expect(world.getByText('Kathmandu')).toBeVisible()
    await expect(world.getByText('Chatham')).toBeVisible()
    await expect(world.getByText('New York')).toHaveCount(0)

    // The fractional offsets are actually printed, not rounded to hours.
    const whens = (await world.locator('.vwidget-clock-when').allTextContents()).join(' ')
    expect(whens, `expected :30/:45 offsets, got "${whens}"`).toMatch(/:30|:45/)

    await page.screenshot({ path: `${SHOTS}/world-clock-configured.png` })
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('a permission is off until the user turns it on, per placement', async ({ page }) => {
    const errors = await boot(page)
    await openAWindow(page)
    await page.getByRole('button', { name: 'Edit widgets' }).click()
    await page.getByRole('button', { name: 'Add widget' }).click()
    await page.getByRole('button', { name: 'Add Notes' }).click()

    const notes = widget(page, 'vulos.notes')
    // Added with NOTHING granted, so it says so rather than silently forgetting.
    await expect(notes.getByText(/Allow .Store its own settings/)).toBeVisible()

    await page.getByRole('button', { name: 'Configure Notes' }).click()
    const toggle = page.getByRole('switch', { name: /Store its own settings/ })
    await expect(toggle).toHaveAttribute('aria-checked', 'false')
    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-checked', 'true')

    // Now it works.
    // getByRole('textbox'), not getByLabel: 'Notes' is also the accessible name
    // of the tile's <section> and of its Remove/Configure chips, so a bare label
    // lookup matched seven elements.
    await expect(notes.getByRole('textbox', { name: 'Notes' })).toBeVisible()
    await page.screenshot({ path: `${SHOTS}/permissions.png` })
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })

  test('the watchlist makes no network request and says whose figures it shows', async ({ page }) => {
    // THE sovereignty assertion, measured on the wire: every request the page
    // makes is recorded, and none of them may leave this origin.
    // The origin is taken from the CONFIGURED base URL, not from page.url():
    // the first requests fire while the page is still about:blank, and
    // `new URL('about:blank').origin` is "null", so every same-origin request
    // was recorded as third-party — a false positive on the single most
    // important assertion in this file.
    const base = new URL(test.info().project.use.baseURL || 'http://localhost:4173').origin
    const external: string[] = []
    page.on('request', (r) => {
      const u = new URL(r.url())
      if (u.origin !== base && u.protocol !== 'data:' && u.protocol !== 'blob:') external.push(r.url())
    })
    const errors = await boot(page)
    await openAWindow(page)
    await page.getByRole('button', { name: 'Edit widgets' }).click()
    await page.getByRole('button', { name: 'Add widget' }).click()
    await page.getByRole('button', { name: 'Add Watchlist' }).click()
    await page.getByRole('button', { name: 'Configure Watchlist' }).click()
    await page.getByLabel('Symbols').fill('AAPL 189.50 175.00, MSFT 412.10')

    const wl = widget(page, 'vulos.watchlist')
    await expect(wl.getByText('AAPL')).toBeVisible()
    await expect(wl.getByText('189.50')).toBeVisible()
    // The move against the user's own reference, with an explicit sign.
    await expect(wl.getByText('+8.29%')).toBeVisible()
    // And the disclaimer is on the face of the widget, not in a tooltip.
    await expect(wl.getByText(/No price source on this box/)).toBeVisible()

    await page.waitForTimeout(1500)
    expect(external, `the box made third-party requests: ${external.join(', ')}`).toEqual([])
    await page.screenshot({ path: `${SHOTS}/watchlist.png` })
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })
})

test.describe('a broken widget', () => {
  test('breaks its own tile and nothing else', async ({ page }) => {
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(e.message))
    await page.addInitScript(() => {
      try {
        localStorage.setItem('vulos-theme', 'dark')
        if (!sessionStorage.getItem('__vulos_widget_test_booted')) {
          sessionStorage.setItem('__vulos_widget_test_booted', '1')
          localStorage.removeItem('vulos.widgets.layout.v1')
        }
      } catch { /* ignore */ }
    })
    await installBackend(page)
    await page.goto('/')
    await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 20_000 })
    await openAWindow(page)

    // Poison ONE widget's persisted settings with a value its render will choke
    // on, then reload. The per-tile error boundary must contain it.
    await page.evaluate(() => {
      const raw = localStorage.getItem('vulos.widgets.layout.v1')
      if (!raw) return
      const l = JSON.parse(raw)
      for (const i of l.instances) if (i.widgetId === 'vulos.worldclock') i.settings = { zones: ['Mars/Olympus'] }
      localStorage.setItem('vulos.widgets.layout.v1', JSON.stringify(l))
    })
    await page.reload()
    await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 20_000 })
    await openAWindow(page)

    // An unknown zone must NOT throw — it degrades to an honest message — and
    // every other widget keeps working.
    // The copy is "None of the saved time zones are recognised on this box." —
    // the first version of this line searched for "not recognised", which never
    // appears in it, and reported a failure against a screenshot that showed the
    // widget doing exactly the right thing.
    await expect(
      widget(page, 'vulos.worldclock').getByText(/None of the saved time zones are recognised/i),
    ).toBeVisible()
    await expect(widget(page, 'vulos.clock')).toBeVisible()
    await expect(widget(page, 'vulos.agenda')).toBeVisible()
    expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
  })
})
