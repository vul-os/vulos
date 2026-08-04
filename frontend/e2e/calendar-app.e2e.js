// calendar-app.e2e.js — real-chromium boot proof for the standalone Calendar app
// (the `vulos-calendar` builtin) reading/writing lilmail's /v1 through the box's
// PIM proxy (/api/pim/calendar/*).
//
// Every test fails on ANY uncaught page error — a new always-mounted builtin is
// exactly the kind of change that can white-screen the OS shell. The /v1 surface
// is mocked at the browser network layer; nothing here talks to a real lilmail.

import { test, expect } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'

// An events payload with one event today (within the visible month grid).
function eventsToday() {
  const start = new Date(Date.now() + 60 * 60 * 1000).toISOString()
  const end = new Date(Date.now() + 2 * 60 * 60 * 1000).toISOString()
  return json({
    events: [{ uid: 'e1', summary: 'Launch review', start, end, location: 'War room', allDay: false }],
  })
}

async function boot(page, overrides = {}) {
  const errors = []
  page.on('pageerror', (e) => errors.push(e.message))
  await installBackend(page, overrides)
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 15_000 })
  return errors
}

// Launch a builtin by name via the ⌘K palette (same lane the Launchpad uses).
async function launch(page, name) {
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  await input.fill(name)
  await expect(page.getByText(name, { exact: true }).first()).toBeVisible()
  await page.keyboard.press('Enter')
}

test('the Calendar app opens, shows the month + an event, and switches to agenda', async ({ page }) => {
  const errors = await boot(page, { 'GET /api/pim/calendar/events': eventsToday() })
  await launch(page, 'Calendar')

  const app = page.locator('[data-calendar-app]')
  await expect(app).toBeVisible()
  await expect(app.getByText('Wed', { exact: true })).toBeVisible() // month grid weekday header
  await expect(app.getByText('Launch review').first()).toBeVisible()

  await app.getByRole('button', { name: 'Agenda' }).click()
  await expect(app.getByText('War room')).toBeVisible()

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})

test('the Calendar app creates an event through the /v1 write path', async ({ page }) => {
  let posted = null
  const errors = await boot(page, {
    'GET /api/pim/calendar/events': json({ events: [] }),
    'POST /api/pim/calendar/events': (req) => { posted = req.postDataJSON(); return json({ event: { uid: 'new' } }, 201) },
  })
  await launch(page, 'Calendar')

  const app = page.locator('[data-calendar-app]')
  await app.getByText('+ New event').click()
  const editor = page.locator('[data-event-editor]')
  await expect(editor).toBeVisible()
  await editor.getByPlaceholder('Event title').fill('Dentist')
  await editor.getByRole('button', { name: 'Save' }).click()

  // The editor closes and the create hit the /v1 seam with the mapped body.
  await expect(editor).toBeHidden()
  expect(posted?.summary).toBe('Dentist')

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})

test('⌘K "New event" opens Calendar with the new-event editor pre-opened', async ({ page }) => {
  // The command-registry action deep-links the builtin with ?action=new; the
  // Calendar must honour it by pre-opening the editor (not just the month grid).
  const errors = await boot(page, { 'GET /api/pim/calendar/events': json({ events: [] }) })

  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  await input.fill('New event')
  // The "New event" Actions row (title text, exact) — click it to run the action.
  const action = page.getByText('New event', { exact: true }).first()
  await expect(action).toBeVisible()
  await action.click()

  // Calendar launches AND its editor is already open on the new-event form.
  const editor = page.locator('[data-event-editor]')
  await expect(editor).toBeVisible()
  await expect(editor.getByPlaceholder('Event title')).toBeVisible()

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})

test('the Calendar app degrades honestly when /v1 is unavailable', async ({ page }) => {
  const errors = await boot(page, {
    'GET /api/pim/calendar/events': json({ error: 'mail service not configured' }, 503),
  })
  await launch(page, 'Calendar')

  const app = page.locator('[data-calendar-app]')
  await expect(app.getByText('Calendar unavailable.')).toBeVisible()
  await expect(app.getByText('Connect Mail →')).toBeVisible()

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})
