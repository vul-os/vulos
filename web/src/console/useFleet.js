/**
 * console/useFleet.js — shared hook for the account's fleet devices/boxes.
 *
 * Wraps GET /api/fleet/devices?account_id=<id> (session-authed; account_id MUST
 * match the signed-in user server-side). Returns the uniform paged envelope's
 * `items` (deviceJSON rows) plus loading/error/reload. The account id comes from
 * the AuthProvider session — never from user input — so there is no IDOR surface.
 */

import { useState, useEffect, useCallback } from 'react'
import { useAuth } from '../auth/AuthProvider.jsx'

export function useFleetDevices() {
  const { user } = useAuth()
  const accountId = user?.id || ''
  const [devices, setDevices] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)

  const load = useCallback(() => {
    if (!accountId) { setLoading(false); return }
    setLoading(true)
    setError(null)
    fetch(`/api/fleet/devices?account_id=${encodeURIComponent(accountId)}`, {
      credentials: 'include',
      headers: { Accept: 'application/json' },
    })
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        return res.json()
      })
      .then((json) => {
        const items = Array.isArray(json) ? json : (json?.items ?? [])
        setDevices(items)
        setLoading(false)
      })
      .catch((err) => { setError(err.message); setLoading(false) })
  }, [accountId])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { load() }, [load])

  return { devices, loading, error, reload: load, accountId }
}

/** Map a device's derived `status`/`health` to a Pill tone. */
export function deviceTone(device) {
  if (device?.decommissioned) return 'faint'
  const s = (device?.status || device?.health || '').toLowerCase()
  if (s === 'online' || s === 'healthy' || s === 'ok') return 'good'
  if (s === 'degraded' || s === 'stale' || s === 'warn') return 'warn'
  if (s === 'offline' || s === 'down' || s === 'unhealthy') return 'danger'
  return 'faint'
}
