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

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function errorMessage(err: unknown, fallback: string): string {
  return (isRecord(err) && typeof err.message === 'string' ? err.message : '') || fallback
}

// ── geolocation-like seams ───────────────────────────────────────────────────
// Structural, deliberately narrower than lib.dom's `Geolocation` — this module
// only ever calls these three methods, and the test suite substitutes a plain
// object exercising a subset of them (see opts.geolocation, a test seam).
export interface GeolocationCoordsLike {
  latitude: number
  longitude: number
  accuracy?: number | null
  altitude?: number | null
  heading?: number | null
  speed?: number | null
}

export interface GeolocationPositionLike {
  coords: GeolocationCoordsLike
}

export interface GeolocationPositionErrorLike {
  code?: number
  message?: string
}

export interface GeolocationLike {
  // This module always calls both callbacks (see startLocationReporting
  // below), so `error` is required here — matching the real call sites and
  // the test-seam fakes, which likewise always accept both.
  watchPosition?(
    success: (pos: GeolocationPositionLike) => void,
    error: (err: GeolocationPositionErrorLike) => void,
    options?: PositionOptions
  ): number
  getCurrentPosition?(
    success: (pos: GeolocationPositionLike) => void,
    error: (err: GeolocationPositionErrorLike) => void,
    options?: PositionOptions
  ): void
  clearWatch?(id: number): void
}

interface Fix {
  lat: number
  lng: number
  ts: number
}

export interface ReporterStatus {
  active: boolean
  lastError: string | null
  lastSentTs: number | null
}

interface ReporterConfig {
  endpoint: string
  minIntervalMs: number
  minDistanceMeters: number
  fetchImpl: typeof fetch
  onError?: (err: GeolocationPositionErrorLike) => void
}

export interface StartLocationReportingOpts {
  endpoint?: string
  minIntervalMs?: number
  minDistanceMeters?: number
  enableHighAccuracy?: boolean
  timeout?: number
  maximumAge?: number
  pollIntervalMs?: number
  geolocation?: GeolocationLike | null
  fetchImpl?: typeof fetch
  onError?: (err: GeolocationPositionErrorLike) => void
}

const DEFAULT_ENDPOINT = '/api/location'
const DEFAULT_MIN_INTERVAL_MS = 10_000
const DEFAULT_MIN_DISTANCE_M = 25
const EARTH_RADIUS_M = 6_371_000

// GeolocationPositionError codes (spec): 1=PERMISSION_DENIED 2=POSITION_UNAVAILABLE 3=TIMEOUT
const ERROR_CODE_NAMES: Record<number, string> = { 1: 'permission_denied', 2: 'position_unavailable', 3: 'timeout' }

// ── module state (singleton — one reporter per page) ────────────────────────
let watchId: number | null = null
let pollTimer: ReturnType<typeof setInterval> | null = null
let activeGeo: GeolocationLike | null = null // the geolocation object we're currently attached to
let lastSent: Fix | null = null // last fix actually POSTed
let status: ReporterStatus = { active: false, lastError: null, lastSentTs: null }

// ── pure helpers (exported for unit testing) ────────────────────────────────

/** Haversine distance in meters between two lat/lng points. */
export function distanceMeters(
  lat1: number | null | undefined,
  lng1: number | null | undefined,
  lat2: number | null | undefined,
  lng2: number | null | undefined
): number {
  if (lat1 == null || lng1 == null || lat2 == null || lng2 == null) return Infinity
  const toRad = (d: number) => (d * Math.PI) / 180
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
export function shouldReport(prev: Fix | null, next: Fix, opts: { minIntervalMs?: number; minDistanceMeters?: number } = {}): boolean {
  const minIntervalMs = opts.minIntervalMs ?? DEFAULT_MIN_INTERVAL_MS
  const minDistanceMeters = opts.minDistanceMeters ?? DEFAULT_MIN_DISTANCE_M
  if (!prev) return true
  const elapsed = next.ts - prev.ts
  if (elapsed >= minIntervalMs) return true
  return distanceMeters(prev.lat, prev.lng, next.lat, next.lng) >= minDistanceMeters
}

interface LocationPayload {
  lat: number
  lng: number
  accuracy: number | null
  altitude: number | null
  heading: number | null
  speed: number | null
}

function toPayload(coords: GeolocationCoordsLike): LocationPayload {
  return {
    lat: coords.latitude,
    lng: coords.longitude,
    accuracy: coords.accuracy ?? null,
    altitude: coords.altitude ?? null,
    heading: coords.heading ?? null,
    speed: coords.speed ?? null,
  }
}

function handlePosition(position: GeolocationPositionLike, cfg: ReporterConfig): void {
  const coords = position?.coords
  if (!coords) return
  const next: Fix = { lat: coords.latitude, lng: coords.longitude, ts: Date.now() }
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
    .catch((err: unknown) => {
      // Network failure: never throw. The next natural fix will retry —
      // we deliberately don't roll back lastSent (avoids hammering a flaky
      // network faster than the throttle window).
      status.lastError = errorMessage(err, 'network_error')
    })
}

function handleError(err: GeolocationPositionErrorLike, cfg: ReporterConfig): void {
  status.lastError = (err && err.code != null && ERROR_CODE_NAMES[err.code]) || err?.message || 'geolocation_error'
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
export function startLocationReporting(opts: StartLocationReportingOpts = {}): boolean {
  try {
    if (status.active) return false

    const geo: GeolocationLike | null = opts.geolocation || (typeof navigator !== 'undefined' ? navigator.geolocation : null)
    if (!geo) {
      status = { active: false, lastError: 'unavailable', lastSentTs: status.lastSentTs }
      return false
    }

    // NOTE: the global fetch MUST be bound. It is stored on cfg and later called
    // as `cfg.fetchImpl(...)`, which sets `this` to cfg — and the browser's
    // fetch refuses to run unless `this` is the Window:
    //   TypeError: Failed to execute 'fetch' on 'Window': Illegal invocation
    // That rejection was swallowed by handlePosition's .catch, so every fix
    // silently failed to send while the reporter still reported itself active.
    // Unit tests did not catch it because they inject opts.fetchImpl, which is a
    // plain function with no receiver requirement; only the real browser does.
    const fetchImpl: typeof fetch | null =
      opts.fetchImpl || (typeof fetch !== 'undefined' ? fetch.bind(globalThis) : null)
    if (!fetchImpl) {
      status = { active: false, lastError: 'no_fetch', lastSentTs: status.lastSentTs }
      return false
    }

    const cfg: ReporterConfig = {
      endpoint: opts.endpoint || DEFAULT_ENDPOINT,
      minIntervalMs: opts.minIntervalMs ?? DEFAULT_MIN_INTERVAL_MS,
      minDistanceMeters: opts.minDistanceMeters ?? DEFAULT_MIN_DISTANCE_M,
      fetchImpl,
      onError: opts.onError,
    }

    const geoOptions: PositionOptions = {
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
        geo.getCurrentPosition?.(
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
    status = { active: false, lastError: errorMessage(err, 'start_failed'), lastSentTs: status.lastSentTs }
    return false
  }
}

/**
 * stopLocationReporting() — clears the active watch/poll and marks the
 * reporter inactive. Idempotent and safe to call even if never started.
 * Never throws.
 */
export function stopLocationReporting(): void {
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
export function getLocationReportingStatus(): ReporterStatus {
  return { ...status }
}
