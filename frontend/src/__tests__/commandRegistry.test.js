import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  registerCommand, unregisterCommand, getCommands, runCommand,
  subscribeCommands, BUILTIN_COMMANDS,
} from '../core/commandRegistry.js'

// The command registry is the "Actions" seam: built-ins ship with the OS, and
// apps contribute more via registerCommand. These pin the seam contract —
// registration, id-replacement, dispatch to run(ctx), predicate filtering, and
// change notifications.

function makeCtx() {
  return {
    openApp: vi.fn(),
    openUrl: vi.fn(),
    openHome: vi.fn(),
    openSettings: vi.fn(),
    openTransparency: vi.fn(),
    toggleTheme: vi.fn(),
    lock: vi.fn(),
    close: vi.fn(),
  }
}

describe('commandRegistry — built-ins', () => {
  it('ships the curated built-in actions', () => {
    const ids = getCommands().map(c => c.id)
    for (const c of BUILTIN_COMMANDS) expect(ids).toContain(c.id)
    // Spot-check the headline actions the task calls out.
    expect(ids).toEqual(expect.arrayContaining([
      'compose', 'new-event', 'open-home', 'open-transparency',
      'open-settings', 'lock', 'toggle-theme',
    ]))
  })

  it('every built-in has a title and a run() function, defaulting to the Actions section', () => {
    for (const c of getCommands().filter(c => BUILTIN_COMMANDS.some(b => b.id === c.id))) {
      expect(typeof c.title).toBe('string')
      expect(typeof c.run).toBe('function')
      expect(c.section).toBe('Actions')
    }
  })

  it('dispatches Compose to open the mail app with the compose deep-link', () => {
    const ctx = makeCtx()
    runCommand('compose', ctx)
    expect(ctx.openApp).toHaveBeenCalledWith('lilmail', { query: 'compose=1' })
  })

  it('dispatches Toggle theme, Lock, Home, Settings, Transparency to their ctx helpers', () => {
    const ctx = makeCtx()
    runCommand('toggle-theme', ctx); expect(ctx.toggleTheme).toHaveBeenCalled()
    runCommand('lock', ctx); expect(ctx.lock).toHaveBeenCalled()
    runCommand('open-home', ctx); expect(ctx.openHome).toHaveBeenCalled()
    runCommand('open-settings', ctx); expect(ctx.openSettings).toHaveBeenCalled()
    runCommand('open-transparency', ctx); expect(ctx.openTransparency).toHaveBeenCalled()
  })
})

describe('commandRegistry — the seam', () => {
  const TEST_ID = 'test:demo-command'
  beforeEach(() => unregisterCommand(TEST_ID))

  it('registers, dispatches, and unregisters a contributed command', () => {
    const run = vi.fn()
    const off = registerCommand({ id: TEST_ID, title: 'Demo', run })
    expect(getCommands().find(c => c.id === TEST_ID)).toBeTruthy()

    const ctx = makeCtx()
    expect(runCommand(TEST_ID, ctx)).toBe(true)
    expect(run).toHaveBeenCalledWith(ctx)

    off()
    expect(getCommands().find(c => c.id === TEST_ID)).toBeUndefined()
    expect(runCommand(TEST_ID, ctx)).toBe(false)
  })

  it('re-registering the same id replaces the command', () => {
    registerCommand({ id: TEST_ID, title: 'First', run: () => {} })
    registerCommand({ id: TEST_ID, title: 'Second', run: () => {} })
    const matches = getCommands().filter(c => c.id === TEST_ID)
    expect(matches).toHaveLength(1)
    expect(matches[0].title).toBe('Second')
  })

  it('rejects malformed commands', () => {
    expect(() => registerCommand({ title: 'no id' })).toThrow()
    expect(() => registerCommand({ id: 'x', title: 'no run' })).toThrow()
  })

  it('filters by the when() predicate against the context', () => {
    registerCommand({ id: TEST_ID, title: 'Conditional', when: (c) => c.flag === true, run: () => {} })
    expect(getCommands({ flag: true }).find(c => c.id === TEST_ID)).toBeTruthy()
    expect(getCommands({ flag: false }).find(c => c.id === TEST_ID)).toBeUndefined()
  })

  it('notifies subscribers on change', () => {
    const fn = vi.fn()
    const unsub = subscribeCommands(fn)
    registerCommand({ id: TEST_ID, title: 'Notify', run: () => {} })
    expect(fn).toHaveBeenCalled()
    unsub()
    fn.mockClear()
    unregisterCommand(TEST_ID)
    expect(fn).not.toHaveBeenCalled()
  })
})
