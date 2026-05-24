/**
 * OfflineIndicator.jsx — visible offline banner for Vulos OS (OFFLINE-02).
 *
 * Consistent with vulos-mail's OfflineIndicator (MAIL-OFFLINE-01) and the
 * office suite's offline UX so the user sees the same chrome wherever the
 * outage hits:
 *
 *   • navigator offline           → "Offline — local box only"
 *   • online but selected endpoint
 *     dropped to same-origin      → "Cloud unreachable — using local box"
 *   • online + cloud or LAN live  → renders nothing (no UI noise)
 *
 * Pure visual; the failover itself is owned by lib/endpoints.js.
 */

import { useEffect, useState } from 'react'
import { currentEndpoint, onEndpointChange } from '../lib/endpoints.js'

export default function OfflineIndicator() {
  const [online, setOnline] = useState(
    typeof navigator === 'undefined' ? true : navigator.onLine !== false
  )
  const [endpoint, setEndpoint] = useState(() => currentEndpoint())

  useEffect(() => {
    const up = () => setOnline(true)
    const down = () => setOnline(false)
    window.addEventListener('online', up)
    window.addEventListener('offline', down)
    const unsub = onEndpointChange((sel) => setEndpoint(sel))
    return () => {
      window.removeEventListener('online', up)
      window.removeEventListener('offline', down)
      unsub()
    }
  }, [])

  // The "all good" case: online, and either we're on same-origin from the
  // start (no failover layer engaged) or a remote endpoint was picked.
  if (online && endpoint !== '') return null
  if (online && endpoint === '' && !hasConfiguredRemote()) return null

  const msg = !online
    ? 'Offline — using local box'
    : 'Cloud unreachable — using local box'

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
