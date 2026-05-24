/**
 * vite.config.office.js — office.vulos.org bundle
 *
 * Builds to dist-office/ from src/entries/office.jsx.
 * Code-splits per app (docs/sheets/slides/pdf) for optimal load times.
 *
 * Deploy: upload dist-office/ to Tigris at office/<sha>/
 *   fly.io SPA fallback: serve index.html for any unmatched path.
 *   TODO: wire Tigris static deploy in DEPLOY.md.
 *
 * Usage: vite build --config vite.config.office.js
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
    outDir: resolve(dir, 'dist-office'),
    emptyOutDir: true,
    chunkSizeWarningLimit: 2500,
    rollupOptions: {
      input: resolve(dir, 'index.office.html'),
      output: {
        manualChunks: {
          'vendor-react':  ['react', 'react-dom', 'react-router-dom'],
          'vendor-tiptap': [
            '@tiptap/react', '@tiptap/starter-kit',
            '@tiptap/extension-image', '@tiptap/extension-link',
            '@tiptap/extension-table', '@tiptap/extension-table-row',
            '@tiptap/extension-table-cell', '@tiptap/extension-table-header',
            '@tiptap/extension-text-align', '@tiptap/extension-text-style',
            '@tiptap/extension-color', '@tiptap/extension-highlight',
            '@tiptap/extension-underline', '@tiptap/extension-task-list',
            '@tiptap/extension-task-item', '@tiptap/extension-character-count',
            '@tiptap/extension-placeholder', '@tiptap/extension-typography',
          ],
          'vendor-sheets': ['@fortune-sheet/react'],
          'vendor-slides': ['reveal.js', 'pptxgenjs'],
          'vendor-export': ['docx', 'xlsx', 'file-saver', 'turndown', 'mammoth'],
          'vendor-pdf':    ['pdfjs-dist', 'pdf-lib', 'signature_pad'],
        },
      },
    },
  },
  server: {
    port: 5174,
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
})
