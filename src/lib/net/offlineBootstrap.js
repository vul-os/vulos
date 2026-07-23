/**
 * offlineBootstrap.js — vulos-native offline boot seam (OFFLINE-03).
 *
 * GREENFIELD DECOUPLE 2026-07-23: was `@vulos/relay-client/offlineBootstrap`.
 * Re-homed natively so the OS builds/runs standalone. Contract preserved:
 *   • registers the service worker ('/sw.js') EXACTLY once, even if called
 *     repeatedly (StrictMode double-invoke safe);
 *   • primes selectEndpoint() so the first API call has a cached selection;
 *   • fires onUpdateAvailable subscribers when a new SW installs while the page
 *     is already controlled by an old one;
 *   • never throws when serviceWorker is unavailable.
 *
 * A Vite alias maps `@vulos/relay-client/offlineBootstrap` → this file.
 */

import { configure, selectEndpoint } from './endpoints.js'

let bootstrapped = false
const updateListeners = new Set()

/** onUpdateAvailable subscribes to "a new service worker is ready" events. */
export function onUpdateAvailable(cb) {
  updateListeners.add(cb)
  return () => updateListeners.delete(cb)
}

function fireUpdateAvailable() {
  for (const cb of updateListeners) {
    try {
      cb()
    } catch {
      /* a listener must never break the SW lifecycle */
    }
  }
}

function registerServiceWorker() {
  if (typeof navigator === 'undefined' || !navigator.serviceWorker) return
  const swc = navigator.serviceWorker
  Promise.resolve(swc.register('/sw.js'))
    .then((reg) => {
      if (!reg || typeof reg.addEventListener !== 'function') return
      reg.addEventListener('updatefound', () => {
        const installing = reg.installing
        if (!installing || typeof installing.addEventListener !== 'function') return
        installing.addEventListener('statechange', () => {
          // A new SW reaching "installed" while the page is ALREADY controlled
          // means an update is waiting (fresh install has no controller).
          if (installing.state === 'installed' && swc.controller) {
            fireUpdateAvailable()
          }
        })
      })
    })
    .catch(() => {
      /* SW registration failed (no HTTPS, blocked, 404) — offline degrades
         gracefully; never fatal to boot */
    })
}

/**
 * bootstrapOffline(opts) — idempotent OS offline boot.
 *
 * opts:
 *   tierHint  fn(): string  — synchronous OS tier resolver (wired into the
 *                             endpoint layer's currentTierHint()).
 *   onBoot    fn()          — called once after the SW register + endpoint
 *                             prime are kicked off (the OS starts its write-
 *                             queue flush loop here).
 */
export function bootstrapOffline(opts = {}) {
  if (bootstrapped) return
  bootstrapped = true

  if (typeof opts.tierHint === 'function') {
    configure({ tierHint: opts.tierHint })
  }

  registerServiceWorker()

  // Prime the endpoint selection so the first request doesn't pay a probe.
  try {
    Promise.resolve(selectEndpoint()).catch(() => {})
  } catch {
    /* selection is best-effort at boot */
  }

  if (typeof opts.onBoot === 'function') {
    try {
      opts.onBoot()
    } catch {
      /* onBoot failure must not abort boot */
    }
  }
}
