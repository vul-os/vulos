// harness.tsx — shared rig for the built-in app conformance matrix.
//
// The matrix asks four questions of every builtin, and this file exists so each
// answer is measured the same way for all of them:
//
//   1. mounts            — renders without throwing / no unhandled rejection
//   2. real content      — the app's OWN surface is on screen, not a spinner
//   3. empty state       — no data produces a deliberate message, not a blank
//   4. backend down      — 500s and dead sockets leave a readable window
//
// Only the network boundary is faked. `installFetch` replaces global.fetch
// (which also bypasses MSW — a reassigned global.fetch wins over MSW's patch,
// see vitest.config.js), and `installWebSocket` replaces global.WebSocket. Every
// layer above those is the shipping component.
//
// This file is deliberately NOT named *.test.* so vitest does not collect it.

import { act } from '@testing-library/react'
import { vi, expect } from 'vitest'

// ---------------------------------------------------------------------------
// fetch
// ---------------------------------------------------------------------------

export interface RouteSpec {
  status?: number
  /** JSON body. Ignored when `text` is set. */
  body?: unknown
  /** Raw text body, for endpoints the app reads with res.text(). */
  text?: string
  /** Reject the fetch promise instead of answering — a network-level failure. */
  networkError?: boolean
  /**
   * Never settle. This is the "slow box" case, and it is the only way to
   * observe an app's first-paint loading state: with a mock that resolves on
   * the next microtask, most of these apps have already swapped to their real
   * surface before the first assertion runs, so a loading state that is a blank
   * rectangle would never be noticed.
   */
  hang?: boolean
}

export type RouteHandler = RouteSpec | ((url: URL, init: RequestInit | undefined) => RouteSpec)

export interface FetchRig {
  /** Every request seen, as "METHOD /pathname" (no query). */
  calls: string[]
  /** Full URLs seen, query included. */
  urls: string[]
  restore: () => void
}

function specToResponse(spec: RouteSpec): Response {
  const status = spec.status ?? 200
  const payload = spec.text !== undefined ? spec.text : JSON.stringify(spec.body ?? {})
  const contentType = spec.text !== undefined ? 'text/plain' : 'application/json'
  return new Response(payload, { status, headers: { 'Content-Type': contentType } })
}

/**
 * Replace global.fetch with a table-driven fake.
 *
 * Keys are "METHOD /pathname" (e.g. "POST /api/exec") or a bare "/pathname",
 * which matches any method. Query strings are never part of the key.
 *
 * Anything not in the table falls through to `fallback` — which defaults to an
 * empty 200 object so an unlisted endpoint is quiet rather than hung. Pass
 * `{ fallback: { status: 500 } }` to drive the "backend is down" case.
 */
export function installFetch(
  table: Record<string, RouteHandler> = {},
  opts: { fallback?: RouteSpec } = {},
): FetchRig {
  const original = global.fetch
  const calls: string[] = []
  const urls: string[] = []
  const fallback = opts.fallback ?? { body: {} }

  const impl = (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    const url = new URL(raw, 'http://localhost')
    const method = (init?.method ?? 'GET').toUpperCase()
    calls.push(`${method} ${url.pathname}`)
    urls.push(raw)

    let handler = table[`${method} ${url.pathname}`] ?? table[url.pathname]
    if (handler === undefined) handler = fallback
    const spec = typeof handler === 'function' ? handler(url, init) : handler

    if (spec.networkError) return Promise.reject(new TypeError('Failed to fetch'))
    if (spec.hang) return new Promise<Response>(() => { /* never settles */ })
    return Promise.resolve(specToResponse(spec))
  }

  global.fetch = vi.fn(impl) as unknown as typeof fetch
  return { calls, urls, restore: () => { global.fetch = original } }
}

/** Every request 500s — the shape of a box whose backend has fallen over. */
export function installFailingFetch(): FetchRig {
  return installFetch({}, { fallback: { status: 500, body: { error: 'backend unavailable' } } })
}

// ---------------------------------------------------------------------------
// WebSocket
// ---------------------------------------------------------------------------

export type SocketMode = 'open' | 'fail'

/**
 * A WebSocket stand-in with two behaviours:
 *
 *   'open' — connects on the next tick and stays open.
 *   'fail' — fires onerror then onclose on the next tick, which is what a
 *            browser does when the server refuses the upgrade. This is the
 *            exact condition the founder's console reported for
 *            /api/telemetry, /api/peering/stream and /api/pty.
 */
