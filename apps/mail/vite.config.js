import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { resolve } from 'path'

// Builds the OS mail app as a React component library.
// The vulos shell imports MailApp directly from source (lazy import in Launchpad.jsx)
// so this build is used for standalone packaging; the shell can also consume directly
// from source via the monorepo without needing this built artifact.
export default defineConfig({
  plugins: [react()],
  build: {
    lib: {
      entry: resolve(import.meta.dirname, 'src/index.js'),
      name: 'VulosOsMailApp',
      formats: ['es'],
      fileName: () => 'index.js',
    },
    outDir: 'dist',
    sourcemap: false,
    rollupOptions: {
      external: ['react', 'react-dom', 'react/jsx-runtime', '@vulos/mail-client'],
    },
  },
})
