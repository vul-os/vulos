// streamedBrowser.ts — starting the built-in Chromium, and reporting it when
// that fails.
//
// ── The defect this exists to close ──────────────────────────────────────────
//
// DesktopCanvas used to launch the streamed browser like this:
//
//     fetch('/api/browser/launch', { method: 'POST' })
//       .then(r => r.ok ? r.json() : null)
//       .catch(() => null)
//       .then(data => {
//         const sessionId = (data && data.id) || 'browser'
//         openWindow({ ...StreamViewer({ sessionId }) })
//       })
//
// Every failure collapsed to the same value. A 500 became `null`; `null` fell
// back to the literal session id `'browser'`, which nothing had ever created;
// and the window opened regardless. The user saw a window titled Chromium with
// a "Starting…" spinner that never resolved, and no error anywhere — in the UI,
// or in the console.
//
// On a bare-metal box that was the guaranteed outcome, because
// POST /api/browser/launch answered 500 {"error":"chromium not found"}:
// services/webbrowser/chrome.go's findBin looks for chromium/google-chrome and
// scripts/build-sh-packages.txt never put any of them in the rootfs (the
// Dockerfile did, which is why it always worked in Docker).
//
// Shipping the binary does not retire this function. A launch can still fail on
// a box with no free display, a software GPU tier with no Xvfb, or a stream pool
// that was killed. A non-ok response is an ERROR, never data — and an ok
// response with no session id is not a session either.

// Generic over the element type C so this module stays free of React types
// while still being assignable to the shell's real openWindow signature — C is
// inferred from the caller (ReactElement in the app, a plain object in tests).
export interface StreamedBrowserDeps<C> {
  openWindow: (opts: {
    appId: string
    title?: string
    icon?: string
    component?: C
    singleton?: boolean
  }) => void
  /** Builds the viewer element for a live session. Injected so this module
   *  stays free of React and of the lazy StreamViewer import. */
  viewer: (sessionId: string) => C
  /** Builds the element shown when the launch failed. */
  errorView: (message: string) => C
  fetchImpl?: typeof fetch
}

/** The window title, used for both the live and the failed window so the
 *  failure replaces the browser rather than appearing beside it. */
export const STREAMED_BROWSER_TITLE = 'Chromium'

/**
 * Reads the server's reason for refusing to launch.
 *
 * The handler answers `{"error":"chromium not found"}` with a non-200
 * (services/webbrowser/chrome.go). Anything unparseable degrades to a generic
 * message rather than throwing — a failure to read the failure must not itself
 * become a silent success.
 */
export async function readLaunchError(res: Response): Promise<string> {
  let detail = ''
  try {
    const body = await res.text()
    if (body) {
      try {
        const parsed = JSON.parse(body) as { error?: unknown; detail?: unknown }
        if (typeof parsed.error === 'string') detail = parsed.error
        else if (typeof parsed.detail === 'string') detail = parsed.detail
      } catch {
        detail = body.slice(0, 200)
      }
    }
  } catch { /* fall through to the generic message */ }
  if (!detail) detail = `the browser service answered ${res.status}`
  return detail
}

/**
 * Starts the per-user streaming Chromium session and opens a window for it.
 *
 * Resolves to the session id on success, or `null` when the launch failed — in
 * which case the window it opened explains why. It never opens a viewer for a
 * session that was not created.
 */
export async function launchStreamedBrowser<C>(deps: StreamedBrowserDeps<C>): Promise<string | null> {
  const doFetch = deps.fetchImpl || fetch
  let res: Response
  try {
    res = await doFetch('/api/browser/launch', { method: 'POST' })
  } catch (e) {
    // The request never completed — offline, aborted, server gone.
    deps.openWindow({
      appId: 'browser-stream',
      title: STREAMED_BROWSER_TITLE,
      icon: 'chrome',
      singleton: true,
      component: deps.errorView(
        `Could not reach the browser service on your box (${e instanceof Error ? e.message : 'network error'}).`,
      ),
    })
    return null
  }

  if (!res.ok) {
    const detail = await readLaunchError(res)
    deps.openWindow({
      appId: 'browser-stream',
      title: STREAMED_BROWSER_TITLE,
      icon: 'chrome',
      singleton: true,
      component: deps.errorView(`Chromium could not start on your box: ${detail}`),
    })
    return null
  }

  // A 200 with no session id is not a session. Opening a viewer for it is the
  // same defect in a different disguise, so it takes the same error path.
  let sessionId = ''
  try {
    const data = (await res.json()) as { id?: unknown }
    if (typeof data?.id === 'string' && data.id) sessionId = data.id
  } catch { /* handled below */ }

  if (!sessionId) {
    deps.openWindow({
      appId: 'browser-stream',
      title: STREAMED_BROWSER_TITLE,
      icon: 'chrome',
      singleton: true,
      component: deps.errorView(
        'The browser service reported success but returned no session to connect to.',
      ),
    })
    return null
  }

  deps.openWindow({
    appId: 'browser-stream',
    title: STREAMED_BROWSER_TITLE,
    icon: 'chrome',
    singleton: true,
    component: deps.viewer(sessionId),
  })
  return sessionId
}
