import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, act } from '@testing-library/react'

// The 'qrcode' package needs a real <canvas> 2D context to draw into, which
// jsdom does not provide in this project (no 'canvas' npm dependency — a
// canvas.getContext('2d') call here always returns null). Mocking the module
// isolates the panel's own logic — does it pass the right payload string?
// does it show an honest fallback on failure? — from that environment gap,
// rather than asserting on a QR canvas that can never actually draw under
// vitest+jsdom.
const toCanvasMock = vi.fn((..._args: unknown[]) => Promise.resolve())
vi.mock('qrcode', () => ({ default: { toCanvas: (...args: unknown[]) => toCanvasMock(...args) } }))

import LANPairingPanel, { PairingQR } from '../core/settings/LANPairingPanel'

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

// PairingQR is driven directly here, not through the panel: the panel fetches
// /api/lan/pairing once, has no retry control, and never changes the `payload`
// it hands down — so a failed draw followed by a new payload is unreachable
// from the outside even though the component is written to handle it.
describe('PairingQR — a failed draw must not be permanent', () => {
  it('draws again when new content arrives after a failure', async () => {
    toCanvasMock.mockImplementationOnce(() => Promise.reject(new Error('no 2d context')))

    const { rerender } = render(<PairingQR content="vulos://pair?name=a" />)
    await screen.findByText(/could not render the qr code/i)

    rerender(<PairingQR content="vulos://pair?name=b" />)
    // Swapping the canvas out for the error text left canvasRef.current null,
    // so the effect's own `if (!canvas) return` guard early-returned for every
    // later payload and this second attempt never happened.
    await waitFor(() => expect(toCanvasMock).toHaveBeenCalledTimes(2))
    expect(toCanvasMock.mock.calls[1][1]).toBe('vulos://pair?name=b')
  })

  it('clears the failure only once a draw has actually succeeded, not when one starts', async () => {
    toCanvasMock.mockImplementationOnce(() => Promise.reject(new Error('no 2d context')))
    let finishSecondDraw: () => void = () => {}
    toCanvasMock.mockImplementationOnce(
      () => new Promise<void>(resolve => { finishSecondDraw = () => resolve() }),
    )

    const { rerender } = render(<PairingQR content="vulos://pair?name=a" />)
    await screen.findByText(/could not render the qr code/i)

    rerender(<PairingQR content="vulos://pair?name=b" />)
    await waitFor(() => expect(toCanvasMock).toHaveBeenCalledTimes(2))

    // An optimistic clear at the top of the effect blanks the message while
    // nothing has been drawn yet, leaving the owner with neither a QR code nor
    // the fingerprint sentence pointing them at the text form of the pin.
    expect(screen.getByText(/could not render the qr code/i)).toBeInTheDocument()

    await act(async () => { finishSecondDraw() })
    await waitFor(() => {
      expect(screen.queryByText(/could not render the qr code/i)).toBeNull()
    })
  })
})
