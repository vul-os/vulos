import { useState, useEffect, useRef } from 'react'

const WS_URL = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/telemetry`

export function useTelemetry() {
  const [stats, setStats] = useState(null)
  const [connected, setConnected] = useState(false)
  const wsRef = useRef(null)
  const retryRef = useRef(0)

  useEffect(() => {
    let alive = true

    function connect() {
      if (!alive) return
      const ws = new WebSocket(WS_URL)
      wsRef.current = ws

      ws.onopen = () => {
        setConnected(true)
        retryRef.current = 0
      }

      ws.onmessage = (e) => {
        try {
          setStats(JSON.parse(e.data))
        } catch { /* noop */ }
      }

      ws.onclose = () => {
        setConnected(false)
        wsRef.current = null
        if (alive) {
          const delay = Math.min(1000 * 2 ** retryRef.current, 30000)
          retryRef.current++
          setTimeout(connect, delay)
        }
      }

      ws.onerror = () => ws.close()
    }

    connect()
    return () => { alive = false; wsRef.current?.close() }
  }, [])

  return { stats, connected }
}
