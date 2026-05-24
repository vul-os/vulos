/**
 * vite.config.talk.js — talk.vulos.org bundle (Spaces)
 *
 * Builds to dist-talk/ from src/entries/talk.jsx.
 *
 * Deploy: upload dist-talk/ to Tigris at talk/<sha>/
 *   fly.io SPA fallback: serve index.html for any unmatched path.
 *   TODO: wire Tigris static deploy in DEPLOY.md.
 *
 * Usage: vite build --config vite.config.talk.js
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
    outDir: resolve(dir, 'dist-talk'),
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
    rollupOptions: {
      input: resolve(dir, 'index.talk.html'),
      output: {
        manualChunks: {
          'vendor-react': ['react', 'react-dom', 'react-router-dom'],
        },
      },
    },
  },
  server: {
    port: 5175,
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
})
