// SharePeerModal.test.tsx — the Files "Share to peer" sheet.
//
// Guards: it loads the peering Drop roster, and sharing a file POSTs the
// drop/send contract (absolute media_path resolved from ~) to the real
// transport endpoint; folders are archived via the exec bridge first.

import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import SharePeerModal from '../SharePeerModal'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

type FetchImpl = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>

let fetchMock: ReturnType<typeof vi.fn<FetchImpl>>

beforeEach(() => {
  fetchMock = vi.fn<FetchImpl>(async (url) => {
    if (String(url).endsWith('/api/peering/drop/nearby')) {
      return new Response(JSON.stringify([
        { vulos_id: 'vula:bob', display_name: 'Bob', addr: '10.0.0.5:8080', is_contact: true },
      ]), { status: 200 })
    }
    if (String(url).endsWith('/api/peering/drop/send')) {
      return new Response(JSON.stringify({ transfer_id: 'tx-1' }), { status: 200 })
    }
    return new Response('nope', { status: 404 })
  })
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(cleanup)

it('lists nearby peers and sends a file with a resolved absolute path', async () => {
  const onClose = vi.fn()
  render(
    <SharePeerModal
      target={{ name: 'report.pdf', path: '~/Documents/report.pdf', isDir: false }}
      home="/home/vula"
      exec={vi.fn<(cmd: string) => Promise<string>>()}
      onClose={onClose}
    />,
  )

  await waitFor(() => expect(screen.getByText('Bob')).toBeInTheDocument())

  fireEvent.click(screen.getByText('Bob'))

  await waitFor(() => {
    const sendCall = fetchMock.mock.calls.find((c) => String(c[0]).endsWith('/api/peering/drop/send'))
    expect(sendCall).toBeTruthy()
    if (!sendCall) throw new Error('expected a drop/send call')
    const opts = sendCall[1]
    if (typeof opts?.body !== 'string') throw new Error('expected a JSON string body')
    const body: unknown = JSON.parse(opts.body)
    expect(body).toMatchObject({
      target_vulos_id: 'vula:bob',
      media_path: '/home/vula/Documents/report.pdf',
      mime_type: 'application/pdf',
      target_addr: 'http://10.0.0.5:8080',
    })
  })
})

it('archives a folder via exec before sending', async () => {
  const exec = vi.fn<(cmd: string) => Promise<string>>(async () => 'OK\n')
  render(
    <SharePeerModal
      target={{ name: 'Photos', path: '~/Photos', isDir: true }}
      home="/home/vula"
      exec={exec}
      onClose={vi.fn()}
    />,
  )
  await waitFor(() => expect(screen.getByText('Bob')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Bob'))

  await waitFor(() => expect(exec).toHaveBeenCalled())
  expect(exec.mock.calls[0][0]).toContain('tar -czf')

  await waitFor(() => {
    const sendCall = fetchMock.mock.calls.find((c) => String(c[0]).endsWith('/api/peering/drop/send'))
    if (!sendCall) throw new Error('expected a drop/send call')
    const opts = sendCall[1]
    if (typeof opts?.body !== 'string') throw new Error('expected a JSON string body')
    const body: unknown = JSON.parse(opts.body)
    if (!isRecord(body)) throw new Error('expected a JSON object body')
    expect(body.media_path).toMatch(/Photos\.tar\.gz$/)
    expect(body.mime_type).toBe('application/gzip')
  })
})
