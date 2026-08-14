import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

// The window title is how labwc decides which physical monitor this browser
// window belongs on. scripts/vulos-kiosk-genconfig.sh emits one rule per output:
//
//   <windowRule title="Vulos — Virtual-2" matchOnce="yes">
//     <action name="MoveToOutput" output="Virtual-2" />
//   </windowRule>
//
// so a window with no such title is a window the compositor cannot place.
//
// It used to be set by an effect inside the top bar's screen chip, and the top
// bar mounts only inside DesktopCanvas — after setup AND after login. The first
// two-display QEMU boot (roadmap/SCREENS-QEMU.md) came up on the setup wizard,
// which meant no top bar, no title, no matching rule, and a black second
// monitor for the entire pre-login life of the session.
//
// These tests are therefore written to be UNABLE to pass that way: not one of
// them mounts DesktopCanvas. A test that logs in first cannot tell a working
// shell from the broken one, which is exactly how this survived to a real boot.

const SHIPPED_TITLE = 'Vulos — A sovereign, web-native operating system'

// Auth state the whole tree reads. Both pre-login states — setup and login —
// are "no user", which is precisely the window in which the old code was mute.
let mockAuth: Record<string, unknown> = {}
vi.mock('../auth/AuthProvider', () => ({
  AuthProvider: ({ children }: { children: React.ReactNode }) => children,
  useAuth: () => mockAuth,
}))

// Leaf markers. The point of each test is WHICH branch of AuthGate rendered,
// not what that branch draws, and stubbing the leaves keeps a title assertion
// from depending on the setup wizard's own network calls.
vi.mock('../layouts/DesktopCanvas', () => ({ default: () => <div data-testid="desktop-canvas" /> }))
vi.mock('../auth/Setup', () => ({ default: () => <div data-testid="setup-wizard" /> }))
vi.mock('../auth/LoginScreen', () => ({ default: () => <div data-testid="login-screen" /> }))

let setupComplete = true

beforeEach(() => {
  setupComplete = true
  mockAuth = { user: null, loading: false, offline: false, unlockOffline: vi.fn(), profile: null }
  vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
    const url = String(input)
    const body = url.includes('/api/setup/status') ? { setup_complete: setupComplete } : {}
    return Promise.resolve(new Response(JSON.stringify(body), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    }))
  }))
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.history.replaceState({}, '', '/')
  document.title = SHIPPED_TITLE
  vi.resetModules()
})

// Loads App the way the browser does — fresh module evaluation against the
// current URL. resetModules matters: the title is applied at module scope, so a
// cached module would carry the previous test's answer.
async function loadApp(search: string) {
  window.history.replaceState({}, '', `/${search}`)
  document.title = SHIPPED_TITLE
  vi.resetModules()
  const mod = await import('../App')
  return mod.default
}

const TWO_SCREEN = '?screen=Virtual-2&screens=2&screenIndex=2'

describe('screen window title, outside the UI layer', () => {
  it('is set by IMPORTING the shell — nothing rendered at all', async () => {
    // The strongest statement of the fix: no React root, no route, no auth
    // state, no components. The compositor contract holds as soon as the
    // document has loaded the shell's code.
    await loadApp(TWO_SCREEN)
    expect(document.title).toBe('Vulos — Virtual-2')
    expect(document.body.querySelector('[data-testid]')).toBeNull()
  })

  it('holds DURING SETUP, with no desktop mounted — the boot that failed', async () => {
    setupComplete = false
    const App = await loadApp(TWO_SCREEN)
    render(<App />)
    await screen.findByTestId('setup-wizard')
    // The exact screendump from the failed two-display boot: setup wizard up,
    // no top bar anywhere, and the window must still be placeable.
    expect(screen.queryByTestId('desktop-canvas')).toBeNull()
    expect(document.title).toBe('Vulos — Virtual-2')
  })

  it('holds AT THE LOGIN SCREEN, with no desktop mounted', async () => {
    const App = await loadApp(TWO_SCREEN)
    render(<App />)
    await screen.findByTestId('login-screen')
    expect(screen.queryByTestId('desktop-canvas')).toBeNull()
    expect(document.title).toBe('Vulos — Virtual-2')
  })

  it('survives login rather than being re-applied by the top bar', async () => {
    // The other half of moving the writer: whatever mounts afterwards must not
    // undo it. TopBar no longer touches the title, so reaching the desktop has
    // to leave the string exactly as it was before login.
    const App = await loadApp(TWO_SCREEN)
    const { rerender } = render(<App />)
    await screen.findByTestId('login-screen')
    mockAuth = { ...mockAuth, user: { id: 'u1' } }
    rerender(<App />)
    await screen.findByTestId('desktop-canvas')
    expect(document.title).toBe('Vulos — Virtual-2')
  })

  it('leaves an ordinary tab untouched, before and after mounting', async () => {
    // No screen= parameter: a hand-opened tab, a phone on the LAN, the dev
    // server. It must look exactly as it did before multi-screen existed.
    const App = await loadApp('')
    expect(document.title).toBe(SHIPPED_TITLE)
    render(<App />)
    await screen.findByTestId('login-screen')
    expect(document.title).toBe(SHIPPED_TITLE)
  })

  it('leaves a ONE-monitor kiosk untouched even though it carries screen=', async () => {
    // The launcher passes the parameter on the single-output path too, and that
    // path uses cage, which needs no rule and wants no connector name.
    const App = await loadApp('?screen=Virtual-1&screens=1&screenIndex=1')
    render(<App />)
    await waitFor(() => expect(document.title).toBe(SHIPPED_TITLE))
  })
})
