import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'

// Mock the telemetry WS hook so the panel renders deterministic live stats.
interface TelemetryStats {
  cpu: number
  mem_percent: number
  mem_used: number
  mem_total: number
  load_avg: string
  uptime: string
  hostname: string
}
let mockTelemetry: { stats: TelemetryStats | null, connected: boolean } = { stats: null, connected: false }
vi.mock('../core/useTelemetry', () => ({
  useTelemetry: () => mockTelemetry,
}))

import BoxHealthPanel from '../core/settings/BoxHealthPanel'

function mockEndpoints({ health, healthStatus = 200, sys }: { health?: unknown, healthStatus?: number, sys?: unknown }) {
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    const u = String(url)
    if (u.includes('/api/health')) {
      return Promise.resolve({ ok: healthStatus === 200, status: healthStatus, json: () => Promise.resolve(health) })
    }
    if (u.includes('/api/system/info')) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(sys) })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  }))
}

beforeEach(() => { mockTelemetry = { stats: null, connected: false } })
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('BoxHealthPanel — live status', () => {
  it('renders a healthy banner and the health checks', async () => {
    mockTelemetry = {
      connected: true,
      stats: { cpu: 12, mem_percent: 40, mem_used: 4e9, mem_total: 8e9, load_avg: '0.5 0.4 0.3', uptime: '3h 2m', hostname: 'box' },
    }
    mockEndpoints({
      health: { status: 'ok', timestamp: '2026-07-08T10:00:00Z', checks: { data_dir_writable: 'ok', disk_space: 'ok: 5000 MiB free' } },
      sys: { storage_total_mb: 100000, storage_used_mb: 20000, hostname: 'box' },
    })
    render(<BoxHealthPanel />)
    expect(await screen.findByText(/All systems healthy/i)).toBeTruthy()
    expect(await screen.findByText(/data dir writable/i)).toBeTruthy()
    expect(screen.getByText('live')).toBeTruthy()
  })

  it('exposes utilization meters as accessible progressbars with live values', async () => {
    mockTelemetry = {
      connected: true,
      stats: { cpu: 12, mem_percent: 40, mem_used: 4e9, mem_total: 8e9, load_avg: '0.5', uptime: '3h', hostname: 'box' },
    }
    mockEndpoints({
      health: { status: 'ok', timestamp: '2026-07-08T10:00:00Z', checks: {} },
      sys: { storage_total_mb: 100000, storage_used_mb: 20000, hostname: 'box' },
    })
    render(<BoxHealthPanel />)
    const cpuBar = await screen.findByRole('progressbar', { name: /CPU utilization/i })
    expect(cpuBar.getAttribute('aria-valuenow')).toBe('12')
    expect(cpuBar.getAttribute('aria-valuemax')).toBe('100')
    // Storage derives its percentage from system info (20 GB of 100 GB → 20%).
    const storageBar = await screen.findByRole('progressbar', { name: /Storage utilization/i })
    expect(storageBar.getAttribute('aria-valuenow')).toBe('20')
  })

  it('shows an attention banner when a check is degraded', async () => {
    mockEndpoints({
      health: { status: 'degraded', timestamp: '2026-07-08T10:00:00Z', checks: { disk_space: 'degraded: only 100 MiB free' } },
      healthStatus: 503,
      sys: {},
    })
    render(<BoxHealthPanel />)
    expect(await screen.findByText(/Attention needed/i)).toBeTruthy()
    expect(await screen.findByText(/only 100 MiB free/i)).toBeTruthy()
  })

  it('degrades gracefully when the health probe is unreachable', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('network'))))
    render(<BoxHealthPanel />)
    expect(await screen.findByText(/Health status unavailable/i)).toBeTruthy()
  })
})

// The four tests above all await a reply and then assert on what the panel says
// about it. None of them asks what the panel says BEFORE the reply, or what it
// says about a reply that is not a health report — and the answer to both was
// "All systems healthy", in green, with a tick.
describe('BoxHealthPanel — what it says when it does not know', () => {
  it('does not declare the box healthy before /api/health has answered', async () => {
    // The request never settles: this is the box that is slow to start, or the
    // one that is not answering at all. The panel is going to be in this state
    // for as long as that lasts, and on a box that never answers, for ever.
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
    render(<BoxHealthPanel />)

    expect(screen.queryByText(/All systems healthy/i), 'the panel called the box healthy before it had asked').toBeNull()
    expect(screen.getByText(/Checking the health of this box/i)).toBeTruthy()
  })

  it('does not declare the box healthy when the health probe returns 401', async () => {
    // A real answer, with a real JSON body, that is not a health report. The
    // body parses fine and simply has no `status` — which used to read as
    // "not degraded", and therefore as healthy.
    mockEndpoints({ health: { error: 'unauthorized' }, healthStatus: 401 })
    render(<BoxHealthPanel />)

    expect(await screen.findByText(/Health status unavailable/i)).toBeTruthy()
    expect(screen.queryByText(/All systems healthy/i), 'a 401 was rendered as a healthy box').toBeNull()
  })

  it('does not declare the box healthy when a 200 body carries no status field', async () => {
    mockEndpoints({ health: { checks: {} }, healthStatus: 200 })
    render(<BoxHealthPanel />)

    expect(await screen.findByText(/Health status unavailable/i)).toBeTruthy()
    expect(screen.queryByText(/All systems healthy/i)).toBeNull()
  })

  it('reads CPU as unknown, not as 0%, when telemetry is not connected', async () => {
    // The pill beside this row already says "offline". The meter said 0%.
    mockTelemetry = { stats: null, connected: false }
    mockEndpoints({ health: { status: 'ok', checks: {} }, sys: {} })
    render(<BoxHealthPanel />)

    const cpu = await screen.findByRole('progressbar', { name: /CPU utilization/i })
    // An indeterminate progressbar omits aria-valuenow; ARIA has that omission
    // for exactly this, and 0 is a claim.
    expect(cpu.getAttribute('aria-valuenow'), 'an unmeasured CPU reported a value of 0').toBeNull()
    expect(cpu.getAttribute('aria-valuetext')).toBe('unknown')
    expect(screen.getByText('offline')).toBeTruthy()
    // And the visible readout is a dash, not a number.
    expect(screen.queryByText('0%'), 'an unmeasured CPU printed "0%"').toBeNull()
  })

  it('does not draw a storage bar for a disk it never measured', async () => {
    mockTelemetry = { stats: null, connected: false }
    // /api/system/info answers, but with nothing about storage.
    mockEndpoints({ health: { status: 'ok', checks: {} }, sys: { hostname: 'box' } })
    render(<BoxHealthPanel />)

    const storage = await screen.findByRole('progressbar', { name: /Storage utilization/i })
    expect(storage.getAttribute('aria-valuenow'), 'an unmeasured disk reported 0% used').toBeNull()
  })
})
