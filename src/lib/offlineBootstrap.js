/**
 * offlineBootstrap.js — wires up the offline-first OS shell (OFFLINE-03).
 *
 * Three responsibilities, run once at app entry:
 *   1. Register the service worker (public/sw.js) that caches the OS shell so
 *      the desktop loads with the internet — and even the box's cloud route —
 *      down.
 *   2. Prime the cloud↔LAN endpoint selection (src/lib/endpoints.js) so the
 *      first /api call already has a reachable endpoint chosen.
 *   3. Start the write-queue flush loop (src/lib/offlineQueue.js) so any
 *      locally queued writes get replayed the moment connectivity is back.
 *
 * Also exposes:
 *   • onUpdateAvailable(cb) — invoked when an updated SW is waiting to take
 *     over. The UI can then prompt the user and call applyUpdate() to swap.
 *   • applyUpdate()        — posts SKIP_WAITING to the waiting SW and reloads
 *     the page once the new worker takes over.
 *
 * Mirrors vulos-office/src/lib/offlineBootstrap.js + webmail-vulos so all
 * three OS surfaces behave identically. Idempotent: safe to import from
 * multiple entry points.
 */

import { selectEndpoint } from './endpoints.js'
import { startOfflineQueueFlushLoop } from './offlineQueue.js'

let _booted = false
let _waitingWorker = null
let _registration = null
const _updateListeners = new Set()

function notifyUpdateAvailable(worker) {
  _waitingWorker = worker
  for (const fn of _updateListeners) {
    try { fn() } catch { /* listener errors are non-fatal */ }
  }
}

/**
 * Subscribe to "a new SW is waiting" events. The callback fires whenever a
 * fresh SW has installed and is sitting in `waiting`. Returns an unsubscribe fn.
 */
export function onUpdateAvailable(cb) {
  _updateListeners.add(cb)
  // If a worker is already waiting at the time of subscription, fire once.
  if (_waitingWorker) {
    try { cb() } catch { /* non-fatal */ }
  }
  return () => _updateListeners.delete(cb)
}

/**
 * Apply a pending SW update: posts SKIP_WAITING to the waiting worker, then
 * reloads the page once it takes over (controllerchange).
 */
export function applyUpdate() {
  if (!_waitingWorker) return false
  let reloaded = false
  const reloadOnce = () => {
    if (reloaded) return
    reloaded = true
    if (typeof window !== 'undefined' && window.location) {
      window.location.reload()
    }
  }
  if (typeof navigator !== 'undefined' && navigator.serviceWorker) {
    navigator.serviceWorker.addEventListener('controllerchange', reloadOnce, { once: true })
  }
  try { _waitingWorker.postMessage({ type: 'SKIP_WAITING' }) } catch { /* non-fatal */ }
  return true
}

function wireUpdateDetection(registration) {
  _registration = registration
  // Worker already waiting at registration time.
  if (registration.waiting && navigator.serviceWorker.controller) {
    notifyUpdateAvailable(registration.waiting)
  }
  registration.addEventListener('updatefound', () => {
    const installing = registration.installing
    if (!installing) return
    installing.addEventListener('statechange', () => {
      if (installing.state === 'installed' && navigator.serviceWorker.controller) {
        // A new SW finished installing and the page is controlled by an old
        // one — there's a pending update.
        notifyUpdateAvailable(installing)
      }
    })
  })
}

export function bootstrapOffline() {
  if (_booted) return
  _booted = true

  // 1. Register the service worker for app-shell caching.
  if (typeof navigator !== 'undefined' && 'serviceWorker' in navigator) {
    const register = () => {
      navigator.serviceWorker.register('/sw.js')
        .then((reg) => { wireUpdateDetection(reg) })
        .catch(() => {
          /* SW registration failure is non-fatal; the app still runs online. */
        })
    }
    if (typeof window !== 'undefined' && document.readyState === 'complete') {
      register()
    } else if (typeof window !== 'undefined') {
      window.addEventListener('load', register, { once: true })
    }
  }

  // 2. Prime the cloud↔LAN failover decision so the first /api call has a
  //    reachable endpoint chosen. Failures are swallowed — the API client
  //    re-selects on demand.
  selectEndpoint().catch(() => {})

  // 3. Start the background flusher for queued writes.
  startOfflineQueueFlushLoop()
}

// Test-only helpers — let suites reset internal state between cases.
export function _resetForTests() {
  _booted = false
  _waitingWorker = null
  _registration = null
  _updateListeners.clear()
}
