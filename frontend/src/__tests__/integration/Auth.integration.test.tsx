/**
 * Auth.integration.test.jsx — wave-28 shell integration suite.
 *
 * Renders the REAL LoginScreen inside the REAL AuthProvider, with MSW answering
 * /api/auth/*. Exercises the two modes the boot flow branches on:
 *   • first-boot SETUP (has_users:false → "Create your account", display-name
 *     field appears, POSTs /api/auth/register),
 *   • normal SIGN-IN (has_users:true → "Sign in", POSTs /api/auth/login),
 * plus the pending state and a friendly error on bad credentials.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { screen, cleanup, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server, http, HttpResponse } from './msw/server.js'
import { renderWithProviders } from './helpers.jsx'

import LoginScreen from '../../auth/LoginScreen.jsx'

// renderWithProviders wraps in I18nProvider + ThemeProvider + AuthProvider +
// SovereigntyProvider (see helpers.jsx). LoginScreen only needs Theme + Auth,
// but the extra two are harmless here — their endpoints (/api/assistant/status,
// /api/auth/masterkey/envelope) are answered by the default MSW handlers — and
// sharing one provider stack with the rest of the suite beats hand-rolling a
// bespoke subset per test file.
function mount() {
  return renderWithProviders(<LoginScreen />)
}

afterEach(() => cleanup())

describe('LoginScreen — SIGN-IN mode (has_users:true)', () => {
  it('renders the sign-in form (no display-name field)', async () => {
    mount()
    expect(await screen.findByText('Sign in')).toBeInTheDocument()
    expect(screen.getByLabelText('Username')).toBeInTheDocument()
    expect(screen.getByLabelText('Password')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('Your name')).toBeNull()
    expect(screen.getByRole('button', { name: 'Sign In' })).toBeInTheDocument()
  })

  it('POSTs credentials to /api/auth/login and shows the pending state', async () => {
    let loginBody: unknown = null
    let release: () => void = () => {}
    const gate = new Promise<void>(r => { release = r })
    server.use(http.post('/api/auth/login', async ({ request }) => {
      loginBody = await request.json()
      await gate
      return HttpResponse.json({ ok: true })
    }))
    const user = userEvent.setup()
    mount()
    await screen.findByLabelText('Username')
    await user.type(screen.getByLabelText('Username'), 'ada')
    await user.type(screen.getByLabelText('Password'), 'hunter2')
    await user.click(screen.getByRole('button', { name: 'Sign In' }))

    // In-flight: button flips to progress label + disables.
    const pending = await screen.findByRole('button', { name: /Signing in/i })
    expect(pending).toBeDisabled()
    expect(loginBody).toEqual({ username: 'ada', password: 'hunter2' })
    release()
  })

  it('surfaces a friendly error on rejected credentials and re-enables the button', async () => {
    server.use(http.post('/api/auth/login', () =>
      HttpResponse.json({ error: 'Incorrect username or password.' }, { status: 401 })))
    const user = userEvent.setup()
    mount()
    await screen.findByLabelText('Username')
    await user.type(screen.getByLabelText('Username'), 'ada')
    await user.type(screen.getByLabelText('Password'), 'wrong')
    await user.click(screen.getByRole('button', { name: 'Sign In' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/Incorrect username or password/i)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Sign In' })).not.toBeDisabled())
  })
})

describe('LoginScreen — first-boot SETUP mode (has_users:false)', () => {
  it('renders the create-account form with a display-name field', async () => {
    server.use(http.get('/api/auth/status', () => HttpResponse.json({ has_users: false })))
    mount()
    expect(await screen.findByText('Create your account')).toBeInTheDocument()
    expect(screen.getByLabelText('Your name')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Create Account' })).toBeInTheDocument()
  })

  it('POSTs the new account (with display_name) to /api/auth/register', async () => {
    let registerBody: unknown = null
    server.use(
      http.get('/api/auth/status', () => HttpResponse.json({ has_users: false })),
      http.post('/api/auth/register', async ({ request }) => {
        registerBody = await request.json()
        return HttpResponse.json({ ok: true })
      }),
    )
    const user = userEvent.setup()
    mount()
    await screen.findByLabelText('Your name')
    await user.type(screen.getByLabelText('Your name'), 'Ada Lovelace')
    await user.type(screen.getByLabelText('Username'), 'ada')
    await user.type(screen.getByLabelText('Password'), 'analytical-engine')
    await user.click(screen.getByRole('button', { name: 'Create Account' }))

    await waitFor(() => expect(registerBody).not.toBeNull())
    expect(registerBody).toEqual({ username: 'ada', password: 'analytical-engine', display_name: 'Ada Lovelace' })
  })
})
