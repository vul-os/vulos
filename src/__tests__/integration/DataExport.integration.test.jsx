/**
 * DataExport.integration.test.jsx — wave-59.
 *
 * Renders the REAL DataExportPanel and drives the "Download My Data" flow with a
 * mocked fetch, asserting:
 *   - the sovereignty framing + honest coverage/gap copy is present,
 *   - clicking Download hits GET /api/export/data (credentialed) and triggers a
 *     real anchor-click download of the returned zip blob,
 *   - the success state renders,
 *   - a 401 surfaces an honest "session expired" error with a Retry,
 *   - a non-ok response surfaces an error rather than a silent failure.
 *
 * The panel is the user-facing half of the account-portability guarantee; this
 * pins its contract to the /api/export/data endpoint.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import DataExportPanel from '../../core/settings/DataExportPanel.jsx'

let clickedAnchor = null

beforeEach(() => {
  clickedAnchor = null
  // Capture the programmatic download without navigating the jsdom document.
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function () {
    clickedAnchor = { href: this.href, download: this.download }
  })
  // jsdom lacks these blob-URL helpers.
  if (!URL.createObjectURL) URL.createObjectURL = () => 'blob:mock'
  vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:mock')
  if (!URL.revokeObjectURL) URL.revokeObjectURL = () => {}
  vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

function mockFetch(impl) {
  vi.stubGlobal('fetch', vi.fn(impl))
}

describe('DataExportPanel — account portability', () => {
  it('renders the sovereignty framing and honest coverage + gaps', () => {
    render(<DataExportPanel />)
    expect(screen.getByRole('heading', { name: /Export My Data/i })).toBeInTheDocument()
    expect(screen.getByText(/Your data is yours/i)).toBeInTheDocument()
    // Included sections.
    expect(screen.getByText('Mail')).toBeInTheDocument()
    expect(screen.getByText('Files')).toBeInTheDocument()
    expect(screen.getByText('Settings')).toBeInTheDocument()
    // Honest boundary about per-app data.
    expect(screen.getByText(/Not in this archive/i)).toBeInTheDocument()
    expect(screen.getByText(/Talk \/ Meet call history/i)).toBeInTheDocument()
    // No-secret promise stated in the UI too.
    expect(screen.getByText(/API keys, PINs and passwords are never included/i)).toBeInTheDocument()
  })

  it('downloads the archive: credentialed GET → blob → anchor click → success', async () => {
    const user = userEvent.setup()
    mockFetch(async () => ({
      ok: true,
      status: 200,
      headers: { get: (k) => (k === 'Content-Disposition' ? 'attachment; filename="vulos-export-20260706.zip"' : null) },
      blob: async () => new Blob(['PK...'], { type: 'application/zip' }),
    }))

    render(<DataExportPanel />)
    await user.click(screen.getByRole('button', { name: /Download My Data/i }))

    await waitFor(() => expect(global.fetch).toHaveBeenCalled())
    const [url, opts] = global.fetch.mock.calls[0]
    expect(url).toBe('/api/export/data')
    expect(opts.credentials).toBe('same-origin')

    await waitFor(() => expect(clickedAnchor).not.toBeNull())
    expect(clickedAnchor.download).toBe('vulos-export-20260706.zip')

    expect(await screen.findByText(/Your archive is downloading/i)).toBeInTheDocument()
  })

  it('surfaces an honest error + Retry when the session is expired (401)', async () => {
    const user = userEvent.setup()
    mockFetch(async () => ({ ok: false, status: 401, headers: { get: () => null }, blob: async () => new Blob() }))

    render(<DataExportPanel />)
    await user.click(screen.getByRole('button', { name: /Download My Data/i }))

    expect(await screen.findByText(/session has expired/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Retry/i })).toBeInTheDocument()
    expect(clickedAnchor).toBeNull() // nothing downloaded on failure
  })

  it('surfaces an error on a non-ok response rather than failing silently', async () => {
    const user = userEvent.setup()
    mockFetch(async () => ({ ok: false, status: 500, headers: { get: () => null }, blob: async () => new Blob() }))

    render(<DataExportPanel />)
    await user.click(screen.getByRole('button', { name: /Download My Data/i }))

    expect(await screen.findByText(/Export failed \(HTTP 500\)/i)).toBeInTheDocument()
  })
})
