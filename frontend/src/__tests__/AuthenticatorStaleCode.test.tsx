import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, act } from '@testing-library/react'

import Authenticator from '../apps/Authenticator/Authenticator'

const ACCOUNT = { id: 'acct-1', name: 'GitHub', issuer: 'GitHub' }

// The row asks for a fresh code once per 30-second TOTP window. `codeAnswer`
// lets the box start answering and then stop, which is the whole point: a code
// that could not be refreshed is expired, and expired is not the same as
// correct-looking.
let codeAnswer: (() => { code: string } | null) = () => ({ code: '847291' })

function mockTotp() {
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    const u = String(url)
    if (u === '/api/auth/totp/list') {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([ACCOUNT]) })
    }
    if (u.startsWith('/api/auth/totp/code/')) {
      const body = codeAnswer()
      if (body === null) return Promise.resolve({ ok: false, status: 503, json: () => Promise.resolve({}) })
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  }))
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true })
  // Land mid-window so the first render is not itself a boundary.
  vi.setSystemTime(new Date('2026-08-18T10:00:15Z'))
  codeAnswer = () => ({ code: '847291' })
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('Authenticator — a code that could not be refreshed must not stay on screen', () => {
  it('shows the code the box returned', async () => {
    mockTotp()
    render(<Authenticator />)
    expect(await screen.findByText('847 291')).toBeInTheDocument()
  })

  // The failure path used to set only the error flag. `displayCode` prefers
  // `code` over `error`, so the PREVIOUS window's digits stayed on screen,
  // formatted normally, with no error styling and Copy still live. The user
  // pastes a one-time code that expired 30 seconds ago, the login is rejected,
  // and nothing on screen ever said why.
  it('drops the expired code when the next window cannot be fetched', async () => {
    mockTotp()
    render(<Authenticator />)
    await screen.findByText('847 291')

    // The box goes away, and the TOTP window rolls over.
    codeAnswer = () => null
    await act(async () => {
      vi.setSystemTime(new Date('2026-08-18T10:00:45Z'))
      await vi.advanceTimersByTimeAsync(600)
    })

    await waitFor(() => expect(screen.queryByText('847 291')).toBeNull())
    expect(screen.getByText('— —')).toBeInTheDocument()
  })
})
