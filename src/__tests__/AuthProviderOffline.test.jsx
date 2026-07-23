import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

// Mock the OS offline-auth gate so we drive AuthProvider's state machine directly.
vi.mock('../lib/offlineAuth.js', () => ({
  unlockOffline: vi.fn().mockResolvedValue(true),
  getCachedIdentity: vi.fn().mockResolvedValue({ id: 'u1', name: 'Ada' }),
  lock: vi.fn(),
}))

import { AuthProvider, useAuth } from '../auth/AuthProvider.jsx'
import * as offlineAuth from '../lib/offlineAuth.js'

function Probe() {
  const a = useAuth()
  return (
    <div>
      <span data-testid="offline">{String(a.offline)}</span>
      <span data-testid="offlineMode">{String(a.offlineMode)}</span>
      <span data-testid="user">{a.user ? 'yes' : 'no'}</span>
      <button onClick={() => a.unlockOffline('pw')}>unlock</button>
      <button onClick={() => a.checkAuth()}>check</button>
    </div>
  )
}

afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('AuthProvider — offline state machine', () => {
  it('marks offline (not logged-out) when the box is unreachable', async () => {
    global.fetch = vi.fn().mockRejectedValue(new Error('network down'))
    render(<AuthProvider><Probe /></AuthProvider>)

    await waitFor(() => expect(screen.getByTestId('offline').textContent).toBe('true'))
    expect(screen.getByTestId('user').textContent).toBe('no')
    expect(screen.getByTestId('offlineMode').textContent).toBe('false')
  })

  it('adopts the real session when the box is reachable and authed', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true, status: 200,
      json: async () => ({ user: { id: 'u1' }, profile: { display_name: 'Ada' } }),
    })
    render(<AuthProvider><Probe /></AuthProvider>)

    await waitFor(() => expect(screen.getByTestId('user').textContent).toBe('yes'))
    expect(screen.getByTestId('offline').textContent).toBe('false')
  })

  it('a reachable 401 ENDS an offline session (reconnect re-validation) and drops the key', async () => {
    const user = userEvent.setup()
    global.fetch = vi.fn().mockRejectedValue(new Error('network down'))
    render(<AuthProvider><Probe /></AuthProvider>)
    await waitFor(() => expect(screen.getByTestId('offline').textContent).toBe('true'))

    // Unlock offline → offline session active.
    await user.click(screen.getByText('unlock'))
    await waitFor(() => expect(screen.getByTestId('offlineMode').textContent).toBe('true'))
    expect(screen.getByTestId('user').textContent).toBe('yes')

    // Box comes back but the session is invalid (401): must end the offline session.
    global.fetch = vi.fn().mockResolvedValue({ ok: false, status: 401, json: async () => ({}) })
    await user.click(screen.getByText('check'))

    await waitFor(() => expect(screen.getByTestId('offlineMode').textContent).toBe('false'))
    expect(screen.getByTestId('user').textContent).toBe('no')
    expect(offlineAuth.lock).toHaveBeenCalled()
  })
})
