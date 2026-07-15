// launchParams.test.js — the builtin deep-link seam (⌘K query → builtin factory).
//
// Pins the contract the CommandPalette + builtinApps rely on: a query stashed for
// an app id is read back exactly once (consume clears it), keyed per app, and
// absent/empty values never surface a stale query.

import { describe, it, expect } from 'vitest'
import { setPendingLaunchQuery, consumePendingLaunchQuery } from '../core/launchParams'

describe('launchParams', () => {
  it('reads back a stashed query exactly once, then clears it', () => {
    setPendingLaunchQuery('vulos-calendar', 'action=new')
    expect(consumePendingLaunchQuery('vulos-calendar')).toBe('action=new')
    // A second consume (e.g. a later plain launch) sees nothing.
    expect(consumePendingLaunchQuery('vulos-calendar')).toBe('')
  })

  it('keys pending queries per app id', () => {
    setPendingLaunchQuery('a', 'x=1')
    setPendingLaunchQuery('b', 'y=2')
    expect(consumePendingLaunchQuery('b')).toBe('y=2')
    expect(consumePendingLaunchQuery('a')).toBe('x=1')
  })

  it('returns "" for an app with no pending query', () => {
    expect(consumePendingLaunchQuery('never-set')).toBe('')
  })

  it('ignores an empty query (never stashes a falsy value)', () => {
    setPendingLaunchQuery('c', '')
    expect(consumePendingLaunchQuery('c')).toBe('')
  })
})
