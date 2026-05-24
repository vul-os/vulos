/**
 * SpacesApp OS wrapper tests
 * 1. Renders with default props
 * 2. Signs out
 * 3. Routes notifications
 * 4. Reads theme
 * 5. MEET-OS-01: tier-hint injection (Free → false, Pro → true)
 * 6. MEET-OS-01: Pro-gate guard falls back to mesh on unknown / missing tier
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, act, fireEvent, waitFor } from '@testing-library/react'

vi.mock('@vulos/office-client/spaces', () => ({
  SpacesLib: vi.fn(({ onSignOut, theme, onNotification }) => (
    <div data-testid="spaces-lib" data-theme={theme}>
      <button data-testid="mock-signout" onClick={onSignOut}>Sign out</button>
      <button data-testid="mock-notify" onClick={() => onNotification?.('New message', 'Hi', 'normal')}>Notify</button>
    </div>
  )),
}))

vi.mock('../../../src/core/ThemeProvider.jsx', () => ({
  useTheme: vi.fn(() => ({ resolved: 'dark' })),
}))

import SpacesApp, { applyLiveKitFlag, resolveTier } from '../SpacesApp.jsx'

describe('SpacesApp OS wrapper', () => {
  beforeEach(() => {
    delete window.location
    window.location = { href: '' }
    delete window.__VULOS_TIER
    delete window.__VULOS_MEET_LIVEKIT
  })

  it('renders with default props and passes theme', async () => {
    await act(async () => { render(<SpacesApp />) })
    const el = screen.getByTestId('spaces-lib')
    expect(el).toBeTruthy()
    expect(el.getAttribute('data-theme')).toBe('dark')
  })

  it('onSignOut navigates to /auth/logout', async () => {
    await act(async () => { render(<SpacesApp />) })
    fireEvent.click(screen.getByTestId('mock-signout'))
    expect(window.location.href).toBe('/auth/logout')
  })

  it('onNotification dispatches vulos:notification with source=spaces', async () => {
    const events = []
    window.addEventListener('vulos:notification', e => events.push(e.detail))
    await act(async () => { render(<SpacesApp />) })
    fireEvent.click(screen.getByTestId('mock-notify'))
    window.removeEventListener('vulos:notification', e => events.push(e.detail))
    expect(events.length).toBe(1)
    expect(events[0].source).toBe('spaces')
  })

  it('reads OS theme from useTheme hook', async () => {
    await act(async () => { render(<SpacesApp />) })
    expect(screen.getByTestId('spaces-lib').getAttribute('data-theme')).toBe('dark')
  })
})

describe('MEET-OS-01 tier-hint injection', () => {
  beforeEach(() => {
    delete window.__VULOS_TIER
    delete window.__VULOS_MEET_LIVEKIT
  })

  it('applyLiveKitFlag(\'pro\') sets window.__VULOS_MEET_LIVEKIT=true', () => {
    expect(applyLiveKitFlag('pro')).toBe(true)
    expect(window.__VULOS_MEET_LIVEKIT).toBe(true)
  })

  it('applyLiveKitFlag(\'free\') sets window.__VULOS_MEET_LIVEKIT=false (mesh path)', () => {
    expect(applyLiveKitFlag('free')).toBe(false)
    expect(window.__VULOS_MEET_LIVEKIT).toBe(false)
  })

  it('applyLiveKitFlag is case-insensitive and recognises team/business/enterprise', () => {
    expect(applyLiveKitFlag('PRO')).toBe(true)
    expect(applyLiveKitFlag('Team')).toBe(true)
    expect(applyLiveKitFlag('business')).toBe(true)
    expect(applyLiveKitFlag('enterprise')).toBe(true)
  })

  it('applyLiveKitFlag falls back to false for unknown tier (Pro-gate guard)', () => {
    expect(applyLiveKitFlag('starter')).toBe(false)
    expect(applyLiveKitFlag('')).toBe(false)
    expect(applyLiveKitFlag(undefined)).toBe(false)
  })

  it('resolveTier reads window.__VULOS_TIER synchronously when set', async () => {
    window.__VULOS_TIER = 'pro'
    const t = await resolveTier({ fetchImpl: () => { throw new Error('should not be called') } })
    expect(t).toBe('pro')
  })

  it('resolveTier falls back to /api/auth/status when window.__VULOS_TIER is missing', async () => {
    const fakeFetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ tier: 'Pro', has_users: true }),
    })
    const t = await resolveTier({ fetchImpl: fakeFetch })
    expect(t).toBe('pro')
    expect(fakeFetch).toHaveBeenCalledWith('/api/auth/status', expect.any(Object))
  })

  it('resolveTier returns \'free\' on fetch failure (safe default)', async () => {
    const t = await resolveTier({ fetchImpl: () => { throw new Error('offline') } })
    expect(t).toBe('free')
  })

  it('SpacesApp mounts with window.__VULOS_TIER=pro → flag flipped before first render', async () => {
    window.__VULOS_TIER = 'pro'
    await act(async () => { render(<SpacesApp />) })
    expect(window.__VULOS_MEET_LIVEKIT).toBe(true)
  })

  it('SpacesApp mounts without tier hint → flag stays false (mesh fallback)', async () => {
    // Stub fetch so resolveTier returns 'free'
    const fakeFetch = vi.fn().mockResolvedValue({ ok: false })
    global.fetch = fakeFetch
    await act(async () => { render(<SpacesApp />) })
    await waitFor(() => {
      expect(window.__VULOS_MEET_LIVEKIT).toBe(false)
    })
    delete global.fetch
  })
})
