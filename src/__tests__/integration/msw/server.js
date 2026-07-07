// MSW (Mock Service Worker) server for the RTL integration suite.
//
// This is the "backend" for the wave-28 shell integration tests: it intercepts
// real `fetch()` calls the shell makes to `/api/**` and `/files/**` and answers
// them with mock responses — so we can render real component trees (App, the
// command palette, the assistant panel, Drive, settings) against a fake backend
// and exercise entire flows end-to-end inside jsdom.
//
// The default handlers below give every test a sane, logged-in, first-boot-done
// baseline. Individual tests override specific endpoints with `server.use(...)`
// to drive validation errors, proposals, empty inboxes, etc.
//
// SSE note: the assistant streaming endpoint (/api/assistant/agent/stream) is
// answered with a real ReadableStream so the shell's `agentStream.js` SSE
// consumer runs for real — the proposal / token flow is genuinely integrated,
// not stubbed at the component boundary.

import { setupServer } from 'msw/node'
import { http, HttpResponse } from 'msw'

// ── SSE helper ────────────────────────────────────────────────────────────────
// Build a streaming Response body from a list of assistant event objects, each
// serialized as a `data: {json}\n\n` SSE frame.
export function sse(frames) {
  const enc = new TextEncoder()
  const stream = new ReadableStream({
    start(controller) {
      for (const f of frames) {
        controller.enqueue(enc.encode(`data: ${JSON.stringify(f)}\n\n`))
      }
      controller.close()
    },
  })
  return new HttpResponse(stream, {
    headers: { 'Content-Type': 'text/event-stream' },
  })
}

// A default logged-in profile.
export const PROFILE = { username: 'ada', display_name: 'Ada Lovelace' }

// The default sovereignty/assistant status — the honest "on device" (local) tier.
export const STATUS_LOCAL = {
  tier: 'local',
  label: 'On your device',
  mail_source: 'lilmail',
  sovereignty: { tier: 'local', provider: null, model: null, endpoint: null, external_allowed: false },
  tier_options: [
    { tier: 'local', label: 'On your device' },
    { tier: 'sovereign', label: 'Operator-declared endpoint (unverified)' },
    { tier: 'brokered', label: 'Brokered · no-train' },
  ],
}

// ── Default handlers — a healthy, logged-in, setup-complete instance ──────────
export const defaultHandlers = [
  // Boot / auth
  http.get('/api/setup/status', () => HttpResponse.json({ setup_complete: true })),
  http.get('/api/auth/status', () => HttpResponse.json({ has_users: true })),
  http.get('/api/auth/me', () => HttpResponse.json({ user: { id: 'u1', username: 'ada' }, profile: PROFILE })),
  http.post('/api/auth/login', () => HttpResponse.json({ ok: true })),
  http.post('/api/auth/register', () => HttpResponse.json({ ok: true })),
  http.get('/api/auth/masterkey/envelope', () => new HttpResponse(null, { status: 404 })),

  // Energy loop (App.jsx polls this every 5s)
  http.get('/api/energy/status', () => HttpResponse.json({ screen_on: true, screen_dimmed: false })),
  http.post('/api/energy/wake', () => HttpResponse.json({ ok: true })),

  // Assistant / sovereignty
  http.get('/api/assistant/status', () => HttpResponse.json(STATUS_LOCAL)),
  http.post('/api/assistant/tier', async ({ request }) => {
    const { tier } = await request.json()
    return HttpResponse.json({ ...STATUS_LOCAL, tier, label: tier })
  }),
  http.post('/api/assistant/agent/stream', () => sse([{ type: 'token', content: 'Hello.' }, { type: 'done' }])),
  http.post('/api/assistant/agent', () => HttpResponse.json({ answer: 'Hello.', proposal: null, error: null })),
  http.post('/api/assistant/execute', () => HttpResponse.json({ result: 'Done — email sent.' })),

  // Mail search (command palette)
  http.get('/api/mail/search', () => HttpResponse.json({ messages: [] })),

  // Notifications bridge
  http.get('/api/notifications', () => HttpResponse.json([])),
  http.get('/api/notifications/unread', () => HttpResponse.json({ unread: 0 })),
  http.post('/api/notifications/read', () => HttpResponse.json({ ok: true })),
  http.post('/api/notifications/clear', () => HttpResponse.json({ ok: true })),
  http.post('/api/notifications/dnd', () => HttpResponse.json({ ok: true })),

  // App launch / router (used by launchApp)
  http.get('/api/router/classify', () => HttpResponse.json({ lane: 'WebApp' })),
  http.post('/api/apps/launch', () => HttpResponse.json({ ok: true })),
  http.post('/api/apps/stop', () => HttpResponse.json({ ok: true })),
  http.get('/api/store/installed', () => HttpResponse.json([])),
  http.get('/api/ai-apps', () => HttpResponse.json([])),

  // AI router settings
  http.get('/api/ai/status', () => HttpResponse.json({ available: true, mode: 'byo', provider: 'ollama', model: 'llama3', providers: [], tier: 'local' })),
  http.get('/api/ai/models', () => HttpResponse.json([])),

  // Drive — the OS lib/api `request()` helper prefixes /api, so these live under
  // /api/files/* (Drive calls request('/files/...')).
  http.get('/api/files/list', () => HttpResponse.json({ nodes: [] })),
  http.get('/api/files/shared-with-me', () => HttpResponse.json({ shares: [] })),
  http.get('/api/files/external/status', () => HttpResponse.json({ available: false, providers: [] })),
  http.get('/api/files/import/status', () => HttpResponse.json({ available: false, sources: [] })),

  // Export
  http.get('/api/export/data', () => new HttpResponse(new Blob(['ZIP'])), { headers: { 'Content-Type': 'application/zip' } }),
]

export const server = setupServer(...defaultHandlers)
export { http, HttpResponse }
