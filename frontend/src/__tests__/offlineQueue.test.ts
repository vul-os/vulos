/**
 * offlineQueue.test.js — OS write-queue (OFFLINE-03).
 *
 * Mirrors lilmail/src/__tests__/outboxQueue.test.js so the OS shell,
 * mail, and office queues all behave the same.
 *
 * Covers:
 *   • enqueue + readAll round-trip
 *   • remove + clear
 *   • flush replays records and drops successes
 *   • flush stops on first network failure + bumps attempts
 *   • flush respects MAX_ATTEMPTS (5) and drops poisoned entries
 *   • survives a corrupted localStorage entry
 *   • onQueueChange listener fires on add + remove + clear
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

async function freshModule() {
  vi.resetModules()
  return import('../lib/offlineQueue.js')
}

beforeEach(() => {
  try { localStorage.clear() } catch { /* ignore */ }
  globalThis.window = globalThis.window || {}
  if (!window.addEventListener) window.addEventListener = () => {}
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('offlineQueue — enqueue/read/remove', () => {
  it('enqueue persists a record with id + queuedAt + zero attempts', async () => {
    const q = await freshModule()
    const rec = q.enqueue({
      kind: 'files.create',
      body: { method: 'POST', path: '/api/files', body: '{"name":"a"}' },
    })
    expect(rec.id).toBeTruthy()
    expect(rec.queuedAt).toBeGreaterThan(0)
    expect(rec.attempts).toBe(0)
    expect(rec.lastError).toBe(null)
    expect(rec.body.method).toBe('POST')
    expect(q.readAll()).toHaveLength(1)
    expect(q.readAll()[0].body.path).toBe('/api/files')
  })

  it('remove pops a single record by id', async () => {
    const q = await freshModule()
    const a = q.enqueue({ body: { path: '/api/a' } })
    const b = q.enqueue({ body: { path: '/api/b' } })
    expect(q.size()).toBe(2)
    q.remove(a.id)
    const rest = q.readAll()
    expect(rest).toHaveLength(1)
    expect(rest[0].id).toBe(b.id)
  })

  it('clear empties the queue', async () => {
    const q = await freshModule()
    q.enqueue({ body: { path: '/api/a' } })
    q.enqueue({ body: { path: '/api/b' } })
    q.clear()
    expect(q.size()).toBe(0)
  })

  it('survives a corrupted localStorage entry', async () => {
    localStorage.setItem('vulos.os.offlineQueue.v1', 'not-json')
    const q = await freshModule()
    expect(q.readAll()).toEqual([])
  })

  it('defaults method to POST and kind to "write"', async () => {
    const q = await freshModule()
    const rec = q.enqueue({ body: { path: '/api/x' } })
    expect(rec.kind).toBe('write')
    expect(rec.body.method).toBe('POST')
  })
})

describe('offlineQueue — flush', () => {
  it('flush calls replay and removes successful records', async () => {
    const q = await freshModule()
    q.enqueue({ body: { method: 'POST', path: '/api/a' } })
    q.enqueue({ body: { method: 'POST', path: '/api/b' } })

    const replay = vi.fn(async () => ({ ok: true }))
    const result = await q.flush({ replay })

    expect(replay).toHaveBeenCalledTimes(2)
    expect(result.sent).toBe(2)
    expect(result.remaining).toBe(0)
    expect(q.size()).toBe(0)
  })

  it('flush stops on the first network failure and increments attempts', async () => {
    const q = await freshModule()
    q.enqueue({ body: { path: '/api/first' } })
    q.enqueue({ body: { path: '/api/second' } })

    const replay = vi.fn(async () => { throw new TypeError('Failed to fetch') })
    const result = await q.flush({ replay })

    // First call failed → stopped before trying the second.
    expect(replay).toHaveBeenCalledTimes(1)
    expect(result.failed).toBe(1)
    expect(q.size()).toBe(2)
    expect(q.readAll()[0].attempts).toBe(1)
    expect(q.readAll()[0].lastError).toContain('Failed to fetch')
  })

  it('flush drops a poisoned record after MAX_ATTEMPTS (5)', async () => {
    const q = await freshModule()
    q.enqueue({ body: { path: '/api/poison' } })

    const replay = vi.fn(async () => { throw new Error('boom') })
    for (let i = 0; i < 5; i++) {
      await q.flush({ replay })
    }
    expect(q.size()).toBe(0)
  })

  it('onQueueChange listeners fire on enqueue + clear', async () => {
    const q = await freshModule()
    const seen: number[] = []
    const off = q.onQueueChange(items => seen.push(items.length))
    q.enqueue({ body: { path: '/api/1' } })
    q.enqueue({ body: { path: '/api/2' } })
    q.clear()
    off()
    q.enqueue({ body: { path: '/api/3' } }) // after off()
    expect(seen).toEqual([1, 2, 0])
  })

  it('flush is reentrant-safe — concurrent calls dedupe', async () => {
    const q = await freshModule()
    q.enqueue({ body: { path: '/api/a' } })

    // A ref object (not a bare `let`) — TS's narrowing otherwise pins a `let`
    // reassigned only inside a nested closure to its initial `null` literal at
    // every read site in this outer scope, regardless of the intervening
    // flush() call that actually invokes the closure and mutates it.
    const resolveReplayRef: { current: ((value: unknown) => void) | null } = { current: null }
    const replay = vi.fn(() => new Promise((res) => { resolveReplayRef.current = res }))

    const p1 = q.flush({ replay })
    const p2 = q.flush({ replay }) // should short-circuit
    // Let the first flush start (vi resolves microtasks):
    await Promise.resolve()
    if (!resolveReplayRef.current) throw new Error('expected replay to have been invoked')
    resolveReplayRef.current({ ok: true })
    const [r1, r2] = await Promise.all([p1, p2])
    // Only the first flush did real work.
    expect(replay).toHaveBeenCalledTimes(1)
    expect(r1.sent + r2.sent).toBe(1)
  })
})
