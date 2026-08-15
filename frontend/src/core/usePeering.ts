/**
 * usePeering — React hook that manages the multiplexed WebSocket connection
 * to /api/peering/stream.
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
 *
 * LIFECYCLE. The connection is owned by the component that calls this hook and
 * is torn down completely when that component unmounts — socket closed, retry
 * timer cleared, reconnect loop latched off. No caller has to remember to do
 * anything.
 *
 * This used to be false. The effect's cleanup was an explicit no-op comment,
 * the alive flag was only cleared by the exported `close()` (which no consumer
 * ever called), and the retry handle was never stored, so every mount left
 * behind an open socket AND a reconnect loop that ran for the rest of the page's
 * life. The docstring claimed the hook was "singleton-like" and opened its
 * socket "once per page lifecycle"; it was not and it did not — the refs are
 * per-call, so each mount opened its own, and none of them ever went away.
 * `close()` is still exported for explicit teardown (logout), but nothing
 * depends on it being called.
 */
import { useEffect, useRef, useState, useCallback } from 'react'
import {
  openReconnectingSocket,
  boxSocketUrl,
  type ReconnectingSocket,
  type SocketStatus,
} from './reconnectingSocket'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

export interface PeerFrame {
  channel: string
  from?: string
  payload?: unknown
}

function toPeerFrame(x: unknown): PeerFrame | null {
  if (!isRecord(x) || typeof x.channel !== 'string') return null
  return {
    channel: x.channel,
    from: typeof x.from === 'string' ? x.from : undefined,
    payload: x.payload,
  }
}

export type PeerFrameHandler = (frame: PeerFrame) => void

export const PEERING_PATH = '/api/peering/stream'

interface ChannelMap {
  MESSAGE: 'message'
  SIGNAL: 'signal'
  COLLAB: 'collab'
  NOTIFICATION: 'notification'
  PRESENCE: 'presence'
}

/** Channels the server understands (mirrors server-side constants). */
export const Channel: ChannelMap = {
  MESSAGE:      'message',
  SIGNAL:       'signal',
  COLLAB:       'collab',
  NOTIFICATION: 'notification',
  PRESENCE:     'presence',
}

export interface PeeringResult {
  connected: boolean
  /**
   * Finer-grained than `connected`: 'unavailable' means the peering service
   * has never answered on this box (render a designed empty state), while
   * 'reconnecting' means it did and the link dropped (keep the last view).
   */
  status: SocketStatus
  subscribe: (channel: string, handler: PeerFrameHandler) => () => void
  send: (frame: PeerFrame) => void
  close: () => void
}

export function usePeering(): PeeringResult {
  const [connected, setConnected] = useState(false)
  const [status, setStatus] = useState<SocketStatus>('connecting')
  const sockRef = useRef<ReconnectingSocket | null>(null)
  // Map<channel, Set<handler>>
  const listenersRef = useRef(new Map<string, Set<PeerFrameHandler>>())

  const dispatch = useCallback((event: MessageEvent) => {
    let frame: PeerFrame | null
    try {
      frame = typeof event.data === 'string' ? toPeerFrame(JSON.parse(event.data)) : null
    } catch {
      return
    }
    if (!frame) return

    // Notify channel-specific subscribers, then wildcard ('*') subscribers.
    // Iterate a copy: a handler that unsubscribes itself (a common pattern for
    // one-shot listeners) would otherwise mutate the Set mid-iteration.
    for (const key of [frame.channel, '*']) {
      const handlers = listenersRef.current.get(key)
      if (!handlers) continue
      for (const fn of [...handlers]) {
        try { fn(frame) } catch (err) {
          console.error('[usePeering] subscriber error', err)
        }
      }
    }
  }, [])

  useEffect(() => {
    const sock = openReconnectingSocket(boxSocketUrl(PEERING_PATH), {
      onMessage: dispatch,
      onStatus: s => {
        setStatus(s)
        setConnected(s === 'open')
      },
    }, { label: 'peering' })
    sockRef.current = sock

    // THE FIX. This cleanup used to be an empty comment. It now closes the
    // socket, clears the pending retry timer and latches the reconnect loop
    // off — unconditionally, on every unmount.
    return () => {
      sock.stop()
      if (sockRef.current === sock) sockRef.current = null
      // No setState here: the component is unmounting and the socket is gone.
    }
  }, [dispatch])

  // ------------------------------------------------------------------
  // Public API
  // ------------------------------------------------------------------

  /**
   * subscribe(channel, handler) — register a handler for a channel.
   * Returns an unsubscribe function.
   *
   * Pass channel='*' to receive all frames regardless of channel.
   */
  const subscribe = useCallback((channel: string, handler: PeerFrameHandler) => {
    const map = listenersRef.current
    let set = map.get(channel)
    if (!set) {
      set = new Set()
      map.set(channel, set)
    }
    set.add(handler)
    return () => {
      map.get(channel)?.delete(handler)
    }
  }, [])

  /**
   * send(frame) — deliver a frame to the server.
   * frame must include at least { channel, payload }.
   * Silently drops if the socket is not open.
   */
  const send = useCallback((frame: PeerFrame) => {
    sockRef.current?.send(JSON.stringify(frame))
  }, [])

  /**
   * close() — tear the connection down early and stop reconnecting, without
   * waiting for unmount (e.g. on logout). Unmount already does this; nothing
   * is required to call it.
   */
  const close = useCallback(() => {
    sockRef.current?.stop()
    sockRef.current = null
  }, [])

  return { connected, status, subscribe, send, close }
}
