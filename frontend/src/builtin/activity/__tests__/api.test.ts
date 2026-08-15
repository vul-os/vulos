import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  loadJSON, toProcesses, toNetConns, toAppList, toResponsiveness, signalProcess,
} from '../api'

const realFetch = global.fetch
afterEach(() => { global.fetch = realFetch })

/** Serve one canned response for the next fetch. */
function serve(status: number, body: unknown) {
  const fn = vi.fn(async () => ({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  }))
  global.fetch = fn as unknown as typeof fetch
  return fn
}

describe('loadJSON — a failure must never look like empty data', () => {
  // THE bug this file exists for. Every /api/* service in this OS answers 5xx
  // with a JSON body that parses cleanly. The old code called r.json()
  // unconditionally and .catch(() => {}), so a 503 flowed into the list
  // narrower, came out as [], and the Activity Monitor rendered its designed
  // "No processes found" empty state — telling the user their box was running
  // nothing, which is never true.
  it('reports a 503 whose body parses cleanly as a failure, not as zero processes', async () => {
    serve(503, { error: 'cannot read /proc on this host', code: 'proc_unavailable' })
    const res = await loadJSON('/api/system/processes', toProcesses)

    expect(res.ok).toBe(false)
    if (res.ok) return
    expect(res.error.status).toBe(503)
    expect(res.error.message).toContain('/proc')
    expect(res.error.code).toBe('proc_unavailable')
  })

  it('reports a 500 with an ARRAY body as a failure too', async () => {
    // A server that answered errors with an array would sail straight through
    // a body-shape check. Only r.ok can tell these apart.
    serve(500, [])
    const res = await loadJSON('/api/system/processes', toProcesses)
    expect(res.ok).toBe(false)
  })

  it('reports a 403 as a failure and surfaces the server message verbatim', async () => {
    serve(403, { error: 'admin only' })
    const res = await loadJSON('/api/proc/apps', toAppList)
    expect(res.ok).toBe(false)
    if (res.ok) return
    expect(res.error.status).toBe(403)
    expect(res.error.message).toBe('admin only')
  })

  it('reports a network failure as status 0 rather than swallowing it', async () => {
    global.fetch = vi.fn(async () => { throw new Error('Failed to fetch') }) as unknown as typeof fetch
    const res = await loadJSON('/api/system/network', toNetConns)
    expect(res.ok).toBe(false)
    if (res.ok) return
    expect(res.error.status).toBe(0)
    expect(res.error.message).toContain('Failed to fetch')
  })

  it('passes a 200 through the narrower', async () => {
    serve(200, [{ pid: 42, name: 'bash', start: 998877, cpu: 1.5 }])
    const res = await loadJSON('/api/system/processes', toProcesses)
    expect(res.ok).toBe(true)
    if (!res.ok) return
    expect(res.value).toHaveLength(1)
    expect(res.value[0].pid).toBe(42)
    expect(res.value[0].start).toBe(998877)
  })
})

describe('narrowing', () => {
  it('keeps start, protected and protected_reason off the wire', () => {
    const [p] = toProcesses([{
      pid: 1, name: 'init', start: 12, protected: true, protected_reason: 'pid 1 is the system init',
    }])
    expect(p.start).toBe(12)
    expect(p.protected).toBe(true)
    expect(p.protected_reason).toContain('init')
  })

  it('drops a non-numeric start rather than coercing it, so no unsafe kill can be formed', () => {
    const [p] = toProcesses([{ pid: 1, start: '12' }])
    expect(p.start).toBeUndefined()
  })

  // An unrecognised status must land on `unknown`, never on `responding`. A
  // future server value the client has not learned yet is an absence of
  // information, and defaulting it to a reassurance is the exact failure this
  // whole surface is designed to avoid.
  it('maps an unrecognised responsiveness status to unknown, not responding', () => {
    expect(toResponsiveness({ status: 'wobbly' }).status).toBe('unknown')
    expect(toResponsiveness({}).status).toBe('unknown')
    expect(toResponsiveness(null).status).toBe('unknown')
  })

  it('keeps the four responsiveness statuses distinct', () => {
    expect(toResponsiveness({ status: 'responding' }).status).toBe('responding')
    expect(toResponsiveness({ status: 'not_responding' }).status).toBe('not_responding')
    expect(toResponsiveness({ status: 'not_applicable' }).status).toBe('not_applicable')
    expect(toResponsiveness({ status: 'unknown' }).status).toBe('unknown')
  })

  it('reads apps out of the {apps:[…]} envelope', () => {
    const list = toAppList({ apps: [{ app_id: 'notes', kind: 'process', running: true, closable: true }] })
    expect(list).toHaveLength(1)
    expect(list[0].app_id).toBe('notes')
    expect(list[0].closable).toBe(true)
  })

  it('keeps a connection PID and its socket inode as separate values', () => {
    const [c] = toNetConns([{ proto: 'tcp', pid: 1201, inode: 918273 }])
    expect(c.pid).toBe(1201)
    expect(c.inode).toBe(918273)
    expect(c.pid).not.toBe(c.inode)
  })
})

describe('signalProcess', () => {
  // The server requires the start time, and the client must not even attempt a
  // request without one. A kill carrying only a pid asks the box to signal
  // whatever process now holds that number.
  it('refuses to send a request for a process with no recorded start time', async () => {
    const fn = serve(200, {})
    const res = await signalProcess({ pid: 4242 }, 'force')
    expect(res.ok).toBe(false)
    expect(fn).not.toHaveBeenCalled()
  })

  it('sends pid, start and mode', async () => {
    const fn = serve(200, { outcome: 'killed' })
    const res = await signalProcess({ pid: 4242, start: 998877 }, 'force')
    expect(res.ok).toBe(true)

    const [, init] = fn.mock.calls[0] as unknown as [string, RequestInit]
    const body = JSON.parse(String(init.body))
    expect(body).toEqual({ pid: 4242, start: 998877, mode: 'force' })
  })

  it('surfaces a 409 identity mismatch instead of retrying', async () => {
    serve(409, { error: 'that pid now belongs to a different process; refresh the list', code: 'identity_mismatch' })
    const res = await signalProcess({ pid: 4242, start: 111 }, 'quit')
    expect(res.ok).toBe(false)
    if (res.ok) return
    expect(res.error.status).toBe(409)
    expect(res.error.code).toBe('identity_mismatch')
  })
})
