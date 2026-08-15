import { describe, it, expect, beforeAll, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

/**
 * What the shell does with the three possible answers to "has this box been set
 * up?" — yes, no, and "the box did not say".
 *
 * The third one is new. It used to be folded into "yes": any failed or slow
 * probe skipped the fifteen-step wizard, on the box most likely to be slow
 * (one that has just powered on for the first time). These tests pin the
 * replacement — a choice put to the user — and, just as importantly, pin that
 * NEITHER default is taken behind their back.
 */

// The whole tree reads auth. "No user" is both pre-login states (setup, login),
// which is precisely the window this gate lives in.
let mockAuth: Record<string, unknown> = {}
vi.mock('../auth/AuthProvider', () => ({
  AuthProvider: ({ children }: { children: React.ReactNode }) => children,
  useAuth: () => mockAuth,
}))

// Leaf markers: the point is WHICH branch of AuthGate rendered.
vi.mock('../layouts/DesktopCanvas', () => ({ default: () => <div data-testid="desktop-canvas" /> }))
vi.mock('../auth/Setup', () => ({ default: () => <div data-testid="setup-wizard" /> }))
vi.mock('../auth/LoginScreen', () => ({ default: () => <div data-testid="login-screen" /> }))

// AuthGate also races an offline-enrolment probe against a 3s timeout before it
// will render anything. Answering it immediately keeps these tests about the
// SETUP branch; "not enrolled" is the ordinary state on a first boot anyway.
vi.mock('../lib/offlineAuth', () => ({
  isEnrolled: async () => false,
  getCachedIdentity: async () => null,
}))

// The probe itself is covered by firstboot-probe.test.ts against a real fetch
// stand-in. Here it is stubbed so each ANSWER can be tested without waiting out
// a retry budget.
const probeResult = { value: undefined as unknown }
vi.mock('../lib/setupProbe', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/setupProbe')>()
  return { ...actual, probeSetupComplete: vi.fn(async () => probeResult.value) }
})

// The shell's module graph is large; transforming it the first time costs
// seconds and would otherwise land entirely on whichever test ran first, timing
// that one test out for a reason that has nothing to do with it.
beforeAll(async () => { await import('../App') }, 60_000)

beforeEach(() => {
  mockAuth = { user: null, loading: false, offline: false, unlockOffline: vi.fn(), profile: null }
  // Everything else the tree fetches (energy, notifications, …) — 200 {}.
  vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', {
    status: 200, headers: { 'Content-Type': 'application/json' },
  })))
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.resetModules()
})

async function bootWith(result: unknown) {
  probeResult.value = result
  vi.resetModules()
  const App = (await import('../App')).default
  render(<App />)
  return App
}

describe('the shell decides whether to run first-boot setup', () => {
  it('runs the wizard when the box says setup is outstanding', async () => {
    await bootWith(false)
    expect(await screen.findByTestId('setup-wizard')).toBeTruthy()
  })

  it('goes to sign-in when the box says setup is done', async () => {
    await bootWith(true)
    expect(await screen.findByTestId('login-screen')).toBeTruthy()
  })

  it('asks the user when the box never answered — and skips NOTHING on its own', async () => {
    await bootWith('unknown')

    expect(await screen.findByRole('button', { name: /run setup/i })).toBeTruthy()
    expect(screen.getByRole('button', { name: /go to sign-in/i })).toBeTruthy()

    // The old behaviour, stated as an assertion: an unanswered probe must not
    // silently land on the sign-in screen, and must not silently land in the
    // wizard either. Both defaults are wrong half the time.
    expect(screen.queryByTestId('login-screen')).toBeNull()
    expect(screen.queryByTestId('setup-wizard')).toBeNull()

    // And it must not sit on the boot spinner forever, which is what happens if
    // the unknown state is checked after the loading gate instead of before it.
    expect(screen.queryByText(/loading/i)).toBeNull()
  })

  it('"Run setup" opens the wizard', async () => {
    await bootWith('unknown')
    await userEvent.click(await screen.findByRole('button', { name: /run setup/i }))
    expect(await screen.findByTestId('setup-wizard')).toBeTruthy()
  })

  it('"Go to sign-in" opens the login screen', async () => {
    await bootWith('unknown')
    await userEvent.click(await screen.findByRole('button', { name: /go to sign-in/i }))
    expect(await screen.findByTestId('login-screen')).toBeTruthy()
  })

  it('"Ask the box again" re-probes, and honours the new answer', async () => {
    await bootWith('unknown')
    const probe = vi.mocked((await import('../lib/setupProbe')).probeSetupComplete)
    const before = probe.mock.calls.length

    // The box finished starting between the two questions.
    probeResult.value = false
    await userEvent.click(await screen.findByRole('button', { name: /ask the box again/i }))

    await waitFor(() => expect(probe.mock.calls.length).toBeGreaterThan(before))
    expect(await screen.findByTestId('setup-wizard')).toBeTruthy()
  })
})
