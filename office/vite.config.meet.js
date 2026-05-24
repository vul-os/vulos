/**
 * vite.config.meet.js — meet.vulos.org bundle (focused meeting join)
 *
 * Builds to dist-meet/ from src/entries/meet.jsx.
 * Imports CallView + Room + Meetings from apps/spaces — all three are
 * code-split so the landing page is small.
 *
 * Deploy: upload dist-meet/ to Tigris at meet/<sha>/
 *   fly.io SPA fallback: serve index.html for any unmatched path.
 *   TODO: wire Tigris static deploy in DEPLOY.md.
 *
 * Usage: vite build --config vite.config.meet.js
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
    outDir: resolve(dir, 'dist-meet'),
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
    rollupOptions: {
      input: resolve(dir, 'index.meet.html'),
      output: {
        manualChunks: {
          'vendor-react': ['react', 'react-dom', 'react-router-dom'],
        },
      },
    },
  },
  server: {
    port: 5177,
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
})
