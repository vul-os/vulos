// reconnectingSocket.test.ts — the leak tests.
//
// A leak test has to OBSERVE the leak, not assert that a cleanup function was
// called. Every test here works the same way: drive the connection into the
// state that leaks, tear it down, then advance fake timers well past every
// retry deadline and assert that NOTHING further happened — no new socket was
// constructed, no timer is pending, no handler fired.
//
// The construction counter is a module-level array of every fake socket ever
// built, so "no new socket" is a count comparison across the teardown, which
// is the only form that can catch a socket opened one tick later.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  openReconnectingSocket,
  backoffDelay,
  BASE_DELAY_MS,
  MAX_DELAY_MS,
  UNAVAILABLE_AFTER,
  type SocketStatus,
} from './reconnectingSocket'

// ---------------------------------------------------------------------------
// A WebSocket fake that records every instance and lets a test drive the
// lifecycle by hand. Deliberately NOT auto-opening: every transition in these
// tests is explicit, so a test cannot pass by accident of timing.
// ---------------------------------------------------------------------------
class FakeSocket {
  static built: FakeSocket[] = []
  static reset() { FakeSocket.built = [] }

  url: string
  readyState = 0 // CONNECTING
  closeCalls = 0
  sent: string[] = []
  onopen: ((ev: unknown) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onclose: ((ev: unknown) => void) | null = null
  onerror: ((ev: unknown) => void) | null = null

  constructor(url: string) {
    this.url = url
    FakeSocket.built.push(this)
  }

  /** Simulate the server accepting the upgrade. */
  open() {
    this.readyState = 1
    this.onopen?.({})
  }

  /** Simulate the peer/server dropping the connection. */
  drop(code = 1006) {
    if (this.readyState === 3) return
    this.readyState = 3
    this.onclose?.({ code, wasClean: false })
  }

  /** Simulate a refused upgrade: error then close, as browsers do. */
  refuse() {
    this.onerror?.({})
    this.drop(1006)
  }

  message(data: string) {
    this.onmessage?.({ data } as MessageEvent)
  }

  send(data: string) {
    this.sent.push(data)
  }

  close() {
    this.closeCalls += 1
    if (this.readyState === 3) return
    this.readyState = 3
    this.onclose?.({ code: 1000, wasClean: true })
  }
}

const factory = (url: string) => new FakeSocket(url) as unknown as WebSocket
const latest = () => FakeSocket.built[FakeSocket.built.length - 1]

// Deterministic jitter: rand()=1 makes backoffDelay return exactly the nominal
// delay, so a test can advance by a known amount instead of guessing.
const noJitter = () => 1

beforeEach(() => {
  FakeSocket.reset()
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

/** Advance far past any conceivable retry deadline. */
function advanceWellPastEveryRetry() {
  vi.advanceTimersByTime(MAX_DELAY_MS * 20)
}

describe('openReconnectingSocket — stop() is total', () => {
  it('closes the live socket and constructs nothing more, forever', () => {
    const h = openReconnectingSocket('ws://box/api/x', {}, { socketFactory: factory, random: noJitter })
    expect(FakeSocket.built).toHaveLength(1)
    latest().open()

    const builtBeforeStop = FakeSocket.built.length
    h.stop()

    expect(FakeSocket.built[0].closeCalls).toBe(1)
    // THE LEAK ASSERTION: nothing new is ever constructed after teardown.
    advanceWellPastEveryRetry()
    expect(FakeSocket.built).toHaveLength(builtBeforeStop)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('clears a PENDING retry timer — the handle the old code never stored', () => {
    const h = openReconnectingSocket('ws://box/api/x', {}, { socketFactory: factory, random: noJitter })
    // Fail the first attempt so a retry is scheduled and in flight.
    latest().refuse()
    expect(vi.getTimerCount()).toBe(1)
    const builtBeforeStop = FakeSocket.built.length

    h.stop()

    // The timer is GONE, not merely neutered. A retry callback that still runs
    // and returns early is still a closure held alive to its deadline.
    expect(vi.getTimerCount()).toBe(0)
    advanceWellPastEveryRetry()
    expect(FakeSocket.built).toHaveLength(builtBeforeStop)
  })

  it('a socket that closes AFTER stop() cannot schedule a reconnect', () => {
    const h = openReconnectingSocket('ws://box/api/x', {}, { socketFactory: factory, random: noJitter })
    const ws = latest()
    ws.open()
    h.stop()

    // The box drops the (already abandoned) socket a moment later. If stop()
    // had only cleared the timer and not latched + detached, this would
    // restart the whole retry loop from a dead component.
    ws.onclose?.({ code: 1006, wasClean: false })
    advanceWellPastEveryRetry()
    expect(FakeSocket.built).toHaveLength(1)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('delivers no messages and no status after stop()', () => {
    const onMessage = vi.fn()
    const onStatus = vi.fn()
    const h = openReconnectingSocket('ws://box/api/x', { onMessage, onStatus }, { socketFactory: factory, random: noJitter })
    const ws = latest()
    ws.open()
    onStatus.mockClear()
    h.stop()

    ws.message('{"hello":1}')
    ws.drop()
    advanceWellPastEveryRetry()

    expect(onMessage).not.toHaveBeenCalled()
    expect(onStatus).not.toHaveBeenCalled()
  })

  it('detaches every handler off the abandoned socket', () => {
    // Structural, on purpose. `alive` alone would make the handlers HARMLESS,
    // but a socket the browser still holds with four live closures attached is
    // still four closures retained until GC gets to the socket. Detaching is
    // what actually drops the references, so it is asserted directly rather
    // than only through its effects (which the alive latch masks).
    const h = openReconnectingSocket('ws://box/api/x', { onMessage: () => {}, onStatus: () => {} }, { socketFactory: factory, random: noJitter })
    const ws = latest()
    ws.open()
    expect(ws.onclose).not.toBeNull()
    h.stop()
    expect(ws.onopen).toBeNull()
    expect(ws.onmessage).toBeNull()
    expect(ws.onclose).toBeNull()
    expect(ws.onerror).toBeNull()
  })

  it('is idempotent — a second stop() closes nothing twice and starts nothing', () => {
    const h = openReconnectingSocket('ws://box/api/x', {}, { socketFactory: factory, random: noJitter })
    latest().open()
    h.stop()
    h.stop()
    h.stop()
    expect(FakeSocket.built[0].closeCalls).toBe(1)
    advanceWellPastEveryRetry()
    expect(FakeSocket.built).toHaveLength(1)
  })

  it('send() after stop() is a no-op that reports failure rather than throwing', () => {
    const h = openReconnectingSocket('ws://box/api/x', {}, { socketFactory: factory, random: noJitter })
    latest().open()
    expect(h.send('a')).toBe(true)
    h.stop()
    expect(h.send('b')).toBe(false)
  })
})

describe('openReconnectingSocket — reconnect while alive', () => {
  it('does reconnect after a drop (the leak fix must not kill the feature)', () => {
    openReconnectingSocket('ws://box/api/x', {}, { socketFactory: factory, random: noJitter })
    latest().open()
    latest().drop()
    expect(FakeSocket.built).toHaveLength(1)
    vi.advanceTimersByTime(BASE_DELAY_MS)
    expect(FakeSocket.built).toHaveLength(2)
  })

  it('never runs two sockets at once across a long outage', () => {
    openReconnectingSocket('ws://box/api/x', {}, { socketFactory: factory, random: noJitter })
    for (let i = 0; i < 8; i++) {
      latest().refuse()
      // One pending retry, never a stack of them.
      expect(vi.getTimerCount()).toBe(1)
      vi.advanceTimersByTime(MAX_DELAY_MS)
    }
    const live = FakeSocket.built.filter(s => s.readyState !== 3)
    expect(live).toHaveLength(1)
  })

  it('a stale socket firing close a second time cannot displace the live one', () => {
    // Real path, not a hypothetical: onerror calls close(), which fires
    // onclose; a browser that ALSO delivers its own close event gives the
    // abandoned socket a second onclose after its replacement is already open.
    // Without the `current !== ws` identity guard that second event nulls the
    // live socket and schedules a retry on top of it — two live sockets, which
    // is precisely the accumulation this module exists to prevent.
    openReconnectingSocket('ws://box/api/x', {}, { socketFactory: factory, random: noJitter })
    const first = latest()
    first.refuse()
    vi.advanceTimersByTime(BASE_DELAY_MS)
    const second = latest()
    second.open()
    expect(FakeSocket.built).toHaveLength(2)

    // The stale socket's duplicate close event.
    first.onclose?.({ code: 1006, wasClean: false })

    expect(vi.getTimerCount()).toBe(0)
    advanceWellPastEveryRetry()
    expect(FakeSocket.built).toHaveLength(2)
    expect(FakeSocket.built.filter(s => s.readyState !== 3)).toHaveLength(1)
    expect(second.readyState).toBe(1)
  })

  it('resets the backoff after a successful open', () => {
    openReconnectingSocket('ws://box/api/x', {}, { socketFactory: factory, random: noJitter })
    latest().refuse()
    vi.advanceTimersByTime(BASE_DELAY_MS)         // attempt 2 at 1s
    latest().refuse()
    vi.advanceTimersByTime(BASE_DELAY_MS * 2)     // attempt 3 at 2s
    const beforeOpen = FakeSocket.built.length
    latest().open()
    latest().drop()
    // Back to the base delay, not the escalated one.
    vi.advanceTimersByTime(BASE_DELAY_MS)
    expect(FakeSocket.built.length).toBe(beforeOpen + 1)
  })

  it('a constructor that throws still schedules a retry instead of dying silently', () => {
    let calls = 0
    const throwOnce = (url: string) => {
      calls += 1
      if (calls === 1) throw new Error('blocked scheme')
      return new FakeSocket(url) as unknown as WebSocket
    }
    vi.spyOn(console, 'error').mockImplementation(() => {})
    openReconnectingSocket('ws://box/api/x', {}, { socketFactory: throwOnce, random: noJitter })
    expect(FakeSocket.built).toHaveLength(0)
    expect(vi.getTimerCount()).toBe(1)
    vi.advanceTimersByTime(BASE_DELAY_MS)
    expect(FakeSocket.built).toHaveLength(1)
  })
})

describe('openReconnectingSocket — unavailable vs failed', () => {
  it('reports `unavailable` after UNAVAILABLE_AFTER never-opened failures', () => {
    const seen: SocketStatus[] = []
    openReconnectingSocket('ws://box/api/x', { onStatus: s => seen.push(s) }, { socketFactory: factory, random: noJitter })
    for (let i = 0; i < UNAVAILABLE_AFTER; i++) {
      latest().refuse()
      vi.advanceTimersByTime(MAX_DELAY_MS)
    }
    expect(seen).toContain('unavailable')
  })

  it('a socket that opened then dropped is `reconnecting`, not `unavailable`', () => {
    const seen: SocketStatus[] = []
    const h = openReconnectingSocket('ws://box/api/x', { onStatus: s => seen.push(s) }, { socketFactory: factory, random: noJitter })
    latest().open()
    expect(h.status()).toBe('open')
    latest().drop()
    expect(h.status()).toBe('reconnecting')
    expect(seen).not.toContain('unavailable')
  })

  it('recovers to `open` when the service comes back', () => {
    const h = openReconnectingSocket('ws://box/api/x', {}, { socketFactory: factory, random: noJitter })
    for (let i = 0; i < UNAVAILABLE_AFTER; i++) {
      latest().refuse()
      vi.advanceTimersByTime(MAX_DELAY_MS)
    }
    expect(h.status()).toBe('unavailable')
    latest().open()
    expect(h.status()).toBe('open')
  })
})

describe('backoffDelay', () => {
  it('grows exponentially from the base and is capped', () => {
    const d = (n: number) => backoffDelay(n, { random: noJitter })
    expect(d(0)).toBe(BASE_DELAY_MS)
    expect(d(1)).toBe(BASE_DELAY_MS * 2)
    expect(d(2)).toBe(BASE_DELAY_MS * 4)
    expect(d(50)).toBe(MAX_DELAY_MS)
  })

  it('never returns less than half the nominal delay (equal jitter has a floor)', () => {
    // Full jitter would allow ~0 here, reintroducing the tight retry loop.
    for (let n = 0; n < 12; n++) {
      const lo = backoffDelay(n, { random: () => 0 })
      const nominal = Math.min(BASE_DELAY_MS * 2 ** n, MAX_DELAY_MS)
      expect(lo).toBe(nominal / 2)
      expect(lo).toBeGreaterThanOrEqual(nominal / 2)
    }
  })

  it('actually varies — two draws from a real random source differ', () => {
    const draws = new Set(Array.from({ length: 40 }, () => backoffDelay(6)))
    // Without jitter every draw would be identical and this set would be size 1.
    expect(draws.size).toBeGreaterThan(1)
  })

  it('a huge attempt index does not overflow to Infinity', () => {
    expect(Number.isFinite(backoffDelay(5000, { random: noJitter }))).toBe(true)
    expect(backoffDelay(5000, { random: noJitter })).toBe(MAX_DELAY_MS)
  })
})
