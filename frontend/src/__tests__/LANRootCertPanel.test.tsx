import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent, within, act } from '@testing-library/react'

// Same reason as LANPairingPanel.test.tsx: jsdom in this project has no canvas
// 2D context, so the real 'qrcode' package can never draw. Mocking it isolates
// what this panel is responsible for — WHICH URL it encodes — from that gap.
const toCanvasMock = vi.fn((..._args: unknown[]) => Promise.resolve())
vi.mock('qrcode', () => ({ default: { toCanvas: (...args: unknown[]) => toCanvasMock(...args) } }))

import LANRootCertPanel, { DownloadQR } from '../core/settings/LANRootCertPanel'

// The real GET /api/lan/rootcert wire shape (rootCertInfo in
// backend/cmd/server/lan_rootcert.go).
const ROOT_INFO = {
  present: true,
  subject: 'Vulos LAN Root CA (home)',
  not_before: '2026-08-17T00:00:00Z',
  not_after: '2036-08-14T00:00:00Z',
  expired: false,
  sha256: '52:66:34:A6:BE:62:5F:19:39:5B:8A:56:A2:5B:ED:1A:93:C3:1D:4A:FA:61:9D:F1:EC:FC:5D:80:9A:96:91:26',
  sha1: '0B:91:BB:FA:53:5B:E1:AF:CD:4E:88:72:D5:AF:24:51:6C:7A:99:49',
  permitted_dns: ['local', 'lan', 'home.arpa', 'lan.vulos.org'],
  permitted_ip: ['10.0.0.0/8', '192.168.0.0/16'],
  path_len_zero: true,
  download_path: '/api/lan/rootcert/download',
  download_url: 'https://192.168.1.50:443/api/lan/rootcert/download',
}

function mockRootEndpoint(result: { ok: boolean, status: number, body: unknown }) {
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    if (String(url) === '/api/lan/rootcert') {
      return Promise.resolve({ ok: result.ok, status: result.status, json: () => Promise.resolve(result.body) })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  }))
}

afterEach(() => { cleanup(); vi.unstubAllGlobals(); toCanvasMock.mockClear() })

