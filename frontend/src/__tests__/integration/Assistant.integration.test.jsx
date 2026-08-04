/**
 * Assistant.integration.test.jsx — wave-28 shell integration suite.
 *
 * Renders the REAL assistant panel (src/builtin/assistant/Assistant.jsx) wired
 * to the REAL agentStream SSE client with MSW as the backend. Exercises:
 *   • the empty state + tier status fetch,
 *   • a plain ask → streamed answer,
 *   • a mutating turn → proposal card → Approve (posts only {id}) / Reject (no-op).
 *
 * This mirrors the command-palette security assertion inside the standalone
 * assistant surface, where the proposal lives as a chat message.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server, http, HttpResponse, sse } from './msw/server.js'

import Assistant from '../../builtin/assistant/Assistant.jsx'

const PROPOSAL = { id: 'prop_asst_77', tool: 'send_email', summary: 'Send email to Bob', args: { to: 'bob@x.io', body: 'Hi Bob' } }

beforeEach(() => vi.clearAllMocks())
afterEach(() => cleanup())

async function ask(user, text) {
  const box = await screen.findByLabelText('Ask about your mail')
  await user.type(box, text)
  await user.click(screen.getByRole('button', { name: 'Send' }))
}

describe('Assistant panel (integration)', () => {
  it('shows the on-device privacy promise in the empty state', async () => {
    render(<Assistant />)
    expect(await screen.findByText(/stays on your own server/i)).toBeInTheDocument()
  })

  it('streams a plain answer from the mocked SSE endpoint', async () => {
    server.use(http.post('/api/assistant/agent/stream', () => sse([
      { type: 'token', content: 'Your next meeting ' },
      { type: 'token', content: 'is at 3pm.' },
      { type: 'done' },
    ])))
    const user = userEvent.setup()
    render(<Assistant />)
    await ask(user, 'when is my meeting?')
    expect(await screen.findByText(/Your next meeting is at 3pm\./)).toBeInTheDocument()
  })

  it('renders the read-only tool trace (steps) when the stream reports tool use', async () => {
    server.use(http.post('/api/assistant/agent/stream', () => sse([
      { type: 'status', tool: 'search_mail', content: 'using search_mail…' },
      { type: 'status', tool: 'read_thread', content: 'using read_thread…' },
      { type: 'token', content: 'You owe $128.40.' },
      { type: 'done' },
    ])))
    const user = userEvent.setup()
    render(<Assistant />)
    await ask(user, 'when is my invoice due?')
    // The answer renders…
    expect(await screen.findByText(/You owe \$128\.40\./)).toBeInTheDocument()
    // …and so does the transparency trace of the read-only tools it ran.
    expect(await screen.findByText('2 steps')).toBeInTheDocument()
    expect(screen.getByText('Searched mail')).toBeInTheDocument()
    expect(screen.getByText('Read a thread')).toBeInTheDocument()
  })

  it('a mutating turn renders a proposal with Approve/Reject', async () => {
    server.use(http.post('/api/assistant/agent/stream', () => sse([
      { type: 'proposal', proposal: PROPOSAL }, { type: 'done' },
    ])))
    const user = userEvent.setup()
    render(<Assistant />)
    await ask(user, 'email bob')
    expect(await screen.findByText('Send email to Bob')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Approve' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reject' })).toBeInTheDocument()
  })

  it('WAVE-13: Approve posts ONLY the opaque id; the answer shows the server result', async () => {
    let executeBody = 'NOT_CALLED'
    server.use(
      http.post('/api/assistant/agent/stream', () => sse([{ type: 'proposal', proposal: PROPOSAL }, { type: 'done' }])),
      http.post('/api/assistant/execute', async ({ request }) => {
        executeBody = await request.json()
        return HttpResponse.json({ result: 'Sent to Bob.' })
      }),
    )
    const user = userEvent.setup()
    render(<Assistant />)
    await ask(user, 'email bob')
    await screen.findByText('Send email to Bob')

    await user.click(screen.getByRole('button', { name: 'Approve' }))

    await waitFor(() => expect(executeBody).not.toBe('NOT_CALLED'))
    expect(executeBody).toEqual({ id: 'prop_asst_77' })
    expect(executeBody).not.toHaveProperty('args')
    expect(await screen.findByText(/Sent to Bob\./)).toBeInTheDocument()
  })

  it('WAVE-13: Reject sends NOTHING to /api/assistant/execute', async () => {
    let executeCalled = false
    server.use(
      http.post('/api/assistant/agent/stream', () => sse([{ type: 'proposal', proposal: PROPOSAL }, { type: 'done' }])),
      http.post('/api/assistant/execute', () => { executeCalled = true; return HttpResponse.json({ result: 'x' }) }),
    )
    const user = userEvent.setup()
    render(<Assistant />)
    await ask(user, 'email bob')
    await screen.findByText('Send email to Bob')

    await user.click(screen.getByRole('button', { name: 'Reject' }))

    expect(await screen.findByText(/Rejected — nothing was done/i)).toBeInTheDocument()
    await new Promise(r => setTimeout(r, 50))
    expect(executeCalled).toBe(false)
  })
})
