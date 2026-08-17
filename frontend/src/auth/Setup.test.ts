// Setup.test.ts — vitest unit tests for the first-boot wizard's step lists.
//
// HISTORY — this file used to declare its OWN `const STEPS = [...]` and its own
// `effectiveSteps()` and assert against those. Nothing it touched was imported
// from Setup.tsx, so it was a test of its own fixture: it kept passing while the
// steps it named ('cloudAccount', 'NETB05_account_choice', 'intent') were deleted
// from the real wizard along with the Vulos Cloud account surface, and while the
// real branch changed from a NETB05 filter to the INIT-09 new/join flow split.
// The assertions ran and the verdict was discarded.
//
// It now imports the REAL arrays and the REAL selection function from Setup.tsx.
// If you find yourself re-declaring a step list here, stop: that is the bug.
import { describe, it, expect } from 'vitest'

import { STEPS, IS09_JOIN_STEPS, baseStepsFor } from './Setup'

describe('Setup STEPS — the real new-system step list', () => {
  it('is the full new-system sequence, in order', () => {
    // Pinned in full: a step added, removed or reordered must be a conscious
    // edit here too. Length alone would let a rename slip through.
    expect(STEPS).toEqual([
      'welcome', 'IS09_chooser', 'device', 'language', 'timezone', 'network',
      'account', 'pin', 'apps', 'appearance', 'identity', 'storage', 'ssh',
      'recoverykit', 'ready',
    ])
  })

  it('starts at welcome and ends at ready', () => {
    expect(STEPS[0]).toBe('welcome')
    expect(STEPS[STEPS.length - 1]).toBe('ready')
  })

  it('BUNDLE-01: the apps step sits between account and appearance', () => {
    expect(STEPS.indexOf('apps')).toBeGreaterThan(STEPS.indexOf('account'))
    expect(STEPS.indexOf('apps')).toBeLessThan(STEPS.indexOf('appearance'))
  })

  it('carries no Vulos Cloud account/enrolment steps', () => {
    // Vulos is free, self-hosted software: there is no cloud account, no
    // enrolment and no paid tier, so no step may reintroduce one. These are the
    // exact names the old private copy of STEPS still claimed to have.
    for (const gone of ['cloudAccount', 'NETB05_account_choice', 'intent']) {
      expect(STEPS).not.toContain(gone)
    }
  })

  it('has no duplicate steps', () => {
    expect(new Set(STEPS).size).toBe(STEPS.length)
  })
})

describe('Setup IS09_JOIN_STEPS — the real join-flow step list', () => {
  it('is the join sequence, in order', () => {
    expect(IS09_JOIN_STEPS).toEqual([
      'welcome', 'IS09_chooser', 'IS09_join_storage', 'IS09_syncing', 'pin', 'ready',
    ])
  })

  it('shares indices 0-1 with STEPS so flipping flow at the chooser keeps step aligned', () => {
    // This is the INIT-09 invariant stated above the arrays in Setup.tsx: the
    // chooser flips flowType WITHOUT moving `step`, so both lists must agree on
    // every index up to and including the chooser.
    const chooserIdx = STEPS.indexOf('IS09_chooser')
    expect(chooserIdx).toBeGreaterThanOrEqual(0)
    expect(IS09_JOIN_STEPS.slice(0, chooserIdx + 1)).toEqual(STEPS.slice(0, chooserIdx + 1))
  })

  it('contains the join-only steps and ends at ready', () => {
    expect(IS09_JOIN_STEPS).toContain('IS09_join_storage')
    expect(IS09_JOIN_STEPS).toContain('IS09_syncing')
    expect(IS09_JOIN_STEPS[IS09_JOIN_STEPS.length - 1]).toBe('ready')
  })

  it('IS09_syncing is a step the mode poll can jump to', () => {
    // Setup.tsx jumps to indexOf('IS09_syncing') when GET /api/setup/mode says
    // 'syncing'; a rename would make that indexOf return -1 and silently land
    // the user back on step 0.
    expect(IS09_JOIN_STEPS.indexOf('IS09_syncing')).toBeGreaterThan(0)
  })
})

describe('Setup baseStepsFor — the real flow-type selection', () => {
  it('returns the join list for flowType=join', () => {
    expect(baseStepsFor('join')).toBe(IS09_JOIN_STEPS)
  })

  it('returns the new-system list for flowType=new', () => {
    expect(baseStepsFor('new')).toBe(STEPS)
  })

  it('defaults to the new-system list for any other value', () => {
    expect(baseStepsFor('')).toBe(STEPS)
    expect(baseStepsFor('normal')).toBe(STEPS)
  })

  it('does not mutate either list', () => {
    const stepsLen = STEPS.length
    const joinLen = IS09_JOIN_STEPS.length
    baseStepsFor('join')
    baseStepsFor('new')
    expect(STEPS.length).toBe(stepsLen)
    expect(IS09_JOIN_STEPS.length).toBe(joinLen)
  })
})
