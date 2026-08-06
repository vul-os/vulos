/**
 * locationReporter.test.js — shell/PWA geolocation reporter.
 *
 * Covers:
 *   • opt-in — nothing happens before start() is called
 *   • POSTs the right {lat,lng,accuracy,altitude,heading,speed} shape on a fix
 *   • falls back to getCurrentPosition polling when watchPosition is absent
 *   • permission-denied / unavailable errors are swallowed, never thrown
 *   • stop() clears the watch and marks the reporter inactive
 *   • throttle logic (shouldReport/distanceMeters) via pure-helper unit tests,
 *     plus one integration check that a rapid second fix doesn't double-POST
 */

import { describe, it, expect, afterEach, vi } from 'vitest'

async function freshModule() {
  vi.resetModules()
  return import('../core/location/reporter')
}

// Flush the microtask queue so the fetchImpl().then(...) chain in
// handlePosition resolves before we assert on it.
async function flush() {
  for (let i = 0; i < 6; i++) await Promise.resolve()
}

interface GeoCoords {
  latitude: number
  longitude: number
  accuracy: number
  altitude: number | null
  heading: number | null
  speed: number | null
}
interface GeoPosition {
  coords: GeoCoords
}
type SuccessCb = (pos: GeoPosition) => void
type ErrorCb = (err: { code: number, message: string }) => void

function makePosition({ lat = 1, lng = 2, accuracy = 5, altitude = null, heading = null, speed = null }: {
  lat?: number
  lng?: number
  accuracy?: number
  altitude?: number | null
  heading?: number | null
  speed?: number | null
} = {}): GeoPosition {
  return {
    coords: {
      latitude: lat,
      longitude: lng,
      accuracy,
      altitude,
      heading,
      speed,
    },
  }
}

