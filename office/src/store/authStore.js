import { create } from 'zustand'
import { api } from '../lib/api'

// Session tokens are stored exclusively in httpOnly cookies managed by the
// backend. No token is ever written to localStorage — only an opaque
// loggedIn flag is kept in memory for UX purposes.

export const useAuthStore = create((set) => ({
  status: null,
  loading: true,
  error: null,
  remainingAttempts: null,

  fetchStatus: async () => {
    try {
      const status = await api.authStatus()
      set({ status, loading: false })
    } catch {
      set({ status: { enabled: false, authenticated: true }, loading: false })
    }
  },

  login: async (password) => {
    set({ error: null, remainingAttempts: null })
    try {
      await api.login(password)
      // The backend sets an httpOnly session cookie on success.
      // We never touch localStorage for tokens.
      set({ status: { enabled: true, authenticated: true }, error: null })
    } catch (err) {
      set({ error: err.error || 'Login failed', remainingAttempts: err.remaining_attempts ?? null })
      throw err
    }
  },

  logout: async () => {
    try { await api.logout() } catch { /* ignore */ }
    // Backend clears the httpOnly cookie. No localStorage cleanup needed.
    set({ status: { enabled: true, authenticated: false }, error: null })
  },
}))
