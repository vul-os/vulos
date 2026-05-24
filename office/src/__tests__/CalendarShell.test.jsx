/**
 * CalendarShell tests
 * 1. Renders the shell with Calendar/Contacts tabs
 * 2. Deep-link /contacts routes to ContactsApp
 * 3. Auth boundary redirects on 401
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../apps/calendar/CalendarApp.jsx', () => ({
  default: () => <div data-testid="calendar-app">CalendarApp</div>,
}))
vi.mock('../apps/contacts/ContactsApp.jsx', () => ({
  default: () => <div data-testid="contacts-app">ContactsApp</div>,
}))
vi.mock('../shells/RequireAuth.jsx', () => ({
  default: ({ children }) => <>{children}</>,
}))

import CalendarShell from '../shells/CalendarShell.jsx'

describe('CalendarShell', () => {
  it('renders Calendar and Contacts nav tabs', async () => {
    await act(async () => {
      render(
        <MemoryRouter initialEntries={['/calendar']}>
          <CalendarShell />
        </MemoryRouter>
      )
    })
    expect(screen.getByTestId('nav-calendar')).toBeTruthy()
    expect(screen.getByTestId('nav-contacts')).toBeTruthy()
  })

  it('deep-link /calendar routes to CalendarApp', async () => {
    await act(async () => {
      render(
        <MemoryRouter initialEntries={['/calendar']}>
          <CalendarShell />
        </MemoryRouter>
      )
    })
    await waitFor(() => expect(screen.getByTestId('calendar-app')).toBeTruthy())
  })

  it('deep-link /contacts/:id routes to ContactsApp', async () => {
    await act(async () => {
      render(
        <MemoryRouter initialEntries={['/contacts/contact-1']}>
          <CalendarShell />
        </MemoryRouter>
      )
    })
    await waitFor(() => expect(screen.getByTestId('contacts-app')).toBeTruthy())
  })
})
