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
 * Captured at 1600x1000. Per-shot `dsf` (deviceScaleFactor) defaults to 1 —
 * heavy maximized apps (App Hub's 52 cards, Dashboard's tables) intermittently
 * capture a BLACK GPU frame in headless Chromium at 2x (a known high-DPI
 * compositor glitch). Lighter shots opt into `dsf: 2` for retina crispness; see
 * the SHOTS array below.
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
    { uid: 'f2', subject: 'Invoice #2043 is due Friday', from_name: 'Nadia Rahman',
      preview: 'Your monthly invoice is ready. Auto-pay is on, no action needed unless you want to review.' },
  ],
  agenda: [
    { id: 'e1', start: iso(todayAt(14, 0)), title: 'Design review', location: 'Design Studio' },
    { id: 'e2', start: iso(todayAt(16, 30)), title: '1:1 with Sam', location: '' },
    { id: 'e3', start: iso(todayAt(9, 0) + dayMs), title: 'Sprint planning', location: 'Office' },
  ],
  agenda_fresh: true,
  invites: [
    { message_uid: 'i1', subject: 'Team offsite', from: 'events@acme.io',
      invite: { summary: 'Team offsite — Lisbon', start: iso(NOW + 3 * dayMs), location: 'Lisbon', organizer: 'events@acme.io' } },
  ],
  activity: [
    { uid: 'a1', unread: true, title: 'Priya replied to “Q3 roadmap sign-off”', subtitle: 'Priya Menon · 22m ago' },
    { uid: 'a2', unread: false, title: 'sovereign-notes.pdf synced across your instances', subtitle: 'Files · 1h ago' },
    { uid: 'a3', unread: false, title: 'Nightly encrypted backup completed', subtitle: 'Backup · 3h ago' },
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
  { app_id: 'immich', instance_id: INST_DEVICE, fqdn: 'photos.ada.vulos.app' },
  { app_id: 'jellyfin', instance_id: INST_PI, fqdn: 'media.ada.vulos.app' },
]

const APP_VISIBILITY = [
  { app_id: 'lilmail', visibility: 'public' },
  { app_id: 'immich', visibility: 'public' },
  { app_id: 'jellyfin', visibility: 'private' },
  { app_id: 'gitea', visibility: 'private' },
  { app_id: 'grafana', visibility: 'private' },
]

const CGROUPS = [
  { app_id: 'lilmail', cpu_pct: 3.2, mem_current: 148 * 1e6, mem_high: 512 * 1e6, mem_max: 1024 * 1e6 },
  { app_id: 'immich', cpu_pct: 11.7, mem_current: 386 * 1e6, mem_high: 768 * 1e6, mem_max: 1536 * 1e6 },
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
  `  ${E}[32moffice${E}[0m     photos.ada.vulos.app\r\n`,
  `  ${E}[32mjellyfin${E}[0m   media.ada.vulos.app\r\n\r\n`,
  `${E}[1;36mada@vulos${E}[0m:${E}[1;34m~${E}[0m$ ${E}[7m ${E}[0m\r\n`,
].join('')

