// Calendar.test.jsx — the standalone Calendar app.
//
// Guards: it renders the month grid without white-screening, surfaces events,
// switches to the agenda, opens the editor to create an event (write goes to the
// /v1 seam), and degrades to an honest "Connect Mail" state when /v1 is down.

import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

const openWindow = vi.fn()
vi.mock('../../../providers/ShellProvider', () => ({ useShell: () => ({ openWindow }) }))
const launchApp = vi.fn()
vi.mock('../../../shell/launchApp', () => ({ launchApp: (...a) => launchApp(...a) }))
vi.mock('../../../core/AppRegistry', () => ({ getAppById: (id) => ({ id }) }))

// Mock the /v1 seam so the component test is about the UI, not the network.
const api = vi.hoisted(() => ({
  listEvents: vi.fn(),
  createEvent: vi.fn(),
  updateEvent: vi.fn(),
  deleteEvent: vi.fn(),
}))
vi.mock('../calendarApi', async () => {
  const real = await vi.importActual('../calendarApi')
  return { ...real, ...api }
})

import Calendar from '../Calendar'

beforeEach(() => {
  api.listEvents.mockReset().mockResolvedValue([])
  api.createEvent.mockReset().mockResolvedValue({})
  api.updateEvent.mockReset().mockResolvedValue({})
  api.deleteEvent.mockReset().mockResolvedValue(null)
  openWindow.mockReset(); launchApp.mockReset()
})
afterEach(cleanup)

it('renders the month grid without crashing', async () => {
  render(<Calendar />)
  await waitFor(() => expect(api.listEvents).toHaveBeenCalled())
  // Weekday header proves the grid mounted.
  expect(screen.getByText('Mon')).toBeInTheDocument()
  expect(screen.getByText('+ New event')).toBeInTheDocument()
})

it('shows an event and reveals it in the agenda view', async () => {
  const start = new Date(Date.now() + 60 * 60 * 1000)
  api.listEvents.mockResolvedValue([
    { id: 'e1', title: 'Launch review', start: start.toISOString(), _start: start, _end: null, location: 'War room', notes: '', allDay: false },
  ])
  render(<Calendar />)
  await waitFor(() => expect(screen.getAllByText('Launch review').length).toBeGreaterThan(0))
  fireEvent.click(screen.getByText('Agenda'))
  expect(screen.getByText('War room')).toBeInTheDocument()
})

it('opens the editor and creates an event via the /v1 seam', async () => {
  render(<Calendar />)
  await waitFor(() => expect(api.listEvents).toHaveBeenCalled())
  fireEvent.click(screen.getByText('+ New event'))
  const title = await screen.findByPlaceholderText('Event title')
  fireEvent.change(title, { target: { value: 'Dentist' } })
  fireEvent.click(screen.getByText('Save'))
  await waitFor(() => expect(api.createEvent).toHaveBeenCalled())
  expect(api.createEvent.mock.calls[0][0]).toMatchObject({ title: 'Dentist' })
})

it('edits an existing event via the /v1 update seam', async () => {
  const start = new Date(Date.now() + 60 * 60 * 1000)
  api.listEvents.mockResolvedValue([
    { id: 'e1', title: 'Launch review', start: start.toISOString(), _start: start, _end: null, location: '', notes: '', allDay: false },
  ])
  render(<Calendar />)
  const evt = await screen.findAllByText('Launch review')
  fireEvent.click(evt[0])
  // The editor opens on the existing event (Edit mode, Delete affordance shown).
  const title = await screen.findByDisplayValue('Launch review')
  fireEvent.change(title, { target: { value: 'Launch review v2' } })
  fireEvent.click(screen.getByText('Save'))
  await waitFor(() => expect(api.updateEvent).toHaveBeenCalled())
  expect(api.updateEvent.mock.calls[0][0]).toBe('e1')
  expect(api.updateEvent.mock.calls[0][1]).toMatchObject({ title: 'Launch review v2' })
})

it('deletes an existing event via the /v1 seam', async () => {
  const start = new Date(Date.now() + 60 * 60 * 1000)
  api.listEvents.mockResolvedValue([
    { id: 'e1', title: 'Dentist', start: start.toISOString(), _start: start, _end: null, location: '', notes: '', allDay: false },
  ])
  render(<Calendar />)
  const evt = await screen.findAllByText('Dentist')
  fireEvent.click(evt[0])
  fireEvent.click(await screen.findByText('Delete'))
  await waitFor(() => expect(api.deleteEvent).toHaveBeenCalledWith('e1'))
})

it('degrades to an honest Connect Mail state when /v1 is down', async () => {
  api.listEvents.mockRejectedValue(new Error('mail service not configured'))
  render(<Calendar />)
  await waitFor(() => expect(screen.getByText('Calendar unavailable.')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Connect Mail →'))
  expect(launchApp).toHaveBeenCalled()
})

it('honours the ?action=new deep-link by pre-opening the new-event editor', async () => {
  // ⌘K "New event" launches the builtin with initialQuery="action=new"; the
  // Calendar must open its editor on mount (not just show the month grid).
  render(<Calendar initialQuery="action=new" />)
  const editor = await screen.findByPlaceholderText('Event title')
  expect(editor).toBeInTheDocument()
  expect(screen.getByText('New event')).toBeInTheDocument()
})

it('does NOT open the editor for a plain launch (no deep-link query)', async () => {
  render(<Calendar />)
  await waitFor(() => expect(api.listEvents).toHaveBeenCalled())
  expect(screen.queryByPlaceholderText('Event title')).not.toBeInTheDocument()
})

it('ignores an unknown ?action value (opens the plain month view)', async () => {
  render(<Calendar initialQuery="action=bogus" />)
  await waitFor(() => expect(api.listEvents).toHaveBeenCalled())
  expect(screen.queryByPlaceholderText('Event title')).not.toBeInTheDocument()
})
