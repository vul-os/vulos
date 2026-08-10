# Vulos — Web-Apps Security Audit

**Branch:** `audit/SEC-WEBAPPS`  
**Date:** 2026-05-21  
**Scope:** `frontend/apps/` (20 bundled web apps *at the time of the audit*), `frontend/src/shell/Window.tsx`, `frontend/src/layouts/MobileStack.tsx`, `frontend/src/shell/Popout.tsx`, `frontend/src/App.tsx`, `backend/services/gateway/`, `backend/services/appfs/`, `backend/services/webproxy/`, `backend/cmd/server/`

> **Currency note (added by a later docs audit).** This is a dated record, and
> the bundled-app set has since drifted. Three apps named below no longer
> exist: `apps/social` (removed 2026-07-23, noted in §2), `apps/maps` and
> `apps/calendar` — calendar is now a React built-in under
> `frontend/src/builtin/calendar/`, not a bundled HTML app. Three apps that
> exist today (`image-editor`, `phone`, `system-info`) were **not** in this
> audit's scope. Findings and fixes below are preserved as written; they are
> the record of what was done in May, not a statement about today's tree.
>
> The one claim re-verified against the current tree: of the 16
> `frontend/apps/*/index.html` files present now, **15 carry the CSP meta
> tag**. The exception is `frontend/apps/site-template/index.html`, which was
> added after this audit and has none.

---

## 1. Content-Security-Policy (CSP)

### Finding
None of the 20 app HTML files had a `Content-Security-Policy` meta tag. The gateway did not inject CSP response headers. Without CSP, any XSS vulnerability becomes trivially exploitable.

### Fix applied
Added `<meta http-equiv="Content-Security-Policy">` to all 20 app `index.html` files. Policy per app:

```
default-src 'self';
script-src 'self' 'unsafe-inline';
style-src 'self' 'unsafe-inline';
img-src 'self' data: blob:;
media-src 'self' blob: [mediastream: where needed];
connect-src 'self' [ws: wss: for collab apps];
frame-src 'none' | 'self';
object-src 'none';
base-uri 'self';
```

App-specific overrides:
- **maps**: `img-src` includes `https://*.tile.openstreetmap.org` (Leaflet tile layer).
- **social**: `img-src` and `media-src` allow `https:` (Mastodon avatar/media CDNs vary by instance).
- **notes, phone, sheets, text-editor**: `connect-src` includes `ws: wss:` for WebSocket collab.
- **browser, pdf-viewer**: `frame-src 'self'` retained (browse proxy renders content in same-origin iframe).
- **camera, screenshot, voice-recorder**: `media-src` includes `mediastream:`.

`'unsafe-inline'` is required because all app scripts are inline in `index.html`. Removing it requires extracting scripts to separate files — tracked as a hardening backlog item.

Also added `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer` to:
- Gateway proxy handler (`backend/services/gateway/gateway.go`)
- Main server security headers middleware (`backend/cmd/server/main.go`)

The gateway also now injects `Permissions-Policy: camera=(), microphone=(), geolocation=(), clipboard-read=(), clipboard-write=()` on all proxied app responses, restricting browser-level permission grants regardless of app manifest.

---

## 2. XSS

### Finding — FIXED: `frontend/apps/notes/index.html` unescaped title
`renderList()` (line 108) interpolated `n.title` into innerHTML without escaping:

```js
// BEFORE (vulnerable):
${n.title || 'Untitled'}

// AFTER (fixed):
${escHtml(n.title || 'Untitled')}
```

`escHtml()` was already defined in the same file (HTML entity encoding). The note preview was already escaped; only the title was missed.

### Finding — RESOLVED (app removed 2026-07-23): `apps/social` raw Mastodon content
> The `social` (Fediverse) app was removed from Vulos on 2026-07-23, along with
> `maps` and `pdf-viewer`. This finding is therefore moot — the accepted XSS
> risk no longer ships. Their CSP/storage entries elsewhere in this doc describe
> apps that are no longer part of the OS.

*(Original finding, retained for history:)* `renderStatus()` rendered `actual.content` as raw HTML, trusting the Mastodon instance to have sanitized it. **Residual risk** was proxy-forwarded content from a non-sanitizing instance; mitigation would have been client-side DOMPurify. Removing the app eliminates the surface entirely.

### Review of other apps
All other apps that use `innerHTML` with user-supplied data were verified to use an `esc()`/`escHtml()`/`escapeHtml()` function. Each app defines its own local escape function — consistent coverage.

**No `dangerouslySetInnerHTML` found** in any React JSX component.

---

## 3. Path Traversal

### Backend file-serve endpoints reviewed

