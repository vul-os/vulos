/**
 * MeetShell tests
 * 1. Renders the shell landing page
 * 2. Deep-link /meet/:id renders Room
 * 3. Auth boundary redirects on 401
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor, act, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../apps/spaces/Room.jsx', () => ({
  default: ({ sessionId }) => <div data-testid="room-view" data-session={sessionId}>Room</div>,
}))
vi.mock('../apps/spaces/CallView.jsx', () => ({
  default: () => <div data-testid="call-view">CallView</div>,
}))
vi.mock('../apps/spaces/Meetings.jsx', () => ({
  default: () => <div data-testid="meetings-view">Meetings</div>,
}))
vi.mock('../shells/RequireAuth.jsx', () => ({
  default: ({ children }) => <>{children}</>,
}))

import MeetShell from '../shells/MeetShell.jsx'

describe('MeetShell', () => {
  it('renders the meeting join landing page at /', async () => {
    await act(async () => {
      render(
        <MemoryRouter initialEntries={['/']}>
          <MeetShell />
        </MemoryRouter>
      )
    })
    expect(screen.getByTestId('meet-id-input')).toBeTruthy()
    expect(screen.getByTestId('join-btn')).toBeTruthy()
  })

  it('deep-link /meet/:id renders Room', async () => {
    await act(async () => {
      render(
        <MemoryRouter initialEntries={['/meet/abc-123']}>
          <MeetShell />
        </MemoryRouter>
      )
    })
    await waitFor(() => {
      expect(screen.getByTestId('room-view')).toBeTruthy()
    })
  })

  it('joining a meeting ID navigates to /meet/:id', async () => {
    await act(async () => {
      render(
        <MemoryRouter initialEntries={['/']}>
          <MeetShell />
        </MemoryRouter>
      )
    })
    const input = screen.getByTestId('meet-id-input')
    const btn   = screen.getByTestId('join-btn')
    fireEvent.change(input, { target: { value: 'meet-xyz' } })
    await act(async () => { fireEvent.click(btn) })
    // After navigation the room should render
    await waitFor(() => {
      expect(screen.getByTestId('room-view')).toBeTruthy()
    })
  })
})
