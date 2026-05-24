import { writeFileSync } from 'node:fs'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// emptyOutDir wipes dist/ on every build, including the dist/.gitkeep
// placeholder that lets `go build` (//go:embed all:dist) compile before any
// frontend build exists. Recreate it after the bundle is written.
const keepGitkeep = {
  name: 'keep-dist-gitkeep',
  closeBundle() {
    writeFileSync('dist/.gitkeep', '')
  },
}

// Default config: monolithic vulos-office build (dist/).
// For subdomain builds use vite.config.{office,talk,calendar,meet}.js.
// For library build use vite.config.lib.js.
export default defineConfig({
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.js'],
    include: ['src/**/*.test.{js,jsx}', 'src/__tests__/**/*.test.{js,jsx}'],
  },
  plugins: [react(), keepGitkeep],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
    rollupOptions: {
      output: {
        manualChunks: {
          'vendor-react': ['react', 'react-dom', 'react-router-dom'],
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
          'vendor-pdf': ['pdfjs-dist', 'pdf-lib', 'signature_pad'],
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
})
