import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, act } from '@testing-library/react'

/**
 * What this app OFFERS, and what it tells someone who cannot see it.
 *
 * Everything here is about the destructive controls. The server is the gate —
 * it answers 403 to a non-admin and refuses a protected pid whoever asks — so
 * none of these assertions are security properties. They are about not
 * inviting an action that will be refused, not hiding which rows are off
 * limits until after the user has picked one, and not presenting the most
 * dangerous button in the OS as an unlabelled "button" to a screen reader.
 */

let mockProfile: Record<string, unknown> | null = { role: 'admin' }
vi.mock('../../../auth/AuthProvider', () => ({
  useAuth: () => ({ profile: mockProfile }),
}))
vi.mock('../../../core/useTelemetry', () => ({
  useTelemetry: () => ({ stats: null, connected: true }),
}))

class NoopResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= NoopResizeObserver as unknown as typeof ResizeObserver

import ActivityMonitor from '../ActivityMonitor'

interface Row {
  pid: number; name: string; start?: number
  protected?: boolean; protected_reason?: string
}

let procRows: Row[] = []
let appRows: unknown[] = []
let posts: string[] = []

function mount() {
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => {
    const u = String(url)
    if (init?.method === 'POST') {
      posts.push(u)
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ outcome: 'terminated', signals: ['SIGTERM'] }) })
    }
    const body = u.includes('/api/system/processes') ? procRows
      : u.includes('/api/proc/apps') ? { apps: appRows }
        : []
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
  }))
  render(<ActivityMonitor />)
}

const quitButton = () => screen.getAllByRole('button', { name: 'Quit' })[0] as HTMLButtonElement
const row = (name: string) => screen.getByText(name).closest('[role="row"]') as HTMLElement

beforeEach(() => {
  posts = []
  mockProfile = { role: 'admin' }
  procRows = [
    { pid: 4242, name: 'editor', start: 111 },
    { pid: 1, name: 'init', start: 1, protected: true, protected_reason: 'pid 1 is the system init; ending it stops the box' },
  ]
  appRows = []
})

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

async function settle() { await act(async () => {}) }

/* ── who may end things ─────────────────────────────────────────────────── */

describe('a user the box will refuse', () => {
  it('is not offered the process controls at all', async () => {
    mockProfile = { role: 'user' }
    mount()
    await settle()

    fireEvent.click(row('editor'))
    expect(quitButton().disabled).toBe(true)
    expect(screen.getByText('Only an administrator can end processes on this box')).toBeTruthy()
  })

  it('cannot reach the confirmation dialog, so is never asked to confirm something that will fail', async () => {
    mockProfile = { role: 'user' }
    mount()
    await settle()

    fireEvent.click(row('editor'))
    fireEvent.click(quitButton())
    await settle()

    expect(screen.queryByRole('dialog')).toBeNull()
    expect(posts).toHaveLength(0)
  })

  it('CONTROL: an admin IS offered them', async () => {
    mockProfile = { role: 'admin' }
    mount()
    await settle()

    fireEvent.click(row('editor'))
    expect(quitButton().disabled).toBe(false)
  })

  /**
   * The affordance fails toward OFFERING, and that is deliberate.
   *
   * `profile` is null while the session loads. Withdrawing the control then
   * would show a real admin dead buttons for as long as the profile takes to
   * arrive, and a dead button with no explanation is indistinguishable from a
   * broken feature. Offering it costs a non-admin nothing worse than the 403
   * they would have received anyway — the server is the gate.
   */
  it('CONTROL: offers the control while the session is still loading rather than guessing', async () => {
    mockProfile = null
    mount()
    await settle()

    fireEvent.click(row('editor'))
    expect(quitButton().disabled).toBe(false)
  })

  it('withdraws EVERY app-row control, with a reason on each', async () => {
    mockProfile = { role: 'user' }
    appRows = [
      {
        app_id: 'notes', name: 'Notes', kind: 'process', running: true, closable: true,
        responding: { status: 'responding', method: 'http_probe', detail: 'HTTP 200 in 4ms' },
      },
      {
        app_id: 'gimp', name: 'GIMP', kind: 'stream', running: true, closable: true,
        responding: { status: 'responding', method: 'x11_ping', detail: 'echo in 8ms' },
      },
    ]
    mount()
    await settle()
    fireEvent.click(screen.getByRole('button', { name: /^Apps/ }))

    // All three, not just the first. Force quit is the one that destroys work
    // without asking, so a test that checked only Close would leave the more
    // dangerous control unguarded — and did.
    for (const name of ['Close Notes', 'Force quit Notes', 'End the session running GIMP']) {
      const b = screen.getByRole('button', { name }) as HTMLButtonElement
      expect(b.disabled, name).toBe(true)
      expect(b.title, name).toBe('Only an administrator can end apps on this box')
    }
  })

  it('CONTROL: an admin gets every app-row control enabled', async () => {
    mockProfile = { role: 'admin' }
    appRows = [{
      app_id: 'notes', name: 'Notes', kind: 'process', running: true, closable: true,
      responding: { status: 'responding', method: 'http_probe', detail: 'HTTP 200 in 4ms' },
    }]
    mount()
    await settle()
    fireEvent.click(screen.getByRole('button', { name: /^Apps/ }))

    for (const name of ['Close Notes', 'Force quit Notes']) {
      expect((screen.getByRole('button', { name }) as HTMLButtonElement).disabled, name).toBe(false)
    }
  })
})

