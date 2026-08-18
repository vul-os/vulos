import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent } from '@testing-library/react'

import FileManager from '../builtin/files/FileManager'

// FileManager lists a directory by shelling out through POST /api/exec and
// parsing `ls -l` output, so the mock answers in that format. Only the
// directory name in the command matters here.
const LISTINGS: Record<string, string[]> = {
  '~': ['drwxr-xr-x 2 me me 4096 Aug 18 10:00 projects'],
  '~/projects': ['-rw-r--r-- 1 me me  120 Aug 18 10:00 notes.txt'],
}

function mockExec() {
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: { body?: string }) => {
    if (String(url) !== '/api/exec') {
      return { ok: true, status: 200, json: async () => ({}) }
    }
    const command: string = JSON.parse(init?.body || '{}').command || ''
    const dir = /"([^"]+)"/.exec(command)?.[1]
    // `readlink -f ~` / `echo ~` style home resolution — leave it unresolved so
    // the component keeps its '~' default and the paths stay predictable.
    if (dir === undefined) return { ok: true, status: 200, json: async () => ({ output: '' }) }
    const lines = LISTINGS[dir] ?? []
    // loadDir strips the `total N` header with `tail -n +2`, which the mock has
    // already accounted for by not emitting one.
    return { ok: true, status: 200, json: async () => ({ output: lines.join('\n') }) }
  }))
}

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('FileManager — history must not start with a phantom entry', () => {
  // `history` is seeded useState(['~']) at index 0, and the mount effect then
  // called loadDir('~', true) — pushing '~' a SECOND time. That left
  // history = ['~','~'] with histIdx = 1 before the user had navigated
  // anywhere, so Back (disabled={histIdx <= 0}) was live from the moment the
  // app opened and led to the folder already on screen.
  it('has Back disabled when the app opens', async () => {
    mockExec()
    render(<FileManager />)

    expect(await screen.findByText('projects')).toBeInTheDocument()
    expect(screen.getByLabelText('Back')).toBeDisabled()
    expect(screen.getByLabelText('Forward')).toBeDisabled()
  })

  // The phantom entry rides along on every later navigation, so returning home
  // from one folder down took two Back presses instead of one.
  it('returns home in a single Back press after entering one folder', async () => {
    mockExec()
    render(<FileManager />)
    fireEvent.doubleClick(await screen.findByText('projects'))
    expect(await screen.findByText('notes.txt')).toBeInTheDocument()

    const back = screen.getByLabelText('Back')
    expect(back).toBeEnabled()
    fireEvent.click(back)

    await waitFor(() => expect(screen.getByText('projects')).toBeInTheDocument())
    expect(screen.queryByText('notes.txt')).toBeNull()
    expect(screen.getByLabelText('Back')).toBeDisabled()
  })
})
