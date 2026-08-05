import { createContext, useContext, useState, useEffect, useCallback, type ReactNode } from 'react'
import * as offlineAuth from '../lib/offlineAuth.js'

// isRecord narrows an `unknown` value (typically parsed JSON from the network)
// to a plain object before any property access — the same guard lib/offlineAuth.ts
// uses at its own trust boundaries.
function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// AuthUser / AuthProfile mirror the `user` / `profile` fields of the
// /api/auth/me (and register/login) response. That payload is NOT validated
// here — it never was: this provider has always passed the parsed JSON
// straight into state for the shell to render. Asserting a nicer-looking
// shape (e.g. `id: string`) would be a cast lying about a guarantee the
// server response never actually gave us, so fields are `unknown` via the
// index signature rather than typed. `offline` is the one field this module
// OWNS (set locally, never from the network), so it alone can be honestly
// typed as `boolean`.
export interface AuthUser {
  offline?: boolean
  [key: string]: unknown
}
export interface AuthProfile {
  offline?: boolean
  [key: string]: unknown
}

// SessionSummary mirrors one entry of /api/auth/sessions's `sessions` array.
// Each field is narrowed independently (typeof-checked, not cast) so a
// malformed/unexpected entry degrades to `undefined` for that field instead
// of the component trusting a lie about its type.
export interface SessionSummary {
  id?: string
  current?: boolean
  provider?: string
  device_id?: string
  created_at?: string
}

export interface SessionConflict {
  others: number
  sessions: SessionSummary[]
}

function toSessionSummary(x: unknown): SessionSummary {
  if (!isRecord(x)) return {}
  return {
    // Used as a React `key` and otherwise only ever displayed — stringify any
    // present primitive rather than silently dropping a non-string id.
    id: x.id !== undefined && x.id !== null ? String(x.id) : undefined,
    // `Boolean(x.current)` mirrors the original `!s.current` truthiness check
    // exactly (for any input type), just made explicit.
    current: 'current' in x ? Boolean(x.current) : undefined,
    provider: typeof x.provider === 'string' ? x.provider : undefined,
    device_id: typeof x.device_id === 'string' ? x.device_id : undefined,
    created_at: typeof x.created_at === 'string' ? x.created_at : undefined,
  }
}

interface AuthContextValue {
  user: AuthUser | null
  profile: AuthProfile | null
  loading: boolean
  error: string | null
  // OFFLINE-AUTH-01: `offline` = the box was unreachable at the last check (a
  // fetch throw, distinct from a 401 "logged out"). `offlineMode` = we are
  // running on a locally-unlocked offline session (cached identity, no live
  // server auth).
  offline: boolean
  offlineMode: boolean
  logout: () => Promise<void>
  updateProfile: (updates: Record<string, unknown>) => Promise<void>
  checkAuth: () => Promise<void>
  unlockOffline: (password: string) => Promise<boolean>
  // SESSION-TAKEOVER: when set (after a fresh sign-in that finds the account
  // already active elsewhere), the shell shows the "take over / keep other"
  // prompt. Null = no conflict / dismissed.
  sessionConflict: SessionConflict | null
  checkConcurrentSessions: () => Promise<void>
  takeOverSession: () => Promise<void>
  keepOtherSessionAndSignOut: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(null)
  const [profile, setProfile] = useState<AuthProfile | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [offline, setOffline] = useState(false)
  const [offlineMode, setOfflineMode] = useState(false)
  const [sessionConflict, setSessionConflict] = useState<SessionConflict | null>(null)

  const checkAuth = useCallback(async () => {
    try {
      const res = await fetch('/api/auth/me')
      if (res.ok) {
        const data: unknown = await res.json()
        // Box reachable and authenticated — adopt the real session and leave any
        // offline session behind (the server is the authority when present).
        // Untrusted network JSON: narrowed to a record before being handed to
        // state, never cast without that check.
        setUser(isRecord(data) && isRecord(data.user) ? (data.user as AuthUser) : null)
        setProfile(isRecord(data) && isRecord(data.profile) ? (data.profile as AuthProfile) : null)
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
  const unlockOffline = useCallback(async (password: string): Promise<boolean> => {
    await offlineAuth.unlockOffline(password) // throws on wrong password (fail-closed)
    const cached = await offlineAuth.getCachedIdentity().catch(() => null)
    // getCachedIdentity() is declared `Promise<unknown>` at its own trust
    // boundary (lib/offlineAuth.ts) — narrow to a record before reading
    // fields, same as everywhere else in this file.
    const identity = isRecord(cached) ? cached : null
    setUser({ id: (identity && identity.id) || 'offline', offline: true })
    setProfile({ display_name: (identity && identity.name) || 'You', offline: true })
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

  // SESSION-TAKEOVER: called by the login screen right after a successful
  // sign-in. If the account is already active on another device, raise the
  // conflict so the shell can prompt. Best-effort — never blocks or fails a
  // login (offline / older box without the endpoint simply skips the prompt).
  const checkConcurrentSessions = useCallback(async () => {
    try {
      const res = await fetch('/api/auth/sessions')
      if (!res.ok) return
      const data: unknown = await res.json()
      if (!isRecord(data)) return
      // `Number(...)` mirrors the original `data.others > 0` comparison's
      // implicit coercion exactly (a non-numeric value fails the same way a
      // JS `>` comparison against it would).
      const others = Number(data.others)
      if (Number.isFinite(others) && others > 0) {
        const sessions = Array.isArray(data.sessions) ? data.sessions.map(toSessionSummary) : []
        setSessionConflict({ others, sessions })
      }
    } catch {
      /* offline / unreachable — no prompt, login proceeds normally */
    }
  }, [])

  // "Use only here": sign out every OTHER device, keep this one. The current
  // session cookie is unaffected, so the shell stays mounted.
  const takeOverSession = useCallback(async () => {
    try {
      await fetch('/api/auth/sessions/revoke-others', { method: 'POST' })
    } catch {
      /* best-effort; a failure just leaves the other sessions alive */
    }
    setSessionConflict(null)
  }, [])

  // "Keep my other session": leave the other device signed in and sign THIS one
  // out (single-active-session semantics until multi-session ships —
  // roadmap/MULTI-SESSION.md).
  const keepOtherSessionAndSignOut = useCallback(async () => {
    setSessionConflict(null)
    await logout()
  }, [logout])

  const updateProfile = useCallback(async (updates: Record<string, unknown>) => {
    if (!user || offlineMode) return // no server writes in an offline session
    const res = await fetch(`/api/profiles/${user.id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(updates),
    })
    if (res.ok) {
      const updated: unknown = await res.json()
      if (isRecord(updated)) setProfile(updated as AuthProfile)
    }
  }, [user, offlineMode])

  return (
    <AuthContext.Provider value={{
      user, profile, loading, error, offline, offlineMode,
      logout, updateProfile, checkAuth, unlockOffline,
      sessionConflict, checkConcurrentSessions, takeOverSession, keepOtherSessionAndSignOut,
    }}>
      {children}
    </AuthContext.Provider>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
