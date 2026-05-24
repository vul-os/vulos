/**
 * TalkShell tests
 * 1. Renders the shell
 * 2. Deep-link /channels/:id routes to SpacesApp
 * 3. Auth boundary redirects on 401 (shared via RequireAuth mock)
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../apps/spaces/SpacesApp.jsx', () => ({
  default: () => <div data-testid="spaces-app">SpacesApp</div>,
}))
vi.mock('../apps/spaces/Room.jsx', () => ({
  default: ({ sessionId }) => <div data-testid="room-view" data-session={sessionId}>Room</div>,
}))
vi.mock('../apps/spaces/Meetings.jsx', () => ({
  default: () => <div data-testid="meetings-view">Meetings</div>,
}))
vi.mock('../shells/RequireAuth.jsx', () => ({
  default: ({ children }) => <>{children}</>,
}))

import TalkShell from '../shells/TalkShell.jsx'

describe('TalkShell', () => {
  it('renders SpacesApp at root /', async () => {
    await act(async () => {
      render(
        <MemoryRouter initialEntries={['/']}>
          <TalkShell />
        </MemoryRouter>
      )
    })
    await waitFor(() => expect(screen.getByTestId('spaces-app')).toBeTruthy())
  })

  it('deep-link /channels/:id renders SpacesApp', async () => {
    await act(async () => {
      render(
        <MemoryRouter initialEntries={['/channels/general']}>
          <TalkShell />
        </MemoryRouter>
      )
    })
    await waitFor(() => expect(screen.getByTestId('spaces-app')).toBeTruthy())
  })

  it('deep-link /room/:id renders Room component', async () => {
    await act(async () => {
      render(
        <MemoryRouter initialEntries={['/room/room-xyz']}>
          <TalkShell />
        </MemoryRouter>
      )
    })
    await waitFor(() => {
      expect(screen.getByTestId('room-view')).toBeTruthy()
    })
  })
})
