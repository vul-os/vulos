/**
 * vulos-cloud — AuthProvider
 *
 * Single source of truth for the current cloud-control-plane session. The
 * backend (CLOUD_SYSTEM_CONTRACT.md, FROZEN wave 1) sets an httpOnly cookie
 * `vc_session`; every fetch here uses `credentials: 'include'` so that cookie
 * round-trips on its own.
 *
 * Surface (frozen per the contract):
 *   useAuth() → {
 *     user,        // { id, email, ... } | null
 *     loading,     // true until the first /api/auth/me probe resolves
 *     error,       // string | null  — last network/auth error
 *     login(email, password),
 *     signup(email, password),    // posts {email, password}; your own external email IS your Vulos account identity (bring-your-own-email)
 *     logout(),
 *     refresh(),   // force re-fetch /api/auth/me
 *   }
 *
 * Cross-tab sync:
 *   - On login/signup/logout we broadcast a synthetic
 *     `window` event `vc:auth-changed` AND (best-effort) write a tick into
 *     localStorage under `vc:auth-tick` so the browser's `storage` event
 *     fires in *other* tabs (the originating tab doesn't get its own storage
 *     event by design — that's why we also dispatch the window event for
 *     same-tab listeners).
 *   - Other tabs listening to `storage` (key `vc:auth-tick`) re-probe
 *     /api/auth/me and update their own state.
 */

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'

import { powFetch } from './pow.js'
import { clientNavigate } from './nav.js'

/* ─── Constants ─────────────────────────────────────────── */
const ME_URL          = '/api/auth/me'
const LOGIN_URL       = '/api/auth/login'
const SIGNUP_URL      = '/api/auth/signup'
const LOGOUT_URL      = '/api/auth/logout'

const SYNC_EVENT      = 'vc:auth-changed'
const SYNC_STORAGE_KEY = 'vc:auth-tick'

/* ─── Context ───────────────────────────────────────────── */
const defaultValue = {
  user: null,
  loading: true,
  error: null,
  login: async () => {},
  signup: async () => {},
  logout: async () => {},
  refresh: async () => {},
}

const AuthContext = createContext(defaultValue)

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth() {
  return useContext(AuthContext)
}

/* ─── Helpers ───────────────────────────────────────────── */

// Try to read a JSON body but tolerate empty/204 responses.
async function readJSON(res) {
  const text = await res.text()
  if (!text) return null
  try { return JSON.parse(text) } catch { return null }
}

// Pull a useful human message out of an error response body, with sensible
// fallbacks per status. The backend (auth wave 1) shapes errors as
// `{ error: "..." }` but we don't bet the farm on that.
function messageForError(status, body) {
  if (body && typeof body === 'object') {
    if (typeof body.error   === 'string' && body.error)   return body.error
    if (typeof body.message === 'string' && body.message) return body.message
  }
  if (status === 401) return 'Incorrect email or password.'
  if (status === 409) return 'An account with that email already exists.'
  if (status === 410) return 'This reset link has expired or already been used.'
  if (status === 429) return 'Too many attempts — try again in a minute.'
  if (status >= 500)  return 'Something went wrong on our end. Please try again.'
  return 'Request failed. Please try again.'
}

// Tiny wrapper so we never forget credentials/JSON headers on auth calls.
// powFetch transparently solves the CaptchaGate's hashcash on gated POSTs —
// without it every login/signup is a hard 403 (see src/auth/pow.js).
async function authFetch(url, body) {
  const init = {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
  }
  if (body !== undefined) init.body = JSON.stringify(body)
  return powFetch(url, init)
}

/* Broadcast a same-tab + cross-tab signal that the session changed. */
function broadcastAuthChange() {
  try {
    window.dispatchEvent(new CustomEvent(SYNC_EVENT))
  } catch { /* very old browsers — ignore */ }
  try {
    // storage events only fire in *other* tabs; we deliberately re-write
    // each time so listeners always see a "change".
    window.localStorage.setItem(SYNC_STORAGE_KEY, String(Date.now()))
  } catch { /* private mode / disabled storage */ }
}

/* ─── Provider ──────────────────────────────────────────── */