// Activity Monitor's demo data — used by the `tiled` shot so the third window
// shows real content instead of its "Connecting to system telemetry…" spinner
// (it needs a WS + two REST endpoints; wired the same honest way as the PTY).
const DEMO_PROCESSES = [
  { pid: 1024, name: 'vulos-shelld', command: '/usr/bin/vulos-shelld', user: 'ada', state: 'running', cpu: 4.2, mem_rss: 86 * 1e6, threads: 12 },
  { pid: 1189, name: 'lilmail', command: '/opt/apps/lilmail/bin/server', user: 'ada', state: 'running', cpu: 2.1, mem_rss: 148 * 1e6, threads: 8 },
  { pid: 1204, name: 'immich', command: '/opt/apps/immich/bin/immich', user: 'ada', state: 'sleeping', cpu: 11.7, mem_rss: 386 * 1e6, threads: 14 },
  { pid: 1310, name: 'jellyfin', command: '/opt/apps/jellyfin/jellyfin', user: 'ada', state: 'running', cpu: 6.4, mem_rss: 512 * 1e6, threads: 22 },
  { pid: 1422, name: 'chromium', command: 'chromium --type=renderer', user: 'ada', state: 'sleeping', cpu: 3.8, mem_rss: 210 * 1e6, threads: 10 },
  { pid: 1508, name: 'sshd', command: '/usr/sbin/sshd -D', user: 'root', state: 'sleeping', cpu: 0.1, mem_rss: 12 * 1e6, threads: 1 },
]
const DEMO_NET_CONNS = [
  { proto: 'tcp', local_addr: '10.0.0.4', local_port: 443, remote_addr: '203.0.113.9', remote_port: 51422, state: 'ESTABLISHED', process: 'lilmail' },
  { proto: 'tcp', local_addr: '10.0.0.4', local_port: 22, remote_addr: '0.0.0.0', remote_port: 0, state: 'LISTEN', process: 'sshd' },
  { proto: 'tcp', local_addr: '10.0.0.4', local_port: 8080, remote_addr: '0.0.0.0', remote_port: 0, state: 'LISTEN', process: 'immich' },
  { proto: 'udp', local_addr: '10.0.0.4', local_port: 51820, remote_addr: '198.51.100.7', remote_port: 51820, state: 'ESTABLISHED', process: 'relay' },
]
const DEMO_TELEMETRY = {
  cpu: 18, mem_percent: 34, mem_used: 5.6 * 1e9, mem_total: 16 * 1e9, mem_cached: 1.9 * 1e9,
  net_rx: 2.4 * 1e6, net_tx: 640 * 1e3, disk_read: 1.1 * 1e6, disk_write: 340 * 1e3,
}

// Calendar (`calendar` shot) — a full month's worth of events (not just today's
// two) so the month grid reads as a lived-in calendar rather than an almost-
// empty one. Spread across the ~42-cell 6-week grid around "today".
const CALENDAR_EVENTS = [
  { uid: 'e1', summary: 'Design review', start: iso(todayAt(14, 0)), end: iso(todayAt(15, 0)), location: 'Meet · war-room', allDay: false },
  { uid: 'e2', summary: '1:1 with Sam', start: iso(todayAt(16, 30)), end: iso(todayAt(17, 0)), location: '', allDay: false },
  { uid: 'e3', summary: 'Sprint planning', start: iso(todayAt(9, 0) + dayMs), end: iso(todayAt(10, 0) + dayMs), location: 'Office', allDay: false },
  { uid: 'e4', summary: 'Dentist', start: iso(todayAt(11, 0) - 2 * dayMs), end: iso(todayAt(11, 30) - 2 * dayMs), location: '', allDay: false },
  { uid: 'e5', summary: 'Team offsite — Lisbon', start: iso(todayAt(9, 0) + 3 * dayMs), end: iso(todayAt(18, 0) + 4 * dayMs), location: 'Lisbon', allDay: true },
  { uid: 'e6', summary: 'Quarterly board call', start: iso(todayAt(10, 0) - 5 * dayMs), end: iso(todayAt(11, 0) - 5 * dayMs), location: '', allDay: false },
  { uid: 'e7', summary: 'Coffee with Priya', start: iso(todayAt(9, 30) + 6 * dayMs), end: iso(todayAt(10, 0) + 6 * dayMs), location: 'Design Studio', allDay: false },
  { uid: 'e8', summary: "Ada's birthday", start: iso(todayAt(0) + 9 * dayMs), end: iso(todayAt(0) + 9 * dayMs), location: '', allDay: true },
  { uid: 'e9', summary: 'Backup verification', start: iso(todayAt(8, 0) - 8 * dayMs), end: iso(todayAt(8, 30) - 8 * dayMs), location: '', allDay: false },
]

