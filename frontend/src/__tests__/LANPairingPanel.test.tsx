import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

// The 'qrcode' package needs a real <canvas> 2D context to draw into, which
// jsdom does not provide in this project (no 'canvas' npm dependency — a
// canvas.getContext('2d') call here always returns null). Mocking the module
// isolates the panel's own logic — does it pass the right payload string?
// does it show an honest fallback on failure? — from that environment gap,
// rather than asserting on a QR canvas that can never actually draw under
// vitest+jsdom.
const toCanvasMock = vi.fn((..._args: unknown[]) => Promise.resolve())
vi.mock('qrcode', () => ({ default: { toCanvas: (...args: unknown[]) => toCanvasMock(...args) } }))

import LANPairingPanel from '../core/settings/LANPairingPanel'

// The real GET /api/lan/pairing wire shape (backend/cmd/server/lan_pairing.go's
// pairingInfo struct) — name/addr/spki/spki_hex/payload.
const PAIRING_INFO = {
  name: 'my-box',
  addr: '192.168.1.42:443',
  spki: 'c29tZS1iYXNlNjQtc3BraQ==',
  spki_hex: 'AB:CD:EF:01:23:45:67:89',
  payload: 'vulos://pair?name=my-box&addr=192.168.1.42%3A443&spki=c29tZS1iYXNlNjQtc3BraQ%3D%3D',
}

function mockPairingEndpoint(result: { ok: boolean, status: number, body: unknown }) {
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    const u = String(url)
    if (u === '/api/lan/pairing') {
      return Promise.resolve({ ok: result.ok, status: result.status, json: () => Promise.resolve(result.body) })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  }))
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); toCanvasMock.mockClear() })

describe('LANPairingPanel — pairing code display', () => {
  it('renders the fingerprint, address and name from a mocked response, and draws the QR payload', async () => {
    mockPairingEndpoint({ ok: true, status: 200, body: PAIRING_INFO })
    render(<LANPairingPanel />)

    expect(await screen.findByText('AB:CD:EF:01:23:45:67:89')).toBeInTheDocument()
    expect(screen.getByText('192.168.1.42:443')).toBeInTheDocument()
    expect(screen.getByText('my-box')).toBeInTheDocument()
    // The full vulos://pair?... payload — not just the fingerprint — is what
    // gets encoded into the QR code. The draw call happens in a child effect
    // that flushes after the text commit, so wait for it rather than racing it.
    await waitFor(() => {
      expect(toCanvasMock).toHaveBeenCalledWith(expect.anything(), PAIRING_INFO.payload, expect.any(Object))
    })
  })

  it('is honest that no native client exists yet to scan the code', async () => {
    mockPairingEndpoint({ ok: true, status: 200, body: PAIRING_INFO })
    render(<LANPairingPanel />)
    await screen.findByText('AB:CD:EF:01:23:45:67:89')

    expect(screen.getByText(/no native client app exists yet/i)).toBeInTheDocument()
  })

  it('shows an honest error state when the endpoint fails, not a blank or fake QR', async () => {
    mockPairingEndpoint({ ok: false, status: 500, body: { error: 'compute SPKI fingerprint: no certificate on disk' } })
    render(<LANPairingPanel />)

    expect(await screen.findByText(/no certificate on disk/i)).toBeInTheDocument()
    expect(screen.queryByText('my-box')).not.toBeInTheDocument()
    expect(toCanvasMock).not.toHaveBeenCalled()
  })

  it('shows an honest error state when the request itself fails', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('network unreachable'))))
    render(<LANPairingPanel />)

    expect(await screen.findByText(/network unreachable/i)).toBeInTheDocument()
    expect(toCanvasMock).not.toHaveBeenCalled()
  })

  it('shows an honest fallback (not a blank canvas) when QR rendering itself fails, without hiding the fingerprint', async () => {
    toCanvasMock.mockImplementationOnce(() => Promise.reject(new Error('canvas unavailable')))
    mockPairingEndpoint({ ok: true, status: 200, body: PAIRING_INFO })
    render(<LANPairingPanel />)

    expect(await screen.findByText(/could not render the qr code/i)).toBeInTheDocument()
    // The fingerprint is still shown as a fallback way to compare the pin,
    // even though the QR itself failed to draw.
    expect(screen.getByText('AB:CD:EF:01:23:45:67:89')).toBeInTheDocument()
  })
})
