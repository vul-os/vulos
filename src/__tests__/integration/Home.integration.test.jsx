/**
 * Home.integration.test.jsx — wave-49 shell integration suite.
 *
 * Renders the REAL assistant Home surface (src/shell/home/Home.jsx) wired to the
 * REAL agentStream SSE client, with MSW answering /api endpoints. Exercises the
 * newer wave-11/40/48 surfaces: the aggregated brief/agenda/invites payload, the
 * always-there composer (streamed answer), and the wave-40 invite RSVP flow that
 * must route through the agent as a LEDGER-GATED proposal (never auto-executed).
 *
 * ShellProvider is mocked (it starts websockets/native polling); everything else
 * — Home, agentStream, askRouting, and the SSE transport over MSW — is real.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server, http, HttpResponse, sse } from './msw/server.js'

const openWindow = vi.fn()
const setLaunchpad = vi.fn()
vi.mock('../../providers/ShellProvider', () => ({
  useShell: () => ({ openWindow, setLaunchpad }),
}))
// Home reads the shared AppRegistry for quick-launch tiles + openApp.
vi.mock('../../core/AppRegistry', () => ({
  getAppById: (id) => ({ id, name: id, icon: '▦', url: `/app/${id}/` }),
}))

import Home from '../../shell/home/Home.jsx'

const INVITE = {
  message_uid: 'inv_1',
  subject: 'Team sync',
  from: 'lead@acme.io',
  invite: { summary: 'Team sync', organizer: 'lead@acme.io', start: '2999-01-01T15:00:00Z' },
}

const HOME_PAYLOAD = {
  greeting: 'Good evening, Ada',
  brief: 'Two threads need a reply; your 3pm moved.',
  focus: [],
  agenda: [],
  agenda_fresh: true,
  invites: [INVITE],
  activity: [],
  sovereignty: { tier: 'local', label: 'On your device' },
}

function homeHandler(payload = HOME_PAYLOAD) {
  return http.get('/api/assistant/home', () => HttpResponse.json(payload))
}

beforeEach(() => vi.clearAllMocks())
afterEach(() => cleanup())

describe('Home (integration)', () => {
  it('renders the aggregated brief + greeting from /api/assistant/home', async () => {
    server.use(homeHandler())
    render(<Home />)
    expect(await screen.findByText('Good evening, Ada')).toBeInTheDocument()
    expect(await screen.findByText(/Two threads need a reply/)).toBeInTheDocument()
  })

  it('shows an honest offline state when the home endpoint is unreachable', async () => {
    server.use(http.get('/api/assistant/home', () => new HttpResponse(null, { status: 503 })))
    render(<Home />)
    expect(await screen.findByText(/Assistant offline/i)).toBeInTheDocument()
  })

  it('streams a plain answer from the always-there composer', async () => {
    server.use(
      homeHandler(),
      http.post('/api/assistant/agent/stream', () => sse([
        { type: 'token', content: 'Your 3pm ' },
        { type: 'token', content: 'moved to 4pm.' },
        { type: 'done' },
      ])),
    )
    const user = userEvent.setup()
    render(<Home />)
    const box = await screen.findByLabelText('Ask your assistant')
    await user.type(box, 'what changed?')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    expect(await screen.findByText(/Your 3pm moved to 4pm\./)).toBeInTheDocument()
  })

  it('WAVE-40/13: RSVP to an invite routes through the agent as a proposal (never auto-execute)', async () => {
    let executeCalled = false
    server.use(
      homeHandler(),
      http.post('/api/assistant/agent/stream', () => sse([
        { type: 'proposal', proposal: { id: 'prop_rsvp_1', tool: 'rsvp_invite', summary: 'RSVP accept to Team sync', args: {} } },
        { type: 'done' },
      ])),
      http.post('/api/assistant/execute', () => { executeCalled = true; return HttpResponse.json({ result: 'RSVP sent.' }) }),
    )
    const user = userEvent.setup()
    render(<Home />)
    // The invite surfaces with its RSVP affordances.
    await screen.findByText('Invites awaiting your response')
    await user.click(screen.getByRole('button', { name: 'Accept' }))

    // A ledger-gated proposal appears — nothing executed yet.
    expect(await screen.findByText(/Needs your approval · RSVP to invite/i)).toBeInTheDocument()
    expect(executeCalled).toBe(false)

    // Approving posts ONLY the opaque id.
    let executeBody = 'NOT_CALLED'
    server.use(http.post('/api/assistant/execute', async ({ request }) => {
      executeBody = await request.json()
      return HttpResponse.json({ result: 'RSVP sent.' })
    }))
    await user.click(screen.getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(executeBody).not.toBe('NOT_CALLED'))
    expect(executeBody).toEqual({ id: 'prop_rsvp_1' })
    expect(executeBody).not.toHaveProperty('args')
  })

  it('the streamed assistant answer is a polite live region, but the user turn is not', async () => {
    server.use(
      homeHandler(),
      http.post('/api/assistant/agent/stream', () => sse([
        { type: 'token', content: 'Done.' }, { type: 'done' },
      ])),
    )
    const user = userEvent.setup()
    render(<Home />)
    const box = await screen.findByLabelText('Ask your assistant')
    await user.type(box, 'hi')
    await user.click(screen.getByRole('button', { name: 'Send' }))
    const answer = await screen.findByText('Done.')
    // The assistant bubble announces; the user's echoed text does not.
    expect(answer).toHaveAttribute('aria-live', 'polite')
    const userBubble = screen.getByText('hi')
    expect(userBubble).not.toHaveAttribute('aria-live')
  })
})
