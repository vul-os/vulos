import { defineConfig, devices } from '@playwright/test'

// Playwright E2E config for the Vulos OS shell (wave-28).
//
// The suite runs the REAL production build served by `vite preview`, and mocks
// the backend entirely at the browser network layer via page.route('**/api/**').
// No Go server, no database — every test declares the API surface it needs, so
// the browser exercises the shipping React shell against a deterministic backend.
//
// Run:  npm run test:e2e            (headless chromium)
//       npm run test:e2e -- --ui    (interactive)
//
// CI wires this via .github/workflows/e2e.yml (chromium installed with deps).

const PORT = Number(process.env.E2E_PORT || 4173)
const BASE_URL = `http://localhost:${PORT}`

export default defineConfig({
  testDir: './e2e',
  // Extension-agnostic: e2e specs are migrating to TypeScript, and a
  // js-only glob would silently collect NOTHING after the rename — zero
  // failures because zero tests, which reads as a pass.
  testMatch: '**/*.e2e.{js,ts}',
  // Fail the build on a stray test.only in CI.
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI ? [['github'], ['list']] : 'list',
  timeout: 30_000,
  expect: { timeout: 7_000 },

  use: {
    baseURL: BASE_URL,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },

  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],

  // Build once, then serve the static bundle. `reuseExistingServer` lets a dev
  // keep a preview running locally; CI always starts fresh.
  // Fails fast if another app already holds the E2E port — see the file's
  // header for the marketing-site incident that motivated it.
  globalSetup: './e2e/assert-correct-app.js',
  webServer: {
    command: `npm run build && npx vite preview --port ${PORT} --strictPort`,
    url: BASE_URL,
    reuseExistingServer: !process.env.CI,
    timeout: 180_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
})
