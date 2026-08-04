// AppOrigins.js — ORIGIN-01: per-app browser origins.
//
// THE INVARIANT THIS FILE EXISTS TO ENFORCE
//
//   An app frame gets `allow-same-origin` if and only if the frame's URL resolves
//   to an origin that is NOT the shell's origin.
//
// That is the whole security argument, and it is deliberately derived from the
// URL the browser will actually load — not from a flag in the app registry, not
// from a first-party allowlist, and not from what the server told us it supports.
// A registry flag can lie (a hostile app.json), an allowlist can drift, and a
// server config can be wrong. The frame URL cannot: it is exactly what the
// browser will use to compute the origin it enforces. So the worst case for any
// bug elsewhere in this change is "an app fails to get its own origin and runs
// opaque" — never "an app runs on the shell's origin with allow-same-origin",
// which is the bug that made this whole exercise necessary.
//
// Previously the shell granted `allow-same-origin` to a handful of first-party
// apps (SANDBOX-01/02) because they read localStorage at load and localStorage
// throws in an opaque origin. Since those apps were served from /app/{id}/ — the
// SHELL's origin — that grant handed them the shell's storage, cookies, DOM and
// gateway auth headers. Any one of them, compromised, was a full shell takeover.
// They now get storage over the postMessage bridge instead (see AppBridge.js).

const PATH_PREFIX = '/app/'
const DEFAULT_PROFILE = 'default'

// The base sandbox for every app frame. Note what is NOT here: allow-same-origin,
// allow-top-navigation, allow-modals, allow-pointer-lock, allow-downloads.
const BASE_SANDBOX = 'allow-scripts allow-forms allow-popups'

// Origin config as advertised by GET /api/apps/origins. Defaults to DISABLED:
// if we never hear back, apps stay on the shell's path prefix and run opaque —
// which is safe. Failing open here would mean minting URLs on hosts that do not
// resolve, so failing closed is also the only thing that works.
let originConfig = { enabled: false, baseDomain: '', profile: DEFAULT_PROFILE }

// setOriginConfig is exported for tests and for the boot fetch below.
export function setOriginConfig(cfg) {
  originConfig = {
    enabled: !!(cfg && cfg.enabled && cfg.base_domain),
    baseDomain: (cfg && cfg.base_domain) || '',
    profile: (cfg && cfg.profile) || DEFAULT_PROFILE,
  }
  return originConfig
}

export async function loadOriginConfig(fetchImpl = globalThis.fetch) {
  try {
    const res = await fetchImpl('/api/apps/origins', { credentials: 'same-origin' })
    if (!res || !res.ok) return originConfig
    return setOriginConfig(await res.json())
  } catch {
    // Network error / offline — stay disabled (opaque origins, bridge storage).
    return originConfig
  }
}

// originOf resolves a possibly-relative URL against the shell's own document and
// returns its origin, or null when it cannot be resolved (in which case the
// caller must treat it as "not distinct" and withhold allow-same-origin).
export function originOf(url, base) {
  const href = base || (typeof window !== 'undefined' ? window.location.href : undefined)
  try {
    const o = new URL(url, href).origin
    // A non-http(s) URL (blob:, data:, about:) serialises its origin as "null"
    // or as something that is never comparable to the shell's — treat as opaque.
    return o && o !== 'null' ? o : null
  } catch {
    return null
  }
}

function shellOrigin() {
  return typeof window !== 'undefined' ? window.location.origin : ''
}

// isDistinctOrigin — the single predicate that gates allow-same-origin.
export function isDistinctOrigin(url) {
  const o = originOf(url)
  return !!o && o !== shellOrigin()
}

// iframeSandboxForURL builds the sandbox attribute for an app frame.
//
// allow-same-origin is added ONLY when the frame's origin is distinct from the
// shell's. On such a frame "same origin" means the APP's own origin: the app gets
// its own localStorage/IndexedDB and a real (non-opaque) origin, and the browser
// keeps it out of the shell's. On the shell's own origin it is never added, so
// the frame runs in an opaque origin and can reach nothing.
export function iframeSandboxForURL(url) {
  return isDistinctOrigin(url) ? `${BASE_SANDBOX} allow-same-origin` : BASE_SANDBOX
}

// expectedFrameOrigin returns the origin the shell must see on postMessage events
// from this frame: the app's own origin, or the literal string 'null' for an
// opaque (sandboxed, shell-origin) frame — which is what event.origin actually
// carries for those. Used by AppBridge to reject messages from anywhere else.
export function expectedFrameOrigin(url) {
  const o = originOf(url)
  return o && o !== shellOrigin() ? o : 'null'
}

// gatewayAppId extracts the app id from a gateway path-prefix URL (/app/{id}/…),
// or null when the URL is not one. Only these URLs are rehomed onto app origins;
// an app with an absolute or non-gateway URL (an AI app on /api/ai-apps/…, a
// suite app deep link) is left exactly where it is.
export function gatewayAppId(url) {
  if (typeof url !== 'string' || !url.startsWith(PATH_PREFIX)) return null
  const rest = url.slice(PATH_PREFIX.length)
  const id = rest.split(/[/?#]/, 1)[0]
  return /^[a-z0-9][a-z0-9-]*$/.test(id) ? id : null
}

// appOriginURL builds the per-app origin URL for an app id, mirroring the server
// scheme in backend/services/appnet/origin.go: {app}--{profile}.{baseDomain},
// on the shell's own scheme and port (the gateway listens on the same port).
export function appOriginURL(appId, cfg = originConfig, loc = typeof window !== 'undefined' ? window.location : null) {
  if (!cfg.enabled || !cfg.baseDomain || !loc) return null
  if (!/^[a-z0-9][a-z0-9-]*$/.test(appId) || appId.includes('--')) return null

  // The shell must itself be served AT the base domain. If it is being browsed at
  // some other name (an alias, a tunnel hostname, a bare IP), then:
  //   - the server would not route {app}--{profile}.{thatName} to the gateway
  //     (it matches the Host suffix against VULOS_DOMAIN), and
  //   - the app's frame-ancestors would name the base-domain origin, not the one
  //     actually framing it, so the frame would be blocked anyway.
  // Refuse to mint an app origin we cannot stand behind, and fall back to the
  // path prefix — which costs the app its own origin (it runs opaque) but never
  // hands it ours.
  if (loc.hostname.toLowerCase() !== cfg.baseDomain.toLowerCase()) return null

  const port = loc.port ? `:${loc.port}` : ''
  return `${loc.protocol}//${appId}--${cfg.profile}.${cfg.baseDomain}${port}`
}

// resolveAppFrameURL maps an app's registry URL to the URL the shell should
// actually frame. When per-app origins are available, a gateway path-prefix URL
// is rehomed onto the app's own origin (preserving any sub-path). Otherwise the
// URL is returned untouched and the frame runs opaque.
export function resolveAppFrameURL(url, cfg = originConfig, loc) {
  const id = gatewayAppId(url)
  if (!id) return url
  const origin = appOriginURL(id, cfg, loc)
  if (!origin) return url
  const suffix = url.slice(PATH_PREFIX.length + id.length) // "", "/", "/deep/path"
  return origin + (suffix.startsWith('/') ? suffix : '/')
}

// appFrameURLFor builds the frame URL for an app that has no explicit registry
// url (the launch-by-id path). Falls back to the gateway path prefix when per-app
// origins are unavailable.
export function appFrameURLFor(appId, cfg = originConfig, loc) {
  return appOriginURL(appId, cfg, loc) ? `${appOriginURL(appId, cfg, loc)}/` : `${PATH_PREFIX}${appId}/`
}
