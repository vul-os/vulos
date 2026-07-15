// calendar-widget.e2e.js — real-chromium boot proof for the two OS-desktop UX
// features shipped in this change:
//
//   1. the ambient right-hand-side Calendar widget renders on the desktop,
//      shows "what's next" from GET /api/pim/calendar/events (the PIM proxy —
//      the widget is decoupled from /api/assistant/home), and expands to the week
//   2. the bucket-backed Files app (Drive, over /api/files/*) opens as a window
//
// Every test fails on ANY uncaught page error — this shell has a blank-screen
// history and a new always-mounted desktop widget is exactly the kind of change
// that can white-screen the whole OS. Nothing here is asserted "green" without
// the real production bundle running in a real browser.

import { test, expect } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'

// A PIM calendar payload carrying two future events (lilmail /v1 shape) so the
// widget has a concrete "next up" plus a second event to reveal on expand.
function agendaEvents() {
  const soon = new Date(Date.now() + 60 * 60 * 1000).toISOString()      // +1h
  const later = new Date(Date.now() + 25 * 60 * 60 * 1000).toISOString() // +25h
  return json({
    events: [
      { uid: 'e1', title: 'Launch review', start: soon, location: 'War room' },
      { uid: 'e2', title: 'Team sync', start: later },
    ],
  })
}

// Attach a pageerror collector BEFORE navigation and boot to the desktop.
async function boot(page, overrides = {}) {
  const errors = []
  page.on('pageerror', (e) => errors.push(e.message))
  await installBackend(page, overrides)
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 15_000 })
  return errors
}

// Launch an app via the ⌘K palette so a window covers the Home backdrop — the
// ambient Calendar widget mounts only when Home is covered (Home itself shows
// the agenda when it's the visible backdrop, so the two never double-render).
async function openAWindow(page, appName = 'Calculator') {
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

test('the RHS Calendar widget renders "what\'s next" and expands to the week', async ({ page }) => {
  const errors = await boot(page, { 'GET /api/pim/calendar/events': agendaEvents() })
  await openAWindow(page)

  // The widget is mounted on the desktop RHS and shows the soonest event.
  const widget = page.locator('[data-calendar-widget]')
  await expect(widget).toBeVisible()
  await expect(widget.getByText('Launch review')).toBeVisible()
  await expect(widget.getByText('War room')).toBeVisible()
  // Live indicator (agenda_fresh) is shown.
  await expect(widget.getByText('live')).toBeVisible()

  // The second event is hidden until the widget is expanded.
  await expect(widget.getByText('Team sync')).toBeHidden()
  await widget.getByLabel('Expand calendar').click()
  await expect(widget.getByText('Team sync')).toBeVisible()
  await expect(widget.getByText('Open Calendar →')).toBeVisible()

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})

test('the Calendar widget degrades honestly when the calendar backend is down', async ({ page }) => {
  // 503 on the PIM calendar read → widget shows an honest unavailable state,
  // never a crash and never invented events.
  const errors = await boot(page, {
    'GET /api/pim/calendar/events': json({ error: 'unavailable' }, 503),
  })
  await openAWindow(page)

  const widget = page.locator('[data-calendar-widget]')
  await expect(widget).toBeVisible()
  await expect(widget.getByText('Calendar unavailable.')).toBeVisible()
  await expect(widget.getByText('Connect Mail →')).toBeVisible()

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})

// The canonical OS Files app (registry id `drive`, name "Files") is the
// bucket-backed file manager over the box's own bucket via /api/files/*. The
// separate local-FS browser is named "File Explorer" (id `files`). This test
// launches the bucket Files app and proves it mounts without white-screening.
test('the bucket-backed Files app opens as a window', async ({ page }) => {
  const errors = await boot(page)

  // Launch via the ⌘K palette — the same launchApp lane the Launchpad uses.
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  // "Drive" uniquely ranks the bucket Files app (keyword) as the top match.
  await input.fill('Drive')
  await expect(page.getByText(/Drive/).first()).toBeVisible()
  await page.keyboard.press('Enter')

  // A window opens with the standard traffic-light chrome and a Dock entry —
  // proof the bucket Files app mounted. Its window/Dock title is "Files".
  await expect(page.getByRole('button', { name: 'Close window' }).first()).toBeVisible()
  await expect(page.getByRole('button', { name: 'Files', exact: true })).toBeVisible()
  // The Drive surface actually rendered (bucket control-plane UI).
  await expect(page.getByText('My Drive').first()).toBeVisible()

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})
