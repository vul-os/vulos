// CalendarWidget.test.jsx — the ambient RHS Calendar widget.
//
// Proves the behaviours the shell depends on, now that the widget reads the PIM
// seam (GET /api/pim/calendar/events via calendarApi.listEvents) instead of the
// assistant Home aggregate — it is fully decoupled from /api/assistant/home:
//   1. renders the soonest upcoming event ("what's next")
//   2. expands to show the rest of the week's agenda on click
//   3. degrades honestly (no crash, no invented events) when the calendar
//      backend is unreachable — it shows "Calendar unavailable" / "Connect Mail"
//   4. clicking an event launches the full Calendar app (vulos-calendar)
//   5. an honest empty state on a successful-but-empty read, and a malformed
//      event (unparseable date) is dropped, never rendered blank
//
// The network boundary (/api/pim/calendar/events) is stubbed at fetch; the
// shell's openWindow and the shared launchApp lane are mocked so the assertion
// is about the widget. The stub mirrors the box's proxy: a text body the
// calendarApi decodes (never res.json()), so the widget-vs-API contract is real.

import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const openWindow = vi.fn()
vi.mock('../providers/ShellProvider', () => ({
  useShell: () => ({ openWindow }),
}))

const launchApp = vi.fn()
vi.mock('../shell/launchApp', () => ({
  launchApp: (...args: unknown[]) => launchApp(...args),
}))

// Fresh module graph per test so the widget's module-scoped agenda cache
// (a real feature — it paints last-known state on remount) can't leak a prior
// test's events into a cold-start assertion. Mock registrations survive
// resetModules within a file, so the mocks above still apply after re-import.
async function loadWidget() {
  vi.resetModules()
  return (await import('../shell/CalendarWidget')).default
}

// Two events in the future so the widget shows one as "next" and the other
// under the expanded agenda. Kept relative to now so the future-filter passes.
// Shape is lilmail's /v1 event (uid/title/start), which calendarApi normalizes.
function eventsPayload() {
  const soon = new Date(Date.now() + 2 * 60 * 60 * 1000).toISOString()
  const later = new Date(Date.now() + 26 * 60 * 60 * 1000).toISOString()
  return {
    events: [
      { uid: 'e1', title: 'Standup', start: soon, location: 'HQ' },
      { uid: 'e2', title: 'Design review', start: later },
    ],
  }
}

// stubFetch mirrors the box proxy: a Response whose body is read via res.text()
// (calendarApi never calls res.json()). `status` <300 ⇒ ok.
function stubFetch(status: number, body: unknown) {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
    ok: status < 300,
    status,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body ?? '')),
  }))
}

beforeEach(() => {
  openWindow.mockClear()
  launchApp.mockClear()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('CalendarWidget', () => {
  it('renders the next upcoming event and expands to the week agenda', async () => {
    stubFetch(200, eventsPayload())
    const CalendarWidget = await loadWidget()
    render(<CalendarWidget />)

    // "What's next" strip shows the soonest event.
    await screen.findByText('Standup')
    expect(screen.getByText('HQ')).toBeInTheDocument()
    // The later event is not visible until expanded.
    expect(screen.queryByText('Design review')).not.toBeInTheDocument()

    fireEvent.click(screen.getByLabelText('Expand calendar'))
    await screen.findByText('Design review')
    expect(screen.getByText('Open Calendar →')).toBeInTheDocument()
  })

  it('launches the Calendar app when the next event is clicked', async () => {
    stubFetch(200, eventsPayload())
    const CalendarWidget = await loadWidget()
    render(<CalendarWidget />)

    fireEvent.click(await screen.findByText('Standup'))
    await waitFor(() => expect(launchApp).toHaveBeenCalled())
    const app = launchApp.mock.calls[0][0]
    expect(app.id).toBe('vulos-calendar')
  })

  it('degrades honestly when the calendar backend is unreachable', async () => {
    // 503 → widget shows an unavailable / connect-mail state, never a crash.
    stubFetch(503, 'mail service not configured')
    const CalendarWidget = await loadWidget()
    render(<CalendarWidget />)

    await screen.findByText('Calendar unavailable.')
    expect(screen.getByText('Connect Mail →')).toBeInTheDocument()
    // Crucially: no invented events.
    expect(screen.queryByText('Standup')).not.toBeInTheDocument()
  })

  it('shows an honest empty state when the agenda read succeeds but is empty', async () => {
    stubFetch(200, { events: [] })
    const CalendarWidget = await loadWidget()
    render(<CalendarWidget />)

    await screen.findByText('Nothing on your calendar.')
    expect(screen.getByText('live')).toBeInTheDocument()
  })

  it('does not crash on a malformed event with an unparseable date', async () => {
    stubFetch(200, { events: [{ uid: 'x', title: 'Broken', start: 'not-a-date' }] })
    const CalendarWidget = await loadWidget()
    render(<CalendarWidget />)

    // Bad event has no parseable start → dropped from the agenda → empty state.
    await screen.findByText('Nothing on your calendar.')
    expect(screen.queryByText('Broken')).not.toBeInTheDocument()
  })
})
