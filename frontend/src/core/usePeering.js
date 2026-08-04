/**
 * usePeering — thin React hook that manages the single multiplexed WebSocket
 * connection to /api/peering/stream.
 *
 * The server sends channel-tagged JSON frames:
 *   { channel: "message|signal|collab|notification|presence", from, payload }
 *
 * Usage
 *   const { connected, subscribe, send } = usePeering()
 *
 *   // receive frames on a specific channel
 *   useEffect(() => subscribe('notification', frame => console.log(frame)), [subscribe])
 *
 *   // push a frame from the browser
 *   send({ channel: 'collab', payload: { op: 'cursor', x: 10, y: 20 } })
 */
import { useEffect, useRef, useState, useCallback } from 'react'

const WS_URL = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/peering/stream`

/** Maximum exponential-backoff delay in milliseconds. */
const MAX_BACKOFF_MS = 30_000

/** Channels the server understands (mirrors server-side constants). */
export const Channel = {
  MESSAGE:      'message',
  SIGNAL:       'signal',
  COLLAB:       'collab',
  NOTIFICATION: 'notification',
  PRESENCE:     'presence',
}

/**
 * usePeering — singleton-like hook.  Safe to call from multiple components;
 * the WebSocket is opened once per page lifecycle (the hook does NOT close on
 * component unmount — use the returned `close` if you need explicit teardown).
 */
export function usePeering() {
  const [connected, setConnected] = useState(false)
  const wsRef       = useRef(null)
  const retryRef    = useRef(0)
  const aliveRef    = useRef(true)
  // Map<channel, Set<handler>>
  const listenersRef = useRef(new Map())

  // ------------------------------------------------------------------
  // Connection lifecycle
  // ------------------------------------------------------------------
  // connectRef lets the onclose handler schedule a reconnect without
  // the linter flagging a "variable accessed before declaration" on `connect`.
  const connectRef = useRef(null)

  const connect = useCallback(() => {
    if (!aliveRef.current) return
    if (wsRef.current && wsRef.current.readyState < WebSocket.CLOSING) return

    const ws = new WebSocket(WS_URL)
    wsRef.current = ws

    ws.onopen = () => {
      setConnected(true)
      retryRef.current = 0
    }

    ws.onmessage = (event) => {
      let frame
      try {
        frame = JSON.parse(event.data)
      } catch {
        return
      }
      if (!frame || !frame.channel) return

      // Notify channel-specific subscribers
      const handlers = listenersRef.current.get(frame.channel)
      if (handlers) {
        for (const fn of handlers) {
          try { fn(frame) } catch (err) {
            console.error('[usePeering] subscriber error', err)
          }
        }
      }

      // Also notify wildcard subscribers ('*')
      const wildcards = listenersRef.current.get('*')
      if (wildcards) {
        for (const fn of wildcards) {
          try { fn(frame) } catch (err) {
            console.error('[usePeering] wildcard subscriber error', err)
          }
        }
      }
    }

    ws.onclose = () => {
      setConnected(false)
      wsRef.current = null
      if (!aliveRef.current) return
      // Exponential back-off: 1s, 2s, 4s … capped at MAX_BACKOFF_MS
      const delay = Math.min(1000 * 2 ** retryRef.current, MAX_BACKOFF_MS)
      retryRef.current = Math.min(retryRef.current + 1, 10)
      setTimeout(() => connectRef.current?.(), delay)
    }

    ws.onerror = () => ws.close()
  }, [])

  useEffect(() => {
    connectRef.current = connect
  }, [connect])

  useEffect(() => {
    aliveRef.current = true
    connect()
    return () => {
      // Hook is intentionally long-lived; we do not close on unmount of any
      // individual consumer.  Call the returned `close()` for explicit teardown.
    }
  }, [connect])

  // ------------------------------------------------------------------
  // Public API
  // ------------------------------------------------------------------

  /**
   * subscribe(channel, handler) — register a handler for a channel.
   * Returns an unsubscribe function.
   *
   * Pass channel='*' to receive all frames regardless of channel.
   */
  const subscribe = useCallback((channel, handler) => {
    const map = listenersRef.current
    if (!map.has(channel)) map.set(channel, new Set())
    map.get(channel).add(handler)
    return () => {
      const set = map.get(channel)
      if (set) set.delete(handler)
    }
  }, [])

  /**
   * send(frame) — deliver a frame to the server.
   * frame must include at least { channel, payload }.
   * Silently drops if the socket is not yet open.
   */
  const send = useCallback((frame) => {
    const ws = wsRef.current
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    try {
      ws.send(JSON.stringify(frame))
    } catch (err) {
      console.error('[usePeering] send error', err)
    }
  }, [])

  /**
   * close() — permanently close the connection and stop reconnecting.
   * Useful when logging out.
   */
  const close = useCallback(() => {
    aliveRef.current = false
    wsRef.current?.close()
  }, [])

  return { connected, subscribe, send, close }
}
