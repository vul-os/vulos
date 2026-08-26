import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

vi.mock('../lib/stepup', () => ({ requireStepUp: () => Promise.resolve() }))

import DomainPanel from '../core/settings/DomainPanel'

// DomainPanel had NO tests. "No published apps yet." is accompanied by
// instructions to go and publish one — bad advice for someone who already has.
function mockApps(body: unknown, ok = true, status = 200) {
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    const u = String(url)
    if (u.startsWith('/api/apps/visibility')) {
      return Promise.resolve({ ok, status, json: () => Promise.resolve(body) })
    }
    return Promise.resolve({ ok: true, status: 404, json: () => Promise.resolve({}) })
  }))
}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('DomainPanel — the publishable-app list', () => {
  it('does NOT say "No published apps yet" when the reply was not a list', async () => {
    mockApps({ not: 'a list' })
    render(<DomainPanel />)
    await waitFor(() =>
      expect(screen.getByText(/did not return an app list/i)).toBeInTheDocument())

    expect(screen.queryByText(/No published apps yet/i)).not.toBeInTheDocument()
  })

  it('says "No published apps yet" when the box really did report none', async () => {
    mockApps([])
    render(<DomainPanel />)
    await waitFor(() =>
      expect(screen.getByText(/No published apps yet/i)).toBeInTheDocument())

    expect(screen.queryByText(/did not return an app list/i)).not.toBeInTheDocument()
  })

  it('offers a public app the box did report', async () => {
    mockApps([{ app_id: 'notes', name: 'Notes', visibility: 'public' }])
    render(<DomainPanel />)
    await waitFor(() =>
      expect(screen.queryByText(/No published apps yet/i)).not.toBeInTheDocument())

    expect(screen.queryByText(/did not return an app list/i)).not.toBeInTheDocument()
  })
})
