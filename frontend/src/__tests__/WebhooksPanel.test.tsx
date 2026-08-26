import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

vi.mock('../lib/stepup', () => ({ requireStepUp: () => Promise.resolve() }))

import WebhooksPanel from '../core/settings/WebhooksPanel'

// WebhooksPanel had NO tests. A webhook list is a statement about what is wired
// to send data OFF this box, so "none configured" is not a sentence to guess.
function mockRoutes(routes: Record<string, { ok?: boolean, status?: number, body: unknown }>) {
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    const u = String(url)
    for (const [prefix, r] of Object.entries(routes)) {
      if (u.startsWith(prefix)) {
        return Promise.resolve({ ok: r.ok ?? true, status: r.status ?? 200, json: () => Promise.resolve(r.body) })
      }
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  }))
}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('WebhooksPanel — the subscription list', () => {
  it('does NOT say "No webhooks configured yet" when the reply carried no subscriptions key', async () => {
    mockRoutes({
      '/api/webhooks/topics': { body: { topics: ['file.created'] } },
      '/api/webhooks': { body: {} }, // 200, but no `subscriptions`
    })
    render(<WebhooksPanel />)
    await waitFor(() =>
      expect(screen.getByText(/did not return a webhook list/i)).toBeInTheDocument())

    expect(screen.queryByText(/No webhooks configured yet/i)).not.toBeInTheDocument()
    // and it must not sit on a spinner forever either
    expect(screen.queryByText(/^Loading…$/)).not.toBeInTheDocument()
  })

  it('says "No webhooks configured yet" when the box really did report none', async () => {
    mockRoutes({
      '/api/webhooks/topics': { body: { topics: ['file.created'] } },
      '/api/webhooks': { body: { subscriptions: [] } },
    })
    render(<WebhooksPanel />)
    await waitFor(() =>
      expect(screen.getByText(/No webhooks configured yet/i)).toBeInTheDocument())

    expect(screen.queryByText(/did not return a webhook list/i)).not.toBeInTheDocument()
  })

  it('lists a subscription the box did report', async () => {
    mockRoutes({
      '/api/webhooks/topics': { body: { topics: ['file.created'] } },
      '/api/webhooks': {
        body: { subscriptions: [{ id: 's1', url: 'https://example.test/hook', topics: ['file.created'], active: true }] },
      },
    })
    render(<WebhooksPanel />)
    await waitFor(() =>
      expect(screen.getByText(/example\.test\/hook/)).toBeInTheDocument())

    expect(screen.queryByText(/No webhooks configured yet/i)).not.toBeInTheDocument()
  })
})
