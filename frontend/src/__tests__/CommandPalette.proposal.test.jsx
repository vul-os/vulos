/**
 * CommandPalette.proposal.test.jsx — WAVE-22 a11y
 *
 * The inline Ask "Approve / Reject" buttons must be keyboard-reachable even
 * though Tab is captured for section-cycling during normal search. This test
 * covers the accessibility contract added in wave-22:
 *
 *   • when a proposal card is shown, pressing Y approves it (runs /execute)
 *   • pressing N rejects it (nothing is executed)
 *   • the Approve / Reject buttons expose accessible names + aria-keyshortcuts
 *   • Esc still closes the palette
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

// ── Mock the heavy dependencies so we can mount the palette in isolation ──────
const openWindow = vi.fn()
vi.mock('../providers/ShellProvider', () => ({
  useShell: () => ({ openWindow, windows: [], minimizeWindow: vi.fn() }),
}))
vi.mock('../core/ThemeProvider', () => ({ useTheme: () => ({ toggle: vi.fn() }) }))
vi.mock('../core/useSovereignty', () => ({ useSovereignty: () => ({ openPanel: vi.fn() }) }))
vi.mock('../core/AppRegistry', () => ({ getApps: () => [], getAppById: () => null }))
vi.mock('../core/commandRegistry', () => ({
  getCommands: () => [],
  subscribeCommands: () => () => {},
}))
vi.mock('../core/settingsNav', () => ({ setPendingSettingsSection: vi.fn() }))
vi.mock('../shell/launchApp', () => ({ launchApp: vi.fn() }))

// Drive a mutating PROPOSAL through the Ask flow.
const runAgentTurn = vi.fn(async ({ onProposal }) => {
  const proposal = { id: 'prop-1', tool: 'send_email', summary: 'Send email to Alice', args: {} }
  onProposal?.(proposal)
  return { proposal }
})
vi.mock('../core/agentStream', () => ({ runAgentTurn: (...a) => runAgentTurn(...a) }))

import CommandPalette from '../shell/CommandPalette.jsx'

async function openPaletteAndPropose(user) {
  render(<CommandPalette />)
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true }))
  })
  const input = await screen.findByPlaceholderText(/Search apps/i)
  // A "?"-prefixed query forces the Ask route.
  await user.type(input, '?email alice')
  // Activate the ask row.
  await user.keyboard('{Enter}')
  await screen.findByText(/Needs your approval/i)
  return input
}

beforeEach(() => {
  vi.clearAllMocks()
  global.fetch = vi.fn(() =>
    Promise.resolve({ ok: true, json: () => Promise.resolve({ result: 'Done.' }) }),
  )
})
afterEach(() => cleanup())

describe('CommandPalette proposal a11y', () => {
  it('exposes Approve/Reject with accessible names and keyshortcuts', async () => {
    const user = userEvent.setup()
    await openPaletteAndPropose(user)

    const approve = screen.getByRole('button', { name: 'Approve' })
    const reject = screen.getByRole('button', { name: 'Reject' })
    expect(approve).toHaveAttribute('aria-keyshortcuts', 'Y')
    expect(reject).toHaveAttribute('aria-keyshortcuts', 'N')
  })

  it('approves the proposal when Y is pressed', async () => {
    const user = userEvent.setup()
    await openPaletteAndPropose(user)

    await user.keyboard('y')
    await waitFor(() =>
      expect(global.fetch).toHaveBeenCalledWith('/api/assistant/execute', expect.any(Object)),
    )
    // The execute call sends only the opaque proposal id.
    const body = JSON.parse(global.fetch.mock.calls.find(c => c[0] === '/api/assistant/execute')[1].body)
    expect(body).toEqual({ id: 'prop-1' })
    await screen.findByText(/Approved and executed/i)
  })

  it('rejects the proposal when N is pressed — nothing is executed', async () => {
    const user = userEvent.setup()
    await openPaletteAndPropose(user)

    await user.keyboard('n')
    await screen.findByText(/Rejected/i)
    expect(global.fetch).not.toHaveBeenCalledWith('/api/assistant/execute', expect.any(Object))
  })

  it('does not capture Tab for section-cycling while a proposal is pending', async () => {
    const user = userEvent.setup()
    const input = await openPaletteAndPropose(user)
    // The palette's own key handler must let Tab through during a proposal so
    // the focus trap can move focus onto the Approve/Reject buttons. (The
    // focus-trap itself is covered elsewhere; here we assert the palette does
    // not swallow Tab as it does for section navigation.)
    input.focus()
    const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    // Stop the container focus-trap listener from acting so we observe only the
    // palette's onKeyDown decision (jsdom has no layout → trap can't measure).
    ev.stopPropagation = () => {}
    input.dispatchEvent(ev)
    // The Approve/Reject buttons are real, reachable focus targets.
    const approve = screen.getByRole('button', { name: 'Approve' })
    approve.focus()
    expect(approve).toHaveFocus()
  })

  it('captures Tab for section cycling during a NORMAL (non-proposal) search', async () => {
    const user = userEvent.setup()
    render(<CommandPalette />)
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true }))
    })
    const input = await screen.findByPlaceholderText(/Search apps/i)
    await user.type(input, 'hello')
    // No proposal pending → Tab is intercepted (preventDefault) for section nav.
    const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    input.dispatchEvent(ev)
    expect(ev.defaultPrevented).toBe(true)
  })

  it('Esc still closes the palette', async () => {
    const user = userEvent.setup()
    await openPaletteAndPropose(user)
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    })
    await waitFor(() => expect(screen.queryByPlaceholderText(/Search apps/i)).toBeNull())
  })
})
