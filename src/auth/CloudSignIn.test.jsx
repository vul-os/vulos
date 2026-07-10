// CloudSignIn.test.jsx — UNIFIED-SIGNIN frontend flow states.
//
// Exercises the useCloudSignIn state machine + CloudSignInFlowExtras rendering
// through a minimal host, with fetch stubbed to play the backend contract:
// full-session success, totp_required, email_verification_required,
// enrollment_required (start → poll → approved → auto-retry), and error paths.

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { useCloudSignIn, CloudSignInFlowExtras, cloudErrorMessage } from './CloudSignIn.jsx'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

// Minimal host wiring the hook to a submit button, mirroring how the Setup
// wizard and LoginScreen embed the flow.
function Host({ onSuccess }) {
  const flow = useCloudSignIn({ onSuccess })
  return (
    <div>
      <CloudSignInFlowExtras flow={flow} />
      {flow.error && <p role="alert">{flow.error}</p>}
      <span data-testid="phase">{flow.phase}</span>
      <button onClick={() => flow.submit({ email: 'ada@vulos.test', password: 'pw' })}>
        submit
      </button>
    </div>
  )
}

function jsonResponse(status, body) {
  return Promise.resolve({ ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) })
}

describe('useCloudSignIn', () => {
  it('calls onSuccess with the session info on a full-session response', async () => {
    const onSuccess = vi.fn()
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(200, {
      session_token: 'sess-1', account_id: 'acc-1', email: 'ada@vulos.test',
    })))

    render(<Host onSuccess={onSuccess} />)
    fireEvent.click(screen.getByText('submit'))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    expect(onSuccess.mock.calls[0][0].session_token).toBe('sess-1')
    expect(screen.getByTestId('phase').textContent).toBe('done')
  })

  it('surfaces totp_required as a TOTP input, then completes with the code', async () => {
    const onSuccess = vi.fn()
    const fetchMock = vi.fn((url, init) => {
      const body = JSON.parse(init?.body || '{}')
      if (body.totp_code) {
        return jsonResponse(200, { session_token: 'sess-2', account_id: 'acc' })
      }
      return jsonResponse(200, { step: 'totp_required' })
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<Host onSuccess={onSuccess} />)
    fireEvent.click(screen.getByText('submit'))

    // TOTP input appears.
    const totpInput = await screen.findByLabelText('Two-factor authentication code')
    fireEvent.change(totpInput, { target: { value: '424242' } })
    fireEvent.click(screen.getByText('submit'))

    await waitFor(() => expect(onSuccess).toHaveBeenCalled())
    // The second request carried the code.
    const withCode = fetchMock.mock.calls.find(([, init]) =>
      init?.body && JSON.parse(init.body).totp_code === '424242')
    expect(withCode).toBeTruthy()
  })

  it('shows a friendly notice on email_verification_required', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(200, { step: 'email_verification_required' })))

    render(<Host onSuccess={vi.fn()} />)
    fireEvent.click(screen.getByText('submit'))

    await screen.findByText(/has not been verified yet/i)
    expect(screen.getByTestId('phase').textContent).toBe('verify-email')
  })

  it('drives the enrollment user-code flow and auto-retries after approval', async () => {
    const onSuccess = vi.fn()
    let enrolled = false
    const fetchMock = vi.fn((url) => {
      const u = String(url)
      if (u.includes('/api/auth/cloud/enroll/start')) {
        return jsonResponse(200, { user_code: 'WXYZ-7890', verification_uri: 'https://vulos.org/activate' })
      }
      if (u.includes('/api/auth/cloud/enroll/status')) {
        enrolled = true
        return jsonResponse(200, { state: 'approved', ulid: 'ULID1' })
      }
      // login endpoint
      if (!enrolled) {
        return jsonResponse(200, { step: 'enrollment_required', enroll_available: true })
      }
      return jsonResponse(200, { session_token: 'sess-3', account_id: 'acc' })
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<Host onSuccess={onSuccess} />)
    fireEvent.click(screen.getByText('submit'))

    // The user-code panel shows the code + verification URI.
    await screen.findByText('WXYZ-7890')
    expect(screen.getByTestId('enroll-panel').textContent).toContain('vulos.org/activate')

    // Poll fires (3s cadence) → approved → auto-retry login → success.
    await waitFor(() => expect(onSuccess).toHaveBeenCalled(), { timeout: 5000 })
    expect(onSuccess.mock.calls[0][0].session_token).toBe('sess-3')
  }, 10000)

  it('reports invalid credentials from a 401', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(401, { error: 'invalid_credentials' })))

    render(<Host onSuccess={vi.fn()} />)
    fireEvent.click(screen.getByText('submit'))

    await screen.findByRole('alert')
    expect(screen.getByRole('alert').textContent).toMatch(/incorrect email or password/i)
  })

  it('explains when enrollment is unavailable on this box', async () => {
    vi.stubGlobal('fetch', vi.fn(() =>
      jsonResponse(200, { step: 'enrollment_required', enroll_available: false })))

    render(<Host onSuccess={vi.fn()} />)
    fireEvent.click(screen.getByText('submit'))

    await screen.findByRole('alert')
    expect(screen.getByRole('alert').textContent).toMatch(/cannot enroll/i)
  })
})

describe('cloudErrorMessage', () => {
  it('maps well-known backend errors to guidance', () => {
    expect(cloudErrorMessage(401, { error: 'invalid_credentials' })).toMatch(/incorrect email/i)
    expect(cloudErrorMessage(401, { error: 'invalid_totp' })).toMatch(/2FA code/i)
    expect(cloudErrorMessage(429, {})).toMatch(/too many attempts/i)
    expect(cloudErrorMessage(502, {})).toMatch(/could not reach/i)
  })
})
