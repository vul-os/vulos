import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'

// Mock the telemetry WS hook so the panel renders deterministic live stats.
let mockTelemetry = { stats: null, connected: false }
vi.mock('../core/useTelemetry', () => ({
  useTelemetry: () => mockTelemetry,
}))

import BoxHealthPanel from '../core/settings/BoxHealthPanel.jsx'

function mockEndpoints({ health, healthStatus = 200, sys }) {
  global.fetch = vi.fn((url) => {
    const u = String(url)
    if (u.includes('/api/health')) {
      return Promise.resolve({ ok: healthStatus === 200, status: healthStatus, json: () => Promise.resolve(health) })
    }
    if (u.includes('/api/system/info')) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(sys) })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  })
}

beforeEach(() => { mockTelemetry = { stats: null, connected: false } })
afterEach(() => cleanup())

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
    global.fetch = vi.fn(() => Promise.reject(new Error('network')))
    render(<BoxHealthPanel />)
    expect(await screen.findByText(/Health status unavailable/i)).toBeTruthy()
  })
})
