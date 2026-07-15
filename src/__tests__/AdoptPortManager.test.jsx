import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor, act } from '@testing-library/react'

import AdoptPortManager from '../core/AdoptPortManager.jsx'

// Drive the backend seam through a fetch mock so we exercise the real component
// logic (list → adopt → revoke) without a live server.
function mockBackend(initial = []) {
  const state = { items: [...initial], posted: null, deleted: null }
  global.fetch = vi.fn((url, opts = {}) => {
    const u = String(url)
    const method = (opts.method || 'GET').toUpperCase()
    if (u.endsWith('/api/apps/proxy') && method === 'GET') {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(state.items) })
    }
    if (u.endsWith('/api/apps/proxy') && method === 'POST') {
      state.posted = JSON.parse(opts.body)
      const item = {
        id: 'my-grafana', name: state.posted.name, port: state.posted.port,
        url: '/app/my-grafana/', healthy: true,
      }
      state.items = [...state.items, item]
      return Promise.resolve({ ok: true, status: 201, json: () => Promise.resolve(item) })
    }
    if (u.includes('/api/apps/proxy/') && method === 'DELETE') {
      state.deleted = decodeURIComponent(u.split('/api/apps/proxy/')[1])
      state.items = state.items.filter(i => i.id !== state.deleted)
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ status: 'revoked' }) })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  })
  return state
}

function open() {
  act(() => { window.dispatchEvent(new CustomEvent('vulos:open-adopt-port')) })
}

afterEach(() => cleanup())
beforeEach(() => { vi.restoreAllMocks() })

describe('AdoptPortManager', () => {
  it('is hidden until the open event fires', () => {
    mockBackend()
    render(<AdoptPortManager />)
    expect(screen.queryByRole('dialog')).toBeNull()
    open()
    expect(screen.getByRole('dialog')).toBeTruthy()
  })

  it('lists adopted ports with health + an Open link to URLForApp(id)', async () => {
    mockBackend([{ id: 'grafana', name: 'Grafana', port: 3000, url: '/app/grafana/', healthy: true }])
    render(<AdoptPortManager />)
    open()
    expect(await screen.findByText('Grafana')).toBeTruthy()
    const link = screen.getByText('Open').closest('a')
    expect(link.getAttribute('href')).toBe('/app/grafana/')
  })

  it('adopts a port via POST /api/apps/proxy and refreshes the list', async () => {
    const state = mockBackend()
    render(<AdoptPortManager />)
    open()
    fireEvent.change(screen.getByLabelText('App name'), { target: { value: 'My Grafana' } })
    fireEvent.change(screen.getByLabelText('Local port'), { target: { value: '33000' } })
    fireEvent.click(screen.getByText('Adopt'))
    await waitFor(() => expect(state.posted).toBeTruthy())
    expect(state.posted).toMatchObject({ name: 'My Grafana', port: 33000 })
    expect(await screen.findByText('My Grafana')).toBeTruthy()
  })

  it('revokes an adopted port via DELETE', async () => {
    const state = mockBackend([{ id: 'grafana', name: 'Grafana', port: 3000, url: '/app/grafana/', healthy: false }])
    render(<AdoptPortManager />)
    open()
    await screen.findByText('Grafana')
    fireEvent.click(screen.getByText('Revoke'))
    await waitFor(() => expect(state.deleted).toBe('grafana'))
  })
})
