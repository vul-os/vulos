#!/usr/bin/env node
/**
 * Vulos OS marketing screenshot generator.
 *
 * PHILOSOPHY (privacy-first, no faking):
 *   This drives the REAL shipping React shell (the production `vite build`
 *   bundle served by `vite preview`) and mocks the entire backend at the
 *   browser network layer — exactly like the Playwright E2E suite
 *   (e2e/mock-backend.js). There is NO Go server and NO real $HOME, so it is
 *   IMPOSSIBLE for real user data (files, mail, calendar) to leak into a shot:
 *   every byte the shell renders comes from the deterministic DEMO fixtures
 *   below (user "Ada Lovelace", home /home/ada, a seeded file tree, agenda,
 *   installed-app catalogue, instances roster, …).
 *
 *   The shell UI is real; only the data behind it is a fixture. The Terminal is
 *   the real xterm.js widget rendering a scripted PTY stream mocked over a fake
 *   WebSocket — no image is ever hand-drawn.
 *
 * Output: docs/screenshots/<name>.png            (dark theme, canonical)
 *         docs/screenshots/<name>-light.png      (light theme, where supported)
 * Captured at 1440x900 @ 2x (deviceScaleFactor:2 → 2880x1800 retina PNGs).
 *
 * Usage:
 *   npm run screenshots
 *   PORT=5310 npm run screenshots
 */

