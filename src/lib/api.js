/**
 * api.js — OS API client routed through the endpoint-failover layer.
 *
 * Every `/api/...` request goes through {@link request}, which:
 *   1. Picks the current best endpoint via selectEndpoint() (LAN → cloud →
 *      same-origin per OFFLINE-02's frozen contract — see ./endpoints.js).
 *   2. Issues the fetch with credentials: 'include' (httpOnly session cookie).
 *   3. On a *network* failure (endpoint unreachable, AbortError, DNS, etc.),
 *      invalidates the cached selection, re-probes, and retries the request
 *      once against the newly-selected endpoint.
 *
 * The OS frontend has historically called fetch('/api/...') directly from
 * dozens of components. Those raw call sites are still valid (same-origin
 * always works when the OS is served from the box), but new code — and any
 * code that needs cloud↔LAN failover — should route through this client.
 *
 * Mirrors the contract used by vulos-office and vulos-mail so all three OS
 * surfaces behave identically when the cloud is down.
 */

import {
  selectEndpoint,
  currentEndpoint,
  invalidateEndpoint,
  seedFromResolveBackend,
} from './endpoints.js'

const API_PREFIX = '/api'

// Resolve the API base URL through the endpoint-failover layer. The selected
// base is a same-origin '' by default, or a cloud/LAN origin when the OS shell
// injects window.__VULOS_ENDPOINTS__. See src/lib/endpoints.js.
async function apiBase() {
  const base = await selectEndpoint()
  return base + API_PREFIX
}

/**
 * Build the full URL for an API path using the cached selection synchronously.
 * Useful for callers that need a string URL (e.g. <img src> or <a href>).
 */
export function apiUrl(path) {
  return currentEndpoint() + API_PREFIX + path
}

/**
 * Fetch a JSON-bodied API endpoint. Throws on non-2xx; returns parsed JSON on
 * success. Handles cloud↔LAN failover transparently on network errors.
 *
 * @param {string} path  Path under /api (must start with '/').
 * @param {RequestInit} [options]
 */
export async function request(path, options = {}) {
  const headers = { 'Content-Type': 'application/json', ...(options.headers || {}) }
  const init = { ...options, headers, credentials: 'include' }

  const base = await apiBase()
  let res
  try {
    res = await fetch(base + path, init)
  } catch (netErr) {
    // Network-level failure (endpoint unreachable): invalidate the selection,
    // re-probe (cloud↔LAN failover), and retry once against the new endpoint.
    invalidateEndpoint()
    const retryBase = (await selectEndpoint({ force: true })) + API_PREFIX
    if (retryBase !== base) {
      res = await fetch(retryBase + path, init)
    } else {
      // Re-probe picked the same endpoint that just failed; nothing else to
      // try, surface the original network error.
      throw netErr
    }
  }
  if (!res.ok) {
    const err = await res.json().catch(() => ({ error: res.statusText }))
    throw Object.assign(new Error(err.error || 'Request failed'), err)
  }
  // Some endpoints return 204 No Content — don't try to parse those.
  if (res.status === 204) return null
  const text = await res.text()
  if (!text) return null
  try { return JSON.parse(text) } catch { return text }
}

/**
 * Lower-level helper for callers that need the raw Response (binary, blob,
 * streaming). Still benefits from cloud↔LAN failover.
 */
export async function rawFetch(path, options = {}) {
  const init = { credentials: 'include', ...options }
  const base = await apiBase()
  try {
    return await fetch(base + path, init)
  } catch (netErr) {
    invalidateEndpoint()
    const retryBase = (await selectEndpoint({ force: true })) + API_PREFIX
    if (retryBase !== base) return fetch(retryBase + path, init)
    throw netErr
  }
}

/**
 * Convenience facade for the most-used endpoints. Add to it freely — every
 * method automatically gets cloud↔LAN failover via {@link request}.
 */
export const api = {
  authStatus: () => request('/auth/status'),
  setupStatus: () => request('/setup/status'),

  /**
   * Call the cloud control plane's /api/resolve/backend (returns a
   * BackendTarget with optional LANCandidate) and seed the local endpoint
   * cache from the response. Safe to call opportunistically — failures fall
   * through to whatever pair is already cached.
   */
  async refreshBackendTarget() {
    try {
      const target = await request('/resolve/backend')
      if (target) seedFromResolveBackend(target)
      return target
    } catch {
      return null
    }
  },
}

// Re-export endpoint helpers so callers only need one import.
export { selectEndpoint, currentEndpoint, invalidateEndpoint, seedFromResolveBackend }
