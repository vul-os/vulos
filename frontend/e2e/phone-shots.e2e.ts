// phone-shots.e2e.ts — renders the Phone app at every width and theme and
// writes the images to e2e-shots/phone/, so they can be LOOKED AT.
//
// This is not an assertion suite and it is not trying to be. Six visual defects
// in this codebase recently survived a green unit suite, eslint and tsc, and
// were found only by rendering at several widths in both themes and reading the
// pictures. The contrast gate next door catches one specific class of those
// (a colour pair below AA); it cannot see a squeezed two-pane layout, a control
// that has fallen off the bottom of a window, or a row rhythm that has gone
// ragged. Nothing but an eye catches those, and an eye needs the images.
//
// Skipped unless SHOTS=1, so it does not slow the normal run.

import { test, expect, type Page } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'
import { launchApp } from './contrast-scan'
import { mkdirSync } from 'node:fs'

const NOW = Math.floor(Date.now() / 1000)
const OUT = 'e2e-shots/phone'

const ROUTES = {
  'GET /api/telephony/status': json({
    available: true, state: 'registered', signal_quality: 72,
    operator: 'Test Net', number: '+27830000001', voice: true,
  }),
  'GET /api/telephony/virtual/status': json({ configured: false, can_call: false }),
  'GET /api/telephony/calls': json([
    { id: 'c1', number: '+27831112222', direction: 'missed', ts: NOW - 300, duration: 0 },
    { id: 'c2', number: '+27835554444', direction: 'incoming', ts: NOW - 3600, duration: 312 },
    { id: 'c3', number: '+27829998888', direction: 'outgoing', ts: NOW - 7200, duration: 95 },
  ]),
  'GET /api/telephony/sms/threads': json([
    { number: '+27831112222', contact_name: '', last_body: 'On my way, ten minutes out', last_ts: NOW - 600, unread_count: 2 },
    { number: '+27835554444', contact_name: '', last_body: 'Thanks for sorting that', last_ts: NOW - 90000, unread_count: 0 },
  ]),
  'GET /api/telephony/sms/thread/%2B27831112222': json([
    { direction: 'incoming', body: 'Are you around this afternoon?', ts: NOW - 900, status: 'delivered' },
    { direction: 'outgoing', body: 'Yes — any time after two works.', ts: NOW - 800, status: 'sent' },
    { direction: 'incoming', body: 'On my way, ten minutes out', ts: NOW - 600, status: 'delivered' },
  ]),
  'GET /api/peering/call/history': json([{
    id: 'p1', peer_id: 'vulos:ed25519:abc', peer_display: 'Thandi Mokoena',
    direction: 'inbound', status: 'completed',
    started_at: new Date((NOW - 1800) * 1000).toISOString(), duration_sec: 240,
  }]),
  'GET /api/contacts/unified': json({
    contacts: [
      { id: 'u1', name: 'Priya Naidoo', phones: ['+27 83 111 2222'], emails: ['priya@example.org'], org: 'Kestrel Labs', sources: ['vulos'] },
      { id: 'u2', name: 'Sipho Dlamini', phones: ['0835554444'], emails: [], org: 'Longmarket Co-op', sources: ['box-sim'] },
      { id: 'u3', name: 'Ayanda Khumalo', phones: ['+27 82 999 8888'], emails: ['ayanda@example.org'], org: '', sources: ['vulos', 'phone'] },
    ],
    sources_active: ['vulos', 'phone', 'box-sim'],
  }),
}

const app = (page: Page) => page.locator('[data-phone-app]')

test.skip(!process.env.SHOTS, 'set SHOTS=1 to write screenshots')

async function boot(page: Page, theme: 'dark' | 'light', overrides = {}) {
  await page.addInitScript((t) => {
    try { localStorage.setItem('vulos-theme', t) } catch { /* private mode */ }
  }, theme)
  await installBackend(page, { ...ROUTES, ...overrides })
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 20_000 })
  await page.evaluate((t) => { document.documentElement.setAttribute('data-theme', t) }, theme)
  await launchApp(page, 'Phone')
  await expect(app(page)).toBeVisible({ timeout: 10_000 })
}

async function dragTo(page: Page, width: number, height = 720) {
  const win = page.locator('[data-window-id]').filter({ has: app(page) })
  const box = await win.boundingBox()
  if (!box) throw new Error('no window box')
  await page.mouse.move(box.x + box.width - 3, box.y + box.height - 3)
  await page.mouse.down()
  await page.mouse.move(box.x + width, box.y + height, { steps: 8 })
  await page.mouse.up()
  await page.waitForTimeout(350)
}

for (const theme of ['dark', 'light'] as const) {
  test(`shots — ${theme}`, async ({ page }) => {
    mkdirSync(OUT, { recursive: true })
    test.setTimeout(180_000)
    await page.setViewportSize({ width: 1600, height: 1000 })
    await boot(page, theme)

    for (const width of [390, 700, 1180]) {
      await dragTo(page, width)
      for (const tab of ['Recents', 'Contacts', 'Keypad', 'Messages'] as const) {
        await app(page).getByRole('tab', { name: tab }).click()
        await page.waitForTimeout(250)
        if (tab !== 'Keypad') {
          const row = app(page).locator('li button').first()
          if (await row.count()) { await row.click(); await page.waitForTimeout(300) }
        }
        await app(page).screenshot({ path: `${OUT}/${theme}-${width}-${tab}.png` })
      }
    }
  })

  test(`shots — ${theme} no modem`, async ({ page }) => {
    mkdirSync(OUT, { recursive: true })
    await page.setViewportSize({ width: 1600, height: 1000 })
    await boot(page, theme, { 'GET /api/telephony/status': json({ available: false }) })
    await app(page).screenshot({ path: `${OUT}/${theme}-no-modem.png` })
  })

  test(`shots — ${theme} service down`, async ({ page }) => {
    mkdirSync(OUT, { recursive: true })
    await page.setViewportSize({ width: 1600, height: 1000 })
    await boot(page, theme, {
      'GET /api/telephony/status': json({ error: 'boom' }, 500),
      'GET /api/contacts/unified': json({ error: 'boom' }, 500),
    })
    await app(page).screenshot({ path: `${OUT}/${theme}-service-down.png` })
  })
}