/* ── what is off limits ─────────────────────────────────────────────────── */

describe('a protected process', () => {
  it('says so in its own row, before anyone selects it', async () => {
    mount()
    await settle()

    // The whole point: no selection has happened.
    const initRow = row('init')
    const mark = initRow.querySelector('[aria-label^="Protected"]')
    expect(mark).toBeTruthy()
    expect(mark!.getAttribute('aria-label')).toContain('pid 1 is the system init')
  })

  it('marks it with more than a colour', async () => {
    mount()
    await settle()

    // A colour is not information to a screen reader and not information to a
    // user who cannot distinguish it. The mark must carry text of its own.
    const mark = row('init').querySelector('[aria-label^="Protected"]')!
    expect(mark.textContent!.trim().length).toBeGreaterThan(0)
  })

  it('CONTROL: an ordinary process carries no such mark', async () => {
    mount()
    await settle()

    expect(row('editor').querySelector('[aria-label^="Protected"]')).toBeNull()
  })
})

/* ── listing semantics ──────────────────────────────────────────────────── */

describe('the process listing', () => {
  it('puts its rows inside a grid, so a row is a row of something', async () => {
    mount()
    await settle()

    const grid = screen.getByRole('grid', { name: 'Processes' })
    expect(grid).toBeTruthy()
    // Every row must be inside it. A role="row" with no grid ancestor is
    // invalid, and aria-selected on such a row is undefined — which is what
    // announced the kill target to nobody.
    for (const r of screen.getAllByRole('row')) {
      expect(grid.contains(r)).toBe(true)
    }
  })

  it('exposes the selected row through aria-selected', async () => {
    mount()
    await settle()

    fireEvent.click(row('editor'))
    expect(row('editor').getAttribute('aria-selected')).toBe('true')
    expect(row('init').getAttribute('aria-selected')).toBe('false')
  })

  it('is one tab stop, not one per process', async () => {
    // 300 processes meant 300 presses of Tab to get past this table.
    procRows = Array.from({ length: 12 }, (_, i) => ({ pid: 100 + i, name: `p${i}`, start: i }))
    mount()
    await settle()

    const rows = screen.getAllByRole('row').filter(r => r.hasAttribute('aria-rowindex'))
    expect(rows.filter(r => r.getAttribute('tabindex') === '0')).toHaveLength(1)
    expect(rows.length).toBeGreaterThan(1)
  })

  it('moves between rows with the arrow keys', async () => {
    procRows = Array.from({ length: 4 }, (_, i) => ({ pid: 100 + i, name: `p${i}`, start: i }))
    mount()
    await settle()

    const rows = screen.getAllByRole('row').filter(r => r.hasAttribute('aria-rowindex'))
    rows[0].focus()
    fireEvent.keyDown(rows[0], { key: 'ArrowDown' })
    expect(document.activeElement).toBe(rows[1])

    fireEvent.keyDown(rows[1], { key: 'End' })
    expect(document.activeElement).toBe(rows[rows.length - 1])
  })
})

