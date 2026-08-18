import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent, act } from '@testing-library/react'

// ThreadView is driven directly, WITHOUT a React `key`. A key at the call site
// would reset its state on every conversation change and hide the whole class of
// bug under test — the component has to be correct standing alone.
import { ThreadView } from '../builtin/peering/Messages'

// Two peers. Nothing here is shared between them — that is the whole point:
// anything from `ALICE` that survives a switch to `BOB` is another peer's
// message content shown under Bob's name.
const ALICE = { id: 'conv-alice', peer_id: 'vid-alice', peer_name: 'Alice', last_message: null, unread_count: 0 }
const BOB = { id: 'conv-bob', peer_id: 'vid-bob', peer_name: 'Bob', last_message: null, unread_count: 0 }

const ALICE_MSGS = [{ id: 'm1', body: 'the safe combination is 4815', direction: 'in', timestamp: '2026-08-18T10:00:00Z' }]
const BOB_MSGS = [{ id: 'm2', body: 'lunch at one?', direction: 'in', timestamp: '2026-08-18T11:00:00Z' }]

// Per-conversation control over WHEN each history request resolves, so a
// thread switch can be observed mid-flight rather than only after it settles.
function mockHistory(handlers: Record<string, () => Promise<unknown>>) {
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    const m = /\/api\/peering\/conversations\/([^/]+)\/messages/.exec(String(url))
    if (m && handlers[m[1]]) {
      return handlers[m[1]]().then(body => ({ ok: true, status: 200, json: () => Promise.resolve(body) }))
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([]) })
  }))
}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('Messages thread switch — one peer’s history must never render under another peer’s name', () => {
  it('does not show the previous conversation’s messages while the next one is still loading', async () => {
    let releaseBob: () => void = () => {}
    mockHistory({
      'conv-alice': () => Promise.resolve(ALICE_MSGS),
      'conv-bob': () => new Promise(resolve => { releaseBob = () => resolve(BOB_MSGS) }),
    })

    const { rerender } = render(<ThreadView conversation={ALICE} myVulosId="me" />)
    expect(await screen.findByText(/safe combination/i)).toBeInTheDocument()

    rerender(<ThreadView conversation={BOB} myVulosId="me" />)

    // The header switched to Bob immediately — it reads straight off the prop.
    // The list below it must not still be Alice's.
    expect(screen.getByText('Bob')).toBeInTheDocument()
    expect(screen.queryByText(/safe combination/i)).toBeNull()

    await act(async () => { releaseBob() })
    expect(await screen.findByText(/lunch at one/i)).toBeInTheDocument()
  })

  it('does not let a slow request for the abandoned conversation overwrite the one on screen', async () => {
    let releaseAlice: () => void = () => {}
    mockHistory({
      'conv-alice': () => new Promise(resolve => { releaseAlice = () => resolve(ALICE_MSGS) }),
      'conv-bob': () => Promise.resolve(BOB_MSGS),
    })

    const { rerender } = render(<ThreadView conversation={ALICE} myVulosId="me" />)
    rerender(<ThreadView conversation={BOB} myVulosId="me" />)
    expect(await screen.findByText(/lunch at one/i)).toBeInTheDocument()

    // Alice's request finally lands, long after the user moved on.
    await act(async () => { releaseAlice() })
    await waitFor(() => expect(screen.getByText(/lunch at one/i)).toBeInTheDocument())
    expect(screen.queryByText(/safe combination/i)).toBeNull()
  })

  it('does not carry a half-typed message from one peer into the next', async () => {
    mockHistory({
      'conv-alice': () => Promise.resolve(ALICE_MSGS),
      'conv-bob': () => Promise.resolve(BOB_MSGS),
    })

    const { rerender } = render(<ThreadView conversation={ALICE} myVulosId="me" />)
    await screen.findByText(/safe combination/i)

    const box = screen.getByPlaceholderText(/Enter to send/i)
    fireEvent.change(box, { target: { value: 'the spare key is under the mat' } })
    expect(box).toHaveValue('the spare key is under the mat')

    rerender(<ThreadView conversation={BOB} myVulosId="me" />)
    await screen.findByText(/lunch at one/i)

    // A draft written for Alice sitting in Bob's composer is one Enter away
    // from being sent to Bob.
    expect(screen.getByPlaceholderText(/Enter to send/i)).toHaveValue('')
  })
})