// Contacts (`contacts` shot) — a small, recognisable address book so the list +
// detail panes both render real content instead of an empty state.
const CONTACTS_CARDS = [
  { uid: 'c1', name: 'Priya Menon', org: 'Acme, Inc.', title: 'Head of Product', emails: ['priya@acme.io'], phones: ['+1 415 555 0134'] },
  { uid: 'c2', name: 'Sam Okafor', org: 'Acme, Inc.', title: 'Engineering Lead', emails: ['sam@acme.io'], phones: ['+1 415 555 0198'] },
  { uid: 'c3', name: 'Marta Costa', org: 'Lisbon Design Collective', title: 'Creative Director', emails: ['marta@lisbondesign.pt'], phones: ['+351 21 555 0198'] },
  { uid: 'c4', name: 'Nadia Rahman', org: 'Northwind Studio', title: 'Studio Manager', emails: ['nadia@northwind.studio'], phones: ['+44 20 7946 0102'] },
  { uid: 'c5', name: 'Events Team', org: 'Acme, Inc.', title: '', emails: ['events@acme.io'], phones: [] },
]

// Assistant (`assistant` shot) — the answer returned for the "What needs my
// attention" quick action, so the shot shows a real Q&A turn instead of the
// empty composer state. Mirrors HOME_PAYLOAD's brief so the story is consistent
// across shots.
const ASSISTANT_ATTENTION_ANSWER =
  "You're mostly clear this morning. Priya is waiting on your ack on the Q3 " +
  "roadmap timeline, and the studio invoice is due Friday — auto-pay is on, so " +
  "no action needed unless you want to review it. Design review moved to 2pm. " +
  "Nothing else is on fire."

