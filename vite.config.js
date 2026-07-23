import { existsSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// GREENFIELD DECOUPLE 2026-07-23: vulos owns its endpoint + offline-boot layer
// natively (src/lib/net/*), so the OS builds and runs STANDALONE without the
// sibling `@vulos/relay-client` package (`../vulos-relay/client`, not part of
// this repo). These aliases keep every existing `@vulos/relay-client/*` import
// site and test working, pointed at the native implementation.
const nativeEndpoints = fileURLToPath(new URL('./src/lib/net/endpoints.js', import.meta.url))
const nativeOfflineBootstrap = fileURLToPath(new URL('./src/lib/net/offlineBootstrap.js', import.meta.url))

// The Go backend auto-enables TLS when the mkcert dev certs exist (see
// backend/cmd/server/main.go cert probe). Mirror that probe here so the dev
// proxy speaks the same scheme — otherwise every /api call 400s with
// "client sent an HTTP request to an HTTPS server".
const backendTLS = existsSync(join(homedir(), '.vulos', 'localhost.pem'))
const backend = {
  target: `${backendTLS ? 'https' : 'http'}://localhost:8080`,
  changeOrigin: true,
  ws: true,
  secure: false, // mkcert CA isn't in Node's trust store
}

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
  ],
  resolve: {
    alias: {
      '@vulos/relay-client/endpoints': nativeEndpoints,
      '@vulos/relay-client/offlineBootstrap': nativeOfflineBootstrap,
    },
  },
  server: {
    proxy: {
      '/api': backend,
      '/app': backend,
    },
  },
})
