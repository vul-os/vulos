// home-assistant.e2e.js — real-browser Vulos OS Home assistant surface (wave-58).
//
// Home (src/shell/home/Home.jsx) is the proactive desktop backdrop shown whenever
// no windows are open — so it renders on every boot. This suite drives the wave-
// 11/40/48/49 surfaces that were deepened since the wave-28 baseline, in a real
// Chromium against the browser-level mock backend:
//
//   • the aggregated brief/greeting/agenda from GET /api/assistant/home,
//   • the always-there composer asking a question → a REAL SSE token stream,
//   • the honest offline + empty states (Home never crashes),
//   • the wave-49 aria-live contract: the streamed answer bubble announces
//     (aria-live=polite) but the user's echoed turn does NOT re-announce,
//   • the wave-40 invite RSVP flow that must route through the agent as a
//     LEDGER-GATED proposal — never auto-executed,
//   • the wave-13 security contract at the browser level: Approve posts ONLY the
//     opaque {id} to /api/assistant/execute (no reconstructed args/tool), and
//     Reject sends NOTHING.
//
// These lift the wave-49 RTL/MSW Home.integration.test.jsx into a shipping-bundle
// Chromium so the ledger contract is pinned end-to-end, at the network layer, in
// the actual production build.

import { test, expect } from '@playwright/test'
import { installBackend, json, sseBody, HOME_PAYLOAD } from './mock-backend.js'

// A future-dated invite so relative-day formatting is stable regardless of when
// the suite runs.
const INVITE = {
  message_uid: 'inv_e2e_1',
  subject: 'Team sync',
  from: 'lead@acme.io',
  invite: { summary: 'Team sync', organizer: 'lead@acme.io', start: '2999-01-01T15:00:00Z' },
}

// Boot into the shell and wait until we're past auth. The menu-bar Applications
// button is the shell-mounted marker used by the other OS specs.
async function boot(page, overrides = {}) {
  await installBackend(page, overrides)
  await page.goto('/')
  await expect(page.getByTitle('Applications')).toBeVisible({ timeout: 15_000 })
}

// The Home composer input (distinct from the ⌘K palette input).
function composer(page) {
  return page.getByLabel('Ask your assistant')
}

test('Home renders the streamed brief + greeting from /api/assistant/home', async ({ page }) => {
  await boot(page, {
    'GET /api/assistant/home': json({
      ...HOME_PAYLOAD,
      greeting: 'Good evening, Ada',
      brief: 'Two threads need a reply; your 3pm moved to 4pm.',
      agenda: [{ id: 'e1', title: 'Design review', start: '2999-01-02T17:00:00Z' }],
    }),
  })
  await expect(page.getByText('Good evening, Ada')).toBeVisible()
  await expect(page.getByText(/Two threads need a reply/)).toBeVisible()
  // The agenda section renders the live event.
  await expect(page.getByText('Design review')).toBeVisible()
})

test('asking a question streams a token answer, then completes', async ({ page }) => {
  await boot(page, {
    'POST /api/assistant/agent/stream': sseBody([
      { type: 'token', content: 'Your 3pm ' },
      { type: 'token', content: 'moved to 4pm.' },
      { type: 'done' },
    ]),
  })
  await composer(page).fill('what changed today?')
  await page.getByRole('button', { name: 'Send' }).click()
  // The accumulated answer renders in the transcript once the stream completes.
  await expect(page.getByText('Your 3pm moved to 4pm.')).toBeVisible()
})

test('honest offline state when /api/assistant/home is unreachable', async ({ page }) => {
  await boot(page, {
    'GET /api/assistant/home': json({ error: 'box offline' }, 503),
  })
  // Home stays alive and tells the truth rather than crashing.
  await expect(page.getByText(/Assistant offline — Home is still here/i)).toBeVisible()
})

