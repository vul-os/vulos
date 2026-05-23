// App.test.jsx — vitest tests for MailApp no-identity empty state (Medium #7).
//
// Tests that:
//   1. When /api/identity/identity returns no identity, the no-identity CTA
//      renders without crashing.
//   2. The setup CTA button/link is visible.
//   3. Compose is not reachable (no compose button in the empty state).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, act } from '@testing-library/react'
import MailApp from './App.jsx'

// ─── fetch mock helpers ───────────────────────────────────────────────────────

function mockIdentityFetch(identityResponse) {
  vi.stubGlobal('fetch', vi.fn((url) => {
    if (url.includes('/api/identity/identity')) {
      return Promise.resolve({
        ok: identityResponse !== null,
        json: () => Promise.resolve(identityResponse),
      })
    }
    // mailbox fetch — return empty list
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve([]),
    })
  }))
}

beforeEach(() => {
  // Default: identity endpoint returns 404 / null
  mockIdentityFetch(null)
})

afterEach(() => {
  vi.restoreAllMocks()
})

// ─── Tests ────────────────────────────────────────────────────────────────────

describe('MailApp no-identity empty state', () => {
  it('renders the no-identity empty state without crashing when identity is null', async () => {
    mockIdentityFetch(null)

    await act(async () => {
      render(<MailApp />)
    })

    // The empty state container must be in the DOM.
    const container = await screen.findByTestId('mail-no-identity')
    expect(container).toBeTruthy()
  })

  it('shows the setup CTA when there is no identity', async () => {
    mockIdentityFetch(null)

    await act(async () => {
      render(<MailApp />)
    })

    const cta = await screen.findByTestId('mail-setup-cta')
    expect(cta).toBeTruthy()
  })

  it('shows "Set up your Vulos Mail" heading in no-identity state', async () => {
    mockIdentityFetch(null)

    await act(async () => {
      render(<MailApp />)
    })

    expect(await screen.findByText(/Set up your Vulos Mail/i)).toBeTruthy()
  })

  it('does not show the compose button when there is no identity', async () => {
    mockIdentityFetch(null)

    await act(async () => {
      render(<MailApp />)
    })

    // The main compose button in the Sidebar should NOT be rendered.
    // (The empty state replaces the whole UI, so compose is unavailable.)
    expect(screen.queryByText(/compose/i)).toBeFalsy()
  })
})
