/**
 * router.jsx — tiny hand-rolled history-API router (no extra deps).
 *
 * Matches the vulos-cloud convention (App.jsx there uses the same approach): a
 * `path` state synced to the History API, intercepted <a> clicks for same-origin
 * navigation, and a `navigate()` helper. Kept deliberately minimal for phase 1 —
 * this is the scaffold the real console pages will hang off in phase 2.
 *
 * BASENAME: the SPA is served under /console/ (see vite.config.js `base` and
 * web/serve.go). Routes here are expressed WITHOUT the basename (e.g. "/",
 * "/dashboard"); the router strips/prepends /console at the History-API edge so
 * page code deals in clean paths.
 */

import { useCallback, useEffect, useState } from 'react'

export const BASENAME = '/console'

/** Strip the basename from a full pathname → an app-relative path ("/…"). */
function toAppPath(pathname) {
  if (pathname === BASENAME) return '/'
  if (pathname.startsWith(BASENAME + '/')) return pathname.slice(BASENAME.length) || '/'
  return pathname || '/'
}

/** Prepend the basename to an app-relative path → a full pathname. */
export function toFullPath(appPath) {
  const p = appPath.startsWith('/') ? appPath : '/' + appPath
  if (p === '/') return BASENAME + '/'
  return BASENAME + p
}

/**
 * useRouter — returns { path, navigate }.
 *   `path`     app-relative current path (basename stripped), e.g. "/" or "/dashboard".
 *   `navigate` push a new app-relative path via the History API.
 */
export function useRouter() {
  const [path, setPath] = useState(() => toAppPath(window.location.pathname))

  const navigate = useCallback((appPath) => {
    const full = toFullPath(appPath)
    if (full !== window.location.pathname) {
      window.history.pushState({}, '', full)
    }
    setPath(toAppPath(window.location.pathname))
    window.scrollTo(0, 0)
  }, [])

  useEffect(() => {
    const onPop = () => setPath(toAppPath(window.location.pathname))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  // Intercept same-origin <a data-router> clicks so nav is client-side.
  useEffect(() => {
    const onClick = (e) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
      const a = e.target.closest('a[data-router]')
      if (!a) return
      const url = new URL(a.href, window.location.origin)
      if (url.origin !== window.location.origin) return
      e.preventDefault()
      navigate(toAppPath(url.pathname))
    }
    document.addEventListener('click', onClick)
    return () => document.removeEventListener('click', onClick)
  }, [navigate])

  return { path, navigate }
}