export class FakeWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  static instances: FakeWebSocket[] = []
  static mode: SocketMode = 'open'

  readonly CONNECTING = 0
  readonly OPEN = 1
  readonly CLOSING = 2
  readonly CLOSED = 3

  url: string
  binaryType = 'blob'
  // Annotated as `number`, not left to infer the literal 0: the constructor
  // narrows it to 0 and then re-checks it against 3 after calling out to
  // onerror (which may have closed the socket), which TS rejects on a literal.
  readyState: number = 0
  onopen: ((ev: unknown) => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onclose: ((ev: unknown) => void) | null = null
  onerror: ((ev: unknown) => void) | null = null
  sent: unknown[] = []

  constructor(url: string) {
    this.url = url
    FakeWebSocket.instances.push(this)
    const mode = FakeWebSocket.mode
    queueMicrotask(() => {
      if (this.readyState !== 0) return
      if (mode === 'open') {
        this.readyState = 1
        this.onopen?.({})
      } else {
        this.onerror?.({})
        // A refused upgrade errors and then closes; useTelemetry/usePeering
        // both route their reconnect through onclose, so firing only onerror
        // would test a state the browser never produces.
        //
        // Read through a `number`-annotated local: the `!== 0` guard above
        // narrows this.readyState to the literal 0, and TS will not compare a
        // 0 against a 3 — but onerror is a component callback that may well
        // have called close() in between, which is exactly what is being
        // checked here.
        const stateAfterError: number = this.readyState
        if (stateAfterError !== 3) {
          this.readyState = 3
          this.onclose?.({ code: 1006, wasClean: false })
        }
      }
    })
  }

  send(data: unknown) { this.sent.push(data) }

  close() {
    if (this.readyState === 3) return
    this.readyState = 3
    this.onclose?.({ code: 1000, wasClean: true })
  }

  addEventListener() { /* components here use the on* properties */ }
  removeEventListener() { /* noop */ }

  /** Push a server frame at the component. */
  emit(data: unknown) {
    this.onmessage?.(new MessageEvent('message', { data }))
  }
}

/** jsdom defines WebSocket as a non-writable own property of window, so a plain
 *  assignment throws in strict mode. Redefine it instead. */
function defineGlobal(key: string, value: unknown) {
  Object.defineProperty(globalThis, key, { value, configurable: true, writable: true })
}

export function installWebSocket(mode: SocketMode = 'open') {
  const original = global.WebSocket
  FakeWebSocket.instances = []
  FakeWebSocket.mode = mode
  defineGlobal('WebSocket', FakeWebSocket)
  return {
    instances: FakeWebSocket.instances,
    /** The most recent socket opened against a path containing `frag`. */
    last: (frag?: string) => {
      const list = frag
        ? FakeWebSocket.instances.filter(s => s.url.includes(frag))
        : FakeWebSocket.instances
      return list[list.length - 1]
    },
    restore: () => {
      defineGlobal('WebSocket', original)
      FakeWebSocket.instances = []
      FakeWebSocket.mode = 'open'
    },
  }
}

// ---------------------------------------------------------------------------
// Browser APIs jsdom does not implement that these apps touch
// ---------------------------------------------------------------------------

/**
 * Stub the handful of DOM/WebRTC APIs the media builtins call at mount. Without
 * these the component throws for an environmental reason and the test would be
 * reporting on jsdom, not on the app.
 */
export function installBrowserStubs() {
  const g = globalThis as Record<string, unknown>
  const saved: Record<string, unknown> = {}
  const set = (key: string, value: unknown) => { saved[key] = g[key]; g[key] = value }

  if (!g.ResizeObserver) {
    set('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  }
  if (!g.RTCPeerConnection) {
    set('RTCPeerConnection', class {
      onicecandidate: unknown = null
      ontrack: unknown = null
      onconnectionstatechange: unknown = null
      connectionState = 'new'
      addTransceiver() { return {} }
      createDataChannel() { return { readyState: 'connecting', send() {}, close() {} } }
      async createOffer() { return { type: 'offer', sdp: 'v=0' } }
      async setLocalDescription() {}
      async setRemoteDescription() {}
      async addIceCandidate() {}
      getReceivers() { return [] }
      getSenders() { return [] }
      close() {}
    })
  }
  if (!window.matchMedia) {
    window.matchMedia = ((q: string) => ({
      matches: false, media: q, onchange: null,
      addListener() {}, removeListener() {},
      addEventListener() {}, removeEventListener() {}, dispatchEvent: () => false,
    })) as typeof window.matchMedia
  }
  if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
  if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {}
  if (!window.scrollTo) window.scrollTo = () => {}

  return () => { for (const [k, v] of Object.entries(saved)) g[k] = v }
}

// ---------------------------------------------------------------------------
// assertions
// ---------------------------------------------------------------------------

/** Let pending promises and effects settle without advancing timers. */
export async function settle(times = 3) {
  for (let i = 0; i < times; i++) {
    await act(async () => { await Promise.resolve() })
  }
}

/**
 * Assert the container is not a blank rectangle: it must carry text a human
 * could read. Whitespace, and the bare word "Loading", do not count.
 *
 * This is the check that would have caught a lazy chunk mounting to nothing.
 */
export function expectNotBlank(container: HTMLElement, label: string) {
  const text = (container.textContent ?? '').replace(/\s+/g, ' ').trim()
  expect(text, `${label}: window rendered no readable text at all`).not.toBe('')
}

/**
 * Assert the surface is not merely a spinner. `content` is the app's own
 * vocabulary — text that no other builtin would produce.
 */
export function expectOwnSurface(container: HTMLElement, content: RegExp, label: string) {
  const text = (container.textContent ?? '').replace(/\s+/g, ' ').trim()
  expect(text, `${label}: expected its own surface to match ${content}, got: ${text.slice(0, 400)}`)
    .toMatch(content)
}
