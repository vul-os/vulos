// LoginScreen.test.jsx — WAVE-23 polish: verifies the sign-in submit button's
// pending + double-submit-guard behaviour (the fix that added a `submitting`
// state so an in-flight login can't be re-fired and the button shows progress).
//
// AuthProvider is mocked so LoginScreen mounts without the real provider's
// /api/auth/me side effects; fetch is stubbed per-test.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

const checkAuth = vi.fn()
vi.mock('./AuthProvider', () => ({
  useAuth: () => ({ checkAuth }),
}))
// ThemeToggle / FullscreenHint pull in browser-heavy deps; stub to keep the
// test focused on the form logic.
vi.mock('./ThemeToggle', () => ({ default: () => null }))
vi.mock('./FullscreenHint', () => ({ default: () => null }))

import LoginScreen from './LoginScreen.jsx'

// A fetch that resolves auth/status immediately (has_users:true → sign-in mode)
// but leaves the login POST pending until we release it, so we can observe the
// in-flight button state.
function makeControllableFetch() {
  let releaseLogin
  const loginPending = new Promise((res) => { releaseLogin = res })
  const fetchMock = vi.fn((url) => {
    if (String(url).includes('/api/auth/status')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ has_users: true }) })
    }
    if (String(url).includes('/api/auth/login')) {
      return loginPending
    }
    return Promise.resolve({ ok: false, json: () => Promise.resolve({}) })
  })
  return { fetchMock, resolveLogin: (val) => releaseLogin(val) }
}

beforeEach(() => {
  checkAuth.mockReset()
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

async function mountSignIn(fetchMock) {
  vi.stubGlobal('fetch', fetchMock)
  render(<LoginScreen />)
  // Wait for the auth/status check to flip out of the loading splash.
  await screen.findByPlaceholderText('Username')
}

describe('LoginScreen submit state', () => {
  it('shows a pending label and disables the button while the login is in flight', async () => {
    const { fetchMock } = makeControllableFetch()
    await mountSignIn(fetchMock)

    fireEvent.change(screen.getByPlaceholderText('Username'), { target: { value: 'ada' } })
    fireEvent.change(screen.getByPlaceholderText('Password'), { target: { value: 'hunter2' } })

    const btn = screen.getByRole('button', { name: 'Sign In' })
    fireEvent.click(btn)

    // While the POST is pending the button switches to its progress label and disables.
    const pending = await screen.findByRole('button', { name: /Signing in/i })
    expect(pending.disabled).toBe(true)
  })

  it('does not fire a second login request while one is already in flight', async () => {
    const { fetchMock } = makeControllableFetch()
    await mountSignIn(fetchMock)

    fireEvent.change(screen.getByPlaceholderText('Username'), { target: { value: 'ada' } })
    fireEvent.change(screen.getByPlaceholderText('Password'), { target: { value: 'hunter2' } })

    const btn = screen.getByRole('button', { name: 'Sign In' })
    fireEvent.click(btn)
    fireEvent.click(btn) // second click while pending — must be ignored

    await screen.findByRole('button', { name: /Signing in/i })
    const loginCalls = fetchMock.mock.calls.filter(([u]) => String(u).includes('/api/auth/login'))
    expect(loginCalls.length).toBe(1)
  })

  it('surfaces a friendly error and re-enables the button on failed credentials', async () => {
    const fetchMock = vi.fn((url) => {
      if (String(url).includes('/api/auth/status')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ has_users: true }) })
      }
      // 401 with no error body → component supplies its own copy.
      return Promise.resolve({ ok: false, json: () => Promise.resolve({}) })
    })
    await mountSignIn(fetchMock)

    fireEvent.change(screen.getByPlaceholderText('Username'), { target: { value: 'ada' } })
    fireEvent.change(screen.getByPlaceholderText('Password'), { target: { value: 'wrong' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign In' }))

    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toMatch(/Incorrect username or password/i)
    // Button returns to its idle, enabled state so the user can retry.
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Sign In' }).disabled).toBe(false)
    })
  })
})
