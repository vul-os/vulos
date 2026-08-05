// command-palette.e2e.js — real-browser ⌘K command palette + assistant flow.
//
// Boots straight into the shell (mocked logged-in session), opens the palette
// with ⌘K, and drives the Ask → assistant path over a REAL SSE stream mocked at
// the browser network layer. The load-bearing assertions are the WAVE-13
// security contract, verified in a real Chromium:
//   • Approve posts ONLY the opaque proposal id to /api/assistant/execute,
//   • Reject sends NOTHING to that endpoint.

import { test, expect, type Page, type Request } from '@playwright/test'
import { installBackend, json, sseBody } from './mock-backend.js'

const PROPOSAL = { id: 'prop_e2e_x1', tool: 'send_email', summary: 'Send email to Dana', args: { to: 'dana@acme.io', body: 'Hi Dana' } }

async function openPalette(page: Page) {
  await page.goto('/')
  // Wait for the shell (menu bar chat button) to confirm we're past auth.
  await expect(page.getByTitle('Chat (Ctrl+K)')).toBeVisible({ timeout: 15_000 })
  const input = page.getByPlaceholder(/Search apps/)
  // The global ⌘K listener is on window; give focus to the document body and
  // retry the shortcut until the palette input appears (avoids a race with the
  // shell's late-mounting keydown handler).
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
}

test('⌘K opens the palette and fuzzy-matches apps', async ({ page }) => {
  await installBackend(page)
  await openPalette(page)
  await page.getByPlaceholder(/Search apps/).fill('term')
  await expect(page.getByText('Terminal').first()).toBeVisible()
})

test('mail search renders results from the mocked endpoint', async ({ page }) => {
  await installBackend(page, {
    'GET /api/mail/search': json({ messages: [{ uid: 'm1', subject: 'Invoice #42', from_name: 'Billing' }] }),
  })
  await openPalette(page)
  await page.getByPlaceholder(/Search apps/).fill('invoice')
  await expect(page.getByText('Invoice #42')).toBeVisible()
})

test('Ask streams a plain answer token-by-token', async ({ page }) => {
  await installBackend(page, {
    'POST /api/assistant/agent/stream': sseBody([
      { type: 'token', content: 'You owe ' },
      { type: 'token', content: '$128.40.' },
      { type: 'done' },
    ]),
  })
  await openPalette(page)
  await page.getByPlaceholder(/Search apps/).fill('?how much do I owe')
  await page.keyboard.press('Enter')
  await expect(page.getByText(/You owe \$128\.40\./)).toBeVisible()
})

test('palette groups results into sections and Tab jumps between them', async ({ page }) => {
  await installBackend(page, {
    // A mail hit so the query yields Apps + Mail + Actions sections together.
    'GET /api/mail/search': json({ messages: [{ uid: 'm9', subject: 'Mailbox full', from_name: 'System' }] }),
  })
  await openPalette(page)
  // "mail" fuzzy-matches app rows AND the "Compose email" action (keyword 'mail'),
  // and returns a mail search hit — so all three section headers render. Section
  // headers are the mono-uppercase labels (scoped so they don't collide with the
  // "Mail" app row / Dock button of the same text).
  await page.getByPlaceholder(/Search apps/).fill('mail')
  const header = (name: string) => page.locator('.uppercase.font-mono', { hasText: new RegExp(`^${name}$`) })
  await expect(header('Apps')).toBeVisible()
  await expect(header('Mail')).toBeVisible()
  await expect(header('Actions')).toBeVisible()
  // The mocked mail hit renders under the Mail section.
  await expect(page.getByText('Mailbox full')).toBeVisible()

  // A default action row (from the command registry) is present.
  const composeRow = page.getByText('Compose email')
  await expect(composeRow).toBeVisible()

  // Selection starts on the first (Apps) row; Tab jumps to the first row of the
  // NEXT section, walking across sections until an Actions row is highlighted.
  // Row highlighting is exposed as data-active on the row element (see Row() in
  // src/shell/CommandPalette.jsx) — not as a Tailwind background class. The row
  // was restyled to a CSS-variable theme (.vshell-row) and the old
  // bg-neutral-800/70 selector this assertion used stopped matching anything.
  const activeAncestor = 'xpath=ancestor::div[@data-active]'
  await expect(async () => {
    await page.keyboard.press('Tab')
    await expect(composeRow.locator(activeAncestor)).toBeVisible({ timeout: 500 })
  }).toPass({ timeout: 5_000 })
})

test('palette add_contact proposal posts only the opaque id (WAVE-13)', async ({ page }) => {
  let executeBody: unknown = null
  await installBackend(page, {
    'POST /api/assistant/agent/stream': sseBody([
      { type: 'proposal', proposal: { id: 'prop_pal_contact', tool: 'add_contact', summary: 'Add Dana Ruiz to contacts', args: { name: 'Dana Ruiz', email: 'dana@acme.io' } } },
      { type: 'done' },
    ]),
    'POST /api/assistant/execute': (req: Request) => { executeBody = JSON.parse(req.postData() || '{}'); return json({ result: 'Contact added.' }) },
  })
  await openPalette(page)
  await page.getByPlaceholder(/Search apps/).fill('?add dana to contacts')
  await page.keyboard.press('Enter')
  await expect(page.getByText(/Needs your approval · Add contact/i)).toBeVisible()
  await page.getByRole('button', { name: 'Approve' }).click()
  await expect.poll(() => executeBody).not.toBeNull()
  expect(executeBody).toEqual({ id: 'prop_pal_contact' })
  expect(executeBody).not.toHaveProperty('args')
})

test('WAVE-13: Approve posts only the opaque id; Reject sends nothing', async ({ page }) => {
  let executeBody: unknown = null
  let executeCount = 0
  await installBackend(page, {
    'POST /api/assistant/agent/stream': sseBody([{ type: 'proposal', proposal: PROPOSAL }, { type: 'done' }]),
    'POST /api/assistant/execute': (req: Request) => {
      executeCount++
      executeBody = JSON.parse(req.postData() || '{}')
      return json({ result: 'Message delivered.' })
    },
  })

  // ── Reject first: nothing must be sent ──────────────────────────────────────
  await openPalette(page)
  await page.getByPlaceholder(/Search apps/).fill('?email dana')
  await page.keyboard.press('Enter')
  await expect(page.getByText(/Needs your approval/i)).toBeVisible()
  await page.getByRole('button', { name: 'Reject' }).click()
  await expect(page.getByText(/Rejected — nothing was done/i)).toBeVisible()
  await page.waitForTimeout(200)
  expect(executeCount).toBe(0) // Reject sent NOTHING

  // ── Approve: exactly one request, body is only the opaque id ────────────────
  const input = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(input).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 10_000 })
  await input.fill('?email dana')
  await page.keyboard.press('Enter')
  await expect(page.getByText(/Needs your approval/i)).toBeVisible()
  await page.getByRole('button', { name: 'Approve' }).click()

  await expect.poll(() => executeCount).toBe(1)
  expect(executeBody).toEqual({ id: 'prop_e2e_x1' })
  expect(executeBody).not.toHaveProperty('args')
  expect(executeBody).not.toHaveProperty('tool')
  await expect(page.getByText(/✓ Approved and executed/i)).toBeVisible()
})
