import { describe, it, expect } from 'vitest'

// launchApp — builtin window sizing (companion to launchApp.test.ts, which
// mocks '../shell/builtinApps' entirely and so never exercises this branch).
//
// Settings ('persona') is the one builtin whose default 720x500 window left
// nearly every panel pre-scrolled on open (nav rail + Card content need more
// room than a generic tool window). BUILTIN_WINDOW_SIZE opts it into a larger
// initial size; every other builtin is unaffected and keeps the shared
// default by omitting `size` entirely (asserted in shellStore.test.tsx's
// "shellReducer — OPEN_WINDOW size" block, which pins the 720x500 default the
// reducer falls back to when no size is given).

import { launchApp, type LaunchAppDeps } from '../shell/launchApp'
import { BUILTIN_WINDOW_SIZE } from '../shell/builtinApps'
import type { AppEntry } from '../shell/appTypes'

type CapturedWindow = Parameters<LaunchAppDeps['openWindow']>[0]

function testApp(overrides: Partial<AppEntry> & Pick<AppEntry, 'id' | 'name'>): AppEntry {
  return { icon: '', description: '', keywords: [], category: 'test', ...overrides }
}

// Capture into an array rather than a `let x: T | null`. TypeScript's
// control-flow analysis cannot see that the openWindow callback ran, so a
// nullable local stays narrowed to `null` after the await and every property
// access lands on `never`. Pushing sidesteps that without a cast.
describe('launchApp — builtin window size', () => {
  it('passes BUILTIN_WINDOW_SIZE.persona when launching Settings', async () => {
    const opened: CapturedWindow[] = []
    await launchApp(testApp({ id: 'persona', name: 'Settings', type: 'desktop' }), { openWindow: (w) => { opened.push(w) } })
    expect(opened).toHaveLength(1)
    expect(opened[0].size).toEqual(BUILTIN_WINDOW_SIZE.persona)
    expect(opened[0].size).toEqual({ width: 860, height: 620 })
  })

  it('leaves `size` undefined for a builtin with no registry entry (keeps the shared 720x500 default)', async () => {
    const opened: CapturedWindow[] = []
    await launchApp(testApp({ id: 'terminal', name: 'Terminal', type: 'desktop' }), { openWindow: (w) => { opened.push(w) } })
    expect(opened).toHaveLength(1)
    expect(opened[0].size).toBeUndefined()
  })
})
