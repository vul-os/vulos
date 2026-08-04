import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { resolve } from 'path'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
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
    // test-setup.js: jest-dom matchers for every test.
    // integration/setup.js: boots the MSW mock backend for the wave-28
    //   integration suite. Unit tests that replace global.fetch bypass MSW
    //   entirely (MSW patches the real fetch; a reassigned global.fetch wins),
    //   so loading it globally is a no-op for them and keeps a single config.
    setupFiles: ['./src/test-setup.js', './src/__tests__/integration/setup.js'],
    include: [
      'src/**/*.test.{js,jsx}',
      'apps/**/__tests__/**/*.test.{js,jsx}',
    ],
  },
})
