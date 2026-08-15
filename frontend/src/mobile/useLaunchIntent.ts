import { useEffect } from 'react'
import { getAppById, subscribeApps, type App } from '../core/AppRegistry'
import { launchApp, type LaunchAppDeps } from '../shell/launchApp'

// MOBILE-11 — `?open=<appId>`, the deep link that makes manifest shortcuts real.
//
// A phone launcher's long-press menu ("Mail", "Calendar", "Ask") is one of the
// few genuinely mobile idioms Vulos can have for free, and it is a real answer
// to "different items on the dock": the launcher icon carries a second, smaller
// set of destinations that the dock does not have room for. Chrome on Android
// builds that menu from the manifest's `shortcuts` array, and each entry is just
// a URL.
//
// But there was no URL that opens an app. `core/launchParams.ts` is an
// in-memory seam keyed by app id — it works for the ⌘K palette, which is already
// running when it stashes a query, and cannot be reached from a cold start.
// Shipping `shortcuts` without this would have been four menu entries that all
// dump you on the home screen: decoration that looks like a feature.
//
// Kept deliberately small:
//   - It is consumed ONCE and the parameter is stripped with replaceState, so a
//     refresh (or the service worker replaying the start URL) does not silently
//     relaunch the app a second time.
//   - The app id is matched against the REGISTRY, never used to build a URL or a
//     path. An unknown id is dropped. So a crafted link can only open something
//     the user already has installed — the same set the launcher shows.
//   - The registry loads asynchronously (refreshInstalled/refreshAIApps fire at
//     boot), so a cold start can resolve the id before the list exists. It
//     subscribes and retries, with a bounded wait, rather than firing once into
//     an empty registry and losing the launch.
//
// SCOPE: mobile only. MobileStack is the only caller, so this changes nothing on
// the desktop shell, where the ⌘K palette already covers the same ground.

// How long to keep waiting for the registry after a cold start before giving up.
// Long enough for the boot fetches, short enough that a stuck backend does not
// leave a launch primed to fire minutes later on top of whatever the user is
// already doing.
const REGISTRY_WAIT_MS = 10_000

export const LAUNCH_PARAM = 'open'

/** Read `?open=<appId>` and strip it from the URL. Returns the raw id, or ''. */
export function takeLaunchIntent(): string {
  if (typeof window === 'undefined') return ''
  let id = ''
  try {
    const url = new URL(window.location.href)
    id = url.searchParams.get(LAUNCH_PARAM) || ''
    if (!id) return ''
    url.searchParams.delete(LAUNCH_PARAM)
    window.history.replaceState(window.history.state, '', url.toString())
  } catch { return '' }
  // Ids are registry keys, not paths. Anything outside this shape cannot match a
  // registry entry anyway; rejecting early keeps the value from ever reaching
  // code that might treat it as a URL fragment.
  return /^[a-zA-Z0-9._-]{1,64}$/.test(id) ? id : ''
}

// Reuse launchApp's own dependency type rather than restating the shape. A
// hand-written `Record<string, unknown>` here type-checked in isolation but was
// contravariantly incompatible with the shell's real openWindow, and it would
// have gone on silently accepting any object at all.
type LaunchIntentDeps = LaunchAppDeps

export function useLaunchIntent({ openWindow }: LaunchIntentDeps): void {
  useEffect(() => {
    const id = takeLaunchIntent()
    if (!id) return undefined

    let done = false
    const fire = (app: App): boolean => {
      if (done) return true
      done = true
      void launchApp(app, { openWindow })
      return true
    }

    const known = getAppById(id)
    if (known) { fire(known); return undefined }

    // Cold start: the registry may still be loading. Wait for it, bounded.
    const unsub = subscribeApps(() => {
      const app = getAppById(id)
      if (app) { fire(app); unsub() }
    })
    const timer = window.setTimeout(() => { done = true; unsub() }, REGISTRY_WAIT_MS)
    return () => { done = true; unsub(); window.clearTimeout(timer) }
  }, [openWindow])
}
