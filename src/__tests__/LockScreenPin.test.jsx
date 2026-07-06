/**
 * LockScreenPin.test.jsx — WAVE-38 UI/UX polish regression
 *
 * Guards a real lockout bug: the Device-PIN settings panel lets a user set a
 * PIN of 4–8 digits (maxLength 8, validates > 8), but the lock-screen unlock
 * input previously capped entry at maxLength 6 — so a 7–8 digit PIN could be
 * set yet never entered to unlock. The unlock field must accept the full
 * settable range.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, act as domAct, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import LockScreen from '../auth/LockScreen.jsx'

afterEach(() => { cleanup(); vi.restoreAllMocks() })

// Reveal the hidden unlock input (it only mounts after the first interaction).
function reveal() {
  domAct(() => {
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'a' }))
  })
}

describe('LockScreen PIN entry', () => {
  it('accepts up to 8 digits so any settable PIN can be entered', () => {
    render(<LockScreen onUnlock={() => {}} userName="T" />)
    reveal()
    const input = screen.getByLabelText(/unlock pin/i)
    expect(input).toBeTruthy()
    // Must match the 4–8 digit range enforced by the Device-PIN settings panel.
    expect(input.getAttribute('maxLength')).toBe('8')
  })
})

describe('LockScreen lockout + error copy (wave-61)', () => {
  it('announces remaining attempts on a wrong PIN', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: true, status: 200,
      json: async () => ({ valid: false, has_pin: true, attempts_left: 3 }),
    })))
    const user = userEvent.setup()
    render(<LockScreen onUnlock={() => {}} userName="T" />)
    reveal()
    await user.type(screen.getByLabelText(/unlock pin/i), '9999')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    // The live region surfaces the honest remaining-attempts copy.
    expect(await screen.findByText(/3 attempts left/i)).toBeInTheDocument()
  })

  it('shows permanent-lock copy and disables the input on permanent_lock', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: true, status: 200,
      json: async () => ({ valid: false, has_pin: true, permanent_lock: true }),
    })))
    const user = userEvent.setup()
    render(<LockScreen onUnlock={() => {}} userName="T" />)
    reveal()
    await user.type(screen.getByLabelText(/unlock pin/i), '9999')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    expect(await screen.findByText(/too many attempts.*sign in/i)).toBeInTheDocument()
    await waitFor(() => expect(screen.getByLabelText(/unlock pin/i)).toBeDisabled())
  })

  it('does not call onUnlock on an incorrect PIN', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: true, status: 200,
      json: async () => ({ valid: false, has_pin: true }),
    })))
    const onUnlock = vi.fn()
    const user = userEvent.setup()
    render(<LockScreen onUnlock={onUnlock} userName="T" />)
    reveal()
    await user.type(screen.getByLabelText(/unlock pin/i), '0000')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    expect(await screen.findByText(/incorrect pin/i)).toBeInTheDocument()
    expect(onUnlock).not.toHaveBeenCalled()
  })
})
