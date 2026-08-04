/**
 * Transparency.integration.test.jsx — wave-28 shell integration suite.
 *
 * The legible-trust surface: the TrustBadge shows the HONEST AI tier read live
 * from /api/assistant/status, and clicking it opens the TransparencyPanel with
 * the inline tier picker (real POST /api/assistant/tier) and the "export my
 * data" action. Rendered inside the REAL SovereigntyProvider with MSW as the
 * backend, so the badge → panel → tier-change → status-refresh flow is real.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { server, http, HttpResponse, STATUS_LOCAL } from './msw/server.js'
import { SovereigntyProvider } from '../../core/useSovereignty'
import TrustBadge from '../../shell/TrustBadge.jsx'
import TransparencyPanel from '../../shell/TransparencyPanel.jsx'

function mount() {
  return render(
    <SovereigntyProvider>
      <TrustBadge />
      <TransparencyPanel />
    </SovereigntyProvider>,
  )
}

afterEach(() => cleanup())

describe('TrustBadge — honest tier (integration)', () => {
  it('renders the on-device tier read from /api/assistant/status', async () => {
    mount()
    // The badge short label for the local tier is "On device".
    expect(await screen.findByText('On device')).toBeInTheDocument()
  })

  it('shows the Brokered tier honestly when the backend reports it', async () => {
    server.use(http.get('/api/assistant/status', () => HttpResponse.json({
      ...STATUS_LOCAL, tier: 'brokered', label: 'Brokered · no-train',
      sovereignty: { tier: 'brokered', provider: 'anthropic', model: 'claude', external_allowed: true },
    })))
    mount()
    expect(await screen.findByText('Brokered')).toBeInTheDocument()
  })
})

describe('TransparencyPanel — open + tier picker + export (integration)', () => {
  it('clicking the badge opens the transparency dialog', async () => {
    const user = userEvent.setup()
    mount()
    await screen.findByText('On device')
    await user.click(screen.getByRole('button', { name: 'Sovereignty and privacy status' }))
    expect(await screen.findByRole('dialog', { name: 'Transparency and sovereignty' })).toBeInTheDocument()
    expect(screen.getByText('Where your AI runs')).toBeInTheDocument()
  })

  it('picking a new tier POSTs it to /api/assistant/tier', async () => {
    let tierBody = null
    server.use(http.post('/api/assistant/tier', async ({ request }) => {
      tierBody = await request.json()
      return HttpResponse.json({ ...STATUS_LOCAL, tier: tierBody.tier, label: tierBody.tier })
    }))
    const user = userEvent.setup()
    mount()
    await screen.findByText('On device')
    await user.click(screen.getByRole('button', { name: 'Sovereignty and privacy status' }))
    const dialog = await screen.findByRole('dialog', { name: 'Transparency and sovereignty' })

    // Pick the sovereign tier from the inline picker.
    await user.click(within(dialog).getByText('Operator-declared endpoint (unverified)'))
    await waitFor(() => expect(tierBody).toEqual({ tier: 'sovereign' }))
  })

  it('exposes the "export my data" download to /api/export/data', async () => {
    const user = userEvent.setup()
    mount()
    await screen.findByText('On device')
    await user.click(screen.getByRole('button', { name: 'Sovereignty and privacy status' }))
    const dialog = await screen.findByRole('dialog', { name: 'Transparency and sovereignty' })
    const link = within(dialog).getByRole('link', { name: /Download my data/i })
    expect(link).toHaveAttribute('href', '/api/export/data')
  })
})
