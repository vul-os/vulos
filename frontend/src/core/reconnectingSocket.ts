// reconnectingSocket.ts — ONE reconnect policy for every long-lived WebSocket
// the shell opens.
//
// WHY THIS EXISTS
//
// The shell opens three long-lived sockets to the box:
//
//   /api/telemetry            → useTelemetry
//   /api/peering/stream       → usePeering
//   /api/notifications/stream → notificationBridge
//
// All three had independently hand-rolled the same retry loop, and all three
// had the same bug in it: the `setTimeout` handle was never stored, so nothing
// could clear it. usePeering was the severe case — its effect cleanup was an
// explicit no-op comment, so the socket was never closed and its alive flag
// was never cleared, and the retry loop outlived the component that started
// it, forever. On a box where the service is genuinely down that turns one
// dead socket into an accumulating pile of them, one per mount, each holding a
// socket, a timer and a closure.
//
// THE INVARIANT this module exists to hold: stop() is total. After it returns,
// no timer is pending, no socket is open, no handler can fire, and no further
// reconnect can be scheduled — and it holds without the caller having to
// remember to do anything beyond calling stop() from its effect cleanup. A
// connection whose correctness depends on every caller doing something extra
// is a connection that will be wrong again.
//
// BACKOFF. Exponential from 1s, doubling, capped at 30s, with equal jitter.
// The cap is the point: a service that is down stays down, and a fixed 1s (or
// notificationBridge's fixed 3s) retry from every mounted consumer is its own
// denial of service against a 2 GB box. At the cap a single consumer costs at
// most ~2 connection attempts per minute. The jitter matters because these
// three sockets die at the same instant — a box restart drops all of them —
// and without it they would retry in lockstep for as long as the box is down,
// re-synchronised by every shared outage.
//
// AVAILABLE vs FAILED. A socket that has never once opened is reporting
// something different from a socket that opened and dropped: the first says
// the service is not there, the second says the connection is flaky. Callers
// get that distinction as `status`, so a panel can render a designed
// "not available on this box" empty state instead of a permanent spinner.

/**
 * connecting  — first attempt in flight, has never opened.
 * open        — the socket is open.
 * reconnecting— it opened at least once and is now retrying (flaky link).
 * unavailable — it has never opened and has failed UNAVAILABLE_AFTER times in
 *               a row (the service is very likely not running on this box).
 *               Retries CONTINUE at the capped interval; this is a reporting
 *               state, not a terminal one, because a box service can come back
 *               (a systemd restart) and the UI must recover on its own.
 */
export type SocketStatus = 'connecting' | 'open' | 'reconnecting' | 'unavailable'

/** First retry delay, doubled per consecutive failure. */
export const BASE_DELAY_MS = 1_000
/** Ceiling on the retry delay: ~2 attempts/minute per consumer at worst. */
export const MAX_DELAY_MS = 30_000
/** Consecutive never-opened failures before we call the service unavailable. */
export const UNAVAILABLE_AFTER = 3
/** Attempt index past which the delay is pinned at MAX_DELAY_MS anyway. */
const MAX_ATTEMPT_EXPONENT = 10

export interface ReconnectingSocketHandlers {
  /** A message arrived. Throwing here is caught and logged, never fatal. */
  onMessage?: (event: MessageEvent) => void
  /** The socket just opened. */
  onOpen?: (socket: WebSocket) => void
  /** Status changed. Never called after stop(). */
  onStatus?: (status: SocketStatus) => void
}

export interface ReconnectingSocketOptions {
  baseDelayMs?: number
  maxDelayMs?: number
  unavailableAfter?: number
  /** Injectable for deterministic tests. Defaults to Math.random. */
  random?: () => number
  /** Injectable for tests / non-DOM callers. Defaults to global WebSocket. */
  socketFactory?: (url: string) => WebSocket
  /** Label used in console diagnostics. */
  label?: string
}

export interface ReconnectingSocket {
  /** Send a frame. Returns false (and drops it) if the socket is not open. */
  send(data: string): boolean
  /** Current status. */
  status(): SocketStatus
  /** The live socket, or null while retrying. Do not stash this. */
  socket(): WebSocket | null
  /**
   * Tear everything down: clears the pending retry timer, detaches every
   * handler, closes the socket and latches the connection closed so no further
   * reconnect can ever be scheduled. Idempotent.
   */
  stop(): void
}

/**
 * backoffDelay(attempt) — exponential backoff with EQUAL jitter.
 *
 * Equal jitter (half fixed, half random) rather than full jitter: full jitter
 * can return a near-zero delay, which reintroduces exactly the tight retry
 * loop the cap exists to prevent. Equal jitter keeps a hard floor of half the
 * nominal delay while still de-phasing concurrent sockets.
 *
 * Exported for direct unit testing — the timing policy is the interesting part
 * and it should not only be observable through a live socket.
 */
export function backoffDelay(
  attempt: number,
  opts: { baseDelayMs?: number; maxDelayMs?: number; random?: () => number } = {},
): number {
  const base = opts.baseDelayMs ?? BASE_DELAY_MS
  const max = opts.maxDelayMs ?? MAX_DELAY_MS
  const rand = opts.random ?? Math.random
  // Clamp the exponent before the shift: 2 ** 1024 is Infinity, and
  // Infinity * base is NaN-adjacent arithmetic we should never reach.
  const n = Math.max(0, Math.min(attempt, MAX_ATTEMPT_EXPONENT))
  const nominal = Math.min(base * 2 ** n, max)
  return Math.round(nominal / 2 + rand() * (nominal / 2))
}

