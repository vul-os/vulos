import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, act, within } from '@testing-library/react'

/**
 * Three ways this app could state something it does not know.
 *
 *  1. A card showing `0%` for a reading that never arrived.
 *  2. A table whose order depends on the order rows were fetched in.
 *  3. A slow response overwriting a newer one, so the screen shows a past
 *     state of the box as the present one.
 *
 * All three produce a display that looks measured and is not, which is this
 * repo's most-repeated defect. None of them are visible in a screenshot of a
 * healthy box, because on a healthy box every one of them happens to be right.
 */

// The Activity Monitor asks who you are before it offers to end anything.
// Admin by default here so these tests stay about what they are testing; the
// non-admin affordance has its own file.
const mockProfile: Record<string, unknown> | null = { role: 'admin' }
vi.mock('../../../auth/AuthProvider', () => ({
  useAuth: () => ({ profile: mockProfile }),
}))

vi.mock('../../../core/useTelemetry', () => ({
  useTelemetry: () => mockTelemetry,
}))

class NoopResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= NoopResizeObserver as unknown as typeof ResizeObserver

import ActivityMonitor, { compareProcesses } from '../ActivityMonitor'
import type { ProcessInfo } from '../api'

let mockTelemetry: { stats: unknown; connected: boolean } = { stats: null, connected: true }

/* ── fabricated readings ───────────────────────────────────────────────── */

describe('a card must not state a reading it does not have', () => {
  interface Deferred { resolve: (rows: unknown) => void }
  let procDeferred: Deferred

  function renderWith(stats: unknown, opts: { procStatus?: number } = {}) {
    mockTelemetry = { stats, connected: true }
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      const u = String(url)
      if (u.includes('/api/system/processes')) {
        if (opts.procStatus && opts.procStatus >= 400) {
          return Promise.resolve({
            ok: false, status: opts.procStatus,
            json: () => Promise.resolve({ error: 'cannot read /proc' }),
          })
        }
        // Never resolves unless the test resolves it: this is the "no reading
        // yet" state, which is what the app shows for its first few seconds.
        return new Promise(res => {
          procDeferred = { resolve: rows => res({ ok: true, status: 200, json: () => Promise.resolve(rows) }) }
        })
      }
      const body = u.includes('/api/proc/apps') ? { apps: [] } : []
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
    }))
    render(<ActivityMonitor />)
  }

  /**
   * The card's own label, not the process table's "Memory" column header or
   * the "Network" tab — several of these words appear more than once on
   * screen. Card labels are the uppercase tracking-widest spans.
   */
  function cardLabel(label: string): HTMLElement {
    const el = screen.getAllByText(label).find(e => e.className.includes('tracking-widest'))
    if (!el) throw new Error(`no graph card labelled ${label}`)
    return el
  }

  /** Opens a card and returns its detail rows as {label: value}. */
  async function expand(label: string): Promise<Record<string, string>> {
    await act(async () => { fireEvent.click(cardLabel(label)) })
    const card = cardLabel(label).closest('div')!.parentElement!.parentElement!
    const out: Record<string, string> = {}
    for (const el of Array.from(card.querySelectorAll('div.flex.justify-between'))) {
      const spans = el.querySelectorAll('span')
      if (spans.length === 2) out[spans[0].textContent!] = spans[1].textContent!
    }
    return out
  }

  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('shows a dash, not 0%, when the telemetry socket has delivered nothing', async () => {
    renderWith(null)
    await act(async () => {})

    // The headline figures are the ones a user reads first, and "0%" on a
    // wedged box is the precise opposite of the truth.
    const cpu = cardLabel('CPU').parentElement!
    expect(within(cpu).queryByText('0%')).toBeNull()
    expect(within(cpu).getByText('—')).toBeTruthy()
  })

  it('CONTROL: shows the real figure when the socket HAS delivered one', async () => {
    renderWith({ cpu: 97, mem_percent: 63 })
    await act(async () => {})

    const cpu = cardLabel('CPU').parentElement!
    expect(within(cpu).getByText('97%')).toBeTruthy()
  })

  it('does not report a process count before the process list has ever loaded', async () => {
    renderWith(null)
    await act(async () => {})
    const d = await expand('CPU')

    expect(d.Processes).toBe('—')
    expect(d.Threads).toBe('—')
  })

  it('CONTROL: reports the real count once the list has loaded', async () => {
    renderWith(null)
    await act(async () => {
      procDeferred.resolve([
        { pid: 1, name: 'init', start: 1, threads: 2 },
        { pid: 2, name: 'sshd', start: 2, threads: 3 },
      ])
    })
    const d = await expand('CPU')

    expect(d.Processes).toBe('2')
    expect(d.Threads).toBe('5')
  })

  it('withdraws the count when the feed that produced it fails', async () => {
    // A count left over from a poll that has since failed is stale, and a
    // stale number presented as a live one is the same claim as a made-up one.
    renderWith(null, { procStatus: 503 })
    await act(async () => {})
    const d = await expand('CPU')

    expect(d.Processes).toBe('—')
  })

  it('does not report free memory as 0 B when neither total nor used is known', async () => {
    renderWith(null)
    await act(async () => {})
    const d = await expand('Memory')

    // "0 B free" reads as a box out of memory — the opposite of "no reading".
    expect(d.Free).toBe('—')
    expect(d.Used).toBe('—')
    expect(d.Total).toBe('—')
  })

  it('CONTROL: computes free memory when both operands are known', async () => {
    renderWith({ mem_total: 8 * 1024 * 1024 * 1024, mem_used: 2 * 1024 * 1024 * 1024 })
    await act(async () => {})
    const d = await expand('Memory')

    expect(d.Free).toBe('6.0 GB')
  })

  it('does not report a network rate of 0 B/s when nothing was measured', async () => {
    renderWith(null)
    await act(async () => {})
    const d = await expand('Network')

    expect(d.Receiving).toBe('—')
    expect(d.Sending).toBe('—')
    // Connections is NOT asserted here: the network feed did resolve, with an
    // empty list, so 0 is a measurement rather than a fabrication. Withdrawing
    // a count when its feed fails is covered by the process-count test above.
  })
})

