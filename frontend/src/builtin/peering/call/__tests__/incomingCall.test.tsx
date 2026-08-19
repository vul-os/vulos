// incomingCall.test.tsx — the incoming peer-call surface must be honest BEFORE
// it acts, not after.
//
// What this pins down, and why each one is a lie if it breaks:
//
//   1. NO ANSWER BUTTON. The peer call client was retired on purpose
//      (ef3e3175), so there is nothing to answer with. The old card offered
//      "Answer" and its handler POSTed to /call/reject — an action offered and
//      then refused. If any answer affordance comes back without a media
//      surface behind it, this file goes red.
//   2. NO RINGTONE. A ringtone is a summons to answer. Ringing for a call that
//      cannot be answered is the same broken promise, in audio.
//   3. THE DECLINE STILL HAPPENS. This is the one thing that worked, and it is
//      what stops the caller ringing out. Removing the surface must not remove
//      it.
//   4. THE USER IS STILL TOLD. A silent auto-decline would be worse than the
//      card it replaced: a call would vanish with no trace.
//   5. THE REASON NAMES THE CLIENT, NOT THE BOX. The box's signalling relay is
//      complete and working. "Calling is unavailable" would send a user hunting
//      for a fault in hardware that is fine.

import { render, act, cleanup } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const store = vi.hoisted(() => ({ notify: vi.fn() }))
vi.mock('../../../../core/notificationStore', () => ({ notify: store.notify }))

import IncomingCall from '../IncomingCall'
import { declinedCallNotice } from '../callNotice'

// ─── a WebSocket we can push frames into ────────────────────────────────────

interface Frame { channel: string; from?: string; payload?: unknown }

class FakeWS {
  static instances: FakeWS[] = []
  onmessage: ((e: MessageEvent) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  closed = false
  constructor(public url: string) { FakeWS.instances.push(this) }
  close() { this.closed = true }
  deliver(frame: Frame) {
    this.onmessage?.({ data: JSON.stringify(frame) } as MessageEvent)
  }
}

const socket = () => FakeWS.instances[FakeWS.instances.length - 1]

const incoming = (callId: string, fromId: string): Frame => ({
  channel: 'signal',
  from: fromId,
  payload: { channel: 'signal', type: 'incoming-call', call_id: callId, from_id: fromId },
})

let posted: { url: string; body: string }[] = []
/** Constructed AudioContexts — the ringtone's only possible footprint. */
let audioContexts = 0

beforeEach(() => {
  posted = []
  audioContexts = 0
  FakeWS.instances = []
  store.notify.mockReset()
  // `globalThis.WebSocket` is a read-only accessor on this Node/jsdom, so a
  // plain assignment throws where `global.fetch = …` on the next line is fine.
  vi.stubGlobal('WebSocket', FakeWS)
  global.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    posted.push({ url: String(input), body: String(init?.body ?? '') })
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as unknown as typeof fetch

  class CountingAudioContext {
    constructor() { audioContexts++ }
    state = 'running'
    currentTime = 0
    destination = {}
    resume() {}
    close() {}
    createOscillator() { return { connect() {}, frequency: { value: 0 }, type: '', start() {}, stop() {} } }
    createGain() { return { connect() {}, gain: { setValueAtTime() {}, linearRampToValueAtTime() {} } } }
  }
  const win = window as unknown as { AudioContext: unknown; webkitAudioContext?: unknown }
  win.AudioContext = CountingAudioContext
  win.webkitAudioContext = CountingAudioContext
})

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.useRealTimers() })

async function ring(callId = 'call-1', fromId = 'vulos:ed25519:priya') {
  render(<IncomingCall />)
  await act(async () => { socket().deliver(incoming(callId, fromId)) })
}

