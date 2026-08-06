/**
 * OfflineIndicator.test.jsx — banner UI states (OFFLINE-02 / -03).
 *
 * Covers the four visible states the OS shell can show:
 *   • all green (online, endpoint OK, queue empty)       → nothing rendered
 *   • offline                                            → "Offline — using local box"
 *   • online + queue non-empty                           → "N change(s) queued — sending…"
 *   • offline + queue non-empty                          → "Offline — N queued to send when online"
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, act, cleanup } from '@testing-library/react'
import OfflineIndicator from '../components/OfflineIndicator.jsx'
import { enqueue, clear } from '../lib/offlineQueue.js'

beforeEach(() => {
  try { localStorage.clear() } catch { /* ignore */ }
  delete window.__VULOS_ENDPOINTS__
  Object.defineProperty(navigator, 'onLine', {
    value: true, configurable: true, writable: true,
  })
})

afterEach(() => {
  cleanup()
  clear()
})

function setOnline(v: boolean) {
  Object.defineProperty(navigator, 'onLine', { value: v, configurable: true, writable: true })
  window.dispatchEvent(new Event(v ? 'online' : 'offline'))
}

describe('OfflineIndicator', () => {
  it('renders nothing when online, no remote configured, and queue empty', () => {
    render(<OfflineIndicator />)
    expect(screen.queryByTestId('offline-indicator')).toBeNull()
  })

  it('renders an offline banner when navigator goes offline', () => {
    render(<OfflineIndicator />)
    act(() => setOnline(false))
    const el = screen.getByTestId('offline-indicator')
    expect(el.textContent).toMatch(/Offline/i)
    expect(el.textContent).toMatch(/local box/i)
  })

  it('shows queue-length when items are enqueued while online', () => {
    render(<OfflineIndicator />)
    act(() => { enqueue({ body: { path: '/api/a' } }) })
    const el = screen.getByTestId('offline-indicator')
    expect(el.textContent).toMatch(/1 change queued/i)
  })

  it('combines offline + queued message when both are true', () => {
    render(<OfflineIndicator />)
    act(() => { enqueue({ body: { path: '/api/a' } }) })
    act(() => { enqueue({ body: { path: '/api/b' } }) })
    act(() => setOnline(false))
    const el = screen.getByTestId('offline-indicator')
    expect(el.textContent).toMatch(/Offline/i)
    expect(el.textContent).toMatch(/2 queued/i)
  })

  it('disappears again once the queue empties and we are online', () => {
    render(<OfflineIndicator />)
    act(() => { enqueue({ body: { path: '/api/a' } }) })
    expect(screen.getByTestId('offline-indicator')).toBeTruthy()
    act(() => { clear() })
    expect(screen.queryByTestId('offline-indicator')).toBeNull()
  })
})
