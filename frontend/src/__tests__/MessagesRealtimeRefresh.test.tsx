import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { StrictMode } from 'react'
import { render, screen, cleanup, waitFor, act } from '@testing-library/react'

// The peering socket is replaced by a hand-held one so a frame can be delivered
// at a chosen moment. `subscribe` is module-level and therefore referentially
// stable, exactly like the real hook's useCallback — an unstable one would
// resubscribe every render and hide the effect churn under test.
type Handler = (frame: unknown) => void
const handlers: Record<string, Handler[]> = {}
function subscribe(channel: string, h: Handler) {
  ;(handlers[channel] ||= []).push(h)
  return () => { handlers[channel] = (handlers[channel] || []).filter(x => x !== h) }
}
vi.mock('../core/usePeering', () => ({
  Channel: { MESSAGE: 'message', SIGNAL: 'signal', COLLAB: 'collab', NOTIFICATION: 'notification', PRESENCE: 'presence' },
  usePeering: () => ({ connected: true, status: 'open', subscribe, send: () => {}, close: () => {} }),
}))

import Messages from '../builtin/peering/Messages'

const KNOWN = { conv_id: 'conv-alice', peer_name: 'Alice', last_activity: '2026-08-18T10:00:00Z', message_count: 2 }

let listFetches = 0
function jsonOk(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
}

beforeEach(() => {
  listFetches = 0
  for (const k of Object.keys(handlers)) delete handlers[k]
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    const u = String(url)
    if (/\/api\/peering\/conversations\/[^/]+\/messages/.test(u)) return jsonOk([])
    if (u.endsWith('/api/peering/conversations')) { listFetches++; return jsonOk([KNOWN]) }
    if (u.includes('/api/peering/identity')) return jsonOk({ vulos_id: 'vid-me' })
    return jsonOk({})
  }))
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

// StrictMode is the point of this file, not decoration: it double-invokes state
// updaters on purpose, so it is what tells a pure updater apart from one with a
// side effect in it. The app itself renders under StrictMode (src/main.tsx).
async function mountMessages() {
  render(<StrictMode><Messages /></StrictMode>)
  expect(await screen.findByText('Alice')).toBeTruthy()
  // Exactly one live subscription after StrictMode's mount/unmount/remount — if
  // this ever reads 2, every frame is delivered twice and the fetch counts
  // below would be measuring a subscription leak instead of the updater.
  expect(handlers.message?.length).toBe(1)
}

async function deliver(payload: Record<string, unknown>) {
  const live = [...(handlers.message || [])]
  await act(async () => {
    for (const h of live) h({ channel: 'message', from: 'vid-bob', payload })
    await Promise.resolve()
  })
}

describe('Messages — a message for an unknown conversation refreshes the list once', () => {
  it('issues exactly one conversation-list request for one incoming frame', async () => {
    await mountMessages()
    const before = listFetches

    await deliver({ id: 'm-new', conversation_id: 'conv-carol', body: 'hello', direction: 'in', timestamp: '2026-08-18T12:00:00Z' })

    // ONE logical update -> ONE request. This used to be two: the refresh was
    // called from inside the setConversations updater, which React double-
    // invokes under StrictMode. A duplicated GET is the mild version; anything
    // non-idempotent reached from a state updater is a real bug.
    await waitFor(() => expect(listFetches).toBe(before + 1))
    // And it stays one — no second request arrives a tick later.
    await act(async () => { await Promise.resolve() })
    expect(listFetches).toBe(before + 1)
  })

  it('does not refetch the list for a message in a conversation it already has', async () => {
    await mountMessages()
    const before = listFetches

    await deliver({ id: 'm-2', conversation_id: 'conv-alice', body: 'still here', direction: 'in', timestamp: '2026-08-18T12:05:00Z' })

    // A known conversation is patched in place; refetching it would be a
    // request per received message.
    expect(listFetches).toBe(before)
    // The behaviour the updater is actually for: the unread count bumps.
    expect(await screen.findByText('1')).toBeTruthy()
  })
})