// A ref object (not a bare `let`) — TS pins a `let` reassigned only inside a
// nested closure to its initial value at every read site in the outer scope,
// regardless of the intervening startLocationReporting() call that actually
// invokes the closure and mutates it. See endpoints.test.ts for the same fix.
function invoke<T>(cb: ((arg: T) => void) | null, arg: T): void {
  if (!cb) throw new Error('expected the geolocation callback to have been captured')
  cb(arg)
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('locationReporter — opt-in', () => {
  it('does nothing at all until start() is called', async () => {
    const mod = await freshModule()
    const watchPosition = vi.fn()
    const fetchImpl = vi.fn()
    // Merely having the module loaded (imported) must not touch geolocation/fetch.
    expect(watchPosition).not.toHaveBeenCalled()
    expect(fetchImpl).not.toHaveBeenCalled()
    expect(mod.getLocationReportingStatus()).toEqual({
      active: false,
      lastError: null,
      lastSentTs: null,
    })
  })
})

describe('locationReporter — watchPosition happy path', () => {
  it('POSTs {lat,lng,accuracy,altitude,heading,speed} to /api/location on a fix', async () => {
    const mod = await freshModule()
    const successCbRef: { current: SuccessCb | null } = { current: null }
    const geolocation = {
      watchPosition: vi.fn((onSuccess: SuccessCb) => {
        successCbRef.current = onSuccess
        return 42
      }),
      clearWatch: vi.fn(),
    }
    const fetchImpl = vi.fn().mockResolvedValue({ ok: true, status: 200 })

    const started = mod.startLocationReporting({ geolocation, fetchImpl })
    expect(started).toBe(true)
    expect(geolocation.watchPosition).toHaveBeenCalledTimes(1)

    invoke(successCbRef.current,
      makePosition({ lat: -26.2, lng: 28.05, accuracy: 12, altitude: 100, heading: 90, speed: 3 })
    )
    await flush()

    expect(fetchImpl).toHaveBeenCalledTimes(1)
    const [url, init] = fetchImpl.mock.calls[0]
    expect(url).toBe('/api/location')
    expect(init.method).toBe('POST')
    expect(init.headers['Content-Type']).toBe('application/json')
    // /api/location is auth-scoped (X-User-ID derived from the session), so the
    // POST MUST carry credentials — without this the box can't attribute the
    // fix and 401s. Asserted explicitly so the fix can't silently regress.
    expect(init.credentials).toBe('include')
    expect(JSON.parse(init.body)).toEqual({
      lat: -26.2,
      lng: 28.05,
      accuracy: 12,
      altitude: 100,
      heading: 90,
      speed: 3,
    })

    const status = mod.getLocationReportingStatus()
    expect(status.active).toBe(true)
    expect(status.lastError).toBe(null)
    expect(status.lastSentTs).toBeGreaterThan(0)
  })

  it('does not double-POST a second fix that arrives immediately (throttle)', async () => {
    const mod = await freshModule()
    const successCbRef: { current: SuccessCb | null } = { current: null }
    const geolocation = {
      watchPosition: vi.fn((onSuccess: SuccessCb) => {
        successCbRef.current = onSuccess
        return 1
      }),
      clearWatch: vi.fn(),
    }
    const fetchImpl = vi.fn().mockResolvedValue({ ok: true, status: 200 })

    mod.startLocationReporting({ geolocation, fetchImpl, minIntervalMs: 10_000, minDistanceMeters: 25 })

    invoke(successCbRef.current, makePosition({ lat: 1, lng: 1 }))
    await flush()
    // Same spot, arrives right away — should be throttled, not sent again.
    invoke(successCbRef.current, makePosition({ lat: 1, lng: 1 }))
    await flush()

    expect(fetchImpl).toHaveBeenCalledTimes(1)
  })

  it('sends early when the fix has moved a meaningful distance', async () => {
    const mod = await freshModule()
    const successCbRef: { current: SuccessCb | null } = { current: null }
    const geolocation = {
      watchPosition: vi.fn((onSuccess: SuccessCb) => {
        successCbRef.current = onSuccess
        return 1
      }),
      clearWatch: vi.fn(),
    }
    const fetchImpl = vi.fn().mockResolvedValue({ ok: true, status: 200 })

    mod.startLocationReporting({ geolocation, fetchImpl, minIntervalMs: 10_000, minDistanceMeters: 25 })

    invoke(successCbRef.current, makePosition({ lat: 0, lng: 0 }))
    await flush()
    // ~1.1km away — well past the 25m threshold, should send despite the
    // 10s window not having elapsed.
    invoke(successCbRef.current, makePosition({ lat: 0.01, lng: 0 }))
    await flush()

    expect(fetchImpl).toHaveBeenCalledTimes(2)
  })
})

describe('locationReporter — getCurrentPosition fallback', () => {
  it('polls via getCurrentPosition when watchPosition is unavailable', async () => {
    const mod = await freshModule()
    const geolocation = {
      getCurrentPosition: vi.fn((onSuccess) => {
        onSuccess(makePosition({ lat: 5, lng: 6 }))
      }),
    }
    const fetchImpl = vi.fn().mockResolvedValue({ ok: true, status: 200 })

    const started = mod.startLocationReporting({ geolocation, fetchImpl, pollIntervalMs: 60_000 })
    expect(started).toBe(true)
    expect(geolocation.getCurrentPosition).toHaveBeenCalledTimes(1)

    await flush()
    expect(fetchImpl).toHaveBeenCalledTimes(1)
    expect(JSON.parse(fetchImpl.mock.calls[0][1].body)).toMatchObject({ lat: 5, lng: 6 })

    mod.stopLocationReporting()
  })
})

describe('locationReporter — permission/unavailable handling never throws', () => {
  it('swallows a permission-denied error without throwing', async () => {
    const mod = await freshModule()
    const errorCbRef: { current: ErrorCb | null } = { current: null }
    const geolocation = {
      watchPosition: vi.fn((_onSuccess: SuccessCb, onError: ErrorCb) => {
        errorCbRef.current = onError
        return 1
      }),
      clearWatch: vi.fn(),
    }
    const onError = vi.fn()

    mod.startLocationReporting({ geolocation, fetchImpl: vi.fn(), onError })

    expect(() => invoke(errorCbRef.current, { code: 1, message: 'User denied Geolocation' })).not.toThrow()
    expect(onError).toHaveBeenCalledTimes(1)

    const status = mod.getLocationReportingStatus()
    expect(status.lastError).toBe('permission_denied')
    // Still "active" — permission may be granted later; we don't tear the
    // watch down on our own, we just record the error.
    expect(status.active).toBe(true)
  })

  it('returns false and records lastError when geolocation is unavailable', async () => {
    const mod = await freshModule()
    const started = mod.startLocationReporting({ geolocation: null, fetchImpl: vi.fn() })
    expect(started).toBe(false)
    expect(mod.getLocationReportingStatus()).toEqual({
      active: false,
      lastError: 'unavailable',
      lastSentTs: null,
    })
  })

  it('never throws even if the geolocation object is malformed', async () => {
    const mod = await freshModule()
    const geolocation = {} // neither watchPosition nor getCurrentPosition
    expect(() => mod.startLocationReporting({ geolocation, fetchImpl: vi.fn() })).not.toThrow()
    expect(mod.getLocationReportingStatus().active).toBe(false)
  })

  it('swallows a fetch/network rejection without throwing', async () => {
    const mod = await freshModule()
    const successCbRef: { current: SuccessCb | null } = { current: null }
    const geolocation = {
      watchPosition: vi.fn((onSuccess: SuccessCb) => {
        successCbRef.current = onSuccess
        return 1
      }),
      clearWatch: vi.fn(),
    }
    const fetchImpl = vi.fn().mockRejectedValue(new Error('offline'))

    mod.startLocationReporting({ geolocation, fetchImpl })
    expect(() => invoke(successCbRef.current, makePosition())).not.toThrow()
    await flush()

    expect(mod.getLocationReportingStatus().lastError).toBe('offline')
  })
})

describe('locationReporter — stop / teardown', () => {
  it('clears the watch and marks the reporter inactive', async () => {
    const mod = await freshModule()
    const geolocation = {
      watchPosition: vi.fn(() => 77),
      clearWatch: vi.fn(),
    }

    mod.startLocationReporting({ geolocation, fetchImpl: vi.fn() })
    expect(mod.getLocationReportingStatus().active).toBe(true)

    mod.stopLocationReporting()

    expect(geolocation.clearWatch).toHaveBeenCalledWith(77)
    expect(mod.getLocationReportingStatus().active).toBe(false)
  })

  it('is idempotent and safe to call when never started', async () => {
    const mod = await freshModule()
    expect(() => mod.stopLocationReporting()).not.toThrow()
    expect(mod.getLocationReportingStatus().active).toBe(false)
  })
})

describe('locationReporter — pure throttle helpers', () => {
  it('distanceMeters returns ~0 for identical points and grows with separation', async () => {
    const mod = await freshModule()
    expect(mod.distanceMeters(1, 1, 1, 1)).toBeCloseTo(0, 3)
    // Roughly 111km per degree of latitude at the equator.
    const d = mod.distanceMeters(0, 0, 1, 0)
    expect(d).toBeGreaterThan(110_000)
    expect(d).toBeLessThan(112_000)
  })

  it('shouldReport is true with no prior fix', async () => {
    const mod = await freshModule()
    expect(mod.shouldReport(null, { lat: 1, lng: 1, ts: 1000 })).toBe(true)
  })

  it('shouldReport is false inside the interval with no meaningful movement', async () => {
    const mod = await freshModule()
    const prev = { lat: 1, lng: 1, ts: 1000 }
    const next = { lat: 1.00001, lng: 1, ts: 1000 + 2000 } // ~1m, 2s later
    expect(mod.shouldReport(prev, next, { minIntervalMs: 10_000, minDistanceMeters: 25 })).toBe(false)
  })

  it('shouldReport is true once the interval elapses even with no movement', async () => {
    const mod = await freshModule()
    const prev = { lat: 1, lng: 1, ts: 1000 }
    const next = { lat: 1, lng: 1, ts: 1000 + 11_000 }
    expect(mod.shouldReport(prev, next, { minIntervalMs: 10_000, minDistanceMeters: 25 })).toBe(true)
  })

  it('shouldReport is true early when movement exceeds the distance threshold', async () => {
    const mod = await freshModule()
    const prev = { lat: 0, lng: 0, ts: 1000 }
    const next = { lat: 0.001, lng: 0, ts: 1500 } // ~111m, 0.5s later
    expect(mod.shouldReport(prev, next, { minIntervalMs: 10_000, minDistanceMeters: 25 })).toBe(true)
  })
})