/* ── the sort comparator ───────────────────────────────────────────────── */

describe('the process table comparator is an ordering', () => {
  // A deliberately awkward set: missing values, a NaN, ties, mixed case.
  const rows: ProcessInfo[] = [
    { pid: 10, name: 'alpha', cpu: 5, threads: 3 },
    { pid: 11, name: 'Beta', cpu: 5, threads: undefined },
    { pid: 12, name: 'gamma', cpu: 0, threads: NaN },
    { pid: 13, name: undefined, cpu: undefined, threads: 900 },
    { pid: 14, name: 'delta', cpu: 99, threads: 1 },
    { pid: 15, name: 'alpha', cpu: NaN, threads: 3 },
  ]
  const cols = ['pid', 'name', 'user', 'state', 'cpu', 'mem_rss', 'threads'] as const

  /**
   * Transitivity is the property the old comparator broke, and it is the one
   * that cannot be seen in a single sorted example — which is why the previous
   * version passed review. Asserted over every ordered triple, in both
   * directions, for every sortable column.
   */
  it('is transitive for every column in both directions', () => {
    for (const col of cols) {
      for (const asc of [true, false]) {
        const cmp = compareProcesses(col, asc)
        for (const a of rows) {
          for (const b of rows) {
            for (const c of rows) {
              if (cmp(a, b) <= 0 && cmp(b, c) <= 0) {
                expect(
                  cmp(a, c),
                  `${col}/${asc ? 'asc' : 'desc'}: ${a.pid}<=${b.pid}<=${c.pid} but not ${a.pid}<=${c.pid}`,
                ).toBeLessThanOrEqual(0)
              }
            }
          }
        }
      }
    }
  })

  it('is antisymmetric — reversing the operands reverses the sign', () => {
    for (const col of cols) {
      const cmp = compareProcesses(col, true)
      for (const a of rows) {
        for (const b of rows) {
          const ab = Math.sign(cmp(a, b))
          const ba = Math.sign(cmp(b, a))
          // Normalised with `|| 0` so the tie case is +0 on both sides:
          // Object.is(+0, -0) is false, which would fail a correct comparator.
          expect(ab || 0, `${col}: ${a.pid} vs ${b.pid}`).toBe(-ba || 0)
        }
      }
    }
  })

  it('produces the same order no matter what order the rows arrived in', () => {
    // The defect this catches: an inconsistent comparator lets V8 return a
    // different arrangement for the same set depending on input order, so the
    // table reshuffles under the pointer between two polls that fetched the
    // same processes.
    const shuffles = [
      [...rows],
      [...rows].reverse(),
      [rows[3], rows[0], rows[5], rows[2], rows[4], rows[1]],
      [rows[2], rows[4], rows[1], rows[5], rows[0], rows[3]],
    ]
    for (const col of cols) {
      for (const asc of [true, false]) {
        const orders = shuffles.map(s => [...s].sort(compareProcesses(col, asc)).map(p => p.pid).join(','))
        expect(new Set(orders).size, `${col}/${asc ? 'asc' : 'desc'} → ${orders.join(' | ')}`).toBe(1)
      }
    }
  })

  it('sorts rows with no value last in BOTH directions, rather than treating them as equal to everything', () => {
    for (const asc of [true, false]) {
      const order = [...rows].sort(compareProcesses('cpu', asc)).map(p => p.pid)
      // 13 has no cpu, 15 has NaN — both are "no reading" and belong together
      // at the end, not scattered through the middle claiming to be idle.
      expect(order.slice(-2).sort()).toEqual([13, 15])
    }
  })

  it('CONTROL: still actually sorts the values it does have', () => {
    const asc = [...rows].sort(compareProcesses('cpu', true)).map(p => p.cpu).filter(v => typeof v === 'number' && !Number.isNaN(v))
    expect(asc).toEqual([0, 5, 5, 99])
    const desc = [...rows].sort(compareProcesses('cpu', false)).map(p => p.cpu).filter(v => typeof v === 'number' && !Number.isNaN(v))
    expect(desc).toEqual([99, 5, 5, 0])
  })

  /**
   * The comparator is an exported pure function, and its contract is "a total
   * order for any ProcessInfo-shaped input" — not "a total order as long as
   * ./api keeps every column homogeneous".
   *
   * Today `toProcess` does keep them homogeneous (`str()` and `num()` return
   * one type or undefined), which is why the mixed case cannot be reached
   * through the UI. That is a property of a different file. If it chose the
   * branch by the LEFT operand only, a string-vs-number pair would compare as
   * strings one way and as numbers the other, agreeing on a sign instead of
   * opposing — and a caller would inherit an ordering that is not one.
   */
  it('stays an ordering even when a column carries both a string and a number', () => {
    const mixed = [
      { pid: 20, state: 'running' },
      { pid: 21, state: 42 },
      { pid: 22, state: 'D' },
      { pid: 23, state: 7 },
    ] as unknown as ProcessInfo[]

    for (const asc of [true, false]) {
      const cmp = compareProcesses('state', asc)
      for (const a of mixed) {
        for (const b of mixed) {
          expect(Math.sign(cmp(a, b)) || 0, `${a.pid} vs ${b.pid}`).toBe(-Math.sign(cmp(b, a)) || 0)
          for (const c of mixed) {
            if (cmp(a, b) <= 0 && cmp(b, c) <= 0) {
              expect(cmp(a, c), `${a.pid}<=${b.pid}<=${c.pid}`).toBeLessThanOrEqual(0)
            }
          }
        }
      }
    }
  })

  it('CONTROL: sorts names case-insensitively rather than by code point', () => {
    // 'Beta' must land between 'alpha' and 'delta'; a code-point sort puts
    // every capitalised name before every lowercase one.
    const order = [...rows].sort(compareProcesses('name', true)).map(p => p.name)
    expect(order.slice(0, 4)).toEqual(['alpha', 'alpha', 'Beta', 'delta'])
  })
})

