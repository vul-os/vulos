import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
  ],
  // DIAG (BMINIT-smoke): keep identifier names + sourcemaps so the TDZ
  // error stack is human-readable. Remove once the kiosk boot bug is fixed.
  build: {
    sourcemap: true,
    minify: 'esbuild',
    esbuild: { keepNames: true, minifyIdentifiers: false },
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