export function AuthProvider({ children }) {
  const [user,    setUser]    = useState(null)
  const [loading, setLoading] = useState(true)
  const [error,   setError]   = useState(null)

  // Guard against setState-after-unmount on the very first probe.
  const mountedRef = useRef(true)
  useEffect(() => {
    mountedRef.current = true
    return () => { mountedRef.current = false }
  }, [])

  /**
   * Probe the cookie session and update state.
   * Used on mount, after auth changes, and from cross-tab sync.
   * Never throws — sets `error` instead, so callers can `await` safely.
   */
  const refresh = useCallback(async () => {
    try {
      const res = await fetch(ME_URL, {
        method: 'GET',
        credentials: 'include',
        headers: { Accept: 'application/json' },
        cache: 'no-store',
      })

      if (res.status === 401 || res.status === 403) {
        if (mountedRef.current) {
          setUser(null)
          setError(null)
        }
        return null
      }
      if (!res.ok) {
        const body = await readJSON(res)
        if (mountedRef.current) {
          setUser(null)
          setError(messageForError(res.status, body))
        }
        return null
      }
      const body = await readJSON(res)
      // backend may return user inline or under `user` — accept both.
      const u = body && typeof body === 'object'
        ? (body.user && typeof body.user === 'object' ? body.user : body)
        : null
      if (mountedRef.current) {
        setUser(u || null)
        setError(null)
      }
      return u
    } catch (e) {
      if (mountedRef.current) {
        setUser(null)
        // Network errors shouldn't render the whole shell as "errored" — the
        // user just isn't logged in yet. Stash the message for diagnostics
        // but page chrome treats `user === null` as "go to /login".
        setError(e && e.message ? e.message : 'Network error')
      }
      return null
    }
  }, [])

  /* ── First probe ─────────────────────────────────────── */
  // `loading` is initialised to `true`, so the effect just clears it once
  // the first /api/auth/me round-trip resolves. The lint rule flags any
  // setState reached from inside an effect (transitively, via the
  // refresh() callback) — this is the canonical "synchronise external
  // session state into React" use case the rule's own docs make an
  // exception for, so we disable it explicitly.
  useEffect(() => {
    let cancelled = false
    // eslint-disable-next-line react-hooks/set-state-in-effect
    refresh().finally(() => {
      if (!cancelled && mountedRef.current) setLoading(false)
    })
    return () => { cancelled = true }
  }, [refresh])

  /* ── Cross-tab + same-tab sync ───────────────────────── */
  useEffect(() => {
    // Same tab: AuthProvider broadcasts on login/signup/logout so other
    // mounted listeners can react (we still self-update; this is for
    // co-mounted widgets).
    const onLocal = () => { void refresh() }
    // Cross tab: storage event in other tabs only.
    const onStorage = (ev) => {
      if (ev && ev.key === SYNC_STORAGE_KEY) void refresh()
    }
    // Coming back from background (e.g. OAuth round-trip in another tab)
    // — re-probe so the UI catches up immediately on tab focus.
    const onVisible = () => {
      if (document.visibilityState === 'visible') void refresh()
    }
    window.addEventListener(SYNC_EVENT, onLocal)
    window.addEventListener('storage', onStorage)
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      window.removeEventListener(SYNC_EVENT, onLocal)
      window.removeEventListener('storage', onStorage)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [refresh])

  /* ── Mutations ───────────────────────────────────────── */

  const login = useCallback(async (email, password, { returnTo } = {}) => {
    setError(null)
    const res = await authFetch(LOGIN_URL, { email, password })
    const body = await readJSON(res)
    if (!res.ok) {
      const msg = messageForError(res.status, body)
      setError(msg)
      const err = new Error(msg)
      err.status = res.status
      throw err
    }

    // POST /api/auth/login returns {"step":"totp_required"} when the account
    // has TOTP enabled.  The server has already set a short-lived partial-session
    // cookie (5 min TTL).  We redirect to /2fa so TwoFactor.jsx can handle the
    // TOTP step; we do NOT call refresh() here because /api/auth/me will 401
    // while the session is still partial.
    if (body && body.step === 'totp_required') {
      const tfaHref = returnTo && returnTo !== '/'
        ? `/2fa?return=${encodeURIComponent(returnTo)}`
        : '/2fa'
      // Basename-aware in-app navigation (prefixes /console, notifies the router).
      clientNavigate(tfaHref)
      // Return a sentinel so callers can detect the redirect.
      return { step: 'totp_required' }
    }

    // Backend issues the cookie; we still want fresh `me` state for any
    // fields the login response didn't return (created_at, oauth links, …).
    await refresh()
    broadcastAuthChange()
    return body
  }, [refresh])

  const signup = useCallback(async (email, password) => {
    setError(null)
    const res = await authFetch(SIGNUP_URL, { email, password })
    const body = await readJSON(res)
    if (!res.ok) {
      const msg = messageForError(res.status, body)
      setError(msg)
      const err = new Error(msg)
      err.status = res.status
      throw err
    }
    await refresh()
    broadcastAuthChange()
    return body
  }, [refresh])

  const logout = useCallback(async () => {
    setError(null)
    try {
      await authFetch(LOGOUT_URL)
    } catch {
      // Even if the server call fails we still drop local state — the cookie
      // may be stale or the network may be gone; either way the user
      // intends to be logged out.
    }
    if (mountedRef.current) setUser(null)
    broadcastAuthChange()
  }, [])

  const value = useMemo(() => ({
    user,
    loading,
    error,
    login,
    signup,
    logout,
    refresh,
  }), [user, loading, error, login, signup, logout, refresh])

  return (
    <AuthContext.Provider value={value}>
      {children}
    </AuthContext.Provider>
  )
}
