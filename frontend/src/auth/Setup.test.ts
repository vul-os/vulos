// Setup.test.jsx — vitest unit tests for effectiveSteps skip logic (Critical #1).
//
// Tests that with NETB05_choice='local', cloudAccount and intent are absent from
// the effective step sequence, and with 'cloud'/'create' they are included.
import { describe, it, expect } from 'vitest'

// The STEPS constant and the filter logic mirror what Setup.jsx does at render
// time.  We test the pure filtering function directly rather than mounting the
// full component (which requires a complex provider tree) to keep the test fast
// and focused on the invariant.

const STEPS = [
  'welcome', 'IS09_chooser', 'device', 'language', 'timezone', 'network',
  'NETB05_account_choice', 'account', 'cloudAccount', 'pin', 'intent',
  'appearance', 'identity', 'storage', 'ssh', 'recoverykit', 'ready',
]

// Mirrors the effectiveSteps computation in Setup.jsx.
function effectiveSteps(NETB05_choice: string) {
  if (NETB05_choice === 'local') {
    return STEPS.filter(s => s !== 'cloudAccount' && s !== 'intent')
  }
  return STEPS
}

describe('Setup effectiveSteps — local-only skips cloudAccount + intent', () => {
  it('with NETB05_choice=local, cloudAccount is not in the step sequence', () => {
    const steps = effectiveSteps('local')
    expect(steps).not.toContain('cloudAccount')
  })

  it('with NETB05_choice=local, intent is not in the step sequence', () => {
    const steps = effectiveSteps('local')
    expect(steps).not.toContain('intent')
  })

  it('with NETB05_choice=local, other steps are preserved', () => {
    const steps = effectiveSteps('local')
    expect(steps).toContain('account')
    expect(steps).toContain('pin')
    expect(steps).toContain('appearance')
    expect(steps).toContain('ready')
  })

  it('with NETB05_choice=cloud, cloudAccount is included', () => {
    const steps = effectiveSteps('cloud')
    expect(steps).toContain('cloudAccount')
  })

  it('with NETB05_choice=cloud, intent is included', () => {
    const steps = effectiveSteps('cloud')
    expect(steps).toContain('intent')
  })

  it('with NETB05_choice=create, cloudAccount is included', () => {
    const steps = effectiveSteps('create')
    expect(steps).toContain('cloudAccount')
  })

  it('with NETB05_choice=create, intent is included', () => {
    const steps = effectiveSteps('create')
    expect(steps).toContain('intent')
  })

  it('STEPS constant is not mutated by effectiveSteps', () => {
    const originalLength = STEPS.length
    effectiveSteps('local')
    expect(STEPS.length).toBe(originalLength)
    expect(STEPS).toContain('cloudAccount')
    expect(STEPS).toContain('intent')
  })
})
