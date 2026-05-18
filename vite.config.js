import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
  ],
  // DIAG (BMINIT-smoke): disable minification + emit sourcemaps so the
  // TDZ error stack is human-readable. Remove once the kiosk boot bug
  // is fixed. (vite v8 + rolldown dropped esbuild as a default dep so we
  // can't use minifyIdentifiers:false there; minify:false is simpler.)
  build: {
    sourcemap: true,
    minify: false,
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        ws: true,
      },
      '/app': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        ws: true,
      },
    },
  },
})
