// reporter.js — shell/PWA geolocation reporter.
//
// Feeds the box the browser's position by POSTing to /api/location.
// OPT-IN: this module does nothing at all until startLocationReporting()
// is called explicitly — no side effects on import.
//
// Design constraints:
//   • Uses navigator.geolocation.watchPosition, falling back to a manual
//     getCurrentPosition poll when watchPosition isn't available.
//   • Permission-aware: denied/unavailable/timeout errors are recorded on
//     the status object and (optionally) forwarded to opts.onError — this
//     module NEVER throws, so a caller can wire it up fire-and-forget.
//   • Throttled: won't POST more than once per opts.minIntervalMs (default
//     10s) unless the fix has moved at least opts.minDistanceMeters (default
//     25m) since the last fix actually sent.
//   • Clean teardown via stopLocationReporting() — clears the watch/poll
//     and is idempotent/safe to call even if never started.
//
// No app-specific imports — only navigator.geolocation + fetch — so this
// is generic and reusable outside Vulos too.

const DEFAULT_ENDPOINT = '/api/location'
const DEFAULT_MIN_INTERVAL_MS = 10_000
const DEFAULT_MIN_DISTANCE_M = 25
const EARTH_RADIUS_M = 6_371_000

// GeolocationPositionError codes (spec): 1=PERMISSION_DENIED 2=POSITION_UNAVAILABLE 3=TIMEOUT
const ERROR_CODE_NAMES = { 1: 'permission_denied', 2: 'position_unavailable', 3: 'timeout' }

// ── module state (singleton — one reporter per page) ────────────────────────
let watchId = null
let pollTimer = null
let activeGeo = null // the geolocation object we're currently attached to
let lastSent = null // { lat, lng, ts } of the last fix actually POSTed
let status = { active: false, lastError: null, lastSentTs: null }

// ── pure helpers (exported for unit testing) ────────────────────────────────

/** Haversine distance in meters between two lat/lng points. */
export function distanceMeters(lat1, lng1, lat2, lng2) {
  if (lat1 == null || lng1 == null || lat2 == null || lng2 == null) return Infinity
  const toRad = (d) => (d * Math.PI) / 180
  const dLat = toRad(lat2 - lat1)
  const dLng = toRad(lng2 - lng1)
  const a =
    Math.sin(dLat / 2) ** 2 +
    Math.cos(toRad(lat1)) * Math.cos(toRad(lat2)) * Math.sin(dLng / 2) ** 2
  return 2 * EARTH_RADIUS_M * Math.asin(Math.min(1, Math.sqrt(a)))
}

/**
 * shouldReport — pure throttle decision, safe to unit-test without touching
 * geolocation/fetch/timers at all.
 *
 * prev: null | { lat, lng, ts }  — last fix actually sent (ts = epoch ms)
 * next: { lat, lng, ts }         — candidate fix
 * opts: { minIntervalMs?, minDistanceMeters? }
 */
export function shouldReport(prev, next, opts = {}) {
  const minIntervalMs = opts.minIntervalMs ?? DEFAULT_MIN_INTERVAL_MS
  const minDistanceMeters = opts.minDistanceMeters ?? DEFAULT_MIN_DISTANCE_M
  if (!prev) return true
  const elapsed = next.ts - prev.ts
  if (elapsed >= minIntervalMs) return true
  return distanceMeters(prev.lat, prev.lng, next.lat, next.lng) >= minDistanceMeters
}

function toPayload(coords) {
  return {
    lat: coords.latitude,
    lng: coords.longitude,
    accuracy: coords.accuracy ?? null,
    altitude: coords.altitude ?? null,
    heading: coords.heading ?? null,
    speed: coords.speed ?? null,
  }
}

function handlePosition(position, cfg) {
  const coords = position?.coords
  if (!coords) return
  const next = { lat: coords.latitude, lng: coords.longitude, ts: Date.now() }
  if (!shouldReport(lastSent, next, cfg)) return

  const payload = toPayload(coords)
  // Optimistic — record as "sent" before the network call resolves so a
  // burst of fixes while the POST is in flight doesn't defeat the throttle.
  lastSent = next

  Promise.resolve()
    .then(() =>
      cfg.fetchImpl(cfg.endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        // Send the session cookie — /api/location is auth-scoped (X-User-ID is
        // derived from the session), same as every other authed call-site.
        credentials: 'include',
        body: JSON.stringify(payload),
      })
    )
    .then((res) => {
      status.lastError = res && res.ok === false ? `http_${res.status}` : null
      status.lastSentTs = next.ts
    })
    .catch((err) => {
      // Network failure: never throw. The next natural fix will retry —
      // we deliberately don't roll back lastSent (avoids hammering a flaky
      // network faster than the throttle window).
      status.lastError = err?.message || 'network_error'
    })
}

