import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, act } from '@testing-library/react'

// The banner reads only { windows, activeWindow } off the shell. Mocking
// useShell drives the one input that matters here — WHICH app is focused —
// without standing up the whole provider, its persistence and its reducer.
const shellState: { windows: { id: string, appId: string }[], activeWindow: string | null } = {
  windows: [],
  activeWindow: null,
}
vi.mock('../providers/ShellProvider', () => ({
  useShell: () => shellState,
}))

import PublicAppBanner from '../shell/PublicAppBanner'

const BANNER = /publicly accessible/i

// GET /api/apps/visibility answers with the whole list; the banner picks its
// focused app out of it. `null` here means the request fails outright, which
// is the case the banner treats as "keep what you had".
function mockVisibility(get: () => { app_id: string, visibility: string }[] | null) {
  vi.stubGlobal('fetch', vi.fn(() => {
    const list = get()
    if (list === null) return Promise.reject(new Error('network down'))
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(list) })
  }))
}

function focus(appId: string) {
  shellState.windows = [{ id: 'w1', appId }]
  shellState.activeWindow = 'w1'
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  shellState.windows = []
  shellState.activeWindow = null
})

describe('PublicAppBanner — a visibility answer is only true of the app it was asked about', () => {
  it('warns for a public app', async () => {
    focus('blog')
    mockVisibility(() => [{ app_id: 'blog', visibility: 'public' }])
    render(<PublicAppBanner />)

    expect(await screen.findByText(BANNER)).toBeInTheDocument()
  })

  // THE FALSE ALARM. The banner names no app — it says "this app" — so a claim
  // carried over from the app the user just left is read as a claim about the
  // one they are now in. Its "Make private" button PATCHes the NEW app, so the
  // only escape route acts on something other than what was warned about.
  it('does not carry a public verdict over to the next app when the poll cannot answer', async () => {
    focus('blog')
    let answer: { app_id: string, visibility: string }[] | null = [{ app_id: 'blog', visibility: 'public' }]
    mockVisibility(() => answer)
    const { rerender } = render(<PublicAppBanner />)
    await screen.findByText(BANNER)

    // Switch to a different app while /api/apps/visibility is unreachable —
    // exactly the moment a user is likely to be poking at network settings.
    answer = null
    focus('journal')
    await act(async () => { rerender(<PublicAppBanner />) })

    await waitFor(() => {
      expect(screen.queryByText(BANNER)).toBeNull()
    })
  })

  // The other direction. This one passed on the broken code too — a carried-over
  // 'private' verdict and an honest "no claim yet" both render nothing, so the
  // omission is not observable from outside. Kept as a plain regression guard,
  // not as evidence of the defect above.
  it('warns for the next app once its own answer arrives', async () => {
    focus('journal')
    let answer: { app_id: string, visibility: string }[] | null = [{ app_id: 'journal', visibility: 'private' }]
    mockVisibility(() => answer)
    const { rerender } = render(<PublicAppBanner />)
    await waitFor(() => expect(fetch).toHaveBeenCalled())
    expect(screen.queryByText(BANNER)).toBeNull()

    answer = [{ app_id: 'blog', visibility: 'public' }]
    focus('blog')
    await act(async () => { rerender(<PublicAppBanner />) })

    expect(await screen.findByText(BANNER)).toBeInTheDocument()
  })

  it('says nothing at all until the box has answered about the focused app', async () => {
    focus('blog')
    let release: () => void = () => {}
    vi.stubGlobal('fetch', vi.fn(() => new Promise(resolve => {
      release = () => resolve({ ok: true, status: 200, json: () => Promise.resolve([{ app_id: 'blog', visibility: 'public' }]) })
    })))

    render(<PublicAppBanner />)
    expect(screen.queryByText(BANNER)).toBeNull()

    await act(async () => { release() })
    expect(await screen.findByText(BANNER)).toBeInTheDocument()
  })
})
