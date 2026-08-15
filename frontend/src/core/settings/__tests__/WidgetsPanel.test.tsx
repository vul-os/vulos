/**
 * WidgetsPanel.test.tsx — the grants review surface must tell the truth about
 * what each widget holds.
 *
 * The widget permission model is default-deny and enforced elsewhere; what this
 * panel adds is VISIBILITY, so the things worth pinning are the distinctions a
 * careless render would collapse:
 *   • granted vs "asked for this and was refused" — an off switch alone reads
 *     as "not used yet",
 *   • which grants can move data off the box,
 *   • the hosts a network widget may reach, shown whether or not it is granted,
 *   • that revoking actually persists, and cannot grant beyond the manifest.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import WidgetsPanel from '../WidgetsPanel'
import { registerWidget, defineWidget, clearRegistry } from '../../../widgets/registry'
import { addWidget, defaultLayout, saveLayout, loadLayout, LAYOUT_STORAGE_KEY } from '../../../widgets/layout'

function place(granted: string[]) {
  registerWidget(defineWidget({
    manifest: {
      id: 'test.tides',
      name: 'Tide Times',
      description: 'Local high and low water.',
      version: '1.0.0',
      sizes: ['small'],
      permissions: ['network', 'storage'],
      hosts: ['tides.example.org'],
    },
    render: () => null,
  }))
  const layout = addWidget(defaultLayout(), 'test.tides', { granted: granted as never })
  saveLayout(layout)
  return layout
}

beforeEach(() => {
  clearRegistry()
  try { localStorage.removeItem(LAYOUT_STORAGE_KEY) } catch { /* jsdom */ }
})
afterEach(() => { cleanup(); vi.restoreAllMocks() })

describe('WidgetsPanel', () => {
  it('lists a placed widget and marks a granted off-box permission', async () => {
    place(['network'])
    render(<WidgetsPanel />)
    expect(await screen.findByText('Tide Times')).toBeInTheDocument()
    // The off-box warning is the point of the panel, not decoration.
    expect(screen.getAllByText('Leaves the box').length).toBeGreaterThan(0)
    expect(screen.getByText('Can reach off-box')).toBeInTheDocument()
  })

  it('says a permission was requested and refused, rather than just showing it off', async () => {
    place([]) // requests network + storage, granted neither
    render(<WidgetsPanel />)
    // Both requested permissions are refused, so both rows say so.
    expect((await screen.findAllByText(/Requested, not granted/)).length).toBe(2)
    expect(screen.getByText('Stays on this box')).toBeInTheDocument()
  })

  it('shows the hosts a network widget may reach even when it is not granted', async () => {
    place([])
    render(<WidgetsPanel />)
    expect(await screen.findByText('tides.example.org')).toBeInTheDocument()
  })

  it('revoking a grant persists it', async () => {
    place(['network', 'storage'])
    const user = userEvent.setup()
    render(<WidgetsPanel />)
    const toggle = await screen.findByRole('switch', { name: /Reach the internet for Tide Times/ })
    expect(toggle).toHaveAttribute('aria-checked', 'true')

    await user.click(toggle)

    // Reloaded from storage, not read back off the component's own state — the
    // failure being guarded is a panel that updates its view and writes nothing.
    const saved = loadLayout()
    expect(saved.instances[0].granted).not.toContain('network')
    expect(saved.instances[0].granted).toContain('storage')
  })

  it('renders an empty state when nothing is placed', async () => {
    render(<WidgetsPanel />)
    expect(await screen.findByText('No widgets placed')).toBeInTheDocument()
  })
})
