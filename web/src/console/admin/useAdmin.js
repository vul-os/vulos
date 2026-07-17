/**
 * console/admin/useAdmin.js — shared data plumbing for the Operator (super-admin)
 * console section.
 *
 * Every operator surface is served by the management binary's JSON admin API
 * under /api/superadmin/*, gated by the RequireSuperAdmin middleware (main
 * session + super-admin row + a SameSite=Strict admin-session cookie obtained by
 * completing the operator login at /superadmin/login). These hooks distinguish
 * the three shapes the gate can return so each page renders an honest state:
 *
 *   200            → authorised, data follows
 *   401            → operator (admin) session missing  → "complete operator sign-in"
 *   403 / 404      → not an operator / console disabled → "operator access required"
 *
 * The React admin never mutates via unsafe verbs without the admin session; all
 * reads are credentialed GETs.
 */

import { useState, useEffect, useCallback } from 'react'

/** adminFetch wraps a credentialed JSON GET, normalising the gate outcomes. */
export async function adminFetch(path) {
  const res = await fetch(path, {
    credentials: 'include',
    headers: { Accept: 'application/json' },
  })
  if (res.status === 401) {
    const e = new Error('operator session required')
    e.needsAdminSession = true
    throw e
  }
  if (res.status === 403 || res.status === 404) {
    const e = new Error('operator access required')
    e.notOperator = true
    throw e
  }
  if (!res.ok) throw new Error(`HTTP ${res.status}`)
  return res.json()
}

/**
 * useAdminResource — GET `path` once (and on demand), classifying the gate
 * outcome. Returns { data, loading, error, needsAdminSession, notOperator, reload }.
 */
export function useAdminResource(path) {
  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [needsAdminSession, setNeedsAdminSession] = useState(false)
  const [notOperator, setNotOperator] = useState(false)

  const load = useCallback(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    setNeedsAdminSession(false)
    setNotOperator(false)
    adminFetch(path)
      .then((json) => { if (!cancelled) { setData(json); setLoading(false) } })
      .catch((err) => {
        if (cancelled) return
        if (err.needsAdminSession) setNeedsAdminSession(true)
        else if (err.notOperator) setNotOperator(true)
        else setError(err.message)
        setLoading(false)
      })
    return () => { cancelled = true }
  }, [path])

  // eslint-disable-next-line react-hooks/set-state-in-effect -- syncs the operator resource; loading reflects the in-flight request.
  useEffect(() => load(), [load])

  return { data, loading, error, needsAdminSession, notOperator, reload: load }
}

/**
 * useOperator — one-shot probe of GET /api/superadmin/whoami used by the shell to
 * decide whether to advertise the Operator nav group. Resolves to false (not an
 * operator / console disabled) rather than erroring, so a normal user's console
 * never shows operator surfaces.
 */
export function useOperator() {
  const [isOperator, setIsOperator] = useState(false)
  const [checked, setChecked] = useState(false)

  useEffect(() => {
    let cancelled = false
    adminFetch('/api/superadmin/whoami')
      .then(() => { if (!cancelled) { setIsOperator(true); setChecked(true) } })
      .catch(() => { if (!cancelled) { setIsOperator(false); setChecked(true) } })
    return () => { cancelled = true }
  }, [])

  return { isOperator, checked }
}
