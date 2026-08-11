import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

// The notice exists because two tabs on one desktop is now SAFE but still
// surprising: the follower's drags snap back, because the writer's state is
// authoritative. Nothing is broken and nothing behaves as expected, which is
// the worst combination to leave unexplained.
//
// What must hold:
//   - it appears ONLY when a peer is genuinely present (a stale or empty peer
//     list must not nag someone working alone);
//   - it offers a way out without forcing one, because sharing is legitimate;
//   - it says something DIFFERENT to the tab that drives and the tab that
//     follows, since only one of them is about to be surprised.

const addDesktop = vi.fn()
let mockSession: { role: 'writer' | 'follower'; peers: unknown[] } | null = null

vi.mock('../providers/ShellProvider', () => ({
  useShell: () => ({
    session: mockSession,
    addDesktop,
    desktops: { 'desktop-1': { id: 'desktop-1', label: 'Desktop 1', windows: [], activeWindow: null } },
  }),
}))

import SharedDesktopNotice from '../shell/SharedDesktopNotice'

afterEach(() => { cleanup(); addDesktop.mockClear(); mockSession = null })

describe('SharedDesktopNotice', () => {
  it('renders nothing when this tab is alone', () => {
    mockSession = { role: 'writer', peers: [] }
    const { container } = render(<SharedDesktopNotice />)
    expect(container).toBeEmptyDOMElement()
  })

  it('renders nothing when there is no session at all', () => {
    // BroadcastChannel is unavailable in some embedded webviews. That must be
    // single-tab behaviour, not a permanent banner.
    mockSession = null
    const { container } = render(<SharedDesktopNotice />)
    expect(container).toBeEmptyDOMElement()
  })

  it('warns the FOLLOWER that its changes will not stick', () => {
    mockSession = { role: 'follower', peers: [{ tabId: 'a' }] }
    render(<SharedDesktopNotice />)
    // The follower is the tab about to be confused, so it gets the explanation.
    expect(screen.getByText(/yours will follow along/i)).toBeTruthy()
  })

  it('tells the WRITER it is the one driving', () => {
    mockSession = { role: 'writer', peers: [{ tabId: 'b' }] }
    render(<SharedDesktopNotice />)
    expect(screen.getByText(/you are driving/i)).toBeTruthy()
    expect(screen.queryByText(/yours will follow along/i)).toBeNull()
  })

  it('offers a way out that actually creates a desktop', () => {
    mockSession = { role: 'follower', peers: [{ tabId: 'a' }] }
    render(<SharedDesktopNotice />)
    fireEvent.click(screen.getByRole('button', { name: /give me my own/i }))
    expect(addDesktop).toHaveBeenCalledTimes(1)
  })

  it('lets the user keep sharing, and stops nagging when dismissed', () => {
    // Sharing is legitimate and now safe. Taking the choice away would be worse
    // than explaining it once.
    mockSession = { role: 'follower', peers: [{ tabId: 'a' }] }
    const { container } = render(<SharedDesktopNotice />)
    fireEvent.click(screen.getByRole('button', { name: /keep sharing/i }))
    expect(container).toBeEmptyDOMElement()
    expect(addDesktop).not.toHaveBeenCalled()
  })

  it('re-arms after the peer leaves and a NEW one arrives', () => {
    // A dismissal applies to the situation the user dismissed, not to every
    // future one. Dismiss now, and an hour later a genuinely new second window
    // must still announce itself.
    //
    // This is also the only test that exercises the re-arm at all: the reset was
    // moved out of an effect and into a render-time adjustment (React's pattern
    // for resetting state when an input changes), and disabling the reset
    // entirely still passed every other test in this file.
    mockSession = { role: 'follower', peers: [{ tabId: 'a' }] }
    const { container, rerender } = render(<SharedDesktopNotice />)
    fireEvent.click(screen.getByRole('button', { name: /keep sharing/i }))
    expect(container).toBeEmptyDOMElement()

    // The peer closes its tab.
    mockSession = { role: 'writer', peers: [] }
    rerender(<SharedDesktopNotice />)
    expect(container).toBeEmptyDOMElement()

    // A different window opens later. The banner is due again.
    mockSession = { role: 'follower', peers: [{ tabId: 'b' }] }
    rerender(<SharedDesktopNotice />)
    expect(container).not.toBeEmptyDOMElement()
    expect(screen.getByRole('button', { name: /keep sharing/i })).toBeTruthy()
  })

  it('counts multiple peers rather than saying "another window"', () => {
    mockSession = { role: 'writer', peers: [{ tabId: 'a' }, { tabId: 'b' }] }
    render(<SharedDesktopNotice />)
    expect(screen.getByText(/2 other windows/i)).toBeTruthy()
  })
})
