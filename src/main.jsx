import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.jsx'
import { bootstrapOffline } from './lib/offlineBootstrap.js'

// OFFLINE-03: register the service worker, prime cloud↔LAN endpoint selection,
// and start the write-queue flush loop. Idempotent — safe under StrictMode's
// double-invoke.
bootstrapOffline()

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

window.addEventListener('error', e => showFatal('window.error', e.error || e.message))
window.addEventListener('unhandledrejection', e => showFatal('unhandledrejection', e.reason))

try {
  createRoot(document.getElementById('root')).render(
    <StrictMode>
      <App />
    </StrictMode>,
  )
} catch (err) {
  showFatal('createRoot.render', err)
}
