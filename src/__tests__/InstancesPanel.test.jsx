import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import InstancesPanel from '../builtin/dashboard/InstancesPanel.jsx'

// The box serves the registry's own wire shape from GET /api/instances:
// an { instances: [...] } envelope of rows keyed by ulid/kind/status/role.
const REGISTRY_ROWS = [
  {
    ulid: '01HWZOWNER0000000000000001',
    display_name: 'This Box',
    kind: 'device',
    role: 'owner',
    status: 'online',
    last_seen_at: '2026-07-13T09:00:00Z',
  },
  {
    ulid: '01HWZPEER00000000000000002',
    display_name: 'Cloud Node',
    kind: 'cloud',
    role: 'peer',
    status: 'offline',
    last_seen_at: '2026-07-13T08:00:00Z',
  },
]

// mockBox records every call so a test can assert what the panel asked the box
// to do — a route that is never called is exactly the bug this panel had.
function mockBox({ rename, remove, storeOnly, rows } = {}) {
  const calls = []
  const registryRows = rows || REGISTRY_ROWS
  global.fetch = vi.fn((url, init = {}) => {
    const u = String(url)
    calls.push({ url: u, method: init.method || 'GET', body: init.body })

    if (u === '/api/instances') {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ instances: registryRows }) })
    }
    if (u === '/api/routing/apps') {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([]) })
    }
    if (u.endsWith('/rename')) {
      const res = rename || { ok: true, status: 200, body: { ulid: '01HWZPEER00000000000000002', display_name: 'Renamed' } }
      return Promise.resolve({ ok: res.ok, status: res.status, json: () => Promise.resolve(res.body) })
    }
    // NODE-CAP-01: match store-only by method+path BEFORE the generic prefix
    // branch, so a toggle isn't silently answered by the remove-shaped mock.
    if (u.endsWith('/store-only')) {
      const res = storeOnly || { ok: true, status: 200, body: { ulid: '01HWZPEER00000000000000002', store_only: true } }
      return Promise.resolve({ ok: res.ok, status: res.status, json: () => Promise.resolve(res.body) })
    }
    if (u.startsWith('/api/instances/')) {
      const res = remove || { ok: true, status: 200, body: { status: 'removed' } }
      return Promise.resolve({ ok: res.ok, status: res.status, json: () => Promise.resolve(res.body) })
    }
    throw new Error(`unexpected fetch: ${init.method || 'GET'} ${u}`)
  })
  return calls
}

afterEach(() => { cleanup(); vi.restoreAllMocks() })
beforeEach(() => { vi.useRealTimers() })

describe('InstancesPanel — fleet roster', () => {
  it('renders the registry wire shape (ulid/kind/status), not a guessed one', async () => {
    mockBox()
    render(<InstancesPanel />)

    expect(await screen.findByText('This Box')).toBeInTheDocument()
    expect(screen.getByText('Cloud Node')).toBeInTheDocument()
    // status → online/offline sections; kind → the type badge.
    expect(screen.getByText('Online (1)')).toBeInTheDocument()
    expect(screen.getByText('Offline (1)')).toBeInTheDocument()
    expect(screen.getByText('Cloud')).toBeInTheDocument()
  })

  it('offers Remove for a peer but never for the owner instance (the box itself)', async () => {
    mockBox()
    render(<InstancesPanel />)
    await screen.findByText('This Box')

    // Two cards, two Rename buttons — but only the peer may be removed.
    expect(screen.getAllByRole('button', { name: 'Rename' })).toHaveLength(2)
    expect(screen.getAllByRole('button', { name: 'Remove' })).toHaveLength(1)
  })

  it('has no "add device" affordance — the box registers no invite route', async () => {
    const calls = mockBox()
    render(<InstancesPanel />)
    await screen.findByText('This Box')

    expect(screen.queryByRole('button', { name: /add device/i })).not.toBeInTheDocument()
    expect(calls.some(c => c.url.includes('/invite'))).toBe(false)
  })
})

describe('InstancesPanel — rename', () => {
  it('PATCHes the box and only then updates the list', async () => {
    const user = userEvent.setup()
    const calls = mockBox()
    render(<InstancesPanel />)
    await screen.findByText('Cloud Node')

    await user.click(screen.getAllByRole('button', { name: 'Rename' })[1])
    await user.clear(screen.getByPlaceholderText('Instance name'))
    await user.type(screen.getByPlaceholderText('Instance name'), 'Renamed')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    const patch = calls.find(c => c.method === 'PATCH')
    expect(patch).toBeDefined()
    expect(patch.url).toBe('/api/instances/01HWZPEER00000000000000002/rename')
    expect(JSON.parse(patch.body)).toEqual({ display_name: 'Renamed' })

    expect(await screen.findByText('Renamed')).toBeInTheDocument()
  })

  it('shows the box’s refusal instead of silently closing', async () => {
    const user = userEvent.setup()
    mockBox({ rename: { ok: false, status: 403, body: { error: 'admin only' } } })
    render(<InstancesPanel />)
    await screen.findByText('Cloud Node')

    await user.click(screen.getAllByRole('button', { name: 'Rename' })[1])
    await user.click(screen.getByRole('button', { name: 'Save' }))

    expect(await screen.findByText('admin only')).toBeInTheDocument()
    // The refused name was NOT applied to the roster.
    expect(screen.getByText('Cloud Node')).toBeInTheDocument()
  })
})

