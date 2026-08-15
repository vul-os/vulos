// contacts-calling.e2e.ts — a phone number in Contacts must actually place a
// call on the box's modem.
//
// It used to be `<a href="tel:…">`. In a browser on a box, that hands the URL to
// the host OS, and the host OS is the box — which has no dialler application,
// because its modem is reached over /api/telephony. The single affordance that
// connected Contacts to the phone did nothing whatsoever, and did it silently.
//
// These tests pin the three outcomes that matter: it dials, it is honestly
// disabled when the box cannot call, and it reports a failure as a failure
// rather than as a call that went through.

import { test, expect, type Page } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'
import { launchApp } from './contrast-scan'

const CARD = {
  uid: 'c-1', name: 'Priya Naidoo', org: 'Kestrel Labs', title: '', note: '',
  emails: ['priya@example.org'], phones: ['+27 83 111 2222'],
}

const VOICE_MODEM = {
  available: true, state: 'registered', signal_quality: 72,
  operator: 'Test Net', number: '+27830000001', voice: true,
}

const BASE = {
  'GET /api/pim/contacts/cards': json({ contacts: [CARD] }),
  'GET /api/contacts/unified': json({ contacts: [], sources_active: [] }),
  'GET /api/telephony/status': json(VOICE_MODEM),
  'GET /api/telephony/virtual/status': json({ configured: false, can_call: false }),
}

async function openContact(page: Page, overrides = {}) {
  await installBackend(page, { ...BASE, ...overrides })
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 20_000 })
  await launchApp(page, 'Contacts')
  await page.getByText('Priya Naidoo').first().click()
  await expect(page.locator('[data-contact-detail]')).toBeVisible({ timeout: 10_000 })
}

const callBtn = (page: Page) => page.locator('[data-call-number]')

test('a phone number places a real call on the box modem', async ({ page }) => {
  await openContact(page)

  // Registered AFTER boot: page.route matches last-registered-first, so this
  // would never fire if it went in before installBackend's `**/api/**`.
  const posts: string[] = []
  await page.route('**/api/telephony/call', async (route) => {
    posts.push(route.request().postData() || '')
    await route.fulfill(json({ ok: true }) as Parameters<typeof route.fulfill>[0])
  })

  await expect(callBtn(page)).toBeEnabled()
  await callBtn(page).click()

  await expect.poll(() => posts.length).toBeGreaterThan(0)
  expect(JSON.parse(posts[0])).toEqual({ to: '+27 83 111 2222' })
})

test('with no modem the call control is disabled and says why', async ({ page }) => {
  await openContact(page, { 'GET /api/telephony/status': json({ available: false }) })

  await expect(callBtn(page)).toBeDisabled()
  await expect(page.locator('[data-contact-detail]')).toContainText(/No modem is connected to this box/i)
})

test('a data/SMS-only modem cannot call, and does not pretend it can', async ({ page }) => {
  await openContact(page, { 'GET /api/telephony/status': json({ ...VOICE_MODEM, voice: false }) })

  await expect(callBtn(page)).toBeDisabled()
  await expect(page.locator('[data-contact-detail]')).toContainText(/data\/SMS only/i)
})

test('a 200 carrying {"error"} is reported, not swallowed', async ({ page }) => {
  // The box answers POST /api/telephony/call with HTTP 200 and an error body
  // when the dial fails. res.ok is true; trusting it reports success.
  await openContact(page, { 'POST /api/telephony/call': json({ error: 'telephony: no modem' }) })

  await callBtn(page).click()
  await expect(page.locator('[data-contact-detail]').getByRole('alert')).toContainText(/No modem is connected/i)
})
