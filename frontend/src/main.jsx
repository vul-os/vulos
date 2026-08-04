import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.jsx'
import { getInitialResolvedTheme } from './core/ThemeProvider.jsx'
// RELAY-CLIENT-04: relay-client shared package, with OS-specific seams.
// configure() MUST run before any other relay-client import touches localStorage
// so existing OS user state under 'vulos.os.endpoints.v1' survives the migration
// (do NOT change the key — that would wipe the cached cloud↔LAN endpoint pair).
import { configure } from './lib/net/endpoints.js'
configure({ lsKeyPrefix: 'vulos.os.endpoints.v1', healthPath: '/api/auth/status' })
import { bootstrapOffline } from './lib/net/offlineBootstrap.js'
import { startOfflineQueueFlushLoop } from './lib/offlineQueue.js'

// Synchronous OS tier resolver. Reads window.__VULOS_TIER (set by the OS
// bootstrap / Setup state). Returns 'free' as a safe default. The shared
// package treats the returned value as opaque and exposes it via
// currentTierHint() for any other OS code that wants a synchronous tier
// read at boot.
function osTierHint() {
  if (typeof window !== 'undefined' && typeof window.__VULOS_TIER === 'string') {
    return window.__VULOS_TIER.toLowerCase()
  }
  return 'free'
}

// OFFLINE-03 / RELAY-CLIENT-04: register the service worker, prime cloud↔LAN
// endpoint selection, capture the OS tier hint, and start the write-queue
// flush loop. Idempotent — safe under StrictMode's double-invoke.
bootstrapOffline({
  tierHint: osTierHint,
  onBoot: () => { startOfflineQueueFlushLoop() },
})

// WAVE-13: apply the persisted shell density before first paint (no flash).
try {
  document.documentElement.dataset.density = localStorage.getItem('vulos.density') || 'comfortable'
} catch { /* localStorage unavailable — default comfortable via CSS */ }

// Apply the resolved theme (Light / Dark / System) before first paint so a
// Light or System-light user never sees a flash of the dark default on reload.
// ThemeProvider re-applies + tracks live once React mounts.
try {
  document.documentElement.setAttribute('data-theme', getInitialResolvedTheme())
} catch { /* localStorage / matchMedia unavailable — CSS :root dark default applies */ }

// Diagnostic surface: if React fails to mount (or any unhandled error fires
// before/during render), paint a *visible* error directly to the document
// body. The kiosk path is black-on-black in software-GL — an unhandled
// runtime error otherwise produces an indistinguishable blank kiosk.
function showFatal(label, err) {
  const msg = (err && (err.stack || err.message)) || String(err)
  const esc = s => String(s).replace(/[<>&]/g, c => ({'<':'&lt;','>':'&gt;','&':'&amp;'}[c]))
  document.body.style.cssText = 'margin:0;padding:0;background:#7c0035;color:#fff;font:14px ui-monospace,monospace'
  document.body.innerHTML =
    '<div style="padding:32px;max-width:1100px">' +
    '<div style="font-size:22px;font-weight:700;margin-bottom:12px;color:#ffd1e8">VULOS BOOT ERROR</div>' +
    '<div style="font-size:14px;opacity:0.85;margin-bottom:16px">React failed to mount. Source: ' + esc(label) + '</div>' +
    '<pre style="white-space:pre-wrap;background:#3d0019;padding:16px;border-radius:8px;line-height:1.5">' + esc(msg) + '</pre>' +
    '</div>'
}

// Track whether React has successfully mounted. Once mounted, runtime errors
// inside the React tree should be handled by component-level error boundaries
// (e.g. Window.jsx) rather than replacing the entire DOM with a boot error
// screen — that would wipe the running desktop.
let reactMounted = false

window.addEventListener('error', e => {
  if (!reactMounted) showFatal('window.error', e.error || e.message)
})
window.addEventListener('unhandledrejection', e => {
  if (!reactMounted) showFatal('unhandledrejection', e.reason)
})

try {
  createRoot(document.getElementById('root')).render(
    <StrictMode>
      <App />
    </StrictMode>,
  )
  reactMounted = true
} catch (err) {
  showFatal('createRoot.render', err)
}
