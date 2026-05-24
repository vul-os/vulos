/**
 * vite.config.lib.js — library build for @vulos/office-client
 *
 * Produces dist-lib/ with ESM + CJS bundles, one entry per app.
 * Externalizes react/react-dom/react-router-dom so consumers can dedupe.
 *
 * Usage: vite build --config vite.config.lib.js
 */

import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { resolve } from 'path'

const dir = import.meta.dirname

const entries = {
  index:    resolve(dir, 'src/lib/index.js'),
  docs:     resolve(dir, 'src/apps/docs/lib.jsx'),
  sheets:   resolve(dir, 'src/apps/sheets/lib.jsx'),
  slides:   resolve(dir, 'src/apps/slides/lib.jsx'),
  pdf:      resolve(dir, 'src/apps/pdf/lib.jsx'),
  spaces:   resolve(dir, 'src/apps/spaces/lib.jsx'),
  calendar: resolve(dir, 'src/apps/calendar/lib.jsx'),
  contacts: resolve(dir, 'src/apps/contacts/lib.jsx'),
}

export default defineConfig({
  plugins: [react()],
  define: {
    'import.meta.env.VITE_BUILD_TARGET': JSON.stringify('lib'),
  },
  build: {
    lib: {
      entry: entries,
      formats: ['es', 'cjs'],
      fileName: (format, entryName) =>
        format === 'es' ? `${entryName}.js` : `${entryName}.cjs`,
    },
    outDir: 'dist-lib',
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      external: [
        'react',
        'react-dom',
        'react/jsx-runtime',
        'react-dom/client',
        'react-router-dom',
      ],
      output: {
        exports: 'named',
        globals: {
          react: 'React',
          'react-dom': 'ReactDOM',
          'react/jsx-runtime': 'ReactJSXRuntime',
          'react-router-dom': 'ReactRouterDOM',
        },
      },
    },
  },
})
