// Setup.match.test.jsx — WAVE-26 polish: unit tests for matchState, the pure
// derivation behind the confirm-password + confirm-PIN inline match feedback
// (the papercut fix that pairs the red/green border with accessible text).
//
import { describe, it, expect } from 'vitest'

import { matchState } from './matchState'

describe('matchState — confirm-field match derivation', () => {
  it('is untouched (no feedback) when confirm is empty', () => {
    const s = matchState('hunter2hunter2', '')
    expect(s.touched).toBe(false)
    expect(s.matches).toBe(false)
    expect(s.mismatch).toBe(false)
  })

  it('flags a mismatch once the user has typed a differing value', () => {
    const s = matchState('correct-horse', 'correct-hor')
    expect(s.touched).toBe(true)
    expect(s.mismatch).toBe(true)
    expect(s.matches).toBe(false)
  })

  it('confirms a match', () => {
    const s = matchState('1234', '1234')
    expect(s.touched).toBe(true)
    expect(s.matches).toBe(true)
    expect(s.mismatch).toBe(false)
  })

  it('treats a nullish confirm as untouched', () => {
    const s = matchState('abc', undefined)
    expect(s.touched).toBe(false)
    expect(s.mismatch).toBe(false)
  })
})