/* ── out-of-order polls ────────────────────────────────────────────────── */

describe('a slow poll must not overwrite a newer one', () => {
  let resolvers: ((rows: unknown) => void)[] = []

  beforeEach(() => {
    resolvers = []
    mockTelemetry = { stats: null, connected: true }
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      const u = String(url)
      if (u.includes('/api/system/processes')) {
        return new Promise(res => {
          resolvers.push(rows => res({ ok: true, status: 200, json: () => Promise.resolve(rows) }))
        })
      }
      const body = u.includes('/api/proc/apps') ? { apps: [] } : []
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
    }))
    render(<ActivityMonitor />)
  })

  afterEach(() => { cleanup(); vi.useRealTimers(); vi.unstubAllGlobals() })

  it('discards a response from a superseded poll', async () => {
    // Poll 1 went out at mount and has not answered. Poll 2 goes out at 3s.
    await act(async () => { await vi.advanceTimersByTimeAsync(3100) })
    expect(resolvers).toHaveLength(2)

    // The NEWER poll answers first, with the current state of the box.
    await act(async () => { resolvers[1]([{ pid: 77, name: 'current', start: 7 }]) })
    expect(screen.queryByText('current')).toBeTruthy()

    // Now the STALE poll finally answers, describing a box that no longer
    // exists. It must not be allowed to replace what is on screen.
    await act(async () => { resolvers[0]([{ pid: 42, name: 'stale', start: 4 }]) })

    expect(screen.queryByText('stale')).toBeNull()
    expect(screen.queryByText('current')).toBeTruthy()
  })

  it('CONTROL: an in-order response IS applied', async () => {
    await act(async () => { resolvers[0]([{ pid: 42, name: 'first', start: 4 }]) })
    expect(screen.queryByText('first')).toBeTruthy()

    await act(async () => { await vi.advanceTimersByTimeAsync(3100) })
    await act(async () => { resolvers[1]([{ pid: 43, name: 'second', start: 5 }]) })

    expect(screen.queryByText('second')).toBeTruthy()
    expect(screen.queryByText('first')).toBeNull()
  })
})