function handleError(err, cfg) {
  status.lastError = (err && ERROR_CODE_NAMES[err.code]) || err?.message || 'geolocation_error'
  if (typeof cfg.onError === 'function') {
    try {
      cfg.onError(err)
    } catch {
      /* never let a caller's handler break us */
    }
  }
}

/**
 * startLocationReporting(opts?) — begin watching + reporting position.
 * Returns true if a watch/poll was armed, false if geolocation/fetch is
 * unavailable or a reporter is already active. Never throws.
 *
 * opts:
 *   endpoint            string   default '/api/location'
 *   minIntervalMs       number   default 10000 — min gap between POSTs
 *   minDistanceMeters   number   default 25 — send early on this much movement
 *   enableHighAccuracy  bool     passed to geolocation
 *   timeout             number   ms, passed to geolocation
 *   maximumAge          number   ms, passed to geolocation
 *   pollIntervalMs      number   default = minIntervalMs — only used when
 *                                falling back to getCurrentPosition polling
 *   geolocation         object   default navigator.geolocation (test seam)
 *   fetchImpl           fn       default global fetch (test seam)
 *   onError             fn(err)  optional — called on any geolocation error
 */
export function startLocationReporting(opts = {}) {
  try {
    if (status.active) return false

    const geo = opts.geolocation || (typeof navigator !== 'undefined' ? navigator.geolocation : null)
    if (!geo) {
      status = { active: false, lastError: 'unavailable', lastSentTs: status.lastSentTs }
      return false
    }

    const fetchImpl = opts.fetchImpl || (typeof fetch !== 'undefined' ? fetch : null)
    if (!fetchImpl) {
      status = { active: false, lastError: 'no_fetch', lastSentTs: status.lastSentTs }
      return false
    }

    const cfg = {
      endpoint: opts.endpoint || DEFAULT_ENDPOINT,
      minIntervalMs: opts.minIntervalMs ?? DEFAULT_MIN_INTERVAL_MS,
      minDistanceMeters: opts.minDistanceMeters ?? DEFAULT_MIN_DISTANCE_M,
      fetchImpl,
      onError: opts.onError,
    }

    const geoOptions = {
      enableHighAccuracy: opts.enableHighAccuracy ?? false,
      timeout: opts.timeout ?? 20_000,
      maximumAge: opts.maximumAge ?? 5_000,
    }

    // Prime state BEFORE arming — watchPosition/getCurrentPosition may invoke
    // the callback synchronously (some hosts, and the test seams do), and
    // handlePosition reads lastSent/status. Setting them after arming would let
    // that first synchronous fix be clobbered by the post-arm reset.
    activeGeo = geo
    lastSent = null
    status = { active: true, lastError: null, lastSentTs: null }

    let armed = false

    if (typeof geo.watchPosition === 'function') {
      watchId = geo.watchPosition(
        (pos) => handlePosition(pos, cfg),
        (err) => handleError(err, cfg),
        geoOptions
      )
      armed = true
    } else if (typeof geo.getCurrentPosition === 'function') {
      // Fallback for hosts without watchPosition: poll getCurrentPosition.
      const poll = () => {
        geo.getCurrentPosition(
          (pos) => handlePosition(pos, cfg),
          (err) => handleError(err, cfg),
          geoOptions
        )
      }
      poll()
      pollTimer = setInterval(poll, opts.pollIntervalMs ?? cfg.minIntervalMs)
      armed = true
    }

    if (!armed) {
      activeGeo = null
      status = { active: false, lastError: 'unavailable', lastSentTs: status.lastSentTs }
      return false
    }

    return true
  } catch (err) {
    status = { active: false, lastError: err?.message || 'start_failed', lastSentTs: status.lastSentTs }
    return false
  }
}

/**
 * stopLocationReporting() — clears the active watch/poll and marks the
 * reporter inactive. Idempotent and safe to call even if never started.
 * Never throws.
 */
export function stopLocationReporting() {
  try {
    if (watchId != null && activeGeo && typeof activeGeo.clearWatch === 'function') {
      activeGeo.clearWatch(watchId)
    }
  } catch {
    /* ignore — teardown must never throw */
  }
  watchId = null
  if (pollTimer != null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
  activeGeo = null
  status = { ...status, active: false }
}

/** getLocationReportingStatus() — snapshot of { active, lastError, lastSentTs }. */
export function getLocationReportingStatus() {
  return { ...status }
}
