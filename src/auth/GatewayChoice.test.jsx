// GatewayChoice.test.jsx — GATEWAY-01 first-boot gateway selector.
//
// Verifies: default is Vulos Cloud (no custom URL surfaced), switching to a
// custom gateway reveals the URL field, a successful connection test marks the
// choice `tested`, and a failed test keeps it untested (blocks continuation).

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { useState } from 'react'
import GatewayChoice from './GatewayChoice.jsx'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function jsonResponse(status, body) {
  return Promise.resolve({ ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) })
}

// Host owns the value state exactly like the Setup wizard does.
function Host() {
  const [gw, setGw] = useState({ mode: 'cloud', url: '', tested: false })
  return (
    <div>
      <GatewayChoice value={gw} onChange={setGw} idPrefix="t" />
      <span data-testid="mode">{gw.mode}</span>
      <span data-testid="tested">{String(gw.tested)}</span>
      <span data-testid="url">{gw.url}</span>
    </div>
  )
}

describe('GatewayChoice', () => {
  it('defaults to Vulos Cloud and hides the URL field', () => {
    render(<Host />)
    expect(screen.getByTestId('mode').textContent).toBe('cloud')
    expect(screen.queryByLabelText('Gateway URL')).toBeNull()
  })

  it('reveals the URL field when switching to a custom gateway', () => {
    render(<Host />)
    fireEvent.click(screen.getByText('Control plane — using Vulos Cloud (advanced)'))
    fireEvent.click(screen.getByText('Use my own gateway'))
    expect(screen.getByTestId('mode').textContent).toBe('custom')
    expect(screen.getByLabelText('Gateway URL')).toBeTruthy()
  })

  it('marks the choice tested after a successful connection test', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(200, { ok: true, url: 'https://cp.example.org' })))
    render(<Host />)
    fireEvent.click(screen.getByText('Control plane — using Vulos Cloud (advanced)'))
    fireEvent.click(screen.getByText('Use my own gateway'))
    fireEvent.change(screen.getByLabelText('Gateway URL'), { target: { value: 'https://cp.example.org' } })
    fireEvent.click(screen.getByText('Test'))
    await waitFor(() => expect(screen.getByTestId('tested').textContent).toBe('true'))
    expect(screen.getByText(/Gateway reachable/i)).toBeTruthy()
    expect(fetch).toHaveBeenCalledWith('/api/gateway/check', expect.objectContaining({ method: 'POST' }))
  })

  it('keeps the choice untested when the test fails', async () => {
    vi.stubGlobal('fetch', vi.fn(() => jsonResponse(200, { ok: false, error: 'gateway unreachable' })))
    render(<Host />)
    fireEvent.click(screen.getByText('Control plane — using Vulos Cloud (advanced)'))
    fireEvent.click(screen.getByText('Use my own gateway'))
    fireEvent.change(screen.getByLabelText('Gateway URL'), { target: { value: 'https://down.example.org' } })
    fireEvent.click(screen.getByText('Test'))
    await waitFor(() => expect(screen.getByText(/gateway unreachable/i)).toBeTruthy())
    expect(screen.getByTestId('tested').textContent).toBe('false')
  })

  it('rejects a non-https URL client-side without calling the backend', async () => {
    const f = vi.fn(() => jsonResponse(200, { ok: true }))
    vi.stubGlobal('fetch', f)
    render(<Host />)
    fireEvent.click(screen.getByText('Control plane — using Vulos Cloud (advanced)'))
    fireEvent.click(screen.getByText('Use my own gateway'))
    fireEvent.change(screen.getByLabelText('Gateway URL'), { target: { value: 'http://insecure.example.org' } })
    fireEvent.click(screen.getByText('Test'))
    await waitFor(() => expect(screen.getByText(/full https:\/\/ URL/i)).toBeTruthy())
    expect(f).not.toHaveBeenCalled()
    expect(screen.getByTestId('tested').textContent).toBe('false')
  })
})
