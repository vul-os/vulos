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

it('degrades to an honest Connect Mail state when /v1 is down', async () => {
  api.listEvents.mockRejectedValue(new Error('mail service not configured'))
  render(<Calendar />)
  await waitFor(() => expect(screen.getByText('Calendar unavailable.')).toBeInTheDocument())
  fireEvent.click(screen.getByText('Connect Mail →'))
  expect(launchApp).toHaveBeenCalled()
})
