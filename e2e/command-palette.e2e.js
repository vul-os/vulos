// command-palette.e2e.js — real-browser ⌘K command palette + assistant flow.
//
// Boots straight into the shell (mocked logged-in session), opens the palette
// with ⌘K, and drives the Ask → assistant path over a REAL SSE stream mocked at
// the browser network layer. The load-bearing assertions are the WAVE-13
// security contract, verified in a real Chromium:
//   • Approve posts ONLY the opaque proposal id to /api/assistant/execute,
//   • Reject sends NOTHING to that endpoint.

import { test, expect } from '@playwright/test'
import { installBackend, json, sseBody } from './mock-backend.js'

const PROPOSAL = { id: 'prop_e2e_x1', tool: 'send_email', summary: 'Send email to Dana', args: { to: 'dana@acme.io', body: 'Hi Dana' } }

async function openPalette(page) {
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

test('WAVE-13: Approve posts only the opaque id; Reject sends nothing', async ({ page }) => {
  let executeBody = null
  let executeCount = 0
  await installBackend(page, {
    'POST /api/assistant/agent/stream': sseBody([{ type: 'proposal', proposal: PROPOSAL }, { type: 'done' }]),
    'POST /api/assistant/execute': (req) => {
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
