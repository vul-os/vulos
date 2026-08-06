// PLAIN JAVASCRIPT ON PURPOSE — do not convert this file to TypeScript.
//
// e2e/location-reporter.e2e.ts reads this file's SOURCE off disk with
// readFileSync and injects it verbatim into a browser as an inline module
// script, so the spec asserts against the exact code the box ships. TypeScript
// syntax cannot execute in a browser, so converting this file broke that spec
// at load time and Playwright silently collected 0 tests in 0 files — which
// reads as a pass. Types for consumers live in reporter.d.ts alongside.
function isRecord(x) {
  return typeof x === "object" && x !== null;
}
function errorMessage(err, fallback) {
  return (isRecord(err) && typeof err.message === "string" ? err.message : "") || fallback;
}
const DEFAULT_ENDPOINT = "/api/location";
const DEFAULT_MIN_INTERVAL_MS = 1e4;
const DEFAULT_MIN_DISTANCE_M = 25;
const EARTH_RADIUS_M = 6371e3;
const ERROR_CODE_NAMES = { 1: "permission_denied", 2: "position_unavailable", 3: "timeout" };
let watchId = null;
let pollTimer = null;
let activeGeo = null;
let lastSent = null;
let status = { active: false, lastError: null, lastSentTs: null };
function distanceMeters(lat1, lng1, lat2, lng2) {
  if (lat1 == null || lng1 == null || lat2 == null || lng2 == null) return Infinity;
  const toRad = (d) => d * Math.PI / 180;
  const dLat = toRad(lat2 - lat1);
  const dLng = toRad(lng2 - lng1);
  const a = Math.sin(dLat / 2) ** 2 + Math.cos(toRad(lat1)) * Math.cos(toRad(lat2)) * Math.sin(dLng / 2) ** 2;
  return 2 * EARTH_RADIUS_M * Math.asin(Math.min(1, Math.sqrt(a)));
}
function shouldReport(prev, next, opts = {}) {
  const minIntervalMs = opts.minIntervalMs ?? DEFAULT_MIN_INTERVAL_MS;
  const minDistanceMeters = opts.minDistanceMeters ?? DEFAULT_MIN_DISTANCE_M;
  if (!prev) return true;
  const elapsed = next.ts - prev.ts;
  if (elapsed >= minIntervalMs) return true;
  return distanceMeters(prev.lat, prev.lng, next.lat, next.lng) >= minDistanceMeters;
}
function toPayload(coords) {
  return {
    lat: coords.latitude,
    lng: coords.longitude,
    accuracy: coords.accuracy ?? null,
    altitude: coords.altitude ?? null,
    heading: coords.heading ?? null,
    speed: coords.speed ?? null
  };
}
function handlePosition(position, cfg) {
  const coords = position?.coords;
  if (!coords) return;
  const next = { lat: coords.latitude, lng: coords.longitude, ts: Date.now() };
  if (!shouldReport(lastSent, next, cfg)) return;
  const payload = toPayload(coords);
  lastSent = next;
  Promise.resolve().then(
    () => cfg.fetchImpl(cfg.endpoint, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      // Send the session cookie — /api/location is auth-scoped (X-User-ID is
      // derived from the session), same as every other authed call-site.
      credentials: "include",
      body: JSON.stringify(payload)
    })
  ).then((res) => {
    status.lastError = res && res.ok === false ? `http_${res.status}` : null;
    status.lastSentTs = next.ts;
  }).catch((err) => {
    status.lastError = errorMessage(err, "network_error");
  });
}
function handleError(err, cfg) {
  status.lastError = err && err.code != null && ERROR_CODE_NAMES[err.code] || err?.message || "geolocation_error";
  if (typeof cfg.onError === "function") {
    try {
      cfg.onError(err);
    } catch {
    }
  }
}
function startLocationReporting(opts = {}) {
  try {
    if (status.active) return false;
    const geo = opts.geolocation || (typeof navigator !== "undefined" ? navigator.geolocation : null);
    if (!geo) {
      status = { active: false, lastError: "unavailable", lastSentTs: status.lastSentTs };
      return false;
    }
    const fetchImpl = opts.fetchImpl || (typeof fetch !== "undefined" ? fetch.bind(globalThis) : null);
    if (!fetchImpl) {
      status = { active: false, lastError: "no_fetch", lastSentTs: status.lastSentTs };
      return false;
    }
    const cfg = {
      endpoint: opts.endpoint || DEFAULT_ENDPOINT,
      minIntervalMs: opts.minIntervalMs ?? DEFAULT_MIN_INTERVAL_MS,
      minDistanceMeters: opts.minDistanceMeters ?? DEFAULT_MIN_DISTANCE_M,
      fetchImpl,
      onError: opts.onError
    };
    const geoOptions = {
      enableHighAccuracy: opts.enableHighAccuracy ?? false,
      timeout: opts.timeout ?? 2e4,
      maximumAge: opts.maximumAge ?? 5e3
    };
    activeGeo = geo;
    lastSent = null;
    status = { active: true, lastError: null, lastSentTs: null };
    let armed = false;
    if (typeof geo.watchPosition === "function") {
      watchId = geo.watchPosition(
        (pos) => handlePosition(pos, cfg),
        (err) => handleError(err, cfg),
        geoOptions
      );
      armed = true;
    } else if (typeof geo.getCurrentPosition === "function") {
      const poll = () => {
        geo.getCurrentPosition?.(
          (pos) => handlePosition(pos, cfg),
          (err) => handleError(err, cfg),
          geoOptions
        );
      };
      poll();
      pollTimer = setInterval(poll, opts.pollIntervalMs ?? cfg.minIntervalMs);
      armed = true;
    }
    if (!armed) {
      activeGeo = null;
      status = { active: false, lastError: "unavailable", lastSentTs: status.lastSentTs };
      return false;
    }
    return true;
  } catch (err) {
    status = { active: false, lastError: errorMessage(err, "start_failed"), lastSentTs: status.lastSentTs };
    return false;
  }
}
function stopLocationReporting() {
  try {
    if (watchId != null && activeGeo && typeof activeGeo.clearWatch === "function") {
      activeGeo.clearWatch(watchId);
    }
  } catch {
  }
  watchId = null;
  if (pollTimer != null) {
    clearInterval(pollTimer);
    pollTimer = null;
  }
  activeGeo = null;
  status = { ...status, active: false };
}
function getLocationReportingStatus() {
  return { ...status };
}
export {
  distanceMeters,
  getLocationReportingStatus,
  shouldReport,
  startLocationReporting,
  stopLocationReporting
};
