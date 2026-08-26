import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

import SecurityPanel from '../core/settings/SecurityPanel'

// SecurityPanel had NO tests. These pin the two defects found auditing it, and
// both are negative assertions on purpose: the point is not that some element
// appears, it is that a specific FALSE SENTENCE does not.
//
// The mechanism (SETTINGS-AUDIT.md, and settings-honesty.e2e.ts for the panels
// inside Settings.tsx): a 200 whose body is missing a field passes isRecord,
// the narrower keeps none of the fields it wanted, and every `x || []` gate
// downstream cannot tell "the box said none" from "the box said nothing".

function mockFeed(body: unknown, ok = true, status = 200) {
  vi.stubGlobal('fetch', vi.fn(() =>
    Promise.resolve({ ok, status, json: () => Promise.resolve(body) })))
}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('SecurityPanel — what it says when the box did not say', () => {
  it('does NOT claim "Nothing recorded yet" when the reply carried no actions list', async () => {
    // A 200 that is a record but has no `actions` key: the exact "answered, but
    // told you nothing" shape. Before the fix this printed a clean bill of health.
    mockFeed({})
    render(<SecurityPanel />)
    await waitFor(() => expect(screen.queryByText(/Loading/)).not.toBeInTheDocument())

    expect(screen.queryByText(/Nothing recorded yet/i)).not.toBeInTheDocument()
    expect(screen.getByText(/did not report any activity list/i)).toBeInTheDocument()
  })

  it('says "Nothing recorded yet" only when the box explicitly reported an empty list', async () => {
    mockFeed({ actions: [], alerts: [] })
    render(<SecurityPanel />)
    await waitFor(() => expect(screen.queryByText(/Loading/)).not.toBeInTheDocument())

    expect(screen.getByText(/Nothing recorded yet/i)).toBeInTheDocument()
    expect(screen.queryByText(/did not report any activity list/i)).not.toBeInTheDocument()
  })

  it('warns that an absent alerts list is not evidence that no alerts were raised', async () => {
    mockFeed({ actions: [] })
    render(<SecurityPanel />)
    await waitFor(() => expect(screen.queryByText(/Loading/)).not.toBeInTheDocument())

    expect(screen.getByText(/not\s+evidence that none were raised/i)).toBeInTheDocument()
  })

  it('does NOT label an alert with no status as "Dismissed", and does not drop it either', async () => {
    // status omitted. It is not pending, so it used to fall into `resolved` and
    // render as "Dismissed" — an alert nobody had seen, reported as cleared.
    mockFeed({
      actions: [],
      alerts: [{ id: 'a1', action: 'password_change', ts: '2026-08-26T10:00:00Z' }],
    })
    render(<SecurityPanel />)
    await waitFor(() => expect(screen.queryByText(/Loading/)).not.toBeInTheDocument())

    expect(screen.queryByText(/Dismissed/i)).not.toBeInTheDocument()
    // ...and it must still be visible. Narrowing the resolved filter without
    // rendering these would hide a security alert entirely, which is worse.
    expect(screen.getByText(/unrecognised state/i)).toBeInTheDocument()
    expect(screen.getByText(/no status reported/i)).toBeInTheDocument()
    expect(screen.getByText(/Password changed/i)).toBeInTheDocument()
  })

  it('still labels genuinely resolved alerts', async () => {
    mockFeed({
      actions: [],
      alerts: [
        { id: 'd1', action: 'password_change', status: 'dismissed', ts: '2026-08-26T10:00:00Z' },
        { id: 'l1', action: 'role_change', status: 'locked', ts: '2026-08-26T11:00:00Z' },
      ],
    })
    render(<SecurityPanel />)
    await waitFor(() => expect(screen.queryByText(/Loading/)).not.toBeInTheDocument())

    expect(screen.getByText(/Dismissed/i)).toBeInTheDocument()
    expect(screen.getByText(/Account locked/i)).toBeInTheDocument()
    expect(screen.queryByText(/unrecognised state/i)).not.toBeInTheDocument()
  })

  it('surfaces a transport failure instead of rendering an empty feed', async () => {
    mockFeed({ error: 'boom' }, false, 500)
    render(<SecurityPanel />)
    await waitFor(() => expect(screen.queryByText(/Loading/)).not.toBeInTheDocument())

    expect(screen.queryByText(/Nothing recorded yet/i)).not.toBeInTheDocument()
    expect(screen.getByText(/HTTP 500/)).toBeInTheDocument()
  })
})