import { chromium } from 'playwright'
import { spawn } from 'node:child_process'
import { mkdir, readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import http from 'node:http'
import { installBackend, json } from '../e2e/mock-backend.js'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = path.resolve(__dirname, '..')
const OUT_DIR = path.join(REPO_ROOT, 'docs', 'screenshots')
const PORT = Number(process.env.PORT || 5317)
const BASE_URL = `http://localhost:${PORT}`
// 1600x1000 @ dsf:1. NOTE: deviceScaleFactor:2 was tried for retina crispness
// but headless Chromium intermittently captures a BLACK GPU frame for the
// heavier maximized apps (App Hub's 52 cards, Dashboard) at 2x — a known
// high-DPI compositor glitch. dsf:1 with a larger viewport renders every view
// reliably, so we trade nominal retina density for correctness.
const VIEWPORT = { width: 1600, height: 1000 }

// ── DEMO fixtures ────────────────────────────────────────────────────────────
// Everything the shell can render is derived from these. No real host state.

const NOW = Date.now()
const iso = (ms) => new Date(ms).toISOString()
const todayAt = (h, m = 0) => { const d = new Date(); d.setHours(h, m, 0, 0); return d.getTime() }
const dayMs = 86_400_000

// The 18 store apps we present as "installed" (recognisable, well-iconed).
const INSTALLED_IDS = [
  'firefox', 'libreoffice', 'gimp', 'vlc', 'jellyfin', 'code-server',
  'jupyter', 'blender', 'inkscape', 'obs-studio', 'syncthing', 'gitea',
  'grafana', 'keepassxc', 'immich', 'navidrome', 'qbittorrent', 'darktable',
]

// A demo home-directory listing in `ls -lA` long format (already stripped of the
// leading `total` line, as the app's `| tail -n +2` would). Parsed by the File
// Explorer into folder/file rows.
const DEMO_HOME_LS = [
  'drwxr-xr-x   6 ada  ada   4096 Jul 15 09:12 Desktop',
  'drwxr-xr-x   8 ada  ada   4096 Jul 16 14:20 Documents',
  'drwxr-xr-x   4 ada  ada   4096 Jul 14 18:03 Downloads',
  'drwxr-xr-x  12 ada  ada   4096 Jul 12 11:47 Pictures',
  'drwxr-xr-x   3 ada  ada   4096 Jul 10 20:55 Music',
  'drwxr-xr-x   3 ada  ada   4096 Jul 09 21:30 Videos',
  'drwxr-xr-x   5 ada  ada   4096 Jul 15 08:00 Projects',
  'drwxr-xr-x   4 ada  ada   4096 Jul 11 13:15 .vulos',
  '-rw-r--r--   1 ada  ada  18452 Jul 16 13:05 welcome.md',
  '-rw-r--r--   1 ada  ada   2304 Jul 15 22:41 todo.txt',
  '-rw-r--r--   1 ada  ada 104857 Jul 14 16:20 budget-2026.ods',
  '-rw-r--r--   1 ada  ada 884213 Jul 13 10:02 sovereign-notes.pdf',
].join('\n')

const HOME_PAYLOAD = {
  greeting: 'Welcome back, Ada',
  brief:
    "You're mostly clear this morning. Two threads want a reply before noon, and " +
    "the design review moved to 2pm. Nothing else is on fire.",
  focus: [
    { uid: 'f1', subject: 'Re: Q3 roadmap sign-off', from_name: 'Priya Menon',
      preview: 'Looks good — just need your ack on the timeline before I send it upstairs.' },
    { uid: 'f2', subject: 'Invoice #2043 is due Friday', from_name: 'Billing · Hetzner',
      preview: 'Your monthly invoice is ready. Auto-pay is on, no action needed unless you want to review.' },
  ],
  agenda: [
    { id: 'e1', start: iso(todayAt(14, 0)), title: 'Design review', location: 'Meet · war-room' },
    { id: 'e2', start: iso(todayAt(16, 30)), title: '1:1 with Sam', location: '' },
    { id: 'e3', start: iso(todayAt(9, 0) + dayMs), title: 'Sprint planning', location: 'Office' },
  ],
  agenda_fresh: true,
  invites: [
    { message_uid: 'i1', subject: 'Team offsite', from: 'events@acme.io',
      invite: { summary: 'Team offsite — Lisbon', start: iso(NOW + 3 * dayMs), location: 'Lisbon', organizer: 'events@acme.io' } },
  ],
  activity: [
    { id: 'a1', kind: 'mail', text: 'Priya replied to “Q3 roadmap sign-off”', ts: iso(NOW - 22 * 60_000) },
    { id: 'a2', kind: 'file', text: 'sovereign-notes.pdf synced to Cloud · eu-central', ts: iso(NOW - 95 * 60_000) },
  ],
  sovereignty: { tier: 'local', label: 'On your device' },
}

const INST_DEVICE = '01JQ9Z6MACBOOKADA0000000001'
const INST_CLOUD = '01JQ9Z6CLOUDEUCENTRAL000002'
const INST_PI = '01JQ9Z6HOMESERVERPI5000003'

const INSTANCES = {
  instances: [
    { ulid: INST_DEVICE, display_name: "Ada's MacBook", kind: 'device', status: 'online',
      last_seen_at: iso(NOW - 8_000), role: 'owner', cpu_pct: 22, ram_pct: 41 },
    { ulid: INST_CLOUD, display_name: 'Cloud · eu-central', kind: 'cloud', status: 'online',
      last_seen_at: iso(NOW - 4_000), role: 'member', cpu_pct: 9, ram_pct: 33 },
    { ulid: INST_PI, display_name: 'Home Server (Pi 5)', kind: 'device', status: 'offline',
      last_seen_at: iso(NOW - 3 * 3_600_000), role: 'member', cpu_pct: 0, ram_pct: 0 },
  ],
}

const ROUTING_APPS = [
  { app_id: 'lilmail', instance_id: INST_CLOUD, fqdn: 'mail.ada.vulos.app' },
  { app_id: 'vulos-office', instance_id: INST_DEVICE, fqdn: 'office.ada.vulos.app' },
  { app_id: 'jellyfin', instance_id: INST_PI, fqdn: 'media.ada.vulos.app' },
]

const APP_VISIBILITY = [
  { app_id: 'lilmail', visibility: 'public' },
  { app_id: 'vulos-office', visibility: 'public' },
  { app_id: 'jellyfin', visibility: 'private' },
  { app_id: 'gitea', visibility: 'private' },
  { app_id: 'grafana', visibility: 'private' },
]

const CGROUPS = [
  { app_id: 'lilmail', cpu_pct: 3.2, mem_current: 148 * 1e6, mem_high: 512 * 1e6, mem_max: 1024 * 1e6 },
  { app_id: 'vulos-office', cpu_pct: 11.7, mem_current: 386 * 1e6, mem_high: 768 * 1e6, mem_max: 1536 * 1e6 },
  { app_id: 'jellyfin', cpu_pct: 6.4, mem_current: 512 * 1e6, mem_high: 1024 * 1e6, mem_max: 2048 * 1e6 },
  { app_id: 'gitea', cpu_pct: 0.8, mem_current: 96 * 1e6, mem_high: 256 * 1e6, mem_max: 512 * 1e6 },
  { app_id: 'grafana', cpu_pct: 1.5, mem_current: 132 * 1e6, mem_high: 384 * 1e6, mem_max: 768 * 1e6 },
]

// Build the App Hub registry (`/api/store/registry`) + installed tiles
// (`/api/store/installed`) from the real registry.json so the catalogue is
// faithful and Browse is populated (not empty), with Installed(18).
async function buildStoreFixtures() {
  const reg = JSON.parse(await readFile(path.join(REPO_ROOT, 'registry.json'), 'utf8'))
  const installedSet = new Set(INSTALLED_IDS)
  const registry = Object.entries(reg.apps).map(([id, a]) => ({
    id,
    name: a.name,
    type: a.type || 'web',
    arch: a.arch || [],
    flatpak_id: '',
    description: a.description || '',
    category: a.category || 'other',
    author: a.author || '',
    icon: a.icon || id,
    vetted: !!a.vetted,
    versions: Object.keys(a.versions || { latest: {} }),
    latest: 'latest',
    installed: installedSet.has(id),
    homepage: a.homepage || '',
    license: a.license || '',
  }))
  const installed = registry
    .filter((a) => installedSet.has(a.id))
    .map((a) => ({ id: a.id, name: a.name, description: a.description, category: a.category, icon: a.icon }))
  return { registry, installed }
}

// A scripted, honest Terminal session rendered by the REAL xterm widget over a
// mocked PTY WebSocket. Pure ANSI bytes — nothing is drawn by hand.
const E = '\x1b'
const TERM_SESSION = [
  `${E}[1;35mVulos OS${E}[0m  ${E}[90mada@vulos-box · sovereign instance${E}[0m\r\n`,
  `${E}[90mType 'vulos help' for the box control CLI.${E}[0m\r\n\r\n`,
  `${E}[1;36mada@vulos${E}[0m:${E}[1;34m~${E}[0m$ vulos status\r\n`,
  `  instance   ${E}[32m●${E}[0m online     region eu-central\r\n`,
  `  reachable  relay + direct (wss/yamux)\r\n`,
  `  apps       18 installed · 3 published\r\n`,
  `  storage    12.4 GB / 50 GB\r\n`,
  `  assistant  ${E}[35mlocal${E}[0m · llama3 (on-device)\r\n\r\n`,
  `${E}[1;36mada@vulos${E}[0m:${E}[1;34m~${E}[0m$ vulos apps ls --published\r\n`,
  `  ${E}[32mmail${E}[0m       mail.ada.vulos.app\r\n`,
  `  ${E}[32moffice${E}[0m     office.ada.vulos.app\r\n`,
  `  ${E}[32mjellyfin${E}[0m   media.ada.vulos.app\r\n\r\n`,
  `${E}[1;36mada@vulos${E}[0m:${E}[1;34m~${E}[0m$ ${E}[7m ${E}[0m\r\n`,
].join('')

// Common overrides applied to every capture.
async function demoOverrides() {
  const { registry, installed } = await buildStoreFixtures()
  return {
    'GET /api/auth/me': json({ user: { id: 'u1', username: 'ada' }, profile: { username: 'ada', display_name: 'Ada Lovelace' } }),
    'GET /api/assistant/home': json(HOME_PAYLOAD),
    // Calendar: return live events so the agenda badge reads "live" and no
    // "Calendar unavailable" toast fires.
    'GET /api/pim/calendar/events': json({
      events: [
        { uid: 'e1', summary: 'Design review', start: iso(todayAt(14, 0)), end: iso(todayAt(15, 0)), location: 'Meet · war-room', allDay: false },
        { uid: 'e2', summary: '1:1 with Sam', start: iso(todayAt(16, 30)), end: iso(todayAt(17, 0)), location: '', allDay: false },
      ],
    }),
    // File Explorer runs `ls`/`echo $HOME` through /api/exec — feed it the demo tree.
    'POST /api/exec': (req) => {
      let cmd = ''
      try { cmd = (JSON.parse(req.postData() || '{}').command) || '' } catch { /* noop */ }
      if (cmd.includes('echo $HOME')) return json({ output: '/home/ada\n' })
      if (cmd.trimStart().startsWith('ls')) return json({ output: DEMO_HOME_LS })
      return json({ output: '' })
    },
    // App Hub catalogue.
    'GET /api/store/registry': json(registry),
    'GET /api/store/installed': json(installed),
    'GET /api/packages/cache': json({ ready: true, arch: 'amd64' }),
    // Dashboard — Web + Instances tabs.
    'GET /api/apps/visibility': json(APP_VISIBILITY),
    'GET /api/cgroups/status': json(CGROUPS),
    'GET /api/instances': json(INSTANCES),
    'GET /api/routing/apps': json(ROUTING_APPS),
    // AI status for Settings → AI Assistant.
    'GET /api/ai/status': json({ available: true, mode: 'byo', provider: 'ollama', model: 'llama3', providers: [{ id: 'ollama', label: 'Ollama (on-device)', ready: true }], tier: 'local' }),
  }
}

// ── shot definitions ─────────────────────────────────────────────────────────
// drive(page) navigates the shell to the desired state; the runner screenshots.

async function openPalette(page) {
  const input = page.getByPlaceholder(/Search apps/)
  // Focus the desktop (far-left edge, clear of the menu bar and of the default
  // top-left window position) so the global ⌘K listener receives the keypress.
  await page.locator('body').click({ position: { x: 12, y: 520 } }).catch(() => {})
  for (let i = 0; i < 12; i++) {
    await page.keyboard.press('Meta+k')
    if (await input.isVisible().catch(() => false)) return input
    await page.waitForTimeout(250)
  }
  return input
}

// Launch an app by exact name via ⌘K (the reliable launch path) and maximize
// its window so the app UI fills the desktop for a crisp shot.
async function launchApp(page, name, { maximize = true } = {}) {
  const input = await openPalette(page)
  await input.fill(name)
  await page.getByText(name, { exact: true }).first().waitFor({ timeout: 6_000 }).catch(() => {})
  await page.keyboard.press('Enter')
  await page.waitForTimeout(1_400)
  if (maximize) {
    const maxBtn = page.getByRole('button', { name: 'Maximize window' }).first()
    if (await maxBtn.isVisible().catch(() => false)) {
      await maxBtn.click().catch(() => {})
      await page.waitForTimeout(700)
    }
  }
  // The always-on menu-bar calendar widget (z-30) legitimately floats above
  // windows, but over a maximized app it clips the top-right content. Hide it
  // for clean app-focused shots — it is kept on the desktop/Home hero, where
  // it belongs. (The app UI itself is untouched and fully real.)
  await page.addStyleTag({ content: '[data-calendar-widget]{display:none !important}' }).catch(() => {})
  await page.waitForTimeout(150)
}

const SHOTS = [
  {
    name: 'hero',
    light: true,
    desc: 'Desktop / Home — the sovereign-instance backdrop',
    async drive(page) {
      // No windows: the Home backdrop is the desktop.
      await page.waitForTimeout(900)
    },
  },
  {
    name: 'files',
    light: true,
    desc: 'File Explorer — seeded demo home directory',
    async drive(page) { await launchApp(page, 'File Explorer') },
  },
  {
    name: 'settings',
    light: true,
    desc: 'Settings — AI Assistant panel',
    async drive(page) { await launchApp(page, 'Settings') },
  },
  {
    name: 'settings-appearance',
    light: true,
    desc: 'Settings — Appearance (theme + accent colours)',
    async drive(page) {
      await launchApp(page, 'Settings')
      await page.getByRole('button', { name: 'Appearance' }).first().click().catch(() => {})
      await page.waitForTimeout(600)
    },
  },
  {
    name: 'apphub',
    light: false,
    desc: 'App Hub — Browse (populated catalogue)',
    async drive(page) { await launchApp(page, 'App Hub') },
  },
  {
    name: 'apphub-installed',
    light: false,
    desc: 'App Hub — Installed (18 apps)',
    async drive(page) {
      await launchApp(page, 'App Hub')
      await page.getByRole('button', { name: /Installed/ }).first().click().catch(() => {})
      await page.waitForTimeout(600)
    },
  },
  {
    name: 'dashboard',
    light: false,
    desc: 'Dashboard — Web publishing + per-app resources',
    async drive(page) { await launchApp(page, 'Dashboard') },
  },
  {
    name: 'instances',
    light: false,
    desc: 'Dashboard — Instances (routing across device + cloud)',
    async drive(page) {
      await launchApp(page, 'Dashboard')
      await page.getByRole('button', { name: 'Instances' }).first().click().catch(() => {})
      await page.waitForTimeout(900)
    },
  },
  {
    name: 'terminal',
    light: false,
    desc: 'Terminal — real xterm over a scripted demo PTY',
    async drive(page) {
      await launchApp(page, 'Terminal')
      await page.waitForTimeout(900)
    },
  },
]

// ── runner ───────────────────────────────────────────────────────────────────

function waitForServer(url, timeoutMs = 60_000) {
  const start = Date.now()
  return new Promise((resolve, reject) => {
    const tick = () => {
      const req = http.get(url, (res) => { res.resume(); resolve() })
      req.once('error', () => {
        if (Date.now() - start > timeoutMs) reject(new Error(`server ${url} did not start`))
        else setTimeout(tick, 400)
      })
      req.setTimeout(2_000, () => req.destroy())
    }
    tick()
  })
}

function run(cmd, args, opts = {}) {
  return new Promise((resolve, reject) => {
    const p = spawn(cmd, args, { cwd: REPO_ROOT, stdio: 'inherit', ...opts })
    p.on('exit', (code) => (code === 0 ? resolve() : reject(new Error(`${cmd} exited ${code}`))))
    p.on('error', reject)
  })
}

async function captureTheme(browser, theme, overrides, results) {
  const suffix = theme === 'light' ? '-light' : ''
  for (const shot of SHOTS) {
    if (theme === 'light' && !shot.light) continue
    // FRESH context per shot: the shell persists open-window state to
    // localStorage, so a shared context would restore prior shots' windows.
    // Isolation guarantees each shot starts from a clean desktop.
    const context = await browser.newContext({
      viewport: VIEWPORT,
      deviceScaleFactor: 1,
      reducedMotion: 'reduce', // kills window open/maximize animations → no mid-transition black frames
      ignoreHTTPSErrors: true,
    })
    const page = await context.newPage()
    try {
      await page.addInitScript((t) => {
        try {
          localStorage.setItem('vulos-theme', t)
          localStorage.setItem('vulos-ai-firstrun-done', '1')
        } catch { /* noop */ }
      }, theme)
      await installBackend(page, overrides)
      // Mock the Terminal PTY WebSocket with a scripted demo session.
      await page.routeWebSocket(/\/api\/pty/, (ws) => {
        ws.onMessage(() => { /* swallow client keystrokes */ })
        ws.send(TERM_SESSION)
      })
      await page.goto(BASE_URL, { waitUntil: 'domcontentloaded' })
      await page.getByTitle('Applications').first().waitFor({ timeout: 20_000 })
      await page.waitForTimeout(600)
      await shot.drive(page)
      // Force two paint frames to settle before capture (belt-and-braces
      // against a black GPU frame).
      await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))).catch(() => {})
      await page.waitForTimeout(250)
      const out = path.join(OUT_DIR, `${shot.name}${suffix}.png`)
      await page.screenshot({ path: out, fullPage: false })
      console.log(`  ✓ ${theme.padEnd(5)} ${path.relative(REPO_ROOT, out)}`)
      results.push({ name: `${shot.name}${suffix}`, status: 'ok' })
    } catch (err) {
      console.error(`  ✗ ${theme} ${shot.name}: ${err.message}`)
      results.push({ name: `${shot.name}${suffix}`, status: 'failed', error: err.message })
    } finally {
      await page.close()
      await context.close()
    }
  }
}

