/**
 * Drive.integration.test.jsx — wave-28 shell integration suite.
 *
 * Renders the REAL Drive app (src/builtin/drive/Drive.jsx) against MSW-backed
 * /api/files/* endpoints and exercises the core file operations:
 *   • list + render nodes, navigate into a folder,
 *   • create a folder (POST /api/files/folder),
 *   • rename (POST /api/files/move),
 *   • delete (POST /api/files/delete, guarded by a confirm),
 *   • upload → grant → commit → per-file "Done" progress row.
 *
 * The upload transport uses XMLHttpRequest for byte progress, which MSW/jsdom
 * don't drive; that test stubs XHR to resolve immediately so the grant→commit
 * orchestration and the progress UI are covered without a real byte stream.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server, http, HttpResponse } from './msw/server.js'

import Drive from '../../builtin/drive/Drive.jsx'

const ROOT_NODES = [
  { id: 'f1', name: 'Photos', is_dir: true },
  { id: 'n1', name: 'budget.xlsx', is_dir: false, size: 2048, content_type: 'application/vnd.ms-excel' },
]

function mockList(byParent) {
  server.use(http.get('/api/files/list', ({ request }) => {
    const parent = new URL(request.url).searchParams.get('parent') || ''
    return HttpResponse.json({ nodes: byParent[parent] || [] })
  }))
}

beforeEach(() => vi.clearAllMocks())
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('Drive — list + navigate (integration)', () => {
  it('lists the root nodes from /api/files/list', async () => {
    mockList({ '': ROOT_NODES })
    render(<Drive />)
    expect(await screen.findByText('Photos')).toBeInTheDocument()
    expect(screen.getByText('budget.xlsx')).toBeInTheDocument()
  })

  it('opening a folder lists its children', async () => {
    mockList({ '': ROOT_NODES, f1: [{ id: 'c1', name: 'holiday.jpg', is_dir: false, size: 100 }] })
    const user = userEvent.setup()
    render(<Drive />)
    await user.click(await screen.findByText('Photos'))
    expect(await screen.findByText('holiday.jpg')).toBeInTheDocument()
  })
})

describe('Drive — mutations (integration)', () => {
  it('creating a folder POSTs to /api/files/folder', async () => {
    let folderBody = null
    mockList({ '': [] })
    server.use(http.post('/api/files/folder', async ({ request }) => {
      folderBody = await request.json()
      return HttpResponse.json({ id: 'new' })
    }))
    const user = userEvent.setup()
    render(<Drive />)
    await user.click(await screen.findByRole('button', { name: '+ Folder' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Invoices')
    await user.click(within(dialog).getByRole('button', { name: 'Create' }))
    await waitFor(() => expect(folderBody).toEqual({ parent_id: '', name: 'Invoices' }))
  })

  it('renaming a node POSTs the new name to /api/files/move', async () => {
    let moveBody = null
    mockList({ '': ROOT_NODES })
    server.use(http.post('/api/files/move', async ({ request }) => {
      moveBody = await request.json()
      return HttpResponse.json({ id: 'n1', name: 'q3-budget.xlsx' })
    }))
    const user = userEvent.setup()
    render(<Drive />)
    await screen.findByText('budget.xlsx')
    await user.click(screen.getByRole('button', { name: 'Actions for budget.xlsx' }))
    await user.click(await screen.findByText('Rename'))
    const dialog = await screen.findByRole('dialog')
    const input = within(dialog).getByDisplayValue('budget.xlsx')
    await user.clear(input)
    await user.type(input, 'q3-budget.xlsx')
    await user.click(within(dialog).getByRole('button', { name: 'Rename' }))
    await waitFor(() => expect(moveBody).toEqual({ node_id: 'n1', new_parent_id: '', new_name: 'q3-budget.xlsx' }))
  })

  it('delete confirms, then POSTs to /api/files/delete', async () => {
    let deleteBody = 'NOT_CALLED'
    mockList({ '': ROOT_NODES })
    server.use(http.post('/api/files/delete', async ({ request }) => {
      deleteBody = await request.json()
      return HttpResponse.json({ ok: true })
    }))
    vi.stubGlobal('confirm', () => true)
    const user = userEvent.setup()
    render(<Drive />)
    await screen.findByText('budget.xlsx')
    await user.click(screen.getByRole('button', { name: 'Actions for budget.xlsx' }))
    await user.click(await screen.findByText('Delete'))
    await waitFor(() => expect(deleteBody).toEqual({ node_id: 'n1' }))
  })
})

describe('Drive — upload progress (integration)', () => {
  it('upload runs grant → commit and shows a per-file Done row', async () => {
    let grantCalled = false
    let commitBody = null
    mockList({ '': [] })
    server.use(
      http.post('/api/files/upload-grant', async () => {
        grantCalled = true
        // No presigned URL → falls to the OS data plane PUT (handled by XHR stub).
        return HttpResponse.json({ node: { id: 'up1' }, grant: { type: 'osdata' } })
      }),
      http.post('/api/files/commit', async ({ request }) => {
        commitBody = await request.json()
        return HttpResponse.json({ id: 'up1', name: 'report.pdf' })
      }),
    )

    // Stub XMLHttpRequest so the byte-PUT resolves instantly with a 200; the
    // grant + commit fetches remain real (MSW).
    class FakeXHR {
      constructor() { this.upload = {}; this.status = 200 }
      open() {}
      setRequestHeader() {}
      send() { this.upload.onprogress?.({ lengthComputable: true, total: 10, loaded: 10 }); this.onload?.() }
    }
    vi.stubGlobal('XMLHttpRequest', FakeXHR)

    const user = userEvent.setup()
    const { container } = render(<Drive />)
    await screen.findByRole('button', { name: '+ Folder' })

    const file = new File(['pdf-bytes'], 'report.pdf', { type: 'application/pdf' })
    const input = container.querySelector('input[type="file"]')
    await user.upload(input, file)

    await waitFor(() => expect(grantCalled).toBe(true))
    await waitFor(() => expect(commitBody).toEqual({ node_id: 'up1', size: file.size, content_type: 'application/pdf', etag: '' }))
    // The progress row reports completion.
    expect(await screen.findByText('Done')).toBeInTheDocument()
  })
})