| Endpoint | File | Assessment |
|---|---|---|
| `appfs` `GET/PUT/DELETE /api/appdata/{app}/{path}` | `backend/services/appfs/appfs.go` | **Safe** — `safeJoin()` rejects `..`, absolute paths, and validates canonical prefix. |
| Desktop icons `GET /api/desktop/icon/{name}` | `backend/services/desktop/desktop.go` | **Safe** — rejects `/` and `..` in name before calling `findSystemIcon()`. |
| Static frontend `GET /` | `backend/cmd/server/main.go` | **Safe** — uses `http.FileServer(http.Dir(webrootDir))` with `filepath.Clean()`. |
| Music `/audio/{path}`, `/api/library`, `/api/art` | `frontend/apps/music/server.py` | **Safe** — `serve_audio` (`:229-236`) and `serve_art` (`:204-213`) `realpath` the joined path and reject anything not under `MUSIC_DIR`. |
| Gallery `/media/{path}`, `/api/media` | `frontend/apps/gallery/server.py` | **Safe** — `serve_media` (`:370-378`) `realpath`s and confines to `MEDIA_DIR`. |
| Screenshot `/api/file/{name}`, `/api/screenshots` | `frontend/apps/screenshot/server.py` | **Safe** — `serve_screenshot` (`:131-139`) rejects any `os.sep` or leading `.` in the name before joining to `SCREENSHOTS_DIR`. |

> **These three are not Vulos backend endpoints.** `/api/library`, `/api/media`,
> `/api/screenshots`, `/api/file/{name}` and `/audio/`, `/media/` are served by
> each app's **own Python sidecar** (`frontend/apps/*/server.py`), not by the Go
> server — none of them is registered on the backend mux. An earlier revision of
> this table listed them as "backend-dependent" and recommended Go-side work
> that has no code to apply to.

No direct `../` injection vectors found in frontend code. All file paths passed to fetch calls are either server-provided enumerations or `encodeURIComponent`-encoded user inputs going to the app's own sidecar.

**Confinement is implemented in each sidecar** using the Python equivalent of
`appfs.safeJoin()` — `os.path.realpath` plus a prefix check against the media
root. The `realpath` ordering matters and is correct in all three: the path is
resolved *before* the prefix comparison, so a symlink out of the media root is
caught rather than followed.

---

## 4. Cross-App Isolation

### App network isolation
Each app runs in a dedicated Linux network namespace (`backend/services/appnet/namespace.go`). Traffic routes: `Browser → :8080/app/{appId}/ → [auth] → namespace:port`. Apps cannot dial each other — they are isolated to loopback inside their namespace.

### Manifest capability declaration
`backend/services/appnet/manifest.go` defines `ValidPermissions` (`network`, `filesystem`, `camera`, `microphone`, etc.) and validates them at manifest load time. Apps without `"filesystem"` in permissions cannot escape their appdata sandbox.

### Runtime enforcement gap
**Finding (not patched — requires kernel-level work):** Permission validation in `manifest.go` occurs only at manifest parse time. The `Launcher` (`launcher.go`) does not enforce capability restrictions at runtime (e.g., no seccomp/landlock rules that block camera device access for apps without `"camera"` permission). The isolation is currently network-level (namespace) + filesystem-level (appfs sandbox), but device permissions are not enforced at the kernel layer.

**Recommendation:** Add a seccomp/landlock profile in `LaunchManifest` that maps declared permissions to OS-level restrictions.

### AppFS isolation
The `appfs` service correctly sandboxes each app under `~/.vulos/{appID}/` and validates canonical path prefix on every operation. Cross-app storage access would require a separate grant mechanism (not currently implemented — apps access only their own sandbox).

---

## 5. postMessage

### Finding
No `window.addEventListener('message', ...)` receiver existed in the shell host (`frontend/src/App.tsx`, `frontend/src/providers/ShellProvider.tsx`, or other shell files). Apps communicate with the backend via WebSocket and fetch, not cross-frame postMessage.

### Fix applied
Added a defensive `usePostMessageGuard()` hook to `frontend/src/App.tsx` (`Shell` component) that:
1. Registers a `message` event listener on `window`.
2. Silently discards any message from an origin other than `window.location.origin`.
3. Drops same-origin messages too (no shell command protocol is currently defined).

This guard prevents future accidental trusting of cross-origin messages if a `postMessage`-based API is later added without origin validation.

### App iframes
App iframes served under `{appId}--default.{host}` or `/app/{appId}/` path are same-origin with the gateway domain. The `allow-same-origin` sandbox flag means they share the shell's origin and can communicate via same-origin postMessage — this is by design and safe given the auth gate on all traffic.

---

## 6. Storage Scoping

### localStorage usage

| App | Keys | Assessment |
|---|---|---|
| calculator | `calc.history`, `calc.mode` | App-prefixed; single-user device. |
| clock | `clock.worldClocks` | App-prefixed. |
| pdf-viewer | `pdfviewer.doc.{id}`, `pdfviewer.recent` | App + doc scoped. |
| weather | `weather.unit`, `weather.location` | App-prefixed. |
| text-editor | `te-theme`, `te-fontsize`, `te-wrap`, `te-file` | App-prefixed. |

All localStorage keys use an app-specific prefix. Storage isolation is enforced by the browser's same-origin policy: each app runs under its own origin (gateway subdomain or path), so `localStorage` is physically isolated per-app in the browser. There is no shared singleton risk between apps.

**Note:** Vulos is a single-user personal OS. Multi-tenant sharing would require per-`user_id` key namespacing, but this is out of scope for the current architecture.

---