/* ── the destructive dialog ─────────────────────────────────────────────── */

describe('the confirmation dialog', () => {
  async function openDialog() {
    mount()
    await settle()
    fireEvent.click(row('editor'))
    fireEvent.click(quitButton())
    await settle()
  }

  it('moves focus into itself, instead of leaving it on the button behind the overlay', async () => {
    await openDialog()

    const dialog = screen.getByRole('dialog')
    expect(dialog.contains(document.activeElement)).toBe(true)
  })

  it('closes on Escape without ending anything', async () => {
    await openDialog()

    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    await settle()

    expect(screen.queryByRole('dialog')).toBeNull()
    expect(posts).toHaveLength(0)
  })

  it('returns focus to where the user was when it closes', async () => {
    mount()
    await settle()
    fireEvent.click(row('editor'))
    const opener = quitButton()
    opener.focus()
    fireEvent.click(opener)
    await settle()
    expect(document.activeElement).not.toBe(opener)

    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    await settle()

    expect(document.activeElement).toBe(opener)
  })

  it('keeps Tab inside itself', async () => {
    await openDialog()
    const dialog = screen.getByRole('dialog')
    const buttons = Array.from(dialog.querySelectorAll('button')) as HTMLButtonElement[]

    // Forwards off the last control wraps to the first, rather than escaping
    // into a process table that aria-modal has told the user is not there.
    buttons[buttons.length - 1].focus()
    fireEvent.keyDown(dialog, { key: 'Tab' })
    expect(document.activeElement).toBe(buttons[0])

    // And backwards off the first wraps to the last.
    buttons[0].focus()
    fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(buttons[buttons.length - 1])
  })

  it('CONTROL: still confirms when the user means to', async () => {
    await openDialog()

    fireEvent.click(screen.getAllByRole('button', { name: 'Quit' })[1])
    await settle()

    expect(posts.filter(u => u.includes('/signal'))).toHaveLength(1)
  })
})

/* ── app-row buttons ────────────────────────────────────────────────────── */

describe('the per-app buttons', () => {
  beforeEach(() => {
    appRows = [
      {
        app_id: 'notes', name: 'Notes', kind: 'process', running: true, closable: true,
        responding: { status: 'responding', method: 'http_probe', detail: 'HTTP 200' },
      },
      {
        app_id: 'gimp', name: 'GIMP', kind: 'stream', running: true, closable: true,
        responding: { status: 'display_not_responding', method: 'x11_ping', detail: 'display stalled' },
      },
    ]
  })

  it('name the app they act on, so they are not three identical "button"s in a row', async () => {
    mount()
    await settle()
    fireEvent.click(screen.getByRole('button', { name: /^Apps/ }))

    expect(screen.getByRole('button', { name: 'Close Notes' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Force quit Notes' })).toBeTruthy()
    // The streamed row ends a SESSION, and its accessible name says so rather
    // than borrowing the single-app wording.
    expect(screen.getByRole('button', { name: 'End the session running GIMP' })).toBeTruthy()
  })

  it('keeps the visible labels short while the accessible names carry the target', async () => {
    mount()
    await settle()
    fireEvent.click(screen.getByRole('button', { name: /^Apps/ }))

    expect(screen.getByRole('button', { name: 'Close Notes' }).textContent).toBe('Close')
  })
})
