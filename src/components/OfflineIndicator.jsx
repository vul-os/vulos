/**
 * OfflineIndicator.jsx — visible offline banner for Vulos OS (OFFLINE-02 / -03).
 *
 * Consistent with lilmail's OfflineIndicator (MAIL-OFFLINE-01) and Ofisi's
 * offline UX so the user sees the same chrome wherever the outage hits:
 *
 *   • navigator offline           → "Offline — using local box"
 *   • online but selected endpoint
 *     dropped to same-origin      → "Cloud unreachable — using local box"
 *   • online + cloud or LAN live + queue empty → renders nothing (no noise)
 *   • online + queue non-empty    → "N queued — sending…"   (OFFLINE-03)
 *   • offline + queue non-empty   → "Offline — N queued to send"
 *
 * Pure visual; failover is owned by src/lib/net/endpoints.js and replay is owned by
 * lib/offlineQueue.js.
 */

import { useEffect, useState } from 'react'
import { currentEndpoint, onEndpointChange } from '../lib/net/endpoints.js'
import { onQueueChange, readAll } from '../lib/offlineQueue.js'

export default function OfflineIndicator() {
  const [online, setOnline] = useState(
    typeof navigator === 'undefined' ? true : navigator.onLine !== false
  )
  const [endpoint, setEndpoint] = useState(() => currentEndpoint())
  const [queueLen, setQueueLen] = useState(() => readAll().length)

  useEffect(() => {
    const up = () => setOnline(true)
    const down = () => setOnline(false)
    window.addEventListener('online', up)
    window.addEventListener('offline', down)
    const unsubEndpoint = onEndpointChange((sel) => setEndpoint(sel))
    const unsubQueue = onQueueChange((items) => setQueueLen(items.length))
    return () => {
      window.removeEventListener('online', up)
      window.removeEventListener('offline', down)
      unsubEndpoint()
      unsubQueue()
    }
  }, [])

  // The "all good" case: online, a remote endpoint is healthy (or same-origin
  // is sufficient), and nothing is queued.
  const endpointOk = online && (endpoint !== '' || !hasConfiguredRemote())
  if (endpointOk && queueLen === 0) return null

  let msg
  if (!online) {
    msg = queueLen > 0
      ? `Offline — ${queueLen} queued to send when online`
      : 'Offline — using local box'
  } else if (endpoint === '' && hasConfiguredRemote()) {
    msg = queueLen > 0
      ? `Cloud unreachable — ${queueLen} queued, will send`
      : 'Cloud unreachable — using local box'
  } else {
    // Online + endpoint OK + queue non-empty → flushing.
    msg = `${queueLen} ${queueLen === 1 ? 'change' : 'changes'} queued — sending…`
  }

  return (
    <div
      role="status"
      aria-live="polite"
      data-testid="offline-indicator"
      style={{
        position: 'fixed',
        bottom: '1rem',
        left: '50%',
        transform: 'translateX(-50%)',
        padding: '0.5rem 1rem',
        borderRadius: '999px',
        background: !online ? 'var(--warning, #b45309)' : 'var(--accent, #2563eb)',
        color: '#fff',
        fontSize: '0.85rem',
        zIndex: 9999,
        boxShadow: '0 4px 16px rgba(0,0,0,0.25)',
        pointerEvents: 'none',
      }}
    >
      {msg}
    </div>
  )
}

// Only show the "cloud unreachable" message when the deployment actually has a
// remote endpoint configured — otherwise same-origin IS the box and there's
// nothing useful to tell the user.
function hasConfiguredRemote() {
  try {
    const g = typeof window !== 'undefined' ? window.__VULOS_ENDPOINTS__ : null
    if (g && (g.cloud || g.lan)) return true
    const raw = typeof localStorage !== 'undefined'
      ? localStorage.getItem('vulos.os.endpoints.v1')
      : null
    if (raw) {
      const v = JSON.parse(raw)
      if (v && (v.cloud || v.lan)) return true
    }
  } catch { /* ignore */ }
  return false
}
