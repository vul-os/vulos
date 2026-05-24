/**
 * vite.config.calendar.js — calendar.vulos.org bundle
 *
 * Builds to dist-calendar/ from src/entries/calendar.jsx.
 *
 * Deploy: upload dist-calendar/ to Tigris at calendar/<sha>/
 *   fly.io SPA fallback: serve index.html for any unmatched path.
 *   TODO: wire Tigris static deploy in DEPLOY.md.
 *
 * Usage: vite build --config vite.config.calendar.js
 */

import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { resolve } from 'path'

const dir = import.meta.dirname

export default defineConfig({
  plugins: [react()],
  root: dir,
  define: {
    'import.meta.env.VITE_BUILD_TARGET': JSON.stringify('web'),
  },
  build: {
    outDir: resolve(dir, 'dist-calendar'),
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
    rollupOptions: {
      input: resolve(dir, 'index.calendar.html'),
      output: {
        manualChunks: {
          'vendor-react': ['react', 'react-dom', 'react-router-dom'],
        },
      },
    },
  },
  server: {
    port: 5176,
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
})
