/**
 * api.test.js — OS API client cloud↔LAN failover retry behavior (OFFLINE-02).
 *
 * Covers the request-level half of the OFFLINE-02 contract:
 *   • A request that hits a TypeError (network down) invalidates the cached
 *     selection, re-probes, and retries once against the new endpoint.
 *   • A retry that picks the same endpoint surfaces the original error
 *     (no infinite loop).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

const CLOUD = 'https://box.vulos.org'
const LAN = 'https://box.abc.lan.vulos.org'

async function freshApi() {
  vi.resetModules()
  return import('../lib/api.js')
}

beforeEach(() => {
  try { localStorage.clear() } catch { /* ignore */ }
  globalThis.window = globalThis.window || {}
  if (!window.addEventListener) window.addEventListener = () => {}
  globalThis.navigator = globalThis.navigator || {}
  window.__VULOS_ENDPOINTS__ = { cloud: CLOUD, lan: LAN }
})

afterEach(() => {
  vi.restoreAllMocks()
  delete window.__VULOS_ENDPOINTS__
})

describe('api.request — cloud↔LAN failover', () => {
  it('retries once against a fresh endpoint after a network error', async () => {
    const { request } = await freshApi()

    // First probe: LAN is reachable → LAN selected.
    // Then the actual request to LAN throws (mid-flight LAN drop).
    // Second probe (forced): LAN down, cloud up → cloud selected.
    // Retry against cloud succeeds with JSON body.
    let lanProbed = 0
    const fetchMock = vi.fn(async (url, init) => {
      const u = String(url)
      // Probes hit /api/auth/status; data requests hit /api/files in this test.
      if (u.endsWith('/api/auth/status')) {
        if (u.startsWith(LAN)) {
          lanProbed++
          if (lanProbed === 1) return { ok: true, status: 200 }  // first probe: LAN up
          throw new Error('LAN dropped')                          // re-probe: LAN down
        }
        return { ok: true, status: 200 }                           // cloud probe: up
      }
      // Data request:
      if (u.startsWith(LAN)) throw new Error('LAN unreachable')   // first data attempt
      if (u.startsWith(CLOUD)) {
        return {
          ok: true,
          status: 200,
          json: async () => ({ ok: true, from: 'cloud' }),
          text: async () => JSON.stringify({ ok: true, from: 'cloud' }),
        }
      }
      throw new Error('unexpected url ' + u)
    })
    vi.stubGlobal('fetch', fetchMock)

    const body = await request('/files')
    expect(body).toEqual({ ok: true, from: 'cloud' })
    // The mock saw the data request hit LAN first, then cloud on retry.
    const dataUrls = fetchMock.mock.calls
      .map(c => String(c[0]))
      .filter(u => u.endsWith('/api/files'))
    expect(dataUrls[0].startsWith(LAN)).toBe(true)
    expect(dataUrls[1].startsWith(CLOUD)).toBe(true)
  })

  it('propagates the network error if re-probe picks the same endpoint', async () => {
    const { request } = await freshApi()
    // Only LAN is configured + always succeeds on probes but always fails on
    // real data requests. Re-probe will pick LAN again → original error
    // bubbles up instead of an infinite retry loop.
    window.__VULOS_ENDPOINTS__ = { cloud: '', lan: LAN }

    const fetchMock = vi.fn(async (url) => {
      if (String(url).endsWith('/api/auth/status')) return { ok: true, status: 200 }
      throw new Error('data path down')
    })
    vi.stubGlobal('fetch', fetchMock)

    await expect(request('/files')).rejects.toThrow('data path down')
  })
})
