import { describe, it, expect, vi } from 'vitest'
import { probeSetupComplete, PROBE_DELAYS_MS } from '../lib/setupProbe'

/**
 * The setup probe must never invent an answer.
 *
 * The shipped shell did:
 *
 *   fetch('/api/setup/status')
 *     .then(r => r.ok ? r.json() : { setup_complete: true })
 *     .catch(() => setSetupDone(true))
 *
 * so a box that was slow, restarting, or answering 500 was recorded as ALREADY
 * SET UP — and the fifteen-step first-boot wizard was skipped on the machine
 * that most needed it. A box that is still coming up is the ordinary state
 * during a first boot, which makes that the likely path, not an edge case.
 *
 * Every test below is written so that the old one-shot fail-open code could not
 * pass it: each asserts either that a retry happened, or that the result is
 * 'unknown' rather than `true`.
 */

// A fetch stand-in returning the given queue of behaviours, one per call.
type Beh =
  | { ok: true; body: unknown }
  | { status: number }
  | { throw: true }

function fetchQueue(behaviours: Beh[]) {
  const calls: string[] = []
  const impl = vi.fn(async (input: RequestInfo | URL) => {
    const b = behaviours[Math.min(calls.length, behaviours.length - 1)]
    calls.push(String(input))
    if ('throw' in b) throw new TypeError('Failed to fetch')
    if ('status' in b) {
      return new Response(JSON.stringify({ error: 'unavailable' }), {
        status: b.status, headers: { 'Content-Type': 'application/json' },
      })
    }
    return new Response(JSON.stringify(b.body), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    })
  })
  return { impl: impl as unknown as typeof fetch, calls }
}

// No real waiting: the sleep is injected, and recorded so the backoff itself
// can be asserted rather than assumed.
function recordingSleep() {
  const slept: number[] = []
  return { slept, sleep: async (ms: number) => { slept.push(ms) } }
}

describe('probeSetupComplete', () => {
  it('returns the box\'s answer, and asks only once when it gets one', async () => {
    for (const answer of [true, false]) {
      const { impl, calls } = fetchQueue([{ ok: true, body: { setup_complete: answer } }])
      const { sleep, slept } = recordingSleep()
      expect(await probeSetupComplete({ fetchImpl: impl, sleep })).toBe(answer)
      expect(calls).toHaveLength(1)
      expect(slept).toEqual([]) // no backoff on a healthy box — boot stays fast
      expect(calls[0]).toContain('/api/setup/status')
    }
  })

  it('retries a failing box and USES the answer when it finally comes', async () => {
    // 500, network error, then a real answer of false. The wizard must run.
    const { impl, calls } = fetchQueue([
      { status: 500 },
      { throw: true },
      { ok: true, body: { setup_complete: false } },
    ])
    const { sleep, slept } = recordingSleep()
    expect(await probeSetupComplete({ fetchImpl: impl, sleep })).toBe(false)
    expect(calls).toHaveLength(3)
    expect(slept).toEqual([PROBE_DELAYS_MS[1], PROBE_DELAYS_MS[2]])
  })

  it('answers "unknown" — never true — when the box never answers', async () => {
    for (const beh of [{ status: 500 } as Beh, { throw: true } as Beh, { status: 404 } as Beh]) {
      const { impl, calls } = fetchQueue([beh])
      const { sleep } = recordingSleep()
      const result = await probeSetupComplete({ fetchImpl: impl, sleep })
      expect(result).toBe('unknown')
      expect(result).not.toBe(true) // the fail-open regression, named
      expect(calls).toHaveLength(PROBE_DELAYS_MS.length)
    }
  })

  it('spends about seven seconds of backoff before giving up', async () => {
    const { impl } = fetchQueue([{ throw: true }])
    const { sleep, slept } = recordingSleep()
    await probeSetupComplete({ fetchImpl: impl, sleep })
    const total = slept.reduce((a, b) => a + b, 0)
    expect(slept).toEqual(PROBE_DELAYS_MS.filter(d => d > 0))
    expect(total).toBeGreaterThanOrEqual(4000)
    expect(total).toBeLessThanOrEqual(9000)
  })

  it('treats a 200 that is not this endpoint\'s answer as no answer at all', async () => {
    // A captive portal or a proxy answering 200 with its own page is not the
    // box saying "setup is done". Missing field, wrong type, and a non-object
    // body all have to be rejected — reading `d.setup_complete !== false` off
    // any of them silently yields "complete".
    for (const body of [{}, { setup_complete: 'yes' }, 'ok', null, []]) {
      const { impl, calls } = fetchQueue([{ ok: true, body }])
      const { sleep } = recordingSleep()
      expect(await probeSetupComplete({ fetchImpl: impl, sleep })).toBe('unknown')
      expect(calls).toHaveLength(PROBE_DELAYS_MS.length)
    }
  })

  it('abandons an attempt that hangs, instead of hanging the boot', async () => {
    // A box that accepts the connection and never replies is the worst case:
    // with no timeout the shell sits on "Loading..." forever.
    const aborted: boolean[] = []
    const impl = (async (_input: RequestInfo | URL, init?: RequestInit) => {
      return await new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => {
          aborted.push(true)
          reject(new DOMException('Aborted', 'AbortError'))
        })
      })
    }) as unknown as typeof fetch
    const { sleep } = recordingSleep()
    const result = await probeSetupComplete({
      fetchImpl: impl, sleep, timeoutMs: 5, delays: [0, 0],
    })
    expect(result).toBe('unknown')
    expect(aborted).toHaveLength(2)
  })
})
