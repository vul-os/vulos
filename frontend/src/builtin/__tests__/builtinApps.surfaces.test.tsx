// builtinApps.surfaces.test.tsx — conformance matrix for the remaining builtins
// this agent owns: Stream, Terminal, Files, Messages, Calendar, Drive,
// Assistant.
//
// Same four questions as builtinApps.system.test.tsx (mounts / own surface /
// deliberate empty state / survives a dead backend), plus a fifth that the
// system apps did not need: these apps hold live sockets, so each is also asked
// what it shows when the socket is refused, and whether closing the window
// actually stops it talking to the box.
//
// The founder's console reported refused upgrades on /api/telemetry,
// /api/peering/stream, /api/notifications/stream and /api/pty, and a video
// player that "just says connecting". Those are the conditions reproduced here.

import { render, screen, cleanup, waitFor, act } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  installFetch, installFailingFetch, installWebSocket, installBrowserStubs,
  settle, expectNotBlank, expectOwnSurface, type FetchRig,
} from './harness'

import StreamViewer from '../stream/StreamViewer'
import FileManager from '../files/FileManager'
import Messages from '../peering/Messages'
import Terminal from '../terminal/Terminal'

let fetchRig: FetchRig | null = null
let socketRig: ReturnType<typeof installWebSocket> | null = null
let restoreStubs: (() => void) | null = null

beforeEach(() => { restoreStubs = installBrowserStubs() })
afterEach(() => {
  cleanup()
  fetchRig?.restore(); fetchRig = null
  socketRig?.restore(); socketRig = null
  restoreStubs?.(); restoreStubs = null
  vi.useRealTimers()
})

// ---------------------------------------------------------------------------
// Stream viewer
//
// This is the app behind the founder's report. Its failure branch used to
// render a spinner over the words "Starting app...", with the actual reason
// demoted to 12px at 40% opacity — so a refused signalling socket, a dead ICE
// path and a 500 from the box all looked exactly like ordinary progress.
// ---------------------------------------------------------------------------

const RUNNING_SESSION = { id: 'sess-1', running: true, width: 1280, height: 720, quality: 'high' }

function mountStream(opts: { sessions?: unknown; socket?: 'open' | 'fail'; failAll?: boolean } = {}) {
  socketRig = installWebSocket(opts.socket ?? 'open')
  fetchRig = opts.failAll
    ? installFailingFetch()
    : installFetch({
      '/api/stream/sessions': { body: opts.sessions ?? [RUNNING_SESSION] },
      '/api/peering/ice': { body: { ice_servers: [] } },
    })
  return render(<StreamViewer sessionId="sess-1" />)
}

describe('Stream viewer', () => {
  it('mounts and puts its real surface — the video element — on screen', async () => {
    const { container } = mountStream()
    await settle(6)

    expectNotBlank(container, 'Stream viewer')
    // The primary surface of a stream viewer is the <video> sink, not the
    // status text over it.
    expect(container.querySelector('video')).toBeTruthy()
    // And it opened the signalling socket for the session it was asked for.
    expect(socketRig!.last('/api/stream/ws')?.url).toContain('id=sess-1')
  })

  it('says "Starting app…" only while it is genuinely waiting for the session', async () => {
    // A session the box knows about but has not started yet: this IS a loading
    // state and is entitled to the spinner.
    mountStream({ sessions: [{ id: 'sess-1', running: false }] })
    await settle(4)
    expect(screen.getByText(/Starting app/)).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  // REGRESSION (defect STREAM-01). The whole point of this file.
  it('shows a failure as a failure, not as "Starting app…" with a spinner', async () => {
    mountStream({ failAll: true })
    await settle(8)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/Stream unavailable/i)
    // The reason must be readable, not decoration.
    expect(alert).toHaveTextContent(/HTTP 500/)
    expect(
      screen.queryByText(/Starting app/),
      'a permanent failure must never be dressed as progress — this is exactly the ' +
      'state the founder saw as a video player that "just says connecting"',
    ).not.toBeInTheDocument()
    // And a way out.
    expect(screen.getByRole('button', { name: /try again|retry/i })).toBeEnabled()
  })

  // REGRESSION (defect STREAM-01, socket half): a refused /api/stream/ws
  // upgrade sets an error, and that error used to render as the same spinner.
  it('reports a refused signalling socket instead of spinning', async () => {
    mountStream({ socket: 'fail' })
    await settle(8)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/Stream unavailable/i)
    expect(screen.queryByText(/Starting app/)).not.toBeInTheDocument()
  })

  // REGRESSION (defect STREAM-02): the "session isn't running yet" poll had no
  // bound at all, so a session that would never start spun forever with nothing
  // to tell the user that nothing was going to happen.
  // A generous timeout: this drives 21 real poll cycles through fake timers on
  // a host that is heavily loaded during these runs. The bound being tested is
  // 20 attempts, not a wall-clock duration.
  it('gives up and explains itself when the session never starts', async () => {
    vi.useFakeTimers()
    mountStream({ sessions: [] })

    // Drive the 1s poll past its bound (MAX_START_ATTEMPTS = 20).
    for (let i = 0; i < 25; i++) {
      await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
    }

    expect(screen.queryByText(/Starting app/)).not.toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent(/no running stream sessions/i)
  }, 30_000)

  // REGRESSION (defect STREAM-03): the retry setTimeout was never tracked, so
  // the unmount cleanup could not cancel it. Closing a stream window whose
  // session was not running left a 1s loop building a fresh RTCPeerConnection
  // and WebSocket forever, against a window that no longer existed.
  it('stops talking to the box once the window is closed', async () => {
    vi.useFakeTimers()
    const { unmount } = mountStream({ sessions: [] })

    await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
    const callsBeforeUnmount = fetchRig!.calls.length
    expect(callsBeforeUnmount, 'expected the start-poll to be running').toBeGreaterThan(1)

    unmount()
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })

    expect(
      fetchRig!.calls.length,
      'an unmounted stream window must not keep polling the box',
    ).toBe(callsBeforeUnmount)
  })
})

