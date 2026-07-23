import { createContext, useContext, useState, useEffect, useCallback } from 'react'
import * as offlineAuth from '../lib/offlineAuth.js'

const AuthContext = createContext(null)

export function AuthProvider({ children }) {
  const [user, setUser] = useState(null)
  const [profile, setProfile] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  // OFFLINE-AUTH-01: `offline` = the box was unreachable at the last check (a
  // fetch throw, distinct from a 401 "logged out"). `offlineMode` = we are running
  // on a locally-unlocked offline session (cached identity, no live server auth).
  const [offline, setOffline] = useState(false)
  const [offlineMode, setOfflineMode] = useState(false)

  const checkAuth = useCallback(async () => {
    try {
      const res = await fetch('/api/auth/me')
      if (res.ok) {
        const data = await res.json()
        // Box reachable and authenticated — adopt the real session and leave any
        // offline session behind (the server is the authority when present).
        setUser(data.user)
        setProfile(data.profile)
        setOffline(false)
        setOfflineMode(false)
        setError(null)
      } else {
        // Box reachable but NOT authenticated (401) — genuinely logged out. This
        // is the reconnect re-validation: it ends ANY session, including an
        // offline one, and drops the in-memory key. The offline session is a
        // bridge for when the box is unreachable, never a way to outlive a real
        // 401 once the box is back.
        setOffline(false)
        if (offlineMode) { try { offlineAuth.lock() } catch { /* no-op */ } }
        setOfflineMode(false)
        setUser(null)
        setProfile(null)
      }
    } catch {
      // Network error — the box is unreachable. Mark offline, but do NOT clear an
      // existing session (online-authed or offline-unlocked); a transient blip
      // must not eject the user.
      setOffline(true)
    } finally {
      setLoading(false)
    }
  }, [offlineMode])

  // Check auth on mount + periodically (detect expired sessions / reconnection).
  useEffect(() => {
    checkAuth()
    const interval = setInterval(checkAuth, 5 * 60 * 1000) // every 5 min
    return () => clearInterval(interval)
  }, [checkAuth])

  // unlockOffline attempts a local, fail-closed offline unlock (OFFLINE-AUTH-01).
  // On success it starts an offline session backed by the cached identity — the
  // shell renders cached data; API calls degrade per OFFLINE.md. Re-throws the
  // offlineAuth error (WRONG_PASSWORD / WIPED / NOT_ENROLLED) so the lock screen
  // can surface it.
  const unlockOffline = useCallback(async (password) => {
    await offlineAuth.unlockOffline(password) // throws on wrong password (fail-closed)
    const id = await offlineAuth.getCachedIdentity().catch(() => null)
    setUser({ id: (id && id.id) || 'offline', offline: true })
    setProfile({ display_name: (id && id.name) || 'You', offline: true })
    setOfflineMode(true)
    setOffline(true)
    return true
  }, [])

  const logout = useCallback(async () => {
    try { await fetch('/api/auth/logout', { method: 'POST' }) } catch { /* offline logout */ }
    try { offlineAuth.lock() } catch { /* no-op */ }
    setUser(null)
    setProfile(null)
    setOfflineMode(false)
  }, [])

  const updateProfile = useCallback(async (updates) => {
    if (!user || offlineMode) return // no server writes in an offline session
    const res = await fetch(`/api/profiles/${user.id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(updates),
    })
    if (res.ok) {
      const updated = await res.json()
      setProfile(updated)
    }
  }, [user, offlineMode])

  return (
    <AuthContext.Provider value={{
      user, profile, loading, error, offline, offlineMode,
      logout, updateProfile, checkAuth, unlockOffline,
    }}>
      {children}
    </AuthContext.Provider>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
