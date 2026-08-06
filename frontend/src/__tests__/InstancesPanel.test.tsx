import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'

import InstancesPanel from '../builtin/dashboard/InstancesPanel.jsx'

interface InstanceRow {
  ulid: string
  display_name: string
  kind: string
  role: string
  status: string
  last_seen_at: string
  store_only?: boolean
}

interface MockResult {
  ok: boolean
  status: number
  body: unknown
}

interface FetchCall {
  url: string
  method: string
  body: unknown
}

// The box serves the registry's own wire shape from GET /api/instances:
// an { instances: [...] } envelope of rows keyed by ulid/kind/status/role.
const REGISTRY_ROWS: InstanceRow[] = [
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
function mockBox({ rename, remove, storeOnly, rows }: {
  rename?: MockResult
  remove?: MockResult
  storeOnly?: MockResult
  rows?: InstanceRow[]
} = {}): FetchCall[] {
  const calls: FetchCall[] = []
  const registryRows = rows || REGISTRY_ROWS
  vi.stubGlobal('fetch', vi.fn((url: string, init: RequestInit = {}) => {
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
  }))
  return calls
}

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals() })
beforeEach(() => { vi.useRealTimers() })

function expectCall(call: FetchCall | undefined): FetchCall {
  if (!call) throw new Error('expected a matching fetch call')
  return call
}
function parseBody(body: unknown): unknown {
  if (typeof body !== 'string') throw new Error('expected a JSON string body')
  return JSON.parse(body)
}

// sectionCount returns the count badge text for a roster section heading
// ("Online" / "Offline"). The heading renders the label and the count as two
// sibling elements, so there is no single "Online (1)" text node to match.
// Returns null when the section is not rendered at all.
function sectionCount(label: string): string | null {
  const heading = screen.queryAllByRole('heading')
    .find(h => (h.textContent || '').trim().startsWith(label))
  if (!heading) return null
  return (heading.textContent || '').trim().slice(label.length).trim()
}

describe('InstancesPanel — fleet roster', () => {
  it('renders the registry wire shape (ulid/kind/status), not a guessed one', async () => {
    mockBox()
    render(<InstancesPanel />)

    expect(await screen.findByText('This Box')).toBeInTheDocument()
    expect(screen.getByText('Cloud Node')).toBeInTheDocument()
    // status → online/offline sections; kind → the type badge.
    // The section heading renders the label and its count as two elements
    // ("Online" + a count badge), so assert on the heading's combined text
    // rather than a literal "Online (1)" string.
    expect(sectionCount('Online')).toBe('1')
    expect(sectionCount('Offline')).toBe('1')
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

    const patch = expectCall(calls.find(c => c.method === 'PATCH'))
    expect(patch.url).toBe('/api/instances/01HWZPEER00000000000000002/rename')
    expect(parseBody(patch.body)).toEqual({ display_name: 'Renamed' })

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
//
// The modal heading is "Remove instance?" — it is composed of a decorative
// aria-hidden marker span plus the text, so match on the heading element's
// normalised text content.
async function confirmRemove(user: UserEvent) {
  await user.click(screen.getByRole('button', { name: 'Remove' }))
  const heading = await screen.findByText(
    (_: string, el: Element | null) => el?.textContent?.trim() === 'Remove instance?',
  )
  const modal = heading.closest('div')
  if (!modal) throw new Error('expected the remove-confirm heading to be inside a modal div')
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
      const del = expectCall(calls.find(c => c.method === 'DELETE'))
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
    expect(sectionCount('Offline')).toBe('1')
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

    const patch = expectCall(calls.find(c => c.method === 'PATCH' && c.url.endsWith('/store-only')))
    expect(patch.url).toBe('/api/instances/01HWZPEER00000000000000002/store-only')
    expect(parseBody(patch.body)).toEqual({ store_only: true })

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
