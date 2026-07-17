import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { fileURLToPath, URL } from 'node:url'

// vulos-management — authed SPA (sign-in + console) build config.
//
// SERVED UNDER /console/ — the Go server (web/serve.go) embeds web/dist and
// mounts it at the /console/ prefix, so `base` MUST match or hashed asset URLs
// 404. Change both together.
const BASE = '/console/'

// Local dev/preview: forward the JSON API + health probes to the local
// self-host control plane binary (cmd/server, default listen :8080) so the SPA
// can talk to /api/* same-origin. Prod serves SPA + API from one origin, so
// this proxy only matters on a dev machine.
const localBackend = 'http://localhost:8080'
const localProxy = {
  '/api': localBackend,
  '/healthz': localBackend,
  '/readyz': localBackend,
  '/version': localBackend,
}

// ── THE COMMERCIAL SEAM (build-time swap) ────────────────────────────────────
// The JS analog of the Go `replace` directive / pkg/billingport injection.
//
//   import { BillingSlot, commercial } from '@vulos/commercial-seam'
//
// resolves to web/src/seam/commercial.jsx in THIS (OSS) build — every slot is a
// NoOp, `commercial.enabled === false`, and a self-hoster never sees a Pay
// button. The cloud build overrides this ONE alias to point at its real,
// closed-source implementation (which must honour the contract in web/SEAM.md)
// and gets billing/usage/invoices/pricing/provisioning UI with zero changes to
// the app itself. See web/SEAM.md for the full contract.
const COMMERCIAL_SEAM =
  process.env.VULOS_COMMERCIAL_SEAM ||
  fileURLToPath(new URL('./src/seam/commercial.jsx', import.meta.url))

// https://vite.dev/config/
export default defineConfig({
  base: BASE,
  plugins: [react()],
  resolve: {
    // Force a SINGLE React instance. When the console is composed for the cloud
    // build (build-console.mjs runs this build with VULOS_COMMERCIAL_SEAM pointed
    // at cloud/src/commercial-seam), the seam's own `react` import would
    // otherwise resolve to the cloud module's node_modules/react while the app
    // uses management/web's copy → two React copies → the dispatcher is null and
    // hooks crash ("can't access property useState, …H is null"). dedupe pins all
    // three React entrypoints to one instance. Harmless to the standalone OSS
    // build (already a single copy).
    dedupe: ['react', 'react-dom', 'react/jsx-runtime'],
    alias: {
      '@vulos/commercial-seam': COMMERCIAL_SEAM,
    },
  },
  server: { proxy: localProxy },
  preview: { proxy: localProxy },
})
