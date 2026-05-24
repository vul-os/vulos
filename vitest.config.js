import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { resolve } from 'path'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      // Allow apps/mail/ to import @vulos/mail-client from the local source tree
      '@vulos/mail-client': resolve(import.meta.dirname, '../vulos-mail/webmail-vulos/src/lib.jsx'),
      // Allow office/spaces/calendar/meet OS wrappers to import @vulos/office-client
      // sub-path exports from the local vulos-office source tree
      '@vulos/office-client/docs':     resolve(import.meta.dirname, '../vulos-office/src/apps/docs/lib.jsx'),
      '@vulos/office-client/sheets':   resolve(import.meta.dirname, '../vulos-office/src/apps/sheets/lib.jsx'),
      '@vulos/office-client/slides':   resolve(import.meta.dirname, '../vulos-office/src/apps/slides/lib.jsx'),
      '@vulos/office-client/pdf':      resolve(import.meta.dirname, '../vulos-office/src/apps/pdf/lib.jsx'),
      '@vulos/office-client/spaces':   resolve(import.meta.dirname, '../vulos-office/src/apps/spaces/lib.jsx'),
      '@vulos/office-client/calendar': resolve(import.meta.dirname, '../vulos-office/src/apps/calendar/lib.jsx'),
      '@vulos/office-client/contacts': resolve(import.meta.dirname, '../vulos-office/src/apps/contacts/lib.jsx'),
      '@vulos/office-client':          resolve(import.meta.dirname, '../vulos-office/src/lib/index.js'),
      // Force a single React instance so apps/* don't get their own copy
      react: resolve(import.meta.dirname, 'node_modules/react'),
      'react-dom': resolve(import.meta.dirname, 'node_modules/react-dom'),
      'react/jsx-runtime': resolve(import.meta.dirname, 'node_modules/react/jsx-runtime'),
      'react/jsx-dev-runtime': resolve(import.meta.dirname, 'node_modules/react/jsx-dev-runtime'),
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.js'],
    include: [
      'src/**/*.test.{js,jsx}',
      'apps/**/__tests__/**/*.test.{js,jsx}',
    ],
  },
})
