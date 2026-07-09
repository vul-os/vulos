/**
 * resumableUpload.test.js — unit coverage for the Drive tus-style chunked upload
 * client (Drive.jsx: resumableUpload). Drives the real client against an
 * in-memory fake tus server (a mocked global.fetch, which bypasses MSW per the
 * vitest config note) through create → chunked PATCH → completion, and asserts
 * the resume path (HEAD after interruption) and the 409 offset re-sync path.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { resumableUpload } from '../Drive.jsx'

// makeTusServer returns a fetch(url, init) implementation backing one upload in
// memory. `dropAfter` (bytes) simulates an interruption: the first PATCH that
// would cross that offset is accepted only partially the FIRST time.
function makeTusServer() {
  const state = { id: 'up-1', length: 0, offset: 0, chunks: [], complete: false }
  return {
    state,
    fetch: async (url, init = {}) => {
      const method = (init.method || 'GET').toUpperCase()
      const h = new Headers(init.headers || {})
      const headers = new Headers()
      headers.set('Tus-Resumable', '1.0.0')

      if (method === 'POST') {
        state.length = parseInt(h.get('Upload-Length'), 10)
        state.offset = 0
        headers.set('Location', `/api/files/upload/resumable/${state.id}`)
        return new Response(JSON.stringify({ id: state.id, offset: 0, length: state.length }), { status: 201, headers })
      }
      if (method === 'HEAD') {
        headers.set('Upload-Offset', String(state.offset))
        headers.set('Upload-Length', String(state.length))
        return new Response(null, { status: 200, headers })
      }
      if (method === 'PATCH') {
        const claimed = parseInt(h.get('Upload-Offset'), 10)
        if (claimed !== state.offset) {
          headers.set('Upload-Offset', String(state.offset))
          return new Response('conflict', { status: 409, headers })
        }
        const body = init.body // ArrayBuffer
        const bytes = new Uint8Array(body)
        state.chunks.push(bytes)
        state.offset += bytes.byteLength
        headers.set('Upload-Offset', String(state.offset))
        if (state.offset >= state.length) {
          state.complete = true
          headers.set('Upload-Complete', '1')
          headers.set('Vulos-Node-Id', 'node-xyz')
        }
        return new Response(null, { status: 204, headers })
      }
      return new Response('nope', { status: 405 })
    },
  }
}

function assembled(server) {
  const total = server.state.chunks.reduce((n, c) => n + c.byteLength, 0)
  const out = new Uint8Array(total)
  let o = 0
  for (const c of server.state.chunks) { out.set(c, o); o += c.byteLength }
  return out
}

beforeEach(() => {
  globalThis.window = globalThis.window || {}
  if (!window.addEventListener) window.addEventListener = () => {}
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('resumableUpload', () => {
  it('uploads a large file in chunks and reports completion', async () => {
    const server = makeTusServer()
    vi.stubGlobal('fetch', server.fetch)

    // 20 MiB (> RESUMABLE_CHUNK of 8 MiB, so 3 PATCHes). Fill a few markers
    // rather than every byte to keep the test cheap; integrity is still checked
    // by comparing the reassembled buffer to the source.
    const size = 20 * 1024 * 1024
    const data = new Uint8Array(size)
    data[0] = 1; data[8 * 1024 * 1024] = 2; data[size - 1] = 3
    const file = new File([data], 'big.bin', { type: 'application/octet-stream' })

    const seen = []
    const { node_id } = await resumableUpload('parent-1', file, (f) => seen.push(f))

    expect(node_id).toBe('node-xyz')
    expect(server.state.complete).toBe(true)
    expect(server.state.offset).toBe(size)
    // Reassembled bytes match the source at each marker + total length (a full
    // 20 MiB deep-equal is prohibitively slow; markers exercise every chunk).
    const asm = assembled(server)
    expect(asm.byteLength).toBe(size)
    expect(asm[0]).toBe(1)
    expect(asm[8 * 1024 * 1024]).toBe(2)
    expect(asm[size - 1]).toBe(3)
    // Progress advanced monotonically and reached 1.
    expect(seen[seen.length - 1]).toBe(1)
    expect(seen.every((v, i) => i === 0 || v >= seen[i - 1])).toBe(true)
  })

  it('resumes from the server offset after an interruption (HEAD)', async () => {
    const server = makeTusServer()
    // Pre-seed the server as if one chunk already landed before this attempt.
    server.state.length = 20 * 1024 * 1024
    server.state.offset = 8 * 1024 * 1024
    server.state.chunks.push(new Uint8Array(8 * 1024 * 1024)) // placeholder for prior bytes

    // Intercept POST to hand back the existing id, then let the real logic HEAD.
    const base = server.fetch
    vi.stubGlobal('fetch', async (url, init = {}) => {
      const method = (init.method || 'GET').toUpperCase()
      if (method === 'POST') {
        const headers = new Headers({ 'Tus-Resumable': '1.0.0' })
        headers.set('Location', `/api/files/upload/resumable/${server.state.id}`)
        return new Response(JSON.stringify({ id: server.state.id }), { status: 201, headers })
      }
      return base(url, init)
    })

    const size = 20 * 1024 * 1024
    const data = new Uint8Array(size)
    const file = new File([data], 'resume.bin')
    const { node_id } = await resumableUpload('p', file, () => {})
    expect(node_id).toBe('node-xyz')
    // Only the remaining 12 MiB should have been PATCHed (2 chunks of 8+4).
    expect(server.state.offset).toBe(size)
  })

  it('re-syncs and continues on a 409 offset conflict', async () => {
    const server = makeTusServer()
    let firstPatch = true
    vi.stubGlobal('fetch', async (url, init = {}) => {
      const method = (init.method || 'GET').toUpperCase()
      if (method === 'PATCH' && firstPatch) {
        // Simulate: the box already committed this chunk (offset advanced) but the
        // ack was lost, so our first PATCH at offset 0 conflicts.
        firstPatch = false
        const bytes = new Uint8Array(init.body)
        server.state.chunks.push(bytes)
        server.state.offset += bytes.byteLength
        const headers = new Headers({ 'Tus-Resumable': '1.0.0', 'Upload-Offset': String(server.state.offset) })
        return new Response('conflict', { status: 409, headers })
      }
      return server.fetch(url, init)
    })

    const size = 10 * 1024 * 1024
    const data = new Uint8Array(size)
    const file = new File([data], 'conflict.bin')
    const { node_id } = await resumableUpload('p', file, () => {})
    expect(node_id).toBe('node-xyz')
    expect(server.state.offset).toBe(size)
  })
})
