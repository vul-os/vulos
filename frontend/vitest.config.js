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
    // Vitest's 5s default is a coin flip for this suite, and the coin is
    // weighted by whatever else the machine happens to be doing. Three tests —
    // OfflineLockScreen's first, appScreenTitle's first, and AppIcons' icon
    // coverage — failed a full run with "Test timed out in 5000ms" while a
    // container build, a Playwright run and two pegged node processes shared
    // the box, and passed on the same commit when it was quiet. None of them
    // is idle in that time: appScreenTitle's does `vi.resetModules()` and then
    // imports the WHOLE App graph cold, and AppIcons' renders every one of the
    // ~110 statically-registered app tiles.
    //
    // A timeout is not an assertion, so raising it weakens nothing — a test
    // that genuinely hangs still fails, just later. A load-dependent one stops
    // reporting a busy CPU as a product defect, which is the more expensive
    // error: this repo has already lost a day to a bad measurement.
    testTimeout: 20000,
    hookTimeout: 20000,
    // test-setup.js: jest-dom matchers for every test.
    // integration/setup.js: boots the MSW mock backend for the wave-28
    //   integration suite. Unit tests that replace global.fetch bypass MSW
    //   entirely (MSW patches the real fetch; a reassigned global.fetch wins),
    //   so loading it globally is a no-op for them and keeps a single config.
    setupFiles: ['./src/test-setup.js', './src/__tests__/integration/setup.js'],
    include: [
      // js/jsx/ts/tsx: the JS->TS migration (see tsconfig.json) is converting
      // test files in place (e.g. src/auth/*.test.tsx) — without ts/tsx here
      // those files are silently never collected (0 failures, but also 0
      // runs), which reads as a pass while being a bigger regression than a
      // failure.
      'src/**/*.test.{js,jsx,ts,tsx}',
      'apps/**/__tests__/**/*.test.{js,jsx,ts,tsx}',
    ],
  },
})
