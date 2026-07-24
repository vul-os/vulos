/**
 * offlineBootstrap.js — vulos-native offline boot seam (OFFLINE-03).
 *
 * The OS owns its offline-boot layer natively.
 * Re-homed natively so the OS builds/runs standalone. Contract preserved:
 *   • registers the service worker ('/sw.js') EXACTLY once, even if called
 *     repeatedly (StrictMode double-invoke safe);
 *   • primes selectEndpoint() so the first API call has a cached selection;
 *   • fires onUpdateAvailable subscribers when a new SW installs while the page
 *     is already controlled by an old one;
 *   • applyUpdate() tells that waiting SW to skip waiting and activate, and is
 *     a safe no-op (returns false) when nothing is waiting;
 *   • never throws when serviceWorker is unavailable.
 *
 */

import { configure, selectEndpoint } from './endpoints.js'

let bootstrapped = false
const updateListeners = new Set()
// The SW that most recently finished installing while an old SW was already
// controlling the page — i.e. the one applyUpdate() should tell to activate.
let waitingWorker = null

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
            // Real browsers move the new worker to reg.waiting at this point;
            // fall back to the installing reference itself (e.g. test doubles)
            // so applyUpdate() always has a worker to signal.
            waitingWorker = reg.waiting || installing
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

/**
 * applyUpdate() tells a waiting service worker to skip waiting and activate
 * (posts { type: 'SKIP_WAITING' } — the sw.js listener applies it). Returns
 * true if a waiting worker was signalled, false if there is nothing to do.
 */
export function applyUpdate() {
  if (!waitingWorker || typeof waitingWorker.postMessage !== 'function') return false
  waitingWorker.postMessage({ type: 'SKIP_WAITING' })
  return true
}

/* Test-only: reset module state so each test starts from a clean slate.
   bootstrapOffline() is deliberately once-only per page load, so a suite that
   exercises it repeatedly needs to clear the guard between cases. */
export function _resetForTests() {
  bootstrapped = false
  updateListeners.clear()
  waitingWorker = null
}
