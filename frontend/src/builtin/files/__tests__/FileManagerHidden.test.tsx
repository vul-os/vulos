import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent } from '@testing-library/react'

import FileManager from '../FileManager'

// The "show hidden files" button (the `.*` one in the toolbar) had never worked,
// for two independent reasons, and fixing either alone leaves a dead button:
//
//  1. THE FLAG DID NOTHING. `const flag = hidden ? '-la' : '-lA'`. Both -a and
//     -A list dotfiles; the only difference between them is `.` and `..`, and
//     the parser drops those by name a few lines further down. So both states
//     produced byte-identical listings — and the OFF state (`-lA`) was the one
//     showing every dotfile in the folder.
//
//  2. THE RELOAD USED THE OLD VALUE. loadDir is useCallback'd on [hidden], and
//     the click did `setHidden(!hidden); loadDir(cwd, false)` — the loadDir it
//     called is this render's, built from the pre-click `hidden`. Even with a
//     working flag the listing would have lagged one click behind.
//
// So the test drives the BUTTON and reads the FILE LIST. A test that asserted
// which flag the command contained would have passed on defect 2, and a test
// that asserted loadDir was called would have passed on both.

// A stand-in for real `ls`: -A (or -a) lists dotfiles, plain -l does not.
const VISIBLE = [
  'drwxr-xr-x 2 me me 4096 Aug 18 10:00 projects',
  '-rw-r--r-- 1 me me  120 Aug 18 10:00 notes.txt',
]
const DOTFILES = [
  'drwxr-xr-x 2 me me 4096 Aug 18 10:00 .config',
  '-rw-r--r-- 1 me me   64 Aug 18 10:00 .bashrc',
]

function mockExec() {
  const commands: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: { body?: string }) => {
    if (String(url) !== '/api/exec') {
      return { ok: true, status: 200, json: async () => ({}) }
    }
    const command: string = JSON.parse(init?.body || '{}').command || ''
    commands.push(command)
    const dir = /"([^"]+)"/.exec(command)?.[1]
    // `echo $HOME` — leave home unresolved so paths stay '~'.
    if (dir === undefined) return { ok: true, status: 200, json: async () => ({ output: '' }) }

    // The flag, read the way ls reads it. `-a` and `-A` both mean "show them".
    const flags = / -(\w+)/.exec(command)?.[1] ?? ''
    const showsDotfiles = flags.includes('a') || flags.includes('A')
    const lines = showsDotfiles ? [...DOTFILES, ...VISIBLE] : VISIBLE
    // loadDir strips the `total N` header with `tail -n +2`; the mock accounts
    // for that by not emitting one.
    return { ok: true, status: 200, json: async () => ({ output: lines.join('\n') }) }
  }))
  return commands
}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('FileManager — the hidden-files button actually hides and shows files', () => {
  it('does not list dotfiles when the toggle is off', async () => {
    mockExec()
    render(<FileManager />)

    expect(await screen.findByText('notes.txt')).toBeInTheDocument()
    expect(screen.queryByText('.config')).toBeNull()
    expect(screen.queryByText('.bashrc')).toBeNull()
    expect(screen.getByLabelText('Toggle hidden files')).toHaveAttribute('aria-pressed', 'false')
  })

  it('lists dotfiles on the FIRST press, not the second', async () => {
    // "Not the second" is the stale-memo half. With the flag fixed but the memo
    // left alone, this listing arrives one click late and the assertion below
    // fails on the first press exactly as a user would experience it.
    mockExec()
    render(<FileManager />)
    await screen.findByText('notes.txt')

    fireEvent.click(screen.getByLabelText('Toggle hidden files'))

    expect(await screen.findByText('.config')).toBeInTheDocument()
    expect(screen.getByText('.bashrc')).toBeInTheDocument()
    // The ordinary files are still there — this shows MORE, it does not filter.
    expect(screen.getByText('notes.txt')).toBeInTheDocument()
    expect(screen.getByLabelText('Toggle hidden files')).toHaveAttribute('aria-pressed', 'true')
  })

  it('hides them again on the next press', async () => {
    mockExec()
    render(<FileManager />)
    await screen.findByText('notes.txt')

    const toggle = screen.getByLabelText('Toggle hidden files')
    fireEvent.click(toggle)
    await screen.findByText('.config')

    fireEvent.click(toggle)
    await waitFor(() => expect(screen.queryByText('.config')).toBeNull())
    expect(screen.queryByText('.bashrc')).toBeNull()
    expect(screen.getByText('notes.txt')).toBeInTheDocument()
  })

  it('sends a flag that a real ls would treat as "no dotfiles" when off', async () => {
    // The cross-check on the mock: the mock above decides what to return from
    // the flag, so a fix that changed the mock's mind rather than the command
    // would pass the three tests above. `-lA` must not be what "off" sends.
    const commands = mockExec()
    render(<FileManager />)
    await screen.findByText('notes.txt')

    const listings = commands.filter(c => c.startsWith('ls '))
    expect(listings.length).toBeGreaterThan(0)
    for (const c of listings) {
      expect(c, 'the OFF state must not pass -a or -A').not.toMatch(/ -l?[aA]/)
    }
  })
})
