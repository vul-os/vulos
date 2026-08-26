import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

vi.mock('../lib/stepup', () => ({ requireStepUp: () => Promise.resolve() }))

import DeveloperPanel from '../core/settings/DeveloperPanel'

// DeveloperPanel had NO tests. This pins the one that matters on a credentials
// screen: believing you hold no API keys when the box never said so.
function mockKeys(body: unknown, ok = true, status = 200) {
  vi.stubGlobal('fetch', vi.fn(() =>
    Promise.resolve({ ok, status, json: () => Promise.resolve(body) })))
}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('DeveloperPanel — the key list', () => {
  it('does NOT say "No API keys yet" when the reply was not a list', async () => {
    // A 200 that is a record rather than an array. `Array.isArray(d) ? … : []`
    // used to turn that into a confident "you have none".
    mockKeys({ unexpected: 'shape' })
    render(<DeveloperPanel />)
    await waitFor(() => expect(screen.queryByText(/^Loading…$/)).not.toBeInTheDocument())

    expect(screen.queryByText(/No API keys yet/i)).not.toBeInTheDocument()
    expect(screen.getByText(/did not return a list of keys/i)).toBeInTheDocument()
  })

  it('says "No API keys yet" when the box really did return an empty list', async () => {
    mockKeys([])
    render(<DeveloperPanel />)
    await waitFor(() => expect(screen.queryByText(/^Loading…$/)).not.toBeInTheDocument())

    expect(screen.getByText(/No API keys yet/i)).toBeInTheDocument()
  })

  it('lists the keys the box did return', async () => {
    mockKeys([{ id: 'k1', name: 'CI token', created_at: '2026-08-26T10:00:00Z' }])
    render(<DeveloperPanel />)
    await waitFor(() => expect(screen.queryByText(/^Loading…$/)).not.toBeInTheDocument())

    expect(screen.getByText(/CI token/)).toBeInTheDocument()
    expect(screen.queryByText(/No API keys yet/i)).not.toBeInTheDocument()
  })
})