// ---------------------------------------------------------------------------
// Files
// ---------------------------------------------------------------------------

// FileManager parses raw `ls -lA` text, so the mock has to answer in that shape.
const LS_OUTPUT = [
  '-rw-r--r-- 1 ada ada  1024 Aug 14 09:10 notes.md',
  'drwxr-xr-x 2 ada ada  4096 Aug 13 22:01 projects',
].join('\n')

function mountFiles(opts: { ls?: string; failExec?: boolean } = {}) {
  fetchRig = installFetch({
    'POST /api/exec': (_url, init) => {
      if (opts.failExec) return { status: 500, body: { error: 'exec not permitted' } }
      const body = typeof init?.body === 'string' ? init.body : ''
      if (body.includes('echo $HOME')) return { body: { output: '/home/ada\n' } }
      return { body: { output: opts.ls ?? LS_OUTPUT } }
    },
  })
  return render(<FileManager />)
}

describe('Files', () => {
  it('mounts and renders the real directory listing', async () => {
    const { container } = mountFiles()
    await settle(8)

    expectNotBlank(container, 'Files')
    // Its own chrome...
    expect(screen.getByText('Places')).toBeInTheDocument()
    expect(screen.getByText('System')).toBeInTheDocument()
    // ...and the actual parsed rows, which is the surface that matters.
    await waitFor(() => expect(screen.getByText('notes.md')).toBeInTheDocument())
    expectOwnSurface(container, /projects/, 'Files')
  })

  it('has a deliberate empty state for an empty directory', async () => {
    mountFiles({ ls: '' })
    await settle(8)
    await waitFor(() => expect(screen.getByText('Empty directory')).toBeInTheDocument())
  })

  // REGRESSION (defect BUILTIN-6): exec() never consulted res.ok, and /api/exec
  // answering 500 with a JSON body parsed cleanly, had no `output` field, and
  // so returned '' — which parses to zero rows and renders the *designed*
  // "Empty directory" state. A permission failure and an empty folder were
  // pixel-identical.
  it('does not present a failed /api/exec as an empty directory', async () => {
    mountFiles({ failExec: true })
    await settle(8)

    await waitFor(() => expect(screen.getByText(/Couldn't read this folder/i)).toBeInTheDocument())
    expect(
      screen.queryByText('Empty directory'),
      'a folder that could not be read is not a folder with nothing in it',
    ).not.toBeInTheDocument()
    // Not stuck mid-read either.
    expect(screen.queryByText('Reading folder…')).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Messages (peering)
// ---------------------------------------------------------------------------

const CONVERSATIONS = [
  { id: 'c1', peer_id: 'vid_abc', peer_name: 'Ada Lovelace', last_message: 'see you then', updated_at: new Date().toISOString() },
]

function mountMessages(opts: { conversations?: unknown; fail?: boolean; socket?: 'open' | 'fail' } = {}) {
  socketRig = installWebSocket(opts.socket ?? 'open')
  fetchRig = opts.fail
    ? installFailingFetch()
    : installFetch({
      '/api/peering/identity': { body: { vulos_id: 'vid_me' } },
      '/api/peering/conversations': { body: opts.conversations ?? CONVERSATIONS },
    })
  return render(<Messages />)
}

describe('Messages', () => {
  it('mounts and renders the real conversation list', async () => {
    const { container } = mountMessages()
    await settle(6)

    expectNotBlank(container, 'Messages')
    expect(screen.getByText('Messages')).toBeInTheDocument()
    // Its own surface: the empty-thread invitation plus a real peer row.
    expect(screen.getByText('Your Messages')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('Ada Lovelace')).toBeInTheDocument())
  })

  it('has a deliberate empty state when there are genuinely no conversations', async () => {
    mountMessages({ conversations: [] })
    await settle(6)
    await waitFor(() => expect(screen.getByText(/No conversations yet/i)).toBeInTheDocument())
  })

  // REGRESSION (defect BUILTIN-7): `r.ok ? r.json() : []` coerced every 500 and
  // 404 into an empty list, and there was no error state in the component at
  // all — so an unreachable box and a brand-new account rendered the identical
  // "No conversations yet. / Start messaging a contact." The app cheerfully
  // reported that you have no correspondents when it simply could not ask.
  it('does not claim you have no conversations when it could not fetch them', async () => {
    mountMessages({ fail: true })
    await settle(6)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/Could not load conversations/i)
    expect(
      screen.queryByText(/No conversations yet/i),
      'an outage must not be reported as an empty address book',
    ).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /try again/i })).toBeEnabled()
  })

  // A refused /api/peering/stream upgrade — one of the four the founder saw —
  // used to change exactly one thing: an 8px dot went grey, with its meaning
  // hidden in a title attribute. The app looked perfectly healthy while live
  // delivery was dead.
  it('says in words that live delivery is down when the peering socket is refused', async () => {
    mountMessages({ socket: 'fail' })
    await settle(8)

    expect(screen.getByText('Offline')).toBeInTheDocument()
    expect(screen.getByText(/Not receiving live messages/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Terminal
//
// xterm.js does mount under jsdom (DOM renderer), so these exercise the real
// component rather than a stand-in.
// ---------------------------------------------------------------------------

const PTY_SESSIONS = [
  { id: 'pty-1', alive: true, title: 'zsh', created_at: new Date().toISOString() },
]

describe('Terminal', () => {
  it('mounts straight into a live terminal when the box has no existing sessions', async () => {
    socketRig = installWebSocket('open')
    fetchRig = installFetch({ '/api/pty/sessions': { body: [] } })
    const { container } = render(<Terminal />)
    await settle(8)

    // Its own surface: an xterm screen, and the pty socket opened with the
    // geometry it measured.
    expect(container.querySelector('.xterm')).toBeTruthy()
    const ws = socketRig.last('/api/pty')
    expect(ws, 'Terminal never opened the pty socket').toBeTruthy()
    expect(ws.url).toMatch(/cols=\d+&rows=\d+/)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('offers the session picker when the box already has a live session', async () => {
    socketRig = installWebSocket('open')
    fetchRig = installFetch({ '/api/pty/sessions': { body: PTY_SESSIONS } })
    const { container } = render(<Terminal />)
    await settle(8)

    expectNotBlank(container, 'Terminal (picker)')
    expect(screen.getByText(/pty-1|zsh/)).toBeInTheDocument()
  })

  // REGRESSION (defect BUILTIN-8): a refused /api/pty upgrade — one of the four
  // in the founder's console — fired error-then-close, and the close handler
  // wrote "[session ended]" in dim grey for a session that never began. No
  // explanation, no way to try again, and a lie about what happened.
  it('says the shell is unavailable when the pty upgrade is refused', async () => {
    socketRig = installWebSocket('fail')
    fetchRig = installFetch({ '/api/pty/sessions': { body: [] } })
    const { container } = render(<Terminal />)
    await settle(8)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/Shell unavailable/i)
    expect(screen.getByRole('button', { name: /reconnect/i })).toBeEnabled()
    expect(
      container.textContent,
      'a connection that was refused never had a session to end',
    ).not.toMatch(/session ended/)
  })

  // REGRESSION (defect BUILTIN-9): while /api/pty/sessions was in flight the
  // root rendered `<div style={{background, height:'100%'}} />` — a coloured
  // rectangle with no text at all, indistinguishable from an app that failed to
  // mount. Only visible on a slow or unreachable box, which is precisely when
  // it matters.
  it('is readable, not a blank rectangle, while the session list is still loading', async () => {
    socketRig = installWebSocket('open')
    fetchRig = installFetch({ '/api/pty/sessions': { hang: true } })
    const { container } = render(<Terminal />)
    await settle(4)

    expectNotBlank(container, 'Terminal (slow box)')
    expect(screen.getByText(/Opening terminal/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// Calendar is deliberately NOT re-tested here.
//
// It needs a ShellProvider and mocks of its /v1 seam, and it already has a
// dedicated suite that asks the same questions properly:
// src/builtin/calendar/__tests__/Calendar.test.tsx covers the month grid, real
// events on it, the agenda switch and the honest "Calendar unavailable. /
// Connect Mail →" degraded state; calendarApi.test.ts covers the transport,
// which — unlike every app repaired in this pass — does throw on a non-2xx.
// Duplicating that here would add a second, weaker assertion of the same
// behaviour.
// ---------------------------------------------------------------------------
