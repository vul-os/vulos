/**
 * Notifications.integration.test.jsx — wave-28 shell integration suite.
 *
 * The wave-13 notification seam: any producer calls notify(); the shell's
 * Toaster and NotificationCenter subscribe. These render BOTH real surfaces
 * against the real store and drive a full producer → toast + center → mute/DND
 * → mark-read flow, plus the critical-bypass-mute contract. No mocking — the
 * store is the system under test end-to-end.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, within, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

// Toasts pulls in useShell only to route action-button clicks to an app; the
// notification flow itself is store-driven, so a minimal stub is enough.
vi.mock('../../providers/ShellProvider', () => ({
  useShell: () => ({ openWindow: vi.fn(), windows: [], minimizeWindow: vi.fn() }),
}))

import Toasts from '../../shell/Toasts.jsx'
import NotificationCenter from '../../shell/NotificationCenter.jsx'
import {
  notify, setMuted, getPrefs, __resetForTests,
} from '../../core/notificationStore.js'

beforeEach(() => { __resetForTests() })
afterEach(() => cleanup())

describe('Notifications — producer → toast (integration)', () => {
  it('a posted notification raises a transient toast', async () => {
    render(<Toasts />)
    act(() => { notify({ title: 'New mail from Grace', source: 'mail' }) })
    expect(await screen.findByText('New mail from Grace')).toBeInTheDocument()
    expect(screen.getByLabelText('Dismiss')).toBeInTheDocument()
  })

  it('an urgent notification is announced assertively (role=alert)', async () => {
    render(<Toasts />)
    act(() => { notify({ title: 'Disk almost full', source: 'system', level: 'urgent' }) })
    const toast = await screen.findByRole('alert')
    expect(within(toast).getByText('Disk almost full')).toBeInTheDocument()
    expect(within(toast).getByText('Urgent')).toBeInTheDocument()
  })

  it('mute (DND) suppresses the toast but a critical still breaks through', async () => {
    render(<Toasts />)
    act(() => { setMuted(true) })
    act(() => { notify({ title: 'quiet mail', source: 'mail' }) })
    // Muted → no toast for a normal notification.
    expect(screen.queryByText('quiet mail')).toBeNull()
    // Critical bypasses mute.
    act(() => { notify({ title: 'BREACH', source: 'system', level: 'critical' }) })
    expect(await screen.findByText('BREACH')).toBeInTheDocument()
  })
})

describe('NotificationCenter — bell, center, mark-read, DND (integration)', () => {
  it('the bell shows the unread count and opens the center', async () => {
    const user = userEvent.setup()
    render(<NotificationCenter />)
    act(() => {
      notify({ title: 'Invoice due', source: 'mail' })
      notify({ title: 'Build passed', source: 'system' })
    })
    // Bell aria-label reflects the unread count.
    const bell = await screen.findByRole('button', { name: /2 unread/i })
    await user.click(bell)
    const panel = await screen.findByRole('dialog', { name: 'Notifications' })
    expect(within(panel).getByText('Invoice due')).toBeInTheDocument()
    expect(within(panel).getByText('Build passed')).toBeInTheDocument()
  })

  it('"Mark all read" clears the unread count', async () => {
    const user = userEvent.setup()
    render(<NotificationCenter />)
    act(() => { notify({ title: 'a', source: 'mail' }); notify({ title: 'b', source: 'mail' }) })
    await user.click(await screen.findByRole('button', { name: /2 unread/i }))
    await user.click(screen.getByRole('button', { name: 'Mark all read' }))
    // Bell drops back to the no-count label.
    expect(await screen.findByRole('button', { name: /^Notifications$/i })).toBeInTheDocument()
  })

  it('the DND switch toggles the store mute pref', async () => {
    const user = userEvent.setup()
    render(<NotificationCenter />)
    act(() => { notify({ title: 'x', source: 'mail' }) })
    await user.click(await screen.findByRole('button', { name: /unread/i }))
    const dnd = screen.getByRole('switch', { name: 'Toggle Do Not Disturb' })
    expect(dnd).toHaveAttribute('aria-checked', 'false')
    await user.click(dnd)
    expect(getPrefs().muted).toBe(true)
    expect(dnd).toHaveAttribute('aria-checked', 'true')
  })
})
