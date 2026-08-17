import { describe, it, expect, beforeAll, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

/**
 * THE OUTCOME: on a box with no users that says setup is outstanding, the
 * fifteen-step wizard is what the person in front of it sees. Not a login form.
 *
 * This is asserted as an outcome and not as a mechanism on purpose. The same
 * user-visible symptom — a bare "Create your account" card on a first boot —
 * has now shipped twice from two unrelated causes:
 *
 *   1. build.sh pre-created /var/lib/vulos/.setup-complete in the rootfs, so
 *      GET /api/setup/status answered {"setup_complete":true} on a pristine
 *      never-booted image (fixed in ab6e79d8).
 *   2. Setup.tsx's own mount effect called onComplete() when GET /api/setup/mode
 *      said "normal" — which it does on EVERY first boot, because the server
 *      writes db/instance.json at startup. AuthGate mounted the wizard and the
 *      wizard handed itself back.
 *
 * A test naming either mechanism would have missed the other. So this one names
 * neither: it fixes the two things a real first boot reports (no users, setup
 * outstanding) and asserts what renders. Both causes are mutation-tested
 * against it.
 *
 * It also renders the REAL Setup. firstboot-gate.test.tsx mocks it —
 * `vi.mock('../auth/Setup', …)` — which is precisely why cause 2 passed through
 * a suite written to cover this exact decision. A leaf marker cannot dismiss
 * itself.
 */

// No user is the state BOTH pre-login screens share; which one renders is the
// whole question.
let mockAuth: Record<string, unknown> = {}
vi.mock('../auth/AuthProvider', () => ({
  AuthProvider: ({ children }: { children: React.ReactNode }) => children,
  useAuth: () => mockAuth,
}))

// The wrong outcome, marked so it can be asserted absent. Setup is NOT mocked.
vi.mock('../auth/LoginScreen', () => ({
  default: () => <div data-testid="login-screen">Create your account</div>,
}))
vi.mock('../layouts/DesktopCanvas', () => ({ default: () => <div data-testid="desktop-canvas" /> }))

// AuthGate races an offline-enrolment probe against a 3s timeout before it
// renders anything. Not enrolled is the ordinary first-boot state.
vi.mock('../lib/offlineAuth', () => ({
  isEnrolled: async () => false,
  getCachedIdentity: async () => null,
}))

/**
 * Every answer GET /api/setup/mode can give, plus the ones it cannot.
 *
 * The three live values come from backend/services/bootmode/bootmode.go and are
 * checked against it from the Go side (TestModeStringsMatchFrontend), so this
 * list cannot quietly fall behind the server.
 *
 * `normal` and `setup` are the retired names, kept here as cases because they
 * are what the two shipped e2e fixtures used: onboarding-walk.ts mocked
 * `mode: 'setup'` (a state no running server can report — it writes
 * instance.json before it accepts a connection), and mock-backend.js answers
 * `{}` to anything unmocked. Both fixtures described boxes that do not exist,
 * and between them they hid the defect from every green run.
 */
const MODE_ANSWERS: Array<[label: string, body: unknown, status?: number]> = [
  ['instance_ready — what a real pristine first boot reports', { mode: 'instance_ready' }],
  ['syncing — a box joining a cluster', { mode: 'syncing' }],
  ['instance_absent — unreachable over HTTP, listed anyway', { mode: 'instance_absent' }],
  ['normal — the retired name that dismissed the wizard', { mode: 'normal' }],
  ['setup — the retired name the e2e fixture invented', { mode: 'setup' }],
  ['{} — what an unmocked endpoint answers', {}],
  ['a captive portal answering something else entirely', { hello: 'world' }],
  ['500', { error: 'unavailable' }, 500],
]

function stubFetch(modeBody: unknown, modeStatus = 200) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(typeof input === 'string' ? input : (input as Request).url ?? input)
      if (url.includes('/api/setup/status')) {
        // The one thing that decides this: the box has not been set up.
        return new Response(JSON.stringify({ setup_complete: false }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      if (url.includes('/api/setup/mode')) {
        return new Response(JSON.stringify(modeBody), {
          status: modeStatus,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      if (url.includes('/api/auth/status')) {
        return new Response(JSON.stringify({ has_users: false }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    }),
  )
}

// The shell's module graph is large; transforming it once up front keeps that
// cost off whichever test happens to run first.
beforeAll(async () => { await import('../App') }, 120_000)

beforeEach(() => {
  mockAuth = { user: null, loading: false, offline: false, unlockOffline: vi.fn(), profile: null }
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.resetModules()
})

async function bootFirstRun(modeBody: unknown, modeStatus = 200) {
  stubFetch(modeBody, modeStatus)
  vi.resetModules()
  const App = (await import('../App')).default
  return render(<App />)
}

describe('a box with no users, reporting setup_complete=false', () => {
  for (const [label, body, status] of MODE_ANSWERS) {
    it(`runs the setup wizard — mode: ${label}`, async () => {
      const { container } = await bootFirstRun(body, status)

      // Asserted on something the wizard HAS. "No login form" would also be
      // satisfied by a blank page or a crash, and this defect rendered a
      // perfectly good login form, so absence-of is exactly the wrong
      // assertion on its own.
      await waitFor(
        () => {
          expect(
            container.querySelector('.wz-root'),
            `the setup wizard did not render on a first boot (mode: ${label})`,
          ).toBeTruthy()
        },
        { timeout: 20_000 },
      )

      // And the regression, stated directly: the login form must not be what
      // the founder gets handed.
      expect(
        screen.queryByTestId('login-screen'),
        `the login screen rendered on a box with no accounts (mode: ${label})`,
      ).toBeNull()
    }, 30_000)
  }

  it('never renders the login screen before the wizard, even momentarily', async () => {
    // The failure mode was a hand-off, not a wrong first paint: the wizard
    // mounted, asked the box a question, and replaced itself. A test that only
    // looks at the settled DOM would still catch that, but one that watches
    // throughout also catches a flash of the wrong screen.
    const seen: string[] = []
    const { container } = await bootFirstRun({ mode: 'instance_ready' })
    const observer = setInterval(() => {
      if (screen.queryByTestId('login-screen')) seen.push('login')
    }, 10)
    try {
      await waitFor(() => expect(container.querySelector('.wz-root')).toBeTruthy(), { timeout: 20_000 })
      await new Promise(r => setTimeout(r, 300))
    } finally {
      clearInterval(observer)
    }
    expect(seen, 'the login screen appeared at some point during a first boot').toEqual([])
  }, 30_000)
})
