import { describe, it, expect, vi } from 'vitest'
import { launchStreamedBrowser, readLaunchError, STREAMED_BROWSER_TITLE } from '../streamedBrowser'

// The defect: a failed browser launch opened a window that spun forever.
//
//   fetch('/api/browser/launch')
//     .then(r => r.ok ? r.json() : null)
//     .catch(() => null)
//     .then(data => openWindow(StreamViewer({ sessionId: (data && data.id) || 'browser' })))
//
// Every failure collapsed to the same value: a 500 became null, null fell back
// to the literal session id 'browser' that nothing had created, and the window
// opened regardless. On bare metal that was the guaranteed outcome, because
// POST /api/browser/launch answered 500 {"error":"chromium not found"} — the
// binary was in the Dockerfile and never in the rootfs.
//
// These pin that a non-ok response is an ERROR and never data.

type Win = { appId: string; title?: string; icon?: string; component?: unknown; singleton?: boolean }

function rig(res: Response | Error) {
  const opened: Win[] = []
  const fetchImpl = vi.fn(async () => {
    if (res instanceof Error) throw res
    return res
  }) as unknown as typeof fetch
  return {
    opened,
    deps: {
      openWindow: (o: Win) => { opened.push(o) },
      viewer: (sessionId: string) => ({ kind: 'viewer', sessionId }),
      errorView: (message: string) => ({ kind: 'error', message }),
      fetchImpl,
    },
  }
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

describe('launchStreamedBrowser', () => {
  it('connects the viewer to the session the server actually created', async () => {
    const { opened, deps } = rig(json({ id: 'browser-u1', display: ':99' }))
    const id = await launchStreamedBrowser(deps)

    expect(id).toBe('browser-u1')
    expect(opened).toHaveLength(1)
    expect(opened[0].component).toEqual({ kind: 'viewer', sessionId: 'browser-u1' })
  })

  it('reports "chromium not found" instead of opening a spinner that never resolves', async () => {
    const { opened, deps } = rig(json({ error: 'chromium not found' }, 500))
    const id = await launchStreamedBrowser(deps)

    expect(id).toBeNull()
    expect(opened).toHaveLength(1)
    const c = opened[0].component as { kind: string; message: string }
    expect(c.kind).toBe('error')
    // The user must be told the actual reason, not a generic failure.
    expect(c.message).toContain('chromium not found')
  })

  it('NEVER falls back to the hardcoded "browser" session id', async () => {
    // This is the precise old bug: `(data && data.id) || 'browser'`.
    for (const res of [json({ error: 'boom' }, 500), json({}, 200), json({ id: '' }, 200)]) {
      const { opened, deps } = rig(res)
      const id = await launchStreamedBrowser(deps)
      expect(id).toBeNull()
      const c = opened[0].component as { kind: string; sessionId?: string }
      expect(c.kind).toBe('error')
      expect(c.sessionId).toBeUndefined()
    }
  })

  it('treats a 200 carrying no session as a failure, not a session', async () => {
    const { opened, deps } = rig(json({ display: ':99' }, 200))
    const id = await launchStreamedBrowser(deps)

    expect(id).toBeNull()
    const c = opened[0].component as { kind: string; message: string }
    expect(c.kind).toBe('error')
    expect(c.message).toMatch(/no session/i)
  })

  it('reports a network failure rather than swallowing it', async () => {
    const { opened, deps } = rig(new TypeError('Failed to fetch'))
    const id = await launchStreamedBrowser(deps)

    expect(id).toBeNull()
    expect(opened).toHaveLength(1)
    const c = opened[0].component as { kind: string; message: string }
    expect(c.kind).toBe('error')
    expect(c.message).toContain('Failed to fetch')
  })

  it('opens exactly one window on every path, under the same title', async () => {
    for (const res of [json({ id: 'ok' }), json({ error: 'x' }, 500), json({}, 200)]) {
      const { opened, deps } = rig(res)
      await launchStreamedBrowser(deps)
      expect(opened).toHaveLength(1)
      expect(opened[0].title).toBe(STREAMED_BROWSER_TITLE)
      expect(opened[0].appId).toBe('browser-stream')
      // Singleton, so a retry replaces the failure rather than stacking windows.
      expect(opened[0].singleton).toBe(true)
    }
  })
})

describe('readLaunchError', () => {
  it('prefers the server\'s own error string', async () => {
    expect(await readLaunchError(json({ error: 'chromium not found' }, 500)))
      .toBe('chromium not found')
  })

  it('falls back to detail, then to raw body, then to the status', async () => {
    expect(await readLaunchError(json({ detail: 'no free display' }, 500)))
      .toBe('no free display')
    expect(await readLaunchError(new Response('plain text boom', { status: 502 })))
      .toBe('plain text boom')
    expect(await readLaunchError(new Response('', { status: 503 })))
      .toContain('503')
  })

  it('never returns an empty reason — an unreadable failure is still a failure', async () => {
    for (const r of [
      json({}, 500),
      new Response('', { status: 500 }),
      new Response('{"error":123}', { status: 500 }),
    ]) {
      expect((await readLaunchError(r)).length).toBeGreaterThan(0)
    }
  })
})
