// socketLifecycle.test.tsx — the leak test at the level the leak was reported.
//
// reconnectingSocket.test.ts pins the primitive. This file pins the three
// CONSUMERS the founder's console named, through their real public surface,
// with a stubbed global WebSocket — because the defect was never in the retry
// policy itself, it was in the wiring between a React effect and that policy.
// A hook can use a perfectly correct connection object and still leak it by
// forgetting to call stop().
//
// Every test has the same shape and it is the only shape that catches this:
//   mount → drive to the leaking state → UNMOUNT →
//   advance fake timers far past every retry deadline →
//   assert no socket was constructed and no timer is pending.
//
// Asserting that a cleanup function ran would not do. The original code HAD a
// cleanup function; it was an empty comment with a paragraph explaining why.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { useEffect } from 'react'
import { render, act } from '@testing-library/react'
import { usePeering, PEERING_PATH, Channel, type PeerFrame } from './usePeering'
import { useTelemetry, TELEMETRY_PATH } from './useTelemetry'
import { startNotificationBridge, NOTIFICATIONS_STREAM_PATH } from './notificationBridge'
import { MAX_DELAY_MS } from './reconnectingSocket'

// ---------------------------------------------------------------------------
// Global WebSocket stub. Sockets do NOT auto-open: connection refused is the
// interesting case (it is the case the founder's box was actually in), and
// leaving the transition explicit means no test passes on a timing accident.
// ---------------------------------------------------------------------------
class StubSocket {
  static built: StubSocket[] = []
  static reset() { StubSocket.built = [] }
  static withUrl(fragment: string) {
    return StubSocket.built.filter(s => s.url.includes(fragment))
  }

  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  url: string
  readyState = 0
  sent: string[] = []
  onopen: ((ev: unknown) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onclose: ((ev: unknown) => void) | null = null
  onerror: ((ev: unknown) => void) | null = null

  constructor(url: string) {
    this.url = url
    StubSocket.built.push(this)
  }

  open() { this.readyState = 1; this.onopen?.({}) }
  message(data: string) { this.onmessage?.({ data } as MessageEvent) }
  /** A refused upgrade, as a browser delivers it: error then close. */
  refuse() {
    this.onerror?.({})
    if (this.readyState !== 3) { this.readyState = 3; this.onclose?.({ code: 1006 }) }
  }
  send(d: string) { this.sent.push(d) }
  close() {
    if (this.readyState === 3) return
    this.readyState = 3
    this.onclose?.({ code: 1000, wasClean: true })
  }
}

/** jsdom defines WebSocket as a non-writable own property, so a plain
 *  assignment throws in strict mode. Redefine it instead. */
function defineGlobal(key: string, value: unknown) {
  Object.defineProperty(globalThis, key, { value, configurable: true, writable: true })
}

let realWebSocket: typeof WebSocket

beforeEach(() => {
  StubSocket.reset()
  realWebSocket = globalThis.WebSocket
  defineGlobal('WebSocket', StubSocket)
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
  defineGlobal('WebSocket', realWebSocket)
  vi.restoreAllMocks()
})

/** Far past every retry deadline the 30s-capped backoff can produce. */
function advancePastEveryRetry() {
  act(() => { vi.advanceTimersByTime(MAX_DELAY_MS * 20) })
}

function PeeringProbe() {
  const { connected, status } = usePeering()
  return <div data-testid="p">{status}:{String(connected)}</div>
}

function TelemetryProbe() {
  const { connected, status } = useTelemetry()
  return <div data-testid="t">{status}:{String(connected)}</div>
}

// ---------------------------------------------------------------------------

describe('usePeering — the reported leak', () => {
  it('opens exactly one socket to the peering stream on mount', () => {
    render(<PeeringProbe />)
    expect(StubSocket.withUrl(PEERING_PATH)).toHaveLength(1)
  })

  it('closes its socket on unmount', () => {
    const view = render(<PeeringProbe />)
    const ws = StubSocket.withUrl(PEERING_PATH)[0]
    act(() => ws.open())
    expect(ws.readyState).toBe(1)
    view.unmount()
    expect(ws.readyState).toBe(3)
  })

  it('LEAK: after unmount, the reconnect loop constructs no further socket, ever', () => {
    const view = render(<PeeringProbe />)
    const ws = StubSocket.withUrl(PEERING_PATH)[0]
    // Put it in the exact state the founder's box was in: the upgrade is
    // refused, so a reconnect is in flight when the component goes away.
    act(() => ws.refuse())
    expect(vi.getTimerCount()).toBeGreaterThan(0)

    view.unmount()

    // The pending retry handle is CLEARED, not merely neutered.
    expect(vi.getTimerCount()).toBe(0)
    advancePastEveryRetry()
    expect(StubSocket.withUrl(PEERING_PATH)).toHaveLength(1)
  })

  it('LEAK: ten mount/unmount cycles leave zero live sockets and zero timers', () => {
    // This is the accumulation the audit described: one socket, one timer and
    // one closure per mount, on a box whose advertised floor is 2 GB.
    for (let i = 0; i < 10; i++) {
      const view = render(<PeeringProbe />)
      act(() => StubSocket.withUrl(PEERING_PATH)[i].refuse())
      view.unmount()
    }
    expect(StubSocket.withUrl(PEERING_PATH)).toHaveLength(10)
    expect(StubSocket.withUrl(PEERING_PATH).filter(s => s.readyState !== 3)).toHaveLength(0)
    expect(vi.getTimerCount()).toBe(0)

    // And the loops stay dead — the assertion that the ORIGINAL code fails.
    advancePastEveryRetry()
    expect(StubSocket.withUrl(PEERING_PATH)).toHaveLength(10)
  })

  it('does not require any consumer to call close() — Messages.tsx never does', () => {
    // The hook's own return value is not touched here on purpose. The whole
    // point of the fix is that teardown is not the caller's job.
    const view = render(<PeeringProbe />)
    act(() => StubSocket.withUrl(PEERING_PATH)[0].refuse())
    view.unmount()
    advancePastEveryRetry()
    expect(StubSocket.withUrl(PEERING_PATH)).toHaveLength(1)
  })

  it('still reconnects while mounted (the fix must not disable the feature)', () => {
    render(<PeeringProbe />)
    act(() => StubSocket.withUrl(PEERING_PATH)[0].refuse())
    advancePastEveryRetry()
    expect(StubSocket.withUrl(PEERING_PATH).length).toBeGreaterThan(1)
  })

  it('still delivers frames to channel and wildcard subscribers', () => {
    const got: PeerFrame[] = []
    function Sub() {
      const { subscribe } = usePeering()
      useEffect(() => {
        const a = subscribe(Channel.MESSAGE, f => got.push(f))
        const b = subscribe('*', f => got.push(f))
        return () => { a(); b() }
      }, [subscribe])
      return null
    }
    render(<Sub />)
    const ws = StubSocket.withUrl(PEERING_PATH)[0]
    act(() => { ws.open(); ws.message(JSON.stringify({ channel: 'message', payload: { a: 1 } })) })
    expect(got).toHaveLength(2) // channel subscriber + wildcard subscriber
    expect(got[0].channel).toBe('message')
  })

  it('a subscriber that unsubscribes itself mid-dispatch does not break the others', () => {
    // Iterating the live Set would throw or skip here; the dispatcher copies.
    const order: string[] = []
    function Sub() {
      const { subscribe } = usePeering()
      useEffect(() => {
        const off = subscribe(Channel.MESSAGE, () => { order.push('one-shot'); off() })
        const b = subscribe(Channel.MESSAGE, () => order.push('second'))
        return () => { off(); b() }
      }, [subscribe])
      return null
    }
    render(<Sub />)
    const ws = StubSocket.withUrl(PEERING_PATH)[0]
    act(() => { ws.open(); ws.message(JSON.stringify({ channel: 'message' })) })
    expect(order).toEqual(['one-shot', 'second'])
    act(() => ws.message(JSON.stringify({ channel: 'message' })))
    expect(order).toEqual(['one-shot', 'second', 'second'])
  })

  it('a handler subscribed DURING a dispatch does not receive the frame in flight', () => {
    // Set iteration visits elements added while iterating, so dispatching over
    // the live Set would deliver the current frame to a listener that was not
    // registered when it arrived — and a handler that subscribes on every
    // frame would spin forever inside one dispatch. The copy is what stops it.
    const late: string[] = []
    function Sub() {
      const { subscribe } = usePeering()
      useEffect(() => subscribe(Channel.MESSAGE, () => {
        subscribe(Channel.MESSAGE, () => late.push('late'))
      }), [subscribe])
      return null
    }
    render(<Sub />)
    const ws = StubSocket.withUrl(PEERING_PATH)[0]
    act(() => { ws.open(); ws.message(JSON.stringify({ channel: 'message' })) })
    expect(late).toEqual([])
    // It is registered for the NEXT frame, though — one late handler per frame
    // seen so far, so the second frame reaches the one added by the first.
    act(() => ws.message(JSON.stringify({ channel: 'message' })))
    expect(late).toEqual(['late'])
  })

  it('reports unavailable rather than a permanent spinner when peering never answers', () => {
    const view = render(<PeeringProbe />)
    for (let i = 0; i < 4; i++) {
      const list = StubSocket.withUrl(PEERING_PATH)
      act(() => list[list.length - 1].refuse())
      advancePastEveryRetry()
    }
    expect(view.getByTestId('p').textContent).toBe('unavailable:false')
  })
})

describe('useTelemetry — the same defect, milder', () => {
  it('LEAK: a pending retry timer does not outlive unmount', () => {
    const view = render(<TelemetryProbe />)
    act(() => StubSocket.withUrl(TELEMETRY_PATH)[0].refuse())
    expect(vi.getTimerCount()).toBeGreaterThan(0)
    view.unmount()
    expect(vi.getTimerCount()).toBe(0)
    advancePastEveryRetry()
    expect(StubSocket.withUrl(TELEMETRY_PATH)).toHaveLength(1)
  })

  it('LEAK: ten mount/unmount cycles leave zero live sockets and zero timers', () => {
    for (let i = 0; i < 10; i++) {
      const view = render(<TelemetryProbe />)
      act(() => StubSocket.withUrl(TELEMETRY_PATH)[i].refuse())
      view.unmount()
    }
    expect(StubSocket.withUrl(TELEMETRY_PATH).filter(s => s.readyState !== 3)).toHaveLength(0)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('still surfaces stats and connected while mounted', () => {
    const view = render(<TelemetryProbe />)
    const ws = StubSocket.withUrl(TELEMETRY_PATH)[0]
    act(() => { ws.open(); ws.message(JSON.stringify({ cpu: 12 })) })
    expect(view.getByTestId('t').textContent).toBe('open:true')
  })

  it('reports unavailable when telemetry never answers on this box', () => {
    const view = render(<TelemetryProbe />)
    for (let i = 0; i < 4; i++) {
      const list = StubSocket.withUrl(TELEMETRY_PATH)
      act(() => list[list.length - 1].refuse())
      advancePastEveryRetry()
    }
    expect(view.getByTestId('t').textContent).toBe('unavailable:false')
  })
})

describe('notificationBridge — the same defect, and the only one that hammered', () => {
  it('LEAK: stop() clears the pending retry, which the fixed-3s loop could not', () => {
    // The old loop was `ws.onclose = () => { if (alive) setTimeout(open, 3000) }`
    // — no handle, no backoff. stop() closed the socket but left the timer.
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('offline'))
    const stop = startNotificationBridge()
    const ws = StubSocket.withUrl(NOTIFICATIONS_STREAM_PATH)[0]
    expect(ws).toBeDefined()
    ws.refuse()
    expect(vi.getTimerCount()).toBeGreaterThan(0)

    stop()

    expect(vi.getTimerCount()).toBe(0)
    vi.advanceTimersByTime(MAX_DELAY_MS * 20)
    expect(StubSocket.withUrl(NOTIFICATIONS_STREAM_PATH)).toHaveLength(1)
  })

  it('backs off instead of retrying every 3s for the life of the outage', () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('offline'))
    const stop = startNotificationBridge()
    try {
      // 60 seconds of a service that is simply not running. The old fixed-3s
      // loop makes 20 attempts in that window; 30s-capped backoff makes far
      // fewer, and that difference is the whole point on a 2 GB box.
      for (let i = 0; i < 60; i++) {
        const list = StubSocket.withUrl(NOTIFICATIONS_STREAM_PATH)
        const live = list[list.length - 1]
        if (live.readyState !== 3) live.refuse()
        vi.advanceTimersByTime(1000)
      }
      expect(StubSocket.withUrl(NOTIFICATIONS_STREAM_PATH).length).toBeLessThan(10)
    } finally {
      stop()
    }
  })
})