// confirmRemove opens the confirm modal from the peer card's Remove button and
// clicks Remove INSIDE the modal (the card's button of the same name is not it).
async function confirmRemove(user) {
  await user.click(screen.getByRole('button', { name: 'Remove' }))
  const modal = (await screen.findByText('Remove Instance?')).closest('div')
  await user.click(within(modal).getByRole('button', { name: 'Remove' }))
}

describe('InstancesPanel — remove', () => {
  it('DELETEs the instance on the box before dropping it from the list', async () => {
    const user = userEvent.setup()
    const calls = mockBox()
    render(<InstancesPanel />)
    await screen.findByText('Cloud Node')

    await confirmRemove(user)

    await waitFor(() => {
      const del = calls.find(c => c.method === 'DELETE')
      expect(del).toBeDefined()
      expect(del.url).toBe('/api/instances/01HWZPEER00000000000000002')
    })
    await waitFor(() => expect(screen.queryByText('Cloud Node')).not.toBeInTheDocument())
  })

  it('keeps the instance listed when the box refuses — the button must not lie', async () => {
    const user = userEvent.setup()
    mockBox({ remove: { ok: false, status: 409, body: { error: 'cannot remove this instance' } } })
    render(<InstancesPanel />)
    await screen.findByText('Cloud Node')

    await confirmRemove(user)

    expect(await screen.findByText('cannot remove this instance')).toBeInTheDocument()
    // Still on the roster (the modal names it too, hence getAllByText).
    expect(screen.getAllByText('Cloud Node').length).toBeGreaterThan(0)
    expect(screen.getByText('Offline (1)')).toBeInTheDocument()
  })
})

describe('InstancesPanel — store-only (NODE-CAP-01)', () => {
  it('shows the Sync-only badge and a "Make serving" toggle for a store-only instance', async () => {
    mockBox({ rows: [
      REGISTRY_ROWS[0],
      { ...REGISTRY_ROWS[1], store_only: true },
    ] })
    render(<InstancesPanel />)
    await screen.findByText('Cloud Node')

    expect(screen.getByText('Sync-only')).toBeInTheDocument()
    // The store-only card's toggle offers to make it serve again (visible text
    // is the accessible name — WCAG 2.5.3).
    expect(screen.getByRole('button', { name: 'Make serving' })).toBeInTheDocument()
  })

  it('PATCHes /store-only and optimistically flips without waiting for a poll', async () => {
    const user = userEvent.setup()
    const calls = mockBox()
    render(<InstancesPanel />)
    await screen.findByText('Cloud Node')

    // Both default rows are serving → their toggles read "Make sync-only".
    // Index [1] is the offline peer (Cloud Node).
    const toggles = screen.getAllByRole('button', { name: 'Make sync-only' })
    await user.click(toggles[1])

    const patch = calls.find(c => c.method === 'PATCH' && c.url.endsWith('/store-only'))
    expect(patch).toBeDefined()
    expect(patch.url).toBe('/api/instances/01HWZPEER00000000000000002/store-only')
    expect(JSON.parse(patch.body)).toEqual({ store_only: true })

    // Optimistic flip: the badge appears immediately (before any 10s poll).
    expect(await screen.findByText('Sync-only')).toBeInTheDocument()
  })

  it('does not flip and surfaces the error when the box refuses', async () => {
    const user = userEvent.setup()
    mockBox({ storeOnly: { ok: false, status: 403, body: { error: 'admin only' } } })
    render(<InstancesPanel />)
    await screen.findByText('Cloud Node')

    const toggles = screen.getAllByRole('button', { name: 'Make sync-only' })
    await user.click(toggles[1])

    expect(await screen.findByText('admin only')).toBeInTheDocument()
    // The refused toggle did NOT apply — no Sync-only badge appeared.
    expect(screen.queryByText('Sync-only')).not.toBeInTheDocument()
  })

  // NOTE: the poll-vs-optimistic overlay (pendingStoreOnlyRef in loadData) is
  // verified by code review rather than an RTL test — driving the 10s setInterval
  // under fake timers alongside async fetch flushing proved too brittle to be a
  // reliable guard. The overlay is small and self-contained; if it grows, extract
  // the merge into a pure helper and unit-test that directly.
})
