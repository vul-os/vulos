/**
 * LockScreenPin.test.jsx — WAVE-38 UI/UX polish regression
 *
 * Guards a real lockout bug: the Device-PIN settings panel lets a user set a
 * PIN of 4–8 digits (maxLength 8, validates > 8), but the lock-screen unlock
 * input previously capped entry at maxLength 6 — so a 7–8 digit PIN could be
 * set yet never entered to unlock. The unlock field must accept the full
 * settable range.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, act as domAct } from '@testing-library/react'
import LockScreen from '../auth/LockScreen.jsx'

afterEach(() => cleanup())

describe('LockScreen PIN entry', () => {
  it('accepts up to 8 digits so any settable PIN can be entered', () => {
    render(<LockScreen onUnlock={() => {}} userName="T" />)
    // The unlock input only reveals after the first interaction.
    domAct(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'a' }))
    })
    const input = screen.getByLabelText(/unlock pin/i)
    expect(input).toBeTruthy()
    // Must match the 4–8 digit range enforced by the Device-PIN settings panel.
    expect(input.getAttribute('maxLength')).toBe('8')
  })
})