async function main() {
  await mkdir(OUT_DIR, { recursive: true })
  const overrides = await demoOverrides()

  console.log('Building production bundle (vite build)…')
  await run('npx', ['vite', 'build'])

  console.log(`Starting vite preview on :${PORT}…`)
  const preview = spawn('npx', ['vite', 'preview', '--port', String(PORT), '--strictPort'], {
    cwd: REPO_ROOT, stdio: 'ignore', detached: false,
  })
  const results = []
  let browser
  try {
    await waitForServer(BASE_URL)
    browser = await chromium.launch({ headless: true, args: ['--disable-gpu'] })
    console.log(`\nCapturing → ${path.relative(REPO_ROOT, OUT_DIR)}`)
    await captureTheme(browser, 'dark', overrides, results)
    await captureTheme(browser, 'light', overrides, results)
  } finally {
    if (browser) await browser.close()
    preview.kill('SIGTERM')
  }

  const ok = results.filter((r) => r.status === 'ok')
  const failed = results.filter((r) => r.status === 'failed')
  console.log(`\n${ok.length} captured, ${failed.length} failed`)
  for (const r of failed) console.log(`  - ${r.name}: ${r.error}`)
  process.exit(failed.length ? 1 : 0)
}

main().catch((err) => { console.error('Fatal:', err); process.exit(1) })
