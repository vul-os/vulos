// contacts-app.e2e.js — real-chromium boot proof for the standalone Contacts app
// (the `vulos-contacts` builtin) over lilmail's /v1 via the box's PIM proxy
// (/api/pim/contacts/*). Fails on ANY uncaught page error.

import { test, expect } from '@playwright/test'
import { installBackend, json } from './mock-backend.js'

function cards() {
  return json({
    contacts: [
      { uid: 'u1', name: 'Ada Lovelace', org: 'Vulos', title: 'Engineer', emails: ['ada@x.com'], phones: ['+1 555'], note: 'first programmer' },
      { uid: 'u2', name: 'Grace Hopper', emails: ['grace@x.com'] },
    ],
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

test('the Contacts app lists contacts and shows a detail pane', async ({ page }) => {
  const errors = await boot(page, { 'GET /api/pim/contacts/cards': cards() })
  await launch(page, 'Contacts')

  const app = page.locator('[data-contacts-app]')
  await expect(app).toBeVisible()
  await expect(app.getByText('Ada Lovelace')).toBeVisible()
  await expect(app.getByText('Grace Hopper')).toBeVisible()

  await app.getByText('Ada Lovelace').click()
  const detail = page.locator('[data-contact-detail]')
  await expect(detail.getByText('Engineer · Vulos')).toBeVisible()
  await expect(detail.getByRole('link', { name: 'ada@x.com' })).toBeVisible()

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})

test('the Contacts app creates a contact through the /v1 write path', async ({ page }) => {
  let posted = null
  const errors = await boot(page, {
    'GET /api/pim/contacts/cards': json({ contacts: [] }),
    'POST /api/pim/contacts': (req) => { posted = req.postDataJSON(); return json({ contact: { uid: 'new' } }, 201) },
  })
  await launch(page, 'Contacts')

  const app = page.locator('[data-contacts-app]')
  await app.getByLabel('New contact').click()
  const editor = page.locator('[data-contact-editor]')
  await expect(editor).toBeVisible()
  await editor.getByLabel('Name').fill('Katherine Johnson')
  await editor.getByLabel('Email 1').fill('kj@nasa.gov')
  await editor.getByRole('button', { name: 'Save' }).click()

  await expect(editor).toBeHidden()
  expect(posted?.name).toBe('Katherine Johnson')
  expect(posted?.emails).toContain('kj@nasa.gov')

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})

test('the Contacts app degrades honestly when /v1 is unavailable', async ({ page }) => {
  const errors = await boot(page, {
    'GET /api/pim/contacts/cards': json({ error: 'unreachable' }, 502),
  })
  await launch(page, 'Contacts')

  const app = page.locator('[data-contacts-app]')
  await expect(app.getByText('Contacts unavailable.')).toBeVisible()
  await expect(app.getByText('Connect Mail →')).toBeVisible()

  expect(errors, `uncaught page errors: ${errors.join(' | ')}`).toEqual([])
})
