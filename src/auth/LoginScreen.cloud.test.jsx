// LoginScreen.cloud.test.jsx — UNIFIED-SIGNIN affordance on the login screen:
// when GET /api/auth/cloud/status reports enrolled, a "Sign in with Vulos
// Cloud" button appears and switches to the cloud form, which signs in via
// POST /api/auth/cloud/login and then re-checks auth.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

const checkAuth = vi.fn()
vi.mock('./AuthProvider', () => ({
  useAuth: () => ({ checkAuth }),
}))
vi.mock('./ThemeToggle', () => ({ default: () => null }))
vi.mock('./FullscreenHint', () => ({ default: () => null }))

import LoginScreen from './LoginScreen.jsx'

function makeFetch({ enrolled }) {
  return vi.fn((url, init) => {
    const u = String(url)
    if (u.includes('/api/auth/status')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ has_users: true }) })
    }
    if (u.includes('/api/auth/cloud/status')) {
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ enrolled }) })
    }
    if (u.includes('/api/auth/cloud/login')) {
      const body = JSON.parse(init.body)
      if (body.email === 'ada@vulos.test' && body.password === 'pw') {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ session_token: 's1', account_id: 'a1' }) })
      }
      return Promise.resolve({ ok: false, status: 401, json: () => Promise.resolve({ error: 'invalid_credentials' }) })
    }
    return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) })
  })
}

beforeEach(() => checkAuth.mockReset())
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('LoginScreen cloud sign-in affordance', () => {
  it('is hidden when the box is not enrolled', async () => {
    vi.stubGlobal('fetch', makeFetch({ enrolled: false }))
    render(<LoginScreen />)
    await screen.findByPlaceholderText('Username')
    expect(screen.queryByText('Sign in with Vulos Cloud')).toBeNull()
  })

  it('appears when enrolled and completes a cloud sign-in', async () => {
    vi.stubGlobal('fetch', makeFetch({ enrolled: true }))
    render(<LoginScreen />)
    await screen.findByPlaceholderText('Username')

    fireEvent.click(await screen.findByText('Sign in with Vulos Cloud'))

    fireEvent.change(screen.getByPlaceholderText('you@vulos.org'), { target: { value: 'ada@vulos.test' } })
    fireEvent.change(screen.getByPlaceholderText('Password'), { target: { value: 'pw' } })
    fireEvent.click(screen.getByRole('button', { name: /sign in$/i }))

    await waitFor(() => expect(checkAuth).toHaveBeenCalled())
  })

  it('shows an error on bad cloud credentials', async () => {
    vi.stubGlobal('fetch', makeFetch({ enrolled: true }))
    render(<LoginScreen />)
    await screen.findByPlaceholderText('Username')

    fireEvent.click(await screen.findByText('Sign in with Vulos Cloud'))
    fireEvent.change(screen.getByPlaceholderText('you@vulos.org'), { target: { value: 'ada@vulos.test' } })
    fireEvent.change(screen.getByPlaceholderText('Password'), { target: { value: 'nope' } })
    fireEvent.click(screen.getByRole('button', { name: /sign in$/i }))

    await screen.findByRole('alert')
    expect(screen.getByRole('alert').textContent).toMatch(/incorrect email or password/i)
    expect(checkAuth).not.toHaveBeenCalled()
  })
})
