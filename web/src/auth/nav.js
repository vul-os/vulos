/**
 * auth/nav.js — basename-aware client navigation for the ported auth flow.
 *
 * The vulos-cloud auth pages were written for a marketing SPA mounted at the
 * ORIGIN ROOT: they pushState absolute app paths ("/login", "/2fa", …) and read
 * window.location.pathname as if it were the app-relative path. The management
 * console SPA is mounted under a BASENAME (/console — see router.jsx + serve.go),
 * so those bare paths must be prefixed with /console at the History-API edge and
 * stripped back off when read. This module centralises that translation so the
 * ported pages navigate through `clientNavigate` / `clientReplace` / `goPostLogin`
 * instead of touching the History API with bare paths directly.
 *
 * Each helper dispatches a synthetic `popstate` so the hand-rolled routers
 * (useRouter in router.jsx) re-render without a full page reload — preserving the
 * AuthProvider session state across auth-flow steps.
 */

import { BASENAME, toFullPath } from '../router.jsx'

/** Full pathname → app-relative path (basename stripped). */
export function toAppPath(pathname) {
  if (pathname === BASENAME) return '/'
  if (pathname.startsWith(BASENAME + '/')) return pathname.slice(BASENAME.length) || '/'
  return pathname || '/'
}

/** The current location as an app-relative path incl. query + hash. */
export function currentAppPath() {
  const p = toAppPath(window.location.pathname || '/')
  return p + (window.location.search || '') + (window.location.hash || '')
}

/** Push an app-relative path (e.g. "/2fa?return=/x") and notify the router. */
export function clientNavigate(appPath) {
  const full = toFullPath(appPath)
  try {
    if (full !== window.location.pathname + window.location.search) {
      window.history.pushState({}, '', full)
    }
    window.dispatchEvent(new PopStateEvent('popstate'))
  } catch {
    window.location.assign(full)
  }
}

/** Replace the current entry with an app-relative path and notify the router. */
export function clientReplace(appPath) {
  const full = toFullPath(appPath)
  try {
    window.history.replaceState({}, '', full)
    window.dispatchEvent(new PopStateEvent('popstate'))
  } catch {
    window.location.replace(full)
  }
}

/**
 * goPostLogin — land after a successful sign-in.
 *   • An absolute http(s) URL (a box origin) → hard navigation to that origin.
 *   • Any other value is treated as an app-relative path → in-app replace so the
 *     user lands in the console shell without a reload.
 */
export function goPostLogin(dest) {
  if (typeof dest === 'string' && /^https?:\/\//.test(dest)) {
    window.location.assign(dest)
    return
  }
  clientReplace(dest || '/')
}
