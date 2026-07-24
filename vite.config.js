import { existsSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

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
  server: {
    proxy: {
      '/api': backend,
      '/app': backend,
    },
  },
})
