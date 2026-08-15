// useTelemetry — the live /api/telemetry socket (CPU, memory, load, uptime).
//
// This hook's cleanup was already closing the socket and clearing its alive
// flag, so it never had usePeering's outliving-unmount bug. It DID share the
// other half: the reconnect `setTimeout` handle was never stored, so a pending
// retry survived unmount and held its closure until it fired (up to 30s), and
// a component that mounted and unmounted repeatedly accumulated one such timer
// per cycle. It now shares one reconnect policy with the other two box sockets
// — see reconnectingSocket.ts for the backoff and the teardown invariant.

import { useState, useEffect } from 'react'
import { openReconnectingSocket, boxSocketUrl, type SocketStatus } from './reconnectingSocket'

export const TELEMETRY_PATH = '/api/telemetry'

interface TelemetryResult {
  // Parsed straight off the wire (JSON.parse of a WebSocket message) — a
  // trust boundary, so left as `unknown`; consumers narrow what they need.
  stats: unknown
  connected: boolean
  /**
   * 'unavailable' means telemetry has never answered on this box (the service
   * is not running) — render a designed empty state, not a spinner.
   * 'reconnecting' means it answered and the link dropped; the last `stats`
   * are still the best available reading.
   */
  status: SocketStatus
}

export function useTelemetry(): TelemetryResult {
  const [stats, setStats] = useState<unknown>(null)
  const [connected, setConnected] = useState(false)
  const [status, setStatus] = useState<SocketStatus>('connecting')

  useEffect(() => {
    const sock = openReconnectingSocket(boxSocketUrl(TELEMETRY_PATH), {
      onMessage: e => {
        try { setStats(JSON.parse(e.data as string)) } catch { /* malformed frame */ }
      },
      onStatus: s => {
        setStatus(s)
        setConnected(s === 'open')
      },
    }, { label: 'telemetry' })

    // Total teardown: socket closed, retry timer cleared, loop latched off.
    return () => sock.stop()
  }, [])

  return { stats, connected, status }
}