describe('LANRootCertPanel — getting the root onto a device', () => {
  it('shows the fingerprint and offers the download', async () => {
    mockRootEndpoint({ ok: true, status: 200, body: ROOT_INFO })
    render(<LANRootCertPanel />)

    expect(await screen.findByTestId('root-sha256')).toHaveTextContent(ROOT_INFO.sha256)

    const link = screen.getByRole('link', { name: /download the certificate/i })
    expect(link).toHaveAttribute('href', '/api/lan/rootcert/download')
    expect(link).toHaveAttribute('download', 'vulos-root.crt')
  })

  // The chicken-and-egg is the point of this panel's copy. The owner fetches
  // the root over a connection they have not yet trusted, and the flow has to
  // SAY that rather than imply the download is verified.
  it('states plainly that the first fetch is not authenticated, and says to verify out of band', async () => {
    mockRootEndpoint({ ok: true, status: 200, body: ROOT_INFO })
    render(<LANRootCertPanel />)
    await screen.findByTestId('root-sha256')

    expect(screen.getByText(/your device does not trust/i)).toBeInTheDocument()
    expect(screen.getByText(/not authenticated/i)).toBeInTheDocument()
    // And it must name a way to check that is NOT this connection.
    expect(screen.getByText(/vulos-lanca inspect/)).toBeInTheDocument()
  })

  // The fingerprint has to come BEFORE the download in the document, for the
  // same reason -print-pairing puts the SPKI in front of the operator: a value
  // shown after the irreversible step is not a verification step.
  it('puts the fingerprint ahead of the download button in the document', async () => {
    mockRootEndpoint({ ok: true, status: 200, body: ROOT_INFO })
    render(<LANRootCertPanel />)

    const fp = await screen.findByTestId('root-sha256')
    const link = screen.getByRole('link', { name: /download the certificate/i })
    const order = fp.compareDocumentPosition(link)
    expect(order & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('encodes the box-supplied absolute URL in the QR, not a relative path', async () => {
    mockRootEndpoint({ ok: true, status: 200, body: ROOT_INFO })
    render(<LANRootCertPanel />)
    await screen.findByTestId('root-sha256')

    await waitFor(() => {
      expect(toCanvasMock).toHaveBeenCalledWith(expect.anything(), ROOT_INFO.download_url, expect.any(Object))
    })
  })

  it('shows what the authority is limited to, read off the certificate', async () => {
    mockRootEndpoint({ ok: true, status: 200, body: ROOT_INFO })
    render(<LANRootCertPanel />)
    await screen.findByTestId('root-sha256')

    expect(screen.getByText('local, lan, home.arpa, lan.vulos.org')).toBeInTheDocument()
    expect(screen.getByText('10.0.0.0/8, 192.168.0.0/16')).toBeInTheDocument()
    expect(screen.getByText(/path length 0/i)).toBeInTheDocument()
  })
})

// The QR is the only path onto a phone that does not involve typing an IP by
// hand, so what it does when it CANNOT draw matters. Two things were wrong and
// neither was tested:
//
//  1. The error text replaced the canvas, so canvasRef.current went null and
//     the effect's `if (!canvasRef.current) return` guard early-returned for
//     every later `content`. The first failure was permanent.
//  2. The error was cleared OPTIMISTICALLY at the top of the effect — before
//     the render that was supposed to replace it had drawn anything, and (per
//     1) after the guard that made the clear unreachable anyway.
//
// These tests pin the difference between "cleared because a new render
// succeeded" and "cleared because a new render was attempted".
describe('DownloadQR — a failed render is cleared by a render that WORKED, not one that started', () => {
  it('offers the typed address instead of a QR when the render fails', async () => {
    toCanvasMock.mockImplementationOnce(() => Promise.reject(new Error('no 2d context')))
    render(<DownloadQR content="https://192.168.1.50:443/api/lan/rootcert/download" />)

    const fallback = await screen.findByText(/Could not render the QR code/i)
    expect(fallback).toHaveTextContent('no 2d context')
  })

  it('retries on new content at all — the canvas is not thrown away by the failure', async () => {
    toCanvasMock.mockImplementationOnce(() => Promise.reject(new Error('no 2d context')))
    const { rerender } = render(<DownloadQR content="https://192.168.1.50/a" />)
    await screen.findByText(/Could not render the QR code/i)

    rerender(<DownloadQR content="https://192.168.1.51/b" />)
    // Unmounting the canvas on error left the ref null and this second attempt
    // never happened.
    await waitFor(() => expect(toCanvasMock).toHaveBeenCalledTimes(2))
    expect(toCanvasMock.mock.calls[1][1]).toBe('https://192.168.1.51/b')
  })

  it('keeps the failure on screen while the retry is in flight, and clears it only when the retry succeeds', async () => {
    toCanvasMock.mockImplementationOnce(() => Promise.reject(new Error('no 2d context')))
    let finishSecondRender: () => void = () => {}
    toCanvasMock.mockImplementationOnce(
      () => new Promise<void>(resolve => { finishSecondRender = () => resolve() }),
    )

    const { rerender } = render(<DownloadQR content="https://192.168.1.50/a" />)
    await screen.findByText(/Could not render the QR code/i)

    rerender(<DownloadQR content="https://192.168.1.51/b" />)
    await waitFor(() => expect(toCanvasMock).toHaveBeenCalledTimes(2))

    // THE DISTINCTION. A clear at the top of the effect blanks the message
    // here, while nothing has yet been drawn — leaving the owner with neither
    // a QR code nor the address to type. The last thing known to be true is
    // still the failure, so it stays.
    expect(screen.getByText(/Could not render the QR code/i)).toBeInTheDocument()

    await act(async () => { finishSecondRender() })
    await waitFor(() => {
      expect(screen.queryByText(/Could not render the QR code/i)).toBeNull()
    })
  })
})

describe('LANRootCertPanel — honesty about what was measured', () => {
  // Nobody on this project has installed this root on any device. Every per-OS
  // guide has to say so on its own, because a single global disclaimer would
  // let a platform quietly inherit a neighbour's credibility.
  it('labels every platform guide as published rather than measured', async () => {
    mockRootEndpoint({ ok: true, status: 200, body: ROOT_INFO })
    render(<LANRootCertPanel />)
    await screen.findByTestId('root-sha256')

    const tabs = screen.getAllByRole('tab')
    expect(tabs.length).toBeGreaterThanOrEqual(6)

    for (const tab of tabs) {
      fireEvent.click(tab)
      const source = await screen.findByTestId('os-source')
      expect(source.textContent || '').toMatch(/published/i)
    }
  })

  it('does not claim Chrome, Firefox, Android or iOS enforcement was measured', async () => {
    mockRootEndpoint({ ok: true, status: 200, body: ROOT_INFO })
    render(<LANRootCertPanel />)
    await screen.findByTestId('root-sha256')

    // textContent, not getByText: the sentence is deliberately split by a
    // <strong> around "not", and a matcher that could not see across that
    // would be satisfied by copy that dropped the word entirely.
    const note = screen.getByTestId('constraint-measured')
    expect(note.textContent || '').toMatch(/not\s+been measured on Chrome, Firefox, Android or iOS/i)
    expect(note.textContent || '').toMatch(/measured on Go/i)
  })

  it('warns about Android’s standing “network may be monitored” notice and the app/browser split', async () => {
    mockRootEndpoint({ ok: true, status: 200, body: ROOT_INFO })
    render(<LANRootCertPanel />)
    await screen.findByTestId('root-sha256')

    fireEvent.click(screen.getByRole('tab', { name: 'Android' }))
    expect(await screen.findByText(/network may be monitored/i)).toBeInTheDocument()
    expect(screen.getByText(/apps do NOT trust user-installed CAs/i)).toBeInTheDocument()
  })

  it('warns that iOS needs the SECOND trust step under About → Certificate Trust Settings', async () => {
    mockRootEndpoint({ ok: true, status: 200, body: ROOT_INFO })
    render(<LANRootCertPanel />)
    await screen.findByTestId('root-sha256')

    fireEvent.click(screen.getByRole('tab', { name: /iPhone/i }))
    const panel = await screen.findByRole('tabpanel')
    // Named twice on purpose — once as a numbered step, once as the warning
    // about skipping it — so this asserts "at least once", not "exactly once".
    expect(within(panel).getAllByText(/Certificate Trust Settings/).length).toBeGreaterThan(0)
    expect(within(panel).getByText(/stopping there does not work/i)).toBeInTheDocument()
  })

  it('warns that Firefox keeps its own store on every platform', async () => {
    mockRootEndpoint({ ok: true, status: 200, body: ROOT_INFO })
    render(<LANRootCertPanel />)
    await screen.findByTestId('root-sha256')

    fireEvent.click(screen.getByRole('tab', { name: /Firefox/i }))
    expect(await screen.findByText(/ignores the OS trust store by default/i)).toBeInTheDocument()
  })
})

describe('LANRootCertPanel — states that are not "it worked"', () => {
  it('treats a box with no root as a normal state, not a fault, and says how to fix it', async () => {
    mockRootEndpoint({ ok: true, status: 200, body: { present: false, download_path: '/api/lan/rootcert/download' } })
    render(<LANRootCertPanel />)

    expect(await screen.findByText(/no certificate authority has reached this box yet/i)).toBeInTheDocument()
    expect(screen.getByText(/normal state, not a fault/i)).toBeInTheDocument()
    expect(screen.getByText(/vulos-lanca init/)).toBeInTheDocument()
    // Nothing to install means nothing to offer.
    expect(screen.queryByRole('link', { name: /download the certificate/i })).not.toBeInTheDocument()
  })

  it('surfaces the box’s refusal verbatim rather than a generic error', async () => {
    mockRootEndpoint({
      ok: true,
      status: 200,
      body: {
        present: false,
        problem: 'lan: REFUSING an UNCONSTRAINED LAN CA root: it carries no X.509 permittedSubtrees',
        download_path: '/api/lan/rootcert/download',
      },
    })
    render(<LANRootCertPanel />)

    expect(await screen.findByText(/permittedSubtrees/)).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /download the certificate/i })).not.toBeInTheDocument()
  })

  it('says an expired authority validates nothing but does not lock anyone out', async () => {
    mockRootEndpoint({ ok: true, status: 200, body: { ...ROOT_INFO, expired: true } })
    render(<LANRootCertPanel />)

    expect(await screen.findByText(/this authority has expired/i)).toBeInTheDocument()
    expect(screen.getByText(/validate nothing/i)).toBeInTheDocument()
  })

  it('shows an honest error when the box cannot be reached', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('network unreachable'))))
    render(<LANRootCertPanel />)

    expect(await screen.findByText(/network unreachable/i)).toBeInTheDocument()
    expect(screen.queryByTestId('root-sha256')).not.toBeInTheDocument()
  })
})