## 7. External Fetch

### Finding — FIXED: `frontend/apps/weather/index.html` direct external fetches
The weather app made three direct cross-origin `fetch()` calls:
1. `https://ip-api.com/json/?fields=...` — IP geolocation
2. `https://geocoding-api.open-meteo.com/v1/search?...` — city search
3. `https://api.open-meteo.com/v1/forecast?...` — weather forecast

These bypassed the host proxy and exposed the user's IP to third-party services directly from the app iframe.

**Fix applied:** All three calls now route through `/api/proxy/https://...` — the backend's `webproxy` service which resolves DNS once, pins the IP, blocks private-range targets (SSRF protection), and strips tracking headers.

### Other apps
- **maps**: Loads `tile.openstreetmap.org` tiles via Leaflet's `tileLayer()` — this is an `<img>` src load, not a `fetch()`, and is allowed by the updated CSP (`img-src https://*.tile.openstreetmap.org`). Routing tile loads through the proxy would add significant latency and is unnecessary for map tiles.
- **social**: All API calls go through `/api/` or `/proxy?` backend endpoints. No direct third-party fetches.
- All other apps: only relative `/api/...` calls found.

---

## 8. App Permissions on First Launch

### Camera (`frontend/apps/camera/index.html`)
`navigator.mediaDevices.getUserMedia()` is called on first use of the capture button (user-initiated). The browser's native permission prompt appears before any stream is granted. No silent auto-request at load time.

### Microphone (`frontend/apps/voice-recorder/index.html`)
`navigator.mediaDevices.getUserMedia({ audio: true })` is called only when the record button is pressed. User-initiated.

### Geolocation (`apps/maps/index.html`)
`navigator.geolocation.getCurrentPosition()` is called only when the "locate me" button is clicked. User-initiated.

### Notifications (`apps/calendar/index.html`, `frontend/apps/clock/index.html`)
`Notification.requestPermission()` is called only when the user explicitly clicks a "Enable Notifications" button. User-initiated.

### Clipboard (`frontend/apps/calculator/index.html`, `frontend/apps/screenshot/index.html`, `frontend/apps/text-editor/index.html`)
`navigator.clipboard.writeText/write()` is triggered by explicit copy/save actions. Clipboard read is not used (only write).

### Screen Capture (`frontend/apps/screenshot/index.html`)
`navigator.mediaDevices.getDisplayMedia()` is called only when the "Start Recording" or "Capture" button is pressed. User-initiated.

**Assessment:** All sensitive permission requests are user-initiated (tied to explicit UI actions). No permissions are requested on page load. This is compliant with best practice.

**Note:** The gateway now injects `Permissions-Policy: camera=(), microphone=(), geolocation=(), clipboard-read=(), clipboard-write=()` on proxied responses. This blocks browser-level permission grants even if an app were to request them outside user interaction. Apps that legitimately require these permissions will need their gateway route excluded from this policy header — tracked as a hardening refinement.

---

## Summary of Changes

| File | Change |
|---|---|
| `frontend/apps/*/index.html` (all 20) | Added `<meta http-equiv="Content-Security-Policy">` |
| `frontend/apps/notes/index.html` | Fixed XSS: escaped `n.title` in `renderList()` |
| `apps/social/index.html` | Added trust-assumption comment on `actual.content` |
| `frontend/apps/weather/index.html` | Rerouted 3 external fetches through `/api/proxy/` |
| `frontend/src/App.tsx` | Added `usePostMessageGuard()` hook |
| `frontend/src/shell/Window.tsx` | Added `referrerPolicy="no-referrer"` to app iframe |
| `frontend/src/layouts/MobileStack.tsx` | Added `referrerPolicy="no-referrer"` to app iframe |
| `frontend/src/shell/Popout.tsx` | Added `referrerPolicy="no-referrer"` to fullscreen iframe |
| `backend/services/gateway/gateway.go` | Added `X-Content-Type-Options`, `Referrer-Policy`, `Permissions-Policy` headers |
| `backend/cmd/server/main.go` | Added `secHeadersMiddleware` wrapping all responses |

## Remaining Hardening Backlog (not patched)

1. **Remove `'unsafe-inline'` from CSP** — Extract all app inline scripts to external `.js` files and use content-hash nonces. Significant refactor of all 20 apps.
2. **Runtime permission enforcement** — Add seccomp/landlock profiles in `LaunchManifest` mapping manifest permissions to OS-level device/syscall restrictions.
3. **Mastodon content sanitization** — Add DOMPurify to `apps/social/index.html` to sanitize `actual.content` client-side, removing trust in Mastodon instance sanitization.
4. **Media backend path confinement** — Audit gallery, music, screenshot backend endpoints that serve files by path; add explicit `filepath.Clean` + tenant-root prefix check mirroring `appfs.safeJoin`.
5. **Permissions-Policy per-app tuning** — Exempt camera/mic/geo apps (camera, voice-recorder, maps) from the blanket `Permissions-Policy` deny on the gateway.
6. **CSP report-only mode** — Enable `Content-Security-Policy-Report-Only` with a backend reporting endpoint to catch violations in production.
