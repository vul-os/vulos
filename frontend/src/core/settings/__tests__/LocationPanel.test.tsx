// LocationPanel.test.tsx — what the panel claims about where your position is.
//
// This is the one Settings control that moves a person's physical location off
// their device, so the gap between "armed" and "actually reporting" is not a
// pedantic distinction: while the browser's permission prompt is open, the user
// has not agreed to anything yet, and the panel used to be showing them a green
// line saying their location was going to their box.
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'

let mockStatus: { active: boolean; lastError: string | null; lastSentTs: number | null } =
  { active: false, lastError: null, lastSentTs: null }

vi.mock('../../location/reporter', () => ({
  startLocationReporting: vi.fn(),
  stopLocationReporting: vi.fn(),
  getLocationReportingStatus: () => mockStatus,
}))

import LocationPanel, { LOCATION_SHARE_KEY } from '../LocationPanel'

beforeEach(() => {
  localStorage.setItem(LOCATION_SHARE_KEY, 'on')
  mockStatus = { active: false, lastError: null, lastSentTs: null }
})
afterEach(() => { cleanup(); localStorage.clear(); vi.clearAllMocks() })

describe('LocationPanel — it only claims to be reporting once something was reported', () => {
  it('does not say the location is being sent while the watch is armed but silent', () => {
    // Exactly the state startLocationReporting() leaves behind before
    // watchPosition has called back: active, no error, nothing ever sent.
    // This is also the permanent state of a device that never gets a fix.
    mockStatus = { active: true, lastError: null, lastSentTs: null }
    render(<LocationPanel />)

    expect(
      screen.queryByText(/Reporting your location to your box/i),
      'the panel claimed the position was being sent before anything had been sent',
    ).toBeNull()
    expect(screen.getByText(/Waiting for this device to report a position/i)).toBeTruthy()
  })

  it('says it is reporting once a report has actually landed', () => {
    mockStatus = { active: true, lastError: null, lastSentTs: Date.now() }
    render(<LocationPanel />)

    expect(screen.getByText(/Reporting your location to your box/i)).toBeTruthy()
  })

  it('surfaces a denied permission instead of a reporting claim', () => {
    mockStatus = { active: true, lastError: 'permission_denied', lastSentTs: null }
    render(<LocationPanel />)

    expect(screen.getByText(/permission was denied/i)).toBeTruthy()
    expect(screen.queryByText(/Reporting your location to your box/i)).toBeNull()
  })

  it('says nothing at all about reporting while the setting is off', () => {
    localStorage.removeItem(LOCATION_SHARE_KEY)
    mockStatus = { active: false, lastError: null, lastSentTs: null }
    render(<LocationPanel />)

    expect(screen.queryByText(/Reporting your location/i)).toBeNull()
    expect(screen.queryByText(/Waiting for this device/i)).toBeNull()
  })
})