test('honest empty state when there is nothing urgent', async ({ page }) => {
  await boot(page, {
    'GET /api/assistant/home': json({ ...HOME_PAYLOAD, brief: '' }),
  })
  await expect(page.getByText(/Nothing urgent — you're clear/i)).toBeVisible()
  await expect(page.getByText(/Nothing on your calendar for the week ahead/i)).toBeVisible()
})

test('WAVE-49: the streamed answer is a polite live region; the user turn is not', async ({ page }) => {
  await boot(page, {
    'POST /api/assistant/agent/stream': sseBody([{ type: 'token', content: 'Done.' }, { type: 'done' }]),
  })
  await composer(page).fill('mark it done')
  await page.getByRole('button', { name: 'Send' }).click()

  const answer = page.getByText('Done.', { exact: true })
  await expect(answer).toBeVisible()
  // The assistant bubble announces exactly once via aria-live; the transcript log
  // wrapper is role="log" (not itself a per-token live region), and the echoed
  // user turn must NOT re-announce.
  await expect(answer).toHaveAttribute('aria-live', 'polite')
  await expect(page.getByText('mark it done', { exact: true })).not.toHaveAttribute('aria-live', /.+/)
  await expect(page.getByRole('log', { name: 'Assistant conversation' })).toBeVisible()
})

test('WAVE-40/13: RSVP to an invite routes through the agent as a ledger-gated proposal, never auto-sent', async ({ page }) => {
  let executeCount = 0
  let executeBody = null
  await boot(page, {
    'GET /api/assistant/home': json({ ...HOME_PAYLOAD, invites: [INVITE] }),
    // The mutating RSVP arrives ONLY as a proposal — the SSE stream never carries
    // an executed action.
    'POST /api/assistant/agent/stream': sseBody([
      { type: 'proposal', proposal: { id: 'prop_rsvp_e2e', tool: 'rsvp_invite', summary: 'RSVP accept to Team sync', args: {} } },
      { type: 'done' },
    ]),
    'POST /api/assistant/execute': (req) => {
      executeCount++
      executeBody = JSON.parse(req.postData() || '{}')
      return json({ result: 'RSVP sent.' })
    },
  })

  // The invite surfaces with its Accept / Tentative / Decline affordances.
  await expect(page.getByText('Invites awaiting your response')).toBeVisible()
  await page.getByRole('button', { name: 'Accept' }).click()

  // A ledger-gated proposal appears in the composer — nothing has executed yet.
  await expect(page.getByText(/Needs your approval · RSVP to invite/i)).toBeVisible()
  await page.waitForTimeout(150)
  expect(executeCount).toBe(0) // clicking Accept auto-sent NOTHING

  // Approving posts ONLY the opaque id — no reconstructed args/tool.
  await page.getByRole('button', { name: 'Approve' }).click()
  await expect.poll(() => executeCount).toBe(1)
  expect(executeBody).toEqual({ id: 'prop_rsvp_e2e' })
  expect(executeBody).not.toHaveProperty('args')
  expect(executeBody).not.toHaveProperty('tool')
  await expect(page.getByText(/✓ Approved and executed/i)).toBeVisible()
})

test('WAVE-13: Home composer — Reject sends nothing; a proposal never leaks args', async ({ page }) => {
  let executeCount = 0
  await boot(page, {
    'POST /api/assistant/agent/stream': sseBody([
      { type: 'proposal', proposal: { id: 'prop_email_e2e', tool: 'send_email', summary: 'Send email to Dana', args: { to: 'dana@acme.io', body: 'Hi Dana' } } },
      { type: 'done' },
    ]),
    'POST /api/assistant/execute': () => { executeCount++; return json({ result: 'Delivered.' }) },
  })
  await composer(page).fill('email dana that I am running late')
  await page.getByRole('button', { name: 'Send' }).click()

  await expect(page.getByText(/Needs your approval · Send email/i)).toBeVisible()
  await page.getByRole('button', { name: 'Reject' }).click()
  await expect(page.getByText(/Rejected — nothing was done/i)).toBeVisible()
  await page.waitForTimeout(200)
  expect(executeCount).toBe(0) // Reject is inert — the ledger is never touched
})

test('WAVE-13: a contacts (add_contact) proposal also posts only the opaque id', async ({ page }) => {
  let executeBody = null
  await boot(page, {
    'POST /api/assistant/agent/stream': sseBody([
      { type: 'proposal', proposal: { id: 'prop_contact_e2e', tool: 'add_contact', summary: 'Add Dana Ruiz to contacts', args: { name: 'Dana Ruiz', email: 'dana@acme.io' } } },
      { type: 'done' },
    ]),
    'POST /api/assistant/execute': (req) => { executeBody = JSON.parse(req.postData() || '{}'); return json({ result: 'Contact added.' }) },
  })
  await composer(page).fill('add dana to my contacts')
  await page.getByRole('button', { name: 'Send' }).click()

  await expect(page.getByText(/Needs your approval · Add contact/i)).toBeVisible()
  await page.getByRole('button', { name: 'Approve' }).click()
  await expect.poll(() => executeBody).not.toBeNull()
  expect(executeBody).toEqual({ id: 'prop_contact_e2e' })
  expect(executeBody).not.toHaveProperty('args') // the email never round-trips back from the client
})
