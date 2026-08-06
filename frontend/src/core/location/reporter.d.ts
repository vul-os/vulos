// Type declarations for reporter.js.
//
// WHY A .d.ts INSTEAD OF CONVERTING reporter.js:
// e2e/location-reporter.e2e.ts reads this file's SOURCE off disk with
// readFileSync and injects it verbatim into a browser as an inline module
// script, so it can assert against the code the box actually ships. TypeScript
// syntax cannot run in a browser, so converting reporter.js broke that spec at
// load time — Playwright then collected 0 tests in 0 files, which reads as a
// pass. The runtime file therefore stays plain JS; this declaration gives every
// TypeScript consumer the same types the conversion had produced.


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

export interface ReporterStatus {
  active: boolean
  lastError: string | null
  lastSentTs: number | null
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

/** A recorded position fix, as held by the reporter's internal state. */
export interface Fix {
  lat: number
  lng: number
  ts: number
}

export function distanceMeters(
  lat1: number | null | undefined,
  lng1: number | null | undefined,
  lat2: number | null | undefined,
  lng2: number | null | undefined,
): number
export function shouldReport(
  prev: Fix | null,
  next: Fix,
  opts?: { minIntervalMs?: number; minDistanceMeters?: number },
): boolean
export function startLocationReporting(opts?: StartLocationReportingOpts): boolean
export function stopLocationReporting(): void
export function getLocationReportingStatus(): ReporterStatus