// Common overrides applied to every capture.
async function demoOverrides() {
  const { registry, installed } = await buildStoreFixtures()
  return {
    'GET /api/auth/me': json({ user: { id: 'u1', username: 'ada' }, profile: { username: 'ada', display_name: 'Ada Lovelace' } }),
    'GET /api/assistant/home': json(HOME_PAYLOAD),
    // Calendar: return live events so the agenda badge reads "live" and no
    // "Calendar unavailable" toast fires.
    'GET /api/pim/calendar/events': json({ events: CALENDAR_EVENTS }),
    // Contacts: address book over the same PIM proxy.
    'GET /api/pim/contacts/cards': json({ contacts: CONTACTS_CARDS }),
    // Assistant quick action ("What needs my attention") — a real answer so the
    // `assistant` shot shows a populated conversation, not the empty composer.
    'POST /api/assistant/attention': json({ answer: ASSISTANT_ATTENTION_ANSWER }),
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
    // Launchpad (`mobile-apps` shot) — apt-installed desktop entries. Must be an
    // array: the unmocked-endpoint catch-all returns `{}`, and Launchpad's
    // `desktopEntries.filter(...)` throws on a non-array, white-screening the
    // whole overlay (a real crash, not a rendering quirk — hence the solid
    // black frame this fixture fixes).
    'GET /api/desktop/entries': json([]),
    // Activity Monitor (`tiled` shot) — process table + network connections.
    // The live CPU/mem/net numbers come over the /api/telemetry WS, mocked
    // per-context in captureTheme() alongside the PTY.
    'GET /api/system/processes': json(DEMO_PROCESSES),
    'GET /api/system/network': json(DEMO_NET_CONNS),
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
    dsf: 2,
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
    name: 'calendar',
    light: false,
    dsf: 2,
    desc: 'Calendar — month view, a lived-in schedule over connected accounts',
    async drive(page) {
      await launchApp(page, 'Calendar')
      await page.waitForTimeout(400)
    },
  },
  {
    name: 'assistant',
    light: false,
    dsf: 2,
    desc: 'Assistant — private AI over your mail, with the sovereignty badge and a real answer',
    async drive(page) {
      await launchApp(page, 'Assistant')
      await page.getByRole('button', { name: /What needs my attention/ }).first().click().catch(() => {})
      await page.waitForTimeout(900)
    },
  },
  {
    name: 'contacts',
    light: false,
    dsf: 2,
    desc: 'Contacts — the address book over your connected accounts',
    async drive(page) {
      await launchApp(page, 'Contacts')
      // Select a contact so the detail pane shows a real card instead of the
      // "Select a contact" empty state.
      await page.getByRole('button', { name: /Priya Menon/ }).first().click().catch(() => {})
      await page.waitForTimeout(400)
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
  {
    name: 'tiled',
    light: true,
    desc: 'Desktop — multiple app windows open at once (real multitasking)',
    async drive(page) {
      // Launch a few real apps WITHOUT maximizing. ShellProvider cascades each
      // new window's default position by +32px (OPEN_WINDOW), so three
      // unmaximized 720x500 windows land staggered — overlapping enough to read
      // as a tiled desktop, each title bar and a slice of its content visible.
      await launchApp(page, 'File Explorer', { maximize: false })
      await launchApp(page, 'Terminal', { maximize: false })
      await launchApp(page, 'Activity Monitor', { maximize: false })
      await page.waitForTimeout(400)
    },
  },
  {
    name: 'mobile',
    light: false,
    dsf: 2,
    desc: 'MobileStack — File Explorer running fullscreen on a phone',
    // MOBILE-06 (src/App.jsx): `layout` comes from useViewport(), purely a
    // window-width media query (<768px → 'mobile') — no device-profile API
    // override needed. A 390px-wide context alone flips `useDesktop` false and
    // renders <MobileStack/> instead of <DesktopCanvas/>.
    //
    // MobileStack has no desktop-style window chrome to maximize, and its
    // bottom-dock "Apps" button is the RUNNING-apps switcher (disabled with
    // zero windows open) — not an app launcher, so clicking it before any app
    // is open does nothing and previously left the shot on the mostly-empty
    // assistant home. The real fix: launch a builtin exactly like the desktop
    // shots do (⌘K still works — CommandPalette is mounted in MobileStack
    // too); ShellProvider's window-count effect then flips MobileStack to its
    // fullscreen "app" view automatically.
    viewport: { width: 390, height: 844 },
    async drive(page) {
      await page.waitForTimeout(700)
      await launchApp(page, 'File Explorer', { maximize: false })
      await page.waitForTimeout(500)
    },
  },
  {
    name: 'mobile-apps',
    light: false,
    dsf: 2,
    desc: 'MobileStack — the full app grid (Library) on a phone',
    viewport: { width: 390, height: 844 },
    async drive(page) {
      await page.waitForTimeout(700)
      // The bottom dock's "Library" button opens the Launchpad app-grid
      // overlay — a richer, more representative phone view than the sparse
      // assistant-first home.
      await page.getByRole('button', { name: 'Library' }).first().click().catch(() => {})
      await page.waitForTimeout(700)
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
    // A shot may request its own (narrower) viewport — e.g. `mobile`, which
    // needs <768px to flip the shell's useViewport() media query and render
    // MobileStack instead of DesktopCanvas (see src/App.jsx `useDesktop`).
    // Existing shots omit `viewport` and get the standard desktop VIEWPORT.
    const isMobileShot = !!shot.viewport
    const context = await browser.newContext({
      viewport: shot.viewport || VIEWPORT,
      // Per-shot override (see SHOTS above): lighter views opt into dsf:2 for
      // retina crispness; heavy maximized apps (App Hub, Dashboard, Instances,
      // tiled multi-window) stay at the default 1 — headless Chromium
      // intermittently captures a BLACK GPU frame for those at 2x.
      deviceScaleFactor: shot.dsf || 1,
      isMobile: isMobileShot,
      hasTouch: isMobileShot,
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
      // Mock Activity Monitor's telemetry WebSocket (`tiled` shot) with one
      // steady demo reading — enough for useTelemetry() to flip `connected`
      // and render real CPU/mem gauges instead of the connecting spinner.
      await page.routeWebSocket(/\/api\/telemetry/, (ws) => {
        ws.send(JSON.stringify(DEMO_TELEMETRY))
      })
      await page.goto(BASE_URL, { waitUntil: 'domcontentloaded' })
      // The desktop TopBar's "Applications" button doesn't exist in
      // MobileStack — wait for that layout's own root marker instead.
      if (isMobileShot) {
        await page.locator('[data-shell="mobile"]').first().waitFor({ timeout: 20_000 })
      } else {
        await page.getByTitle('Applications').first().waitFor({ timeout: 20_000 })
      }
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
