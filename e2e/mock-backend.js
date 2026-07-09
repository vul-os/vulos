// mock-backend.js — the browser-level fake backend for the Playwright E2E suite.
//
// installBackend(page, overrides) registers page.route handlers for every /api
// endpoint the boot → desktop flow touches, returning a healthy, logged-in,
// setup-complete instance by default. Individual specs pass `overrides` (keyed
// by "METHOD path") to drive login errors, proposals, empty states, etc.
//
// The assistant streaming endpoint is answered with a real text/event-stream
// body so the shell's SSE consumer runs for real in the browser.

const PROFILE = { user: { id: 'u1', username: 'ada' }, profile: { username: 'ada', display_name: 'Ada Lovelace' } }

// A quiet-but-alive Home aggregate (GET /api/assistant/home). Home is the desktop
// backdrop shown whenever no windows are open, so it renders on every boot; this
// keeps it from falling through to the empty catch-all. Specs that exercise Home
// pass a richer payload (brief/greeting/agenda/invites) via overrides.
const HOME_PAYLOAD = {
  greeting: 'Good evening, Ada',
  brief: '',
  focus: [],
  agenda: [],
  agenda_fresh: true,
  invites: [],
  activity: [],
  sovereignty: { tier: 'local', label: 'On your device' },
}

const STATUS_LOCAL = {
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

function json(body, status = 200) {
  return { status, contentType: 'application/json', body: JSON.stringify(body) }
}

// Build an SSE body from assistant event frames.
export function sseBody(frames) {
  return {
    status: 200,
    contentType: 'text/event-stream',
    body: frames.map(f => `data: ${JSON.stringify(f)}\n\n`).join(''),
  }
}

// Default responses keyed by "METHOD pathname". A value may be a static object
// (a Playwright fulfill spec) or a function (request) => fulfill-spec.
function defaults() {
  return {
    'GET /api/setup/status': json({ setup_complete: true }),
    'GET /api/auth/status': json({ has_users: true }),
    'GET /api/auth/me': json(PROFILE),
    'POST /api/auth/login': json({ ok: true }),
    'POST /api/auth/register': json({ ok: true }),
    'GET /api/auth/masterkey/envelope': json({}, 404),

    'GET /api/energy/status': json({ screen_on: true, screen_dimmed: false }),
    'POST /api/energy/wake': json({ ok: true }),

    'GET /api/assistant/home': json(HOME_PAYLOAD),
    'GET /api/assistant/status': json(STATUS_LOCAL),
    'POST /api/assistant/tier': json(STATUS_LOCAL),
    'POST /api/assistant/agent/stream': sseBody([{ type: 'token', content: 'Hello.' }, { type: 'done' }]),
    'POST /api/assistant/agent': json({ answer: 'Hello.', proposal: null, error: null }),
    'POST /api/assistant/execute': json({ result: 'Done.' }),

    'GET /api/mail/search': json({ messages: [] }),

    'GET /api/notifications': json([]),
    'GET /api/notifications/unread': json({ unread: 0 }),
    'POST /api/notifications/read': json({ ok: true }),
    'POST /api/notifications/clear': json({ ok: true }),
    'POST /api/notifications/dnd': json({ ok: true }),

    'GET /api/router/classify': json({ lane: 'WebApp' }),
    'POST /api/apps/launch': json({ ok: true }),
    'POST /api/apps/stop': json({ ok: true }),
    'GET /api/store/installed': json([]),
    'GET /api/ai-apps': json([]),
    'GET /api/ai/status': json({ available: true, mode: 'byo', provider: 'ollama', model: 'llama3', providers: [], tier: 'local' }),
    'GET /api/ai/models': json([]),
    'GET /api/shell/windows': json([]),

    'GET /api/files/list': json({ nodes: [] }),
    'GET /api/files/shared-with-me': json({ shares: [] }),
    'GET /api/files/external/status': json({ available: false, providers: [] }),
    'GET /api/files/import/status': json({ available: false, sources: [] }),

    // Web Push (PUSH-CELL-01): default to NOT configured so the settings toggle
    // renders its honest "unavailable" state. Specs that exercise the enabled
    // path override this with { enabled: true, publicKey: '…' }.
    'GET /api/notifications/push/vapid-public': json({ enabled: false }),
  }
}

// Records every intercepted request path (for asserting "nothing was sent").
export async function installBackend(page, overrides = {}) {
  const table = { ...defaults(), ...overrides }
  const seen = []

  // Seed localStorage BEFORE any navigation so the one-time "Introducing the AI
  // assistant" first-run modal (which appears 800ms after boot and would steal
  // keyboard focus / intercept ⌘K) never shows in tests.
  await page.addInitScript(() => {
    try { localStorage.setItem('vulos-ai-firstrun-done', '1') } catch { /* noop */ }
  })

  await page.route('**/api/**', async (route) => {
    const req = route.request()
    const url = new URL(req.url())
    const key = `${req.method()} ${url.pathname}`
    seen.push(key)

    let spec = table[key]
    if (typeof spec === 'function') spec = spec(req)
    if (!spec) {
      // Unmocked /api call → empty 200 rather than a hung request.
      spec = json({})
    }
    await route.fulfill(spec)
  })

  // Silence websocket attempts (notifications stream) — the OS reconnects on
  // close, which is harmless for the tests.
  await page.route('**/api/notifications/stream', (route) => route.abort())

  return { seen, json, sseBody }
}

export { json, PROFILE, STATUS_LOCAL, HOME_PAYLOAD }