/**
 * openReconnectingSocket — open `url` and keep it open, with bounded backoff.
 *
 * The returned handle's stop() is the ONLY teardown a caller needs; wire it
 * straight into a React effect cleanup:
 *
 *   useEffect(() => openReconnectingSocket(url, handlers).stop, [url])
 *
 * (careful: that returns stop bound to the handle, which is what you want).
 */
export function openReconnectingSocket(
  url: string,
  handlers: ReconnectingSocketHandlers = {},
  options: ReconnectingSocketOptions = {},
): ReconnectingSocket {
  const unavailableAfter = options.unavailableAfter ?? UNAVAILABLE_AFTER
  const makeSocket = options.socketFactory ?? ((u: string) => new WebSocket(u))
  const label = options.label ?? url

  let alive = true
  let current: WebSocket | null = null
  // The retry handle. The whole point of this module: it is ALWAYS stored, so
  // stop() can always clear it.
  let timer: ReturnType<typeof setTimeout> | null = null
  let attempt = 0
  let everOpened = false
  let state: SocketStatus = 'connecting'

  function setStatus(next: SocketStatus) {
    if (!alive || state === next) return
    state = next
    try { handlers.onStatus?.(next) } catch (err) {
      console.error(`[reconnectingSocket:${label}] onStatus error`, err)
    }
  }

  function clearTimer() {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }

  /** Detach every handler so a socket we have let go of can never call back. */
  function detach(ws: WebSocket) {
    ws.onopen = null
    ws.onmessage = null
    ws.onclose = null
    ws.onerror = null
  }

  function scheduleRetry() {
    if (!alive) return
    // Never stack timers: a stray extra retry is a second connection.
    clearTimer()
    const delay = backoffDelay(attempt, options)
    attempt += 1
    timer = setTimeout(() => {
      timer = null
      connect()
    }, delay)
  }

  function connect() {
    if (!alive) return
    // An existing socket that is not yet closing is already doing this job.
    if (current && current.readyState < WebSocket.CLOSING) return

    let ws: WebSocket
    try {
      ws = makeSocket(url)
    } catch (err) {
      // A constructor that throws (bad URL, blocked scheme) is a failure like
      // any other — it must still schedule a retry, or the connection dies
      // silently on the very first attempt.
      console.error(`[reconnectingSocket:${label}] construct failed`, err)
      onFailure()
      return
    }
    current = ws

    ws.onopen = () => {
      if (!alive || current !== ws) return
      everOpened = true
      attempt = 0
      setStatus('open')
      try { handlers.onOpen?.(ws) } catch (err) {
        console.error(`[reconnectingSocket:${label}] onOpen error`, err)
      }
    }

    ws.onmessage = (event: MessageEvent) => {
      if (!alive || current !== ws) return
      try { handlers.onMessage?.(event) } catch (err) {
        console.error(`[reconnectingSocket:${label}] onMessage error`, err)
      }
    }

    ws.onclose = () => {
      // Two guards, both load-bearing. `alive` is the unmount latch. The
      // identity check stops a socket we already replaced or abandoned from
      // scheduling a reconnect on behalf of the live one.
      if (!alive || current !== ws) return
      current = null
      onFailure()
    }

    ws.onerror = () => {
      if (!alive || current !== ws) return
      // Let onclose drive the retry so there is exactly one retry path; a
      // refused upgrade fires error then close, and retrying from both would
      // open two sockets.
      try { ws.close() } catch { /* already gone */ }
    }
  }

  function onFailure() {
    current = null
    if (!alive) return
    // `attempt` is the count of retries already SCHEDULED, so this failure is
    // number attempt+1 in the current consecutive run (a successful open
    // resets it to 0).
    const consecutive = attempt + 1
    if (consecutive >= unavailableAfter) {
      // Three failures in a row means the service is not answering, whether or
      // not it once did. Saying "reconnecting…" forever for a service that was
      // uninstalled is the same lie as saying "loading" for a 404.
      setStatus('unavailable')
    } else {
      setStatus(everOpened ? 'reconnecting' : 'connecting')
    }
    scheduleRetry()
  }

  function stop() {
    if (!alive) return
    // Latch FIRST: everything below can synchronously re-enter through a
    // close handler, and every one of those paths checks `alive`.
    alive = false
    clearTimer()
    const ws = current
    current = null
    if (ws) {
      detach(ws)
      try { ws.close() } catch { /* already gone */ }
    }
  }

  connect()

  return {
    send(data: string): boolean {
      const ws = current
      if (!alive || !ws || ws.readyState !== WebSocket.OPEN) return false
      try {
        ws.send(data)
        return true
      } catch (err) {
        console.error(`[reconnectingSocket:${label}] send error`, err)
        return false
      }
    },
    status: () => state,
    socket: () => current,
    stop,
  }
}

/** Build a same-origin ws:// or wss:// URL for a box API path. */
export function boxSocketUrl(path: string): string {
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws'
  return `${scheme}://${location.host}${path}`
}
