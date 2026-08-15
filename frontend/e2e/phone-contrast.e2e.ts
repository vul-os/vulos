// phone-contrast.e2e.ts — the Phone app measured on COMPOSITED PIXELS, at every
// width it is actually used at, in BOTH themes.
//
// Both halves of that matter and each has burned this codebase before:
//
//  • Composited, not tokens. A stylesheet audit cannot see `opacity`, and the
//    failure is never a value in a file — it is a pair. contrast-scan.ts paints
//    each colour onto a 1x1 canvas so oklch()/color-mix() resolve too, which a
//    regex-based parser silently skipped (and then reported clean).
//
//  • Both themes. This app is written against dark; a dark-only sweep is how
//    light-theme copy ends up at 2.4:1. Contacts' own empty state failed at
//    2.42:1 in light while "passing" in dark.
//
// Widths: the app is a WINDOW, so the interesting axis is the window's width,
// not the viewport's. We drag it across the phone/tablet/desktop range plus the
// awkward sizes in between, and separately check the real 390x844 phone and the
// two tablet viewports where the shell itself changes shape.

import { test, expect, type Page } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'
import { launchApp, belowAA, textNodeCount } from './contrast-scan'

const NOW = Math.floor(Date.now() / 1000)

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

const THEMES = ['dark', 'light'] as const
const TABS = ['Recents', 'Contacts', 'Keypad', 'Messages'] as const
const app = (page: Page) => page.locator('[data-phone-app]')

async function boot(page: Page, theme: 'dark' | 'light') {
  await page.addInitScript((t) => {
    try { localStorage.setItem('vulos-theme', t) } catch { /* private mode */ }
  }, theme)
  await installBackend(page, ROUTES)
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 20_000 })
  await page.evaluate((t) => { document.documentElement.setAttribute('data-theme', t) }, theme)
  await launchApp(page, 'Phone')
  await expect(app(page)).toBeVisible({ timeout: 10_000 })
}

async function dragWindowTo(page: Page, width: number, height = 720) {
  const win = page.locator('[data-window-id]').filter({ has: app(page) })
  const box = await win.boundingBox()
  if (!box) throw new Error('window has no bounding box')
  await page.mouse.move(box.x + box.width - 3, box.y + box.height - 3)
  await page.mouse.down()
  await page.mouse.move(box.x + width, box.y + height, { steps: 10 })
  await page.mouse.up()
  await page.waitForTimeout(350)
}

// Every width the app is genuinely used at: a phone, both common tablets, a
// default window, and the awkward sizes on either side of each breakpoint
// (560 and 900) where a layout is most likely to be wrong.
const WIDTHS = [390, 555, 565, 768, 834, 895, 905, 1180]

for (const theme of THEMES) {
  test(`no sub-AA text in the Phone app at any width — ${theme}`, async ({ page }) => {
    await page.setViewportSize({ width: 1600, height: 1000 })
    await boot(page, theme)

    const failures: string[] = []
    for (const width of WIDTHS) {
      await dragWindowTo(page, width)
      for (const tab of TABS) {
        await app(page).getByRole('tab', { name: tab }).click()
        await page.waitForTimeout(250)

        // Messages: open a thread so the bubbles (the highest-risk pair in the
        // app — accent background under accent-contrast text) are measured too.
        if (tab === 'Messages') {
          const thread = app(page).locator('li button').first()
          if (await thread.count()) { await thread.click(); await page.waitForTimeout(250) }
        } else if (tab !== 'Keypad') {
          const row = app(page).locator('li button').first()
          if (await row.count()) { await row.click(); await page.waitForTimeout(250) }
        }

        // A screen that failed to render must not pass by having nothing to
        // measure — that is the shape of a gate that checks nothing.
        expect(await textNodeCount(page), `${theme} ${width}px ${tab} rendered nothing`).toBeGreaterThan(8)

        for (const f of await belowAA(page)) failures.push(`${theme} ${width}px ${tab}: ${f}`)
      }
    }
    expect(failures, failures.join('\n')).toEqual([])
  })
}

// The degraded states, which the width sweep above never reaches because it
// boots a healthy modem. Both are full-surface takeovers with their own
// colours — the "Try again" button was an accent fill with white text, the same
// pair that measured 3.68:1 elsewhere, and nothing was measuring it.
for (const theme of THEMES) {
  for (const [name, override] of [
    ['no modem', { 'GET /api/telephony/status': json({ available: false }) }],
    ['service down', {
      'GET /api/telephony/status': json({ error: 'upstream unavailable' }, 500),
      'GET /api/contacts/unified': json({ error: 'upstream unavailable' }, 500),
    }],
  ] as const) {
    test(`no sub-AA text in the "${name}" state — ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width: 1600, height: 1000 })
      await page.addInitScript((t) => {
        try { localStorage.setItem('vulos-theme', t) } catch { /* private mode */ }
      }, theme)
      await installBackend(page, { ...ROUTES, ...override })
      await page.goto('/')
      await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 20_000 })
      await page.evaluate((t) => { document.documentElement.setAttribute('data-theme', t) }, theme)
      await launchApp(page, 'Phone')
      await expect(app(page)).toBeVisible({ timeout: 10_000 })
      await page.waitForTimeout(600)

      expect(await textNodeCount(page)).toBeGreaterThan(8)
      const failures = (await belowAA(page)).map((f) => `${theme} ${name}: ${f}`)
      expect(failures, failures.join('\n')).toEqual([])
    })
  }
}

// The real phone and tablet viewports, where the SHELL itself changes shape and
// the app may be laid out by something other than the desktop window manager.
for (const theme of THEMES) {
  for (const [w, h, name] of [[390, 844, 'phone'], [768, 1024, 'tablet-portrait'], [834, 1194, 'tablet-large']] as const) {
    test(`no sub-AA text at the real ${name} viewport — ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width: w, height: h })
      await page.addInitScript((t) => {
        try { localStorage.setItem('vulos-theme', t) } catch { /* private mode */ }
      }, theme)
      await installBackend(page, ROUTES)
      await page.goto('/')
      await page.evaluate((t) => { document.documentElement.setAttribute('data-theme', t) }, theme)
      await page.waitForTimeout(2500)

      // The whole screen is measured here, not just the app: at these sizes the
      // shell may render the mobile stack, and the app's chrome is the shell's.
      expect(await textNodeCount(page)).toBeGreaterThan(8)
      const failures = (await belowAA(page)).map((f) => `${theme} ${name}: ${f}`)
      expect(failures, failures.join('\n')).toEqual([])
    })
  }
}
