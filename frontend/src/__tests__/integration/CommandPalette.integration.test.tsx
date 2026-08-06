/**
 * CommandPalette.integration.test.tsx — wave-28 shell integration suite.
 *
 * Renders the REAL command palette wired to the REAL agentStream SSE client,
 * with MSW answering /api endpoints as a fake backend. This is an integration
 * test, not a unit test: the ⌘K → search → Ask → proposal → approve/reject path
 * runs through the shipping code, and the network is the only mocked seam.
 *
 * The load-bearing assertion is the WAVE-13 security contract at the UI level:
 *   • Approve posts ONLY the opaque proposal id to /api/assistant/execute
 *     (never client-supplied args — a forged proposal can't smuggle actions).
 *   • Reject sends NOTHING to the network.
 *
 * ShellProvider is mocked (it starts websockets/native polling); everything
 * else — the palette, fuzzy search, askRouting, agentStream, and the SSE
 * transport over MSW — is real.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server, http, HttpResponse, sse } from './msw/server.js'
import { pressCmdK } from './helpers.jsx'

// ── Mock only the shell context; agentStream + network stay real. ─────────────
const openWindow = vi.fn()
vi.mock('../../providers/ShellProvider', () => ({
  useShell: () => ({ openWindow, windows: [], minimizeWindow: vi.fn() }),
}))
vi.mock('../../core/ThemeProvider', () => ({ useTheme: () => ({ toggle: vi.fn() }) }))
vi.mock('../../core/useSovereignty', () => ({ useSovereignty: () => ({ openPanel: vi.fn() }) }))
// Give the palette a couple of real apps to fuzzy-search.
vi.mock('../../core/AppRegistry', () => ({
  getApps: () => [
    { id: 'terminal', name: 'Terminal', icon: '>_', keywords: ['shell', 'console'], description: 'Command line' },
    { id: 'drive', name: 'Drive', icon: '☁', keywords: ['files', 'storage'], description: 'Your files' },
  ],
  getAppById: (id: string) => ({ terminal: { id: 'terminal', name: 'Terminal' }, drive: { id: 'drive', name: 'Drive' } }[id] || null),
}))
vi.mock('../../shell/launchApp', () => ({ launchApp: vi.fn() }))

import CommandPalette from '../../shell/CommandPalette.jsx'

// A proposal SSE frame the palette must render as "Needs your approval".
const PROPOSAL = { id: 'prop_opaque_9f2', tool: 'send_email', summary: 'Send email to Dana', args: { to: 'dana@acme.io', body: 'Hi Dana' } }

beforeEach(() => vi.clearAllMocks())
afterEach(() => cleanup())

async function openPalette() {
  render(<CommandPalette />)
  await act(async () => { pressCmdK() })
  return screen.findByPlaceholderText(/Search apps/i)
}

describe('CommandPalette — open + fuzzy app search (integration)', () => {
  it('opens on ⌘K and fuzzy-matches apps by keyword', async () => {
    const user = userEvent.setup()
    const input = await openPalette()
    await user.type(input, 'consol') // fuzzy → Terminal (keyword "console")
    expect(await screen.findByText('Terminal')).toBeInTheDocument()
  })

  it('live mail-search renders results from the mocked /api/mail/search', async () => {
    server.use(http.get('/api/mail/search', () =>
      HttpResponse.json({ messages: [{ uid: 'm1', subject: 'Invoice #42', from_name: 'Billing' }] })))
    const user = userEvent.setup()
    const input = await openPalette()
    await user.type(input, 'invoice')
    expect(await screen.findByText('Invoice #42')).toBeInTheDocument()
  })
})

describe('CommandPalette — Ask → assistant (integration, real SSE over MSW)', () => {
  it('streams a plain answer token-by-token', async () => {
    server.use(http.post('/api/assistant/agent/stream', () => sse([
      { type: 'token', content: 'You owe ' },
      { type: 'token', content: '$128.40.' },
      { type: 'done' },
    ])))
    const user = userEvent.setup()
    const input = await openPalette()
    await user.type(input, '?how much do I owe')
    await user.keyboard('{Enter}')
    expect(await screen.findByText(/You owe \$128\.40\./)).toBeInTheDocument()
  })

  it('a mutating turn surfaces a proposal card awaiting approval', async () => {
    server.use(http.post('/api/assistant/agent/stream', () => sse([
      { type: 'proposal', proposal: PROPOSAL },
      { type: 'done' },
    ])))
    const user = userEvent.setup()
    const input = await openPalette()
    await user.type(input, '?email dana')
    await user.keyboard('{Enter}')
    expect(await screen.findByText(/Needs your approval/i)).toBeInTheDocument()
    expect(screen.getByText('Send email to Dana')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Approve' })).toHaveAttribute('aria-keyshortcuts', 'Y')
    expect(screen.getByRole('button', { name: 'Reject' })).toHaveAttribute('aria-keyshortcuts', 'N')
  })

  it('WAVE-13: Approve posts ONLY the opaque proposal id to /api/assistant/execute', async () => {
    let executeBody: unknown = 'NOT_CALLED'
    server.use(
      http.post('/api/assistant/agent/stream', () => sse([{ type: 'proposal', proposal: PROPOSAL }, { type: 'done' }])),
      http.post('/api/assistant/execute', async ({ request }) => {
        executeBody = await request.json()
        return HttpResponse.json({ result: 'Message delivered to Dana.' })
      }),
    )
    const user = userEvent.setup()
    const input = await openPalette()
    await user.type(input, '?email dana')
    await user.keyboard('{Enter}')
    await screen.findByText(/Needs your approval/i)

    await user.click(screen.getByRole('button', { name: 'Approve' }))

    await waitFor(() => expect(executeBody).not.toBe('NOT_CALLED'))
    // The wave-13 contract: the request body is EXACTLY the opaque id — no args,
    // no summary, no tool. The server runs the args it stored, not the client's.
    expect(executeBody).toEqual({ id: 'prop_opaque_9f2' })
    expect(executeBody).not.toHaveProperty('args')
    expect(executeBody).not.toHaveProperty('tool')
    // The card confirms completion once the execute resolves.
    expect(await screen.findByText(/✓ Approved and executed/i)).toBeInTheDocument()
    expect(screen.getByText(/Message delivered to Dana/i)).toBeInTheDocument()
  })

  it('WAVE-13: Reject sends NOTHING to the network', async () => {
    let executeCalled = false
    server.use(
      http.post('/api/assistant/agent/stream', () => sse([{ type: 'proposal', proposal: PROPOSAL }, { type: 'done' }])),
      http.post('/api/assistant/execute', () => { executeCalled = true; return HttpResponse.json({ result: 'x' }) }),
    )
    const user = userEvent.setup()
    const input = await openPalette()
    await user.type(input, '?email dana')
    await user.keyboard('{Enter}')
    await screen.findByText(/Needs your approval/i)

    await user.click(screen.getByRole('button', { name: 'Reject' }))

    expect(await screen.findByText(/Rejected — nothing was done/i)).toBeInTheDocument()
    // Give any (erroneous) request a chance to fire, then assert none did.
    await new Promise(r => setTimeout(r, 50))
    expect(executeCalled).toBe(false)
  })
})