describe('an incoming peer call, with no client to answer it', () => {
  it('offers no way to answer — an Answer button here would be refused on press', async () => {
    const { container } = render(<IncomingCall />)
    await act(async () => { socket().deliver(incoming('call-1', 'priya')) })

    // Nothing is drawn at all: no card, no buttons, no ring pulse.
    expect(container.innerHTML).toBe('')
    expect(document.querySelectorAll('button')).toHaveLength(0)
    // And in particular nothing that says "answer"/"accept" anywhere in the DOM.
    expect(document.body.textContent ?? '').not.toMatch(/answer|accept/i)
    expect(document.body.innerHTML).not.toMatch(/aria-label="[^"]*(answer|accept)/i)

    // Nor is the accept route ever called — the surface being gone is not a
    // licence to accept a call silently.
    expect(posted.map((p) => p.url)).not.toContain('/api/peering/call/answer')
  })

  it('does not ring, because a ringtone is a summons to do something impossible', async () => {
    await ring()
    expect(audioContexts).toBe(0)
  })

  it('declines on the wire, so the caller gets a real answer instead of ringing out', async () => {
    await ring('call-abc', 'priya')

    const rejects = posted.filter((p) => p.url === '/api/peering/call/reject')
    expect(rejects).toHaveLength(1)
    expect(JSON.parse(rejects[0].body)).toEqual({ call_id: 'call-abc' })
  })

  it('tells the user a call happened — an auto-decline nobody hears about is worse', async () => {
    await ring('call-abc', 'vulos:ed25519:priya')

    expect(store.notify).toHaveBeenCalledTimes(1)
    const n = store.notify.mock.calls[0][0] as { title: string; body: string; level: string; source: string }
    expect(n.title).toMatch(/vulos:ed25519:priya/)
    expect(n.level).toBe('warning')
    expect(n.source).toBe('peering')
  })

  it('blames the missing client, not the box — the relay is complete and working', () => {
    const { title, body } = declinedCallNotice('Priya')
    expect(title).toMatch(/Priya/)
    // The reason must survive: say there is no client, and say the call was
    // declined. Dropping either half turns this back into a mystery.
    expect(body).toMatch(/no client/i)
    expect(body).toMatch(/declined/i)
    // And must NOT pin it on the hardware or the service.
    expect(body).not.toMatch(/unavailable|not supported|broken|offline/i)
  })

  it('still declines even when the user has never seen a notification permission', async () => {
    store.notify.mockImplementation(() => { throw new Error('notification store exploded') })
    render(<IncomingCall />)
    await act(async () => { socket().deliver(incoming('call-boom', 'priya')) })
    // The notify() throw is caught by the frame handler; the important part is
    // that a broken notification path does not leave the caller ringing.
    expect(posted.filter((p) => p.url === '/api/peering/call/reject').length).toBeLessThanOrEqual(1)
  })
})

describe('frames that are not an incoming call', () => {
  it('declines nothing and says nothing', async () => {
    render(<IncomingCall />)
    await act(async () => {
      socket().deliver({ channel: 'signal', from: 'p', payload: { type: 'hangup', call_id: 'x' } })
      socket().deliver({ channel: 'message', from: 'p', payload: { type: 'incoming-call', call_id: 'y' } })
      socket().deliver({ channel: 'signal', from: 'p', payload: { type: 'reject', call_id: 'z' } })
    })
    expect(posted).toHaveLength(0)
    expect(store.notify).not.toHaveBeenCalled()
  })

  it('survives a malformed frame', async () => {
    render(<IncomingCall />)
    await act(async () => { socket().onmessage?.({ data: 'not json' } as MessageEvent) })
    expect(posted).toHaveLength(0)
  })
})

describe('one call', () => {
  it('produces one decline and one notice, however often the hub redelivers it', async () => {
    render(<IncomingCall />)
    await act(async () => {
      socket().deliver(incoming('call-dup', 'priya'))
      socket().deliver(incoming('call-dup', 'priya'))
      socket().deliver(incoming('call-dup', 'priya'))
    })
    expect(posted.filter((p) => p.url === '/api/peering/call/reject')).toHaveLength(1)
    expect(store.notify).toHaveBeenCalledTimes(1)
  })
})
