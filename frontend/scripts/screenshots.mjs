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
// This script lives in frontend/scripts/ (beside its playwright dependency —
// node resolves imports from the importing FILE's directory upward, so a
// repo-root script cannot see frontend/node_modules).
const FRONTEND = path.resolve(__dirname, '..')
const REPO_ROOT = path.resolve(__dirname, '..', '..')
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

// Settings → Network → Relay & Reachability (`settings-relay` shot). The box
// runs on Vulos's own built-in relay (the default) with two operator-diverse
// relay nodes, both healthy and holding a live tunnel. Shapes match
// src/core/settings/RelayPanel.jsx: GET /api/relayconfig → { config, effective }
// and GET /api/network/reach → { enabled, endpoints:[Status], links:[LinkStatus] }.
const RELAY_CONFIG = {
  config: {
    provider: 'vulos',
    turn: { ice_servers: [] },
    libp2p: { relay_peers: [] },
    wireguard: { endpoint: '', network: '' },
  },
  effective: {
    ingress: {
      mode: 'relay-tunnel',
      detail: 'wss://relay.eu-central.vulos.host → this box (yamux, dialled out)',
    },
    // Public STUN + this box's own TURN — what the "N configured" count reads.
    ice_servers: [
      { urls: ['stun:stun.l.google.com:19302'] },
      { urls: ['turn:box.ada.example:3478?transport=udp', 'turn:box.ada.example:3478?transport=tcp'] },
    ],
  },
}

const RELAY_REACH = {
  enabled: true,
  endpoints: [
    { endpoint: { name: 'relay-eu', url: 'wss://relay.eu-central.vulos.host', region: 'eu-central' },
      healthy: true, down_for_seconds: 0, last_error: '' },
    { endpoint: { name: 'relay-us', url: 'wss://relay.us-east.vulos.host', region: 'us-east' },
      healthy: true, down_for_seconds: 0, last_error: '' },
  ],
  links: [
    { name: 'relay-eu', state: 'up', public_url: 'https://ada.eu-central.vulos.host' },
    { name: 'relay-us', state: 'up', public_url: 'https://ada.us-east.vulos.host' },
  ],
}

// Settings → Network → Custom Domain (`settings-domain` shot). lilmail is the
// first public app in APP_VISIBILITY, so DomainPanel default-selects it; return
// a verified record (TLS active) for it. Shape matches src/core/settings/
// DomainPanel.jsx: { domain, status, txt_record, challenge_token, created_at,
// verified_at }.
const DOMAIN_RECORD = {
  domain: 'cloud.ada.example',
  status: 'verified',
  txt_record: '_vulos-challenge.cloud.ada.example',
  challenge_token: 'vulos-verify=8f3c1a9e4b7d2065',
  created_at: iso(NOW - 6 * dayMs),
  verified_at: iso(NOW - 6 * dayMs + 42 * 60 * 1000),
}

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
  // Vulos first-party apps — these live outside the third-party FOSS registry
  // and render with their own coloured brand marks (public/product-logos/*).
  // Showing them installed is what makes the launcher/App Hub look like a real
  // Vulos box rather than a generic app store.
  const FIRST_PARTY = [
    { id: 'diwan', name: 'Diwan', description: 'Documents, sheets & slides — on your box', category: 'productivity' },
    { id: 'lilmail', name: 'lilmail', description: 'Your mail, hosted on your own server', category: 'productivity' },
    { id: 'envoir', name: 'envoir', description: 'Private messaging & portable identity', category: 'internet' },
    { id: 'llmux', name: 'llmux', description: 'Private AI gateway — your models, your keys', category: 'productivity' },
    { id: 'kerf', name: 'Kerf', description: 'Version-controlled notes & knowledge base', category: 'productivity' },
    { id: 'soko', name: 'Soko', description: 'Your own storefront & catalog', category: 'internet' },
    { id: 'gitstate', name: 'GitState', description: 'Local-first project & repo dashboard', category: 'development' },
    { id: 'vuna', name: 'Vuna', description: 'Crawl, extract & search the open web', category: 'internet' },
  ]
  const installed = [
    ...FIRST_PARTY,
    ...registry
      .filter((a) => installedSet.has(a.id))
      .map((a) => ({ id: a.id, name: a.name, description: a.description, category: a.category, icon: a.icon })),
  ]
  // Also surface the first-party apps in the Browse catalogue.
  for (const fp of FIRST_PARTY) {
    registry.unshift({ id: fp.id, name: fp.name, type: 'web', arch: [], flatpak_id: '', description: fp.description, category: fp.category, author: 'Vulos', icon: fp.id, vetted: true, versions: ['latest'], latest: 'latest', installed: true, homepage: '', license: '' })
  }
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
  // Rows 7-13 exist for the TABLET shot. Half of a 1024x768 tablet is a tall
  // pane, and six processes left roughly a third of it blank — the window read
  // as half-loaded rather than busy. A real box runs more than six things, so
  // filling the table is also the more honest picture. The desktop `tiled` shot
  // shares this data and simply scrolls the extras out of view.
  { pid: 1601, name: 'postgres', command: '/usr/lib/postgresql/bin/postgres', user: 'postgres', state: 'sleeping', cpu: 1.9, mem_rss: 224 * 1e6, threads: 9 },
  { pid: 1655, name: 'coturn', command: '/usr/bin/turnserver -c /etc/turnserver.conf', user: 'turn', state: 'running', cpu: 1.4, mem_rss: 38 * 1e6, threads: 6 },
  { pid: 1702, name: 'vulos-relayd', command: '/usr/bin/vulos relay serve', user: 'ada', state: 'running', cpu: 1.1, mem_rss: 64 * 1e6, threads: 7 },
  { pid: 1788, name: 'ollama', command: '/usr/local/bin/ollama serve', user: 'ada', state: 'sleeping', cpu: 0.9, mem_rss: 742 * 1e6, threads: 11 },
  { pid: 1844, name: 'caddy', command: '/usr/bin/caddy run --config /etc/caddy/Caddyfile', user: 'root', state: 'running', cpu: 0.6, mem_rss: 54 * 1e6, threads: 8 },
  { pid: 1902, name: 'systemd-journald', command: '/lib/systemd/systemd-journald', user: 'root', state: 'sleeping', cpu: 0.3, mem_rss: 28 * 1e6, threads: 3 },
  { pid: 1977, name: 'dhcpcd', command: '/usr/sbin/dhcpcd -q -b', user: 'root', state: 'sleeping', cpu: 0.1, mem_rss: 9 * 1e6, threads: 1 },
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

// A machine that is actually doing something.
//
// The telemetry mock used to send DEMO_TELEMETRY once. Activity Monitor builds
// its sparklines by appending each frame to a history buffer, so one frame —
// or a repeated identical one — draws four dead-flat lines. The graphs were
// technically "live" and looked like a screenshot of nothing.
//
// This walks each series with bounded, series-appropriate motion: CPU drifts
// and occasionally spikes the way a real box does under bursty load; memory
// moves slowly because it does; network and disk are spikier. Deterministic
// (a seeded LCG, no Math.random) so the same run produces the same picture and
// a regenerated screenshot does not churn its diff for no reason.
function telemetryWalk(count = 140) {
  let seed = 0x5eed
  const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff)
  const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v))

  let cpu = 18, mem = 34, rx = 2.4e6, tx = 6.4e5, dr = 1.1e6, dw = 3.4e5
  const frames = []
  for (let i = 0; i < count; i++) {
    // CPU: gentle drift, with an occasional burst that decays — the shape a
    // build or an indexing pass leaves behind.
    cpu = clamp(cpu + (rnd() - 0.48) * 9 + (rnd() > 0.94 ? 34 : 0) - (cpu > 55 ? 7 : 0), 4, 92)
    mem = clamp(mem + (rnd() - 0.5) * 1.6, 28, 47)
    rx = clamp(rx * (0.82 + rnd() * 0.5) + (rnd() > 0.9 ? 9e6 : 0), 2e5, 4.2e7)
    tx = clamp(tx * (0.85 + rnd() * 0.45) + (rnd() > 0.93 ? 2.4e6 : 0), 6e4, 9e6)
    dr = clamp(dr * (0.8 + rnd() * 0.55) + (rnd() > 0.91 ? 6e6 : 0), 1e5, 2.6e7)
    dw = clamp(dw * (0.84 + rnd() * 0.5) + (rnd() > 0.95 ? 3e6 : 0), 5e4, 1.1e7)
    frames.push({
      ...DEMO_TELEMETRY,
      cpu: Math.round(cpu),
      mem_percent: Math.round(mem),
      mem_used: Math.round((mem / 100) * 16 * 1e9),
      net_rx: Math.round(rx), net_tx: Math.round(tx),
      disk_read: Math.round(dr), disk_write: Math.round(dw),
    })
  }
  return frames
}

const DEMO_TELEMETRY_SERIES = telemetryWalk()

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

// Unified address book (`/api/contacts/unified`) — the same people, merged with
// the phone's device/SIM contacts and the box SIM, so the Contacts app can badge
// where each contact lives. Priya is fused across Vulos+Device; Sam adds Box SIM;
// two entries (Thabo, Mum) come only from the phone/SIM.
const CONTACTS_UNIFIED = [
  { id: 'u1', name: 'Priya Menon', org: 'Acme, Inc.', emails: ['priya@acme.io'], phones: ['+1 415 555 0134'], sources: ['vulos', 'phone'] },
  { id: 'u2', name: 'Sam Okafor', org: 'Acme, Inc.', emails: ['sam@acme.io'], phones: ['+1 415 555 0198'], sources: ['vulos', 'phone', 'box-sim'] },
  { id: 'u3', name: 'Marta Costa', org: 'Lisbon Design Collective', emails: ['marta@lisbondesign.pt'], phones: ['+351 21 555 0198'], sources: ['vulos'] },
  { id: 'u4', name: 'Nadia Rahman', org: 'Northwind Studio', emails: ['nadia@northwind.studio'], phones: ['+44 20 7946 0102'], sources: ['vulos'] },
  { id: 'u5', name: 'Events Team', org: 'Acme, Inc.', emails: ['events@acme.io'], phones: [], sources: ['vulos'] },
  { id: 'u6', name: 'Thabo Mokoena', org: '', emails: [], phones: ['+27 82 555 0147'], sources: ['phone'] },
  { id: 'u7', name: 'Mum', org: '', emails: [], phones: ['+27 82 555 0102'], sources: ['box-sim'] },
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
    // role:'admin' → Settings' isOwner is true, so the owner-only Network panels
    // (Relay & Reachability, CDN, …) appear in the nav for the settings-relay shot.
    'GET /api/auth/me': json({ user: { id: 'u1', username: 'ada' }, profile: { username: 'ada', display_name: 'Ada Lovelace', role: 'admin' } }),
    'GET /api/assistant/home': json(HOME_PAYLOAD),
    // Calendar: return live events so the agenda badge reads "live" and no
    // "Calendar unavailable" toast fires.
    'GET /api/pim/calendar/events': json({ events: CALENDAR_EVENTS }),
    // Contacts: address book over the same PIM proxy.
    'GET /api/pim/contacts/cards': json({ contacts: CONTACTS_CARDS }),
    'GET /api/contacts/unified': json({ contacts: CONTACTS_UNIFIED, sources_active: ['vulos', 'phone', 'box-sim'] }),
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
    // Settings → Network → Relay & Reachability (settings-relay shot).
    'GET /api/relayconfig': json(RELAY_CONFIG),
    'GET /api/network/reach': json(RELAY_REACH),
    // Settings → Network → Custom Domain (settings-domain shot). DomainPanel
    // default-selects lilmail (first public app); its record is verified/TLS-active.
    'GET /api/apps/lilmail/domain': json(DOMAIN_RECORD),
    // Settings → Network → Connection Mode / Remote Access (kept coherent with
    // the custom-domain story; harmless for the two target shots).
    'GET /api/network/mode': json({ mode: 'own', external_listener_blocked: false, status: { domain: 'cloud.ada.example', instance_id: INST_DEVICE } }),
    'GET /api/network/config': json({ app_url: 'https://cloud.ada.example' }),
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
  //
  // Desktop only. At phone width there IS no exposed desktop: MobileStack gives
  // the running app the whole screen, so (12,520) lands inside that app's UI and
  // clicks whatever happens to be there — in the recents shot it was landing in
  // the File Explorer listing between launches. The palette listener is global
  // and needs no such nudge on mobile, which is why driving it without the click
  // works there.
  const vp = page.viewportSize()
  if (!vp || vp.width >= 768) {
    await page.locator('body').click({ position: { x: 12, y: 520 } }).catch(() => {})
  }
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
  // NOTE: this used to inject `[data-calendar-widget]{display:none}` because the
  // calendar widget was a z-30 overlay that floated above a maximized app and
  // clipped its top-right content. That is no longer true: the widget was folded
  // into the ambient desktop column (shell/DesktopWidgets.tsx renders
  // <CalendarWidget embedded/>) which sits at Z_DESKTOP_WIDGETS = 5, BENEATH
  // every window. A maximized app already covers it. The style tag survived the
  // redesign as a no-op for the maximized shots — and as an active defect for the
  // stacked ones, where the column is meant to be visible and it silently punched
  // the agenda card out of the middle of it.
  await page.waitForTimeout(150)
}

// Snap a window into a tile zone using the shell's real keyboard tiling
// (useWindowShortcuts → nextTile → tileWindow), e.g. ['Left','Up'] walks
// floating → left half → top-left quarter.
//
// The title-bar click is load-bearing, not cosmetic: an app's UI renders in an
// iframe, and right after launchApp() focus sits INSIDE it. The tiling listener
// is bound to the shell document, so a keystroke sent while the iframe holds
// focus never reaches it and the window silently stays where it was. Clicking
// the title bar moves focus back to the shell document first. Title bars are
// matched by app name because the windows overlap before they are tiled.
// VERIFIES the result and retries, because a silent no-op here is invisible:
// the first version of this shot tiled only the LAST window and still reported
// success, leaving the other two at their cascade positions and a quarter of
// the canvas empty. Retrying is safe because the sequences are idempotent —
// nextTile(zone,'left') returns 'left' from null/'left'/'top-left'/'bottom-left',
// so re-pressing normalises rather than walking somewhere new.
//
// zone must match src/shell/windowTiling.js tileGeometry().
// MEASURE the chrome rather than restating its dimensions.
//
// This used to hardcode `top = 32` with a comment pointing at MENU_BAR_H, and
// reserved nothing at the bottom — a third copy of the tiling arithmetic
// alongside windowTiling.ts and (until recently) the shell reducer. The moment
// tiling learned to reserve the dock, this copy was wrong and the tiled shots
// failed with "wanted ~(0,516), got (0,482)".
//
// A number restated in a second place drifts; a measurement cannot. The menu bar
// is .vshell-bar and the dock is the toolbar named "Dock", both of which are on
// screen at capture time, so ask them.
async function zoneOrigin(page, zone) {
  const vp = page.viewportSize()
  const top = await page.locator('.vshell-bar').first().boundingBox()
    .then(b => (b ? Math.round(b.y + b.height) : 32)).catch(() => 32)
  const dockTop = await page.getByRole('toolbar', { name: 'Dock' }).boundingBox()
    .then(b => (b ? Math.round(b.y) : vp.height)).catch(() => vp.height)
  const usableH = Math.max(0, dockTop - top)
  const halfW = Math.floor(vp.width / 2)
  const halfH = Math.floor(usableH / 2)
  const want = {
    'left': { x: 0, y: top },
    'right': { x: halfW, y: top },
    'top-left': { x: 0, y: top },
    'top-right': { x: halfW, y: top },
    'bottom-left': { x: 0, y: top + halfH },
    'bottom-right': { x: halfW, y: top + halfH },
  }[zone]
  if (!want) throw new Error(`unsupported tile zone: ${zone}`)
  return want
}

// Assert the FINAL layout, at capture time. snapWindow verifying its own effect
// is not enough on its own: opening another window after a tile can move an
// already-placed one, so every snapWindow can pass and the captured frame still
// be wrong (it was — the Terminal filled the whole left half). This is the check
// that actually guards the image.
async function assertTiled(page, pairs) {
  const bad = []
  for (const [title, zone] of pairs) {
    const want = await zoneOrigin(page, zone)
    const box = await page.locator('.vwin-titlebar', { hasText: title }).first()
      .boundingBox().catch(() => null)
    if (!box || Math.abs(box.x - want.x) > 6 || Math.abs(box.y - want.y) > 6) {
      bad.push(`${title}: want ${zone} ~(${want.x},${want.y}), got ` +
        (box ? `(${Math.round(box.x)},${Math.round(box.y)})` : 'no box'))
    }
  }
  if (bad.length) throw new Error(`final tiling wrong — ${bad.join('; ')}`)
}

async function snapWindow(page, title, dirs, zone) {
  const bar = page.locator('.vwin-titlebar', { hasText: title }).first()
  const want = await zoneOrigin(page, zone)

  for (let attempt = 1; attempt <= 4; attempt++) {
    // Click the title bar first: the app renders in an iframe which grabs focus
    // asynchronously once loaded, and the tiling listener is bound to the SHELL
    // document — keys sent while the iframe holds focus are swallowed. This is
    // exactly why the earlier version only tiled the last window.
    // NOT wrapped in .catch(): if the title bar is covered by another window,
    // Playwright's actionability check fails here — and swallowing that is how
    // an earlier version silently tiled the WRONG window (the click landed on
    // the window on top, so File Explorer got moved when Terminal was meant to).
    // Callers avoid the overlap by tiling in reverse launch order.
    await bar.click({ timeout: 5_000 })
    // Then blur whatever still holds DOM focus. The Terminal is xterm.js, which
    // keeps a hidden <textarea> focused to receive input, and useWindowShortcuts
    // deliberately ignores any key whose target is an input/textarea so it never
    // hijacks typing — so without this the tiling shortcut is dropped for the
    // Terminal specifically. Clicking the title bar sets the ACTIVE window in
    // React state; blurring makes <body> the key target so the shortcut runs.
    await page.evaluate(() => document.activeElement?.blur?.()).catch(() => {})
    await page.waitForTimeout(250)
    for (const dir of dirs) {
      await page.keyboard.press(`Meta+Arrow${dir}`)
      await page.waitForTimeout(300)
    }
    const box = await bar.boundingBox().catch(() => null)
    if (box && Math.abs(box.x - want.x) <= 6 && Math.abs(box.y - want.y) <= 6) return
    if (attempt === 4) {
      throw new Error(
        `snapWindow: "${title}" never reached ${zone} — wanted ~(${want.x},${want.y}), ` +
        `got ${box ? `(${Math.round(box.x)},${Math.round(box.y)})` : 'no box'}`)
    }
    await page.waitForTimeout(500)
  }
}

// ── stacked (overlapping) window layouts ─────────────────────────────────────
// A tiled grid reads as a dashboard. Floating windows that overlap, with the
// focused one in front casting its shadow across the ones behind, are what make
// the shell read as a real desktop — so the multi-window shots CASCADE, and the
// single `tiled` shot below is kept only as the "…and it tiles too" demo.

/** The window frame (not just its title bar) that carries `title`. */
function winRoot(page, title) {
  return page.locator('.vwin').filter({ has: page.locator('.vwin-titlebar', { hasText: title }) }).first()
}

// ── "the window rendered its app", asserted rather than assumed ──────────────
//
// Every builtin is a React.lazy import behind a <Suspense fallback> that reads
// "Loading..." (shell/builtinApps.tsx). A window can therefore be present, sized
// and positioned exactly as asked while containing nothing but that fallback —
// and the runner reports a clean capture, because it counts files. It happened:
// a three-window shot shipped with two empty grey panes.
//
// So each multi-window shot declares what its app must actually be showing, and
// that is checked twice: once after launch (below) and again at capture time
// (assertStacked / assertTiled). `text` is a string only that app renders;
// `selector` covers the Terminal, whose xterm surface exposes no useful
// textContent (its DOM text is the character-measurement helper, not the
// session).
//
// THIS IS NOT A RACE GUARD. The stall it caught did not resolve with time: it
// was Activity Monitor's AreaGraph ResizeObserver re-rendering on every
// observation (a fresh {w,h} object defeats React's bail-out, and the re-render
// refires the observer), which starved the main thread so hard that other
// windows' lazy chunks never committed. That root cause is FIXED — see
// nextGraphSize in builtin/activity/ActivityMonitor.tsx and its regression test.
//
// The assertion stays regardless. It is what turned an invisible "two windows
// shipped empty and the runner said 40 captured, 0 failed" into a loud failure,
// and it will catch the next thing that leaves a pane on its fallback.
// expectVisibleText fails the shot if `text` is not on screen. Used by shots
// whose subject is a SURFACE rather than a window (the phone recents deck), so
// an empty overlay cannot be captured as if it were the feature — the same
// class of silent-empty capture that shipped two "Loading..." windows.
async function expectVisibleText(page, text, timeout = 10_000) {
  try {
    await page.getByText(text, { exact: false }).first().waitFor({ state: 'visible', timeout })
  } catch {
    // Say what WAS there. A bare "did not render" sends the reader back to
    // guessing, and the useful signal is almost always the text that showed up
    // instead.
    const seen = await page.locator('body').innerText().catch(() => '')
    // Save the frame too. Text alone has repeatedly been ambiguous here, and
    // the image says immediately whether the surface is absent, mid-animation,
    // or simply behind something.
    await page.screenshot({ path: `/tmp/shotfail-${Date.now()}.png` }).catch(() => {})
    throw new Error(
      `expected "${text}" to be visible before capture — the surface did not render.\n` +
      `      on screen: ${JSON.stringify(seen.replace(/\s*\n+\s*/g, ' | ').slice(0, 260))}`)
  }
}

async function waitForAppReady(page, title, { text, selector } = {}, timeout = 20_000) {
  const win = winRoot(page, title)
  const content = win.locator('.vwin-content').first()
  const deadline = Date.now() + timeout
  let body = ''
  for (;;) {
    body = await content.innerText().catch(() => '')
    const okText = text ? body.includes(text) : true
    const okSel = selector ? await win.locator(selector).first().isVisible().catch(() => false) : true
    if (okText && okSel && !body.includes('Loading...')) return
    if (Date.now() > deadline) {
      throw new Error(
        `"${title}" never rendered its app UI` +
        (body.includes('Loading...')
          ? ' — the window is still on its React.lazy Suspense fallback. The ' +
            'known cause of this was Activity Monitor\'s ResizeObserver render ' +
            'loop starving the main thread, fixed in nextGraphSize; if you are ' +
            'seeing it again, something is blocking the main thread once more.'
          : ` — wanted ${text ? JSON.stringify(text) : selector}, content was ` +
            `${JSON.stringify(body.slice(0, 120))}`))
    }
    await page.waitForTimeout(250)
  }
}

async function dragBy(page, fromX, fromY, toX, toY) {
  await page.mouse.move(fromX, fromY)
  await page.mouse.down()
  // Several intermediate moves: the drag/resize handlers are pointermove
  // listeners, so a single jump would deliver exactly one move event and the
  // shell's snap-zone tracking would never see the path.
  await page.mouse.move(toX, toY, { steps: 12 })
  await page.mouse.up()
  await page.waitForTimeout(180)
}

/**
 * Put a floating window at an exact position and size, driving only the real
 * pointer paths a user has: the bottom-right resize grip and the title-bar drag.
 *
 * The keyboard tile to 'top-left' at the start is load-bearing, not tidiness. A
 * freshly opened window is 720x500 (ShellProvider OPEN_WINDOW), which is TALLER
 * than a landscape-phone viewport — its resize grip sits below the fold and can
 * never be grabbed, so the phone shot could not be composed at all. Tiling first
 * parks the window at a guaranteed on-screen quarter. It does not leave a tiled
 * window behind: MOVE_WINDOW and RESIZE_WINDOW both clear `_tile`, so what the
 * capture sees is genuinely floating chrome.
 *
 * Call this straight after launching each window, back-most first. A newly
 * opened window is focused, so it is the top of the stack and both its title bar
 * and its grip are guaranteed hit-testable; placing everything at the end
 * instead means reaching for handles that the window in front is covering.
 */
async function placeWindow(page, title, { x, y, width, height }) {
  const win = winRoot(page, title)

  for (let attempt = 1; attempt <= 3; attempt++) {
    await snapWindow(page, title, ['Left', 'Up'], 'top-left')

    // Resize: drag the bottom-right grip by the delta to the target size.
    const grip = win.locator('.vwin-resize')
    let box = await win.boundingBox()
    const gb = await grip.boundingBox()
    if (gb) {
      const gx = gb.x + gb.width / 2
      const gy = gb.y + gb.height / 2
      await dragBy(page, gx, gy, gx + (width - box.width), gy + (height - box.height))
    }

    // Move: drag the title bar. Grab at 30% of the width — clear of the traffic
    // lights on the left and of the pop-out/save chrome buttons on the right,
    // both of which are [data-no-drag] and would swallow the gesture.
    box = await win.boundingBox()
    const bar = await win.locator('.vwin-titlebar').boundingBox()
    const px = bar.x + Math.max(90, box.width * 0.3)
    const py = bar.y + bar.height / 2
    await dragBy(page, px, py, px + (x - box.x), py + (y - box.y))

    box = await win.boundingBox()
    if (Math.abs(box.x - x) <= 3 && Math.abs(box.y - y) <= 3 &&
        Math.abs(box.width - width) <= 3 && Math.abs(box.height - height) <= 3) return
    if (attempt === 3) {
      throw new Error(
        `placeWindow: "${title}" never reached ${width}x${height}@(${x},${y}) — got ` +
        `${Math.round(box.width)}x${Math.round(box.height)}@(${Math.round(box.x)},${Math.round(box.y)})`)
    }
  }
}

/**
 * Raise a window to the top of the stack by clicking a part of it that no other
 * window is covering.
 *
 * Needed because the front window is not always the last one launched: launch
 * order fixes the base stacking order (WINDOW_Z_INACTIVE is the same 10 for
 * every unfocused window, so DOM order decides), while the focused window alone
 * gets WINDOW_Z_ACTIVE. When the composition wants the LAST-launched app in the
 * middle, this is what puts the right frame in front — and with it the deeper
 * shadow and the accent rim.
 *
 * The point is searched for rather than guessed: a fixed offset lands on
 * whichever window happens to cover that spot, and clicking the wrong window is
 * silent. Candidates skip the outer 90px of the title bar (the traffic lights on
 * one side, the [data-no-drag] chrome buttons on the other, both of which would
 * fire an action instead of focusing).
 */
async function bringToFront(page, title) {
  const target = await winRoot(page, title).boundingBox()
  if (!target) throw new Error(`bringToFront: no window titled "${title}"`)

  const all = page.locator('.vwin')
  const others = []
  for (let i = 0, n = await all.count(); i < n; i++) {
    const b = await all.nth(i).boundingBox()
    if (!b) continue
    if (Math.abs(b.x - target.x) < 1 && Math.abs(b.y - target.y) < 1) continue
    others.push(b)
  }
  const covered = (x, y) => others.some(o =>
    x > o.x - 4 && x < o.x + o.width + 4 && y > o.y - 4 && y < o.y + o.height + 4)

  const rows = [target.y + 17, target.y + target.height - 20, target.y + target.height / 2]
  for (const y of rows) {
    for (let x = target.x + 90; x < target.x + target.width - 90; x += 20) {
      if (covered(x, y)) continue
      await page.mouse.click(x, y)
      await page.waitForTimeout(250)
      const active = await page.locator('.vwin[data-active]').first().boundingBox().catch(() => null)
      if (active && Math.abs(active.x - target.x) < 2 && Math.abs(active.y - target.y) < 2) return
    }
  }
  throw new Error(`bringToFront: "${title}" has no uncovered spot to click — it ` +
    `cannot be raised, so the stack would capture with the wrong window in front`)
}

/**
 * Assert the composition at capture time. `specs` is back-to-front.
 *
 * Geometry alone is not enough to guard this shot. The thing being sold is
 * DEPTH, and there are two ways to lose it while every coordinate still looks
 * plausible: the windows drift apart into a grid, or the wrong window ends up
 * focused so the front frame carries the inactive shadow and the accent rim
 * lands on something behind it. Both are asserted here, because both have a
 * perfectly successful capture as their failure mode.
 */
async function assertStacked(page, specs) {
  const bad = []
  const rects = []
  const vp = page.viewportSize()

  for (const s of specs) {
    const box = await winRoot(page, s.title).boundingBox().catch(() => null)
    rects.push(box)
    if (!box) { bad.push(`${s.title}: no window`); continue }
    if (Math.abs(box.x - s.x) > 3 || Math.abs(box.y - s.y) > 3 ||
        Math.abs(box.width - s.width) > 3 || Math.abs(box.height - s.height) > 3) {
      bad.push(`${s.title}: want ${s.width}x${s.height}@(${s.x},${s.y}), got ` +
        `${Math.round(box.width)}x${Math.round(box.height)}@(${Math.round(box.x)},${Math.round(box.y)})`)
    }
    if (box.x < 0 || box.y < 32 || box.x + box.width > vp.width || box.y + box.height > vp.height) {
      bad.push(`${s.title}: falls outside the ${vp.width}x${vp.height} desktop`)
    }
  }

  // Overlap — the difference between a stack and a grid.
  for (let i = 0; i + 1 < rects.length; i++) {
    const a = rects[i], b = rects[i + 1]
    if (!a || !b) continue
    const ow = Math.min(a.x + a.width, b.x + b.width) - Math.max(a.x, b.x)
    const oh = Math.min(a.y + a.height, b.y + b.height) - Math.max(a.y, b.y)
    if (ow < 100 || oh < 60) {
      bad.push(`${specs[i].title} / ${specs[i + 1].title} overlap by only ` +
        `${Math.round(ow)}x${Math.round(oh)}px — that is a grid, not a stack`)
    }
  }

  // z-order — the front window must be the focused one (.vwin[data-active],
  // WINDOW_Z_ACTIVE=20 vs 10, plus the deeper shadow + accent rim).
  const front = rects.at(-1)
  const active = await page.locator('.vwin[data-active]').first().boundingBox().catch(() => null)
  if (!active || !front || Math.abs(active.x - front.x) > 3 || Math.abs(active.y - front.y) > 3) {
    bad.push(`the focused window is not "${specs.at(-1).title}" — the front frame ` +
      `would carry the inactive shadow`)
  }

  if (bad.length) throw new Error(`stacked layout wrong — ${bad.join('; ')}`)
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
    name: 'settings-relay',
    light: true,
    desc: 'Settings — Relay & Reachability (owner): built-in Vulos relay + live relay nodes',
    async drive(page) {
      await launchApp(page, 'Settings')
      // The Settings nav renders every section as a flat button (no collapsible
      // groups), so the owner-only Network item is directly clickable once
      // role:'admin' makes it visible.
      await page.getByRole('button', { name: 'Relay & Reachability' }).first().click().catch(() => {})
      await page.waitForTimeout(900)
    },
  },
  {
    name: 'settings-domain',
    light: true,
    desc: 'Settings — Custom Domain: a verified custom domain with TLS active',
    async drive(page) {
      await launchApp(page, 'Settings')
      await page.getByRole('button', { name: 'Custom Domain' }).first().click().catch(() => {})
      await page.waitForTimeout(900)
    },
  },
  {
    name: 'calendar',
    light: true,
    dsf: 2,
    desc: 'Calendar — month view, a lived-in schedule over connected accounts',
    async drive(page) {
      await launchApp(page, 'Calendar')
      await page.waitForTimeout(400)
    },
  },
  {
    name: 'assistant',
    light: true,
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
    light: true,
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
    name: 'launchpad',
    light: true,
    dsf: 2,
    desc: 'Launchpad — the full colourful app grid (built-ins + installed apps)',
    async drive(page) {
      // Open the app grid via the menu-bar "Applications" button. Give the
      // installed-app list (loaded async at boot) a beat to populate, then let
      // the staggered tile entrance settle before capture.
      await page.waitForTimeout(800)
      await page.getByRole('button', { name: 'Applications' }).first().click().catch(() => {})
      await page.waitForTimeout(1_100)
    },
  },
  {
    name: 'apphub',
    light: true,
    desc: 'App Hub — Browse (populated catalogue)',
    async drive(page) { await launchApp(page, 'App Hub') },
  },
  {
    name: 'apphub-installed',
    light: true,
    desc: 'App Hub — Installed (18 apps)',
    async drive(page) {
      await launchApp(page, 'App Hub')
      await page.getByRole('button', { name: /Installed/ }).first().click().catch(() => {})
      await page.waitForTimeout(600)
    },
  },
  {
    name: 'dashboard',
    light: true,
    desc: 'Dashboard — Web publishing + per-app resources',
    async drive(page) { await launchApp(page, 'Dashboard') },
  },
  {
    name: 'instances',
    light: true,
    desc: 'Dashboard — Instances (routing across device + cloud)',
    async drive(page) {
      await launchApp(page, 'Dashboard')
      await page.getByRole('button', { name: 'Instances' }).first().click().catch(() => {})
      await page.waitForTimeout(900)
    },
  },
  {
    name: 'terminal',
    light: true,
    desc: 'Terminal — real xterm over a scripted demo PTY',
    async drive(page) {
      await launchApp(page, 'Terminal')
      await page.waitForTimeout(900)
    },
  },
  {
    name: 'stacked',
    light: true,
    desc: 'Desktop — three floating windows stacked, the focused one in front',
    // THE multi-window shot. Free-floating windows that overlap, with a clear
    // z-order and the focused frame's shadow falling across the ones behind it,
    // are what make this read as an operating system; a neat tiled grid reads as
    // a dashboard. `tiled` below is kept as the separate "…and it tiles too"
    // demo, and is the only shot that tiles.
    //
    // The cascade runs down-and-right, so each window behind shows its title bar
    // and its top-left corner — which is where the three apps look least alike:
    // the Terminal's ANSI banner, File Explorer's Places sidebar, Activity
    // Monitor's coloured stat cards. The front frame stops at x=1320, clear of
    // the ambient widget column (fixed right-3 w-60 → x 1348..1588), so the
    // wallpaper, the clock/agenda/notification cards and the dock all stay in
    // frame around the stack instead of being papered over by it.
    async drive(page) {
      await launchApp(page, 'Terminal', { maximize: false })
      await placeWindow(page, 'Terminal', { x: 150, y: 96, width: 720, height: 460 })

      await launchApp(page, 'File Explorer', { maximize: false })
      await placeWindow(page, 'File Explorer', { x: 300, y: 200, width: 800, height: 520 })

      await launchApp(page, 'Activity Monitor', { maximize: false })
      await placeWindow(page, 'Activity Monitor', { x: 460, y: 320, width: 860, height: 540 })

      await page.waitForTimeout(500)
      await assertStacked(page, [
        { title: 'Terminal', x: 150, y: 96, width: 720, height: 460 },
        { title: 'File Explorer', x: 300, y: 200, width: 800, height: 520 },
        { title: 'Activity Monitor', x: 460, y: 320, width: 860, height: 540 },
      ])
    },
  },
  {
    name: 'tiled',
    light: true,
    desc: 'Desktop — the same windows snapped into a tiled layout ("…and it tiles too")',
    // This shot used to just launch three windows and let ShellProvider's
    // +32px cascade stagger them, on the theory that overlap "reads as tiled".
    // It does not: the result is three windows stacked in the top-left corner
    // with the lower two almost entirely hidden, and half the canvas empty.
    // It also failed to show the feature it captions — drag, snap and tile.
    // Drive the REAL tiling path instead (useWindowShortcuts: Super/⌘+arrow →
    // nextTile → tileWindow), so the shot is the actual feature and fills the
    // frame: two stacked quarters on the left, one half on the right.
    async drive(page) {
      // Launch ALL windows before tiling any of them. Interleaving the two
      // disturbs already-placed windows: an earlier version tiled each window
      // straight after launching it, every snapWindow verified and passed, and
      // the captured frame still showed the Terminal filling the whole left half.
      // Opening a window after a tile is what moves it, so do all the opening
      // first, then place, then assert the final layout at capture time.
      await launchApp(page, 'File Explorer', { maximize: false })
      await launchApp(page, 'Terminal', { maximize: false })
      await launchApp(page, 'Activity Monitor', { maximize: false })

      // Tile in REVERSE launch order — topmost first. Newly opened windows
      // cascade over the earlier ones, so tiling front-to-back means the window
      // being clicked is never covered by one still sitting at its cascade spot.
      await snapWindow(page, 'Activity Monitor', ['Right'], 'right')
      await snapWindow(page, 'Terminal', ['Left', 'Down'], 'bottom-left')
      await snapWindow(page, 'File Explorer', ['Left', 'Up'], 'top-left')

      await page.waitForTimeout(400)
      await assertTiled(page, [
        ['File Explorer', 'top-left'],
        ['Terminal', 'bottom-left'],
        ['Activity Monitor', 'right'],
      ])
    },
  },
  {
    name: 'mobile',
    light: true,
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
    light: true,
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
  {
    name: 'mobile-windows',
    // Light too: the README and the landing page both present their screenshot
    // grids in the light theme, so a dark-only phone shot could not be shown
    // alongside the others.
    light: true,
    dsf: 2,
    desc: 'Phone — several apps running, in the recents deck',
    // A PHONE, IN PORTRAIT, showing how multitasking actually works on a phone.
    //
    // This shot was previously captured at 932x430 — an iPhone held sideways.
    // src/App.jsx picks its layout purely on WIDTH (<768px → MobileStack), so
    // 932px is over the line and renders the full DesktopCanvas. The result was
    // an honest capture of a real mode, but it was indistinguishable from a
    // tablet: floating windows, menu bar, widget column. As "the mobile
    // screenshot" it misrepresented the phone experience, which is what people
    // actually want to see.
    //
    // 390x844 is a plain iPhone in portrait, and at that width the shell
    // deliberately has no floating windows: an app owns the screen and the
    // recents deck replaces the window stack. That deck is the mobile
    // multitasking story, so it is what this shows.
    viewport: { width: 390, height: 844 },
    async drive(page) {
      await page.waitForTimeout(1200)
      // Drive the palette directly rather than through launchApp(): that helper
      // is built for the desktop shell (it nudges focus by clicking the
      // wallpaper and then looks for a Maximize button), and none of that
      // applies inside MobileStack, where the running app owns the screen.
      for (const app of ['File Explorer', 'Terminal', 'Calendar']) {
        await page.keyboard.press('Meta+k')
        const input = page.getByPlaceholder(/Search apps/)
        if (!await input.isVisible({ timeout: 4_000 }).catch(() => false)) {
          throw new Error(`the ⌘K palette never opened for "${app}" — nothing was launched`)
        }
        await input.fill(app)
        await page.keyboard.press('Enter')
        await page.waitForTimeout(1_200)
      }

      // Open the recents deck from the dock. TAP, not click: this context is
      // created with isMobile + hasTouch (see captureTheme), so the page is
      // emulating a touch device.
      //
      // Verified in a loop rather than fired once, because the freshly-launched
      // app is still settling. Checking BEFORE each retry matters: the control
      // is a toggle, so a blind second tap would close a deck that had already
      // opened.
      const deck = page.getByText('Running apps').first()
      for (let i = 0; i < 6; i++) {
        if (await deck.isVisible().catch(() => false)) break
        const appsAll = page.getByRole('button', { name: 'Apps' })
        // TWO elements answer to "Apps" at phone size: one in the top bar at
        // y≈10, and the dock control at y≈784. `.first()` picked the top-bar one
        // every time, so every tap "succeeded" and nothing opened — the tap was
        // landing on a real element, just not this one. Pick the dock by
        // position rather than by order.
        const cnt = await appsAll.count().catch(() => 0)
        if (cnt === 0) {
          throw new Error('the dock\'s "Apps" control is not on screen — MobileStack did not mount')
        }
        const vh = page.viewportSize().height
        let apps = null
        for (let k = 0; k < cnt; k++) {
          const cand = appsAll.nth(k)
          const box = await cand.boundingBox().catch(() => null)
          if (box && box.y > vh * 0.7) { apps = cand; break }
        }
        if (!apps) {
          throw new Error(`found ${cnt} "Apps" control(s) but none in the bottom dock — the dock did not render`)
        }
        await apps.tap({ timeout: 4_000 }).catch(() => {})
        await page.waitForTimeout(700)
      }

      // The subject of this shot is the deck itself, so assert it rendered with
      // all three apps. An empty overlay would otherwise capture silently — the
      // same failure that once shipped two "Loading..." windows in a hero shot.
      await expectVisibleText(page, 'Running apps')
      await expectVisibleText(page, '3 open')
    },
  },
  {
    name: 'tablet-windows',
    light: true,
    dsf: 2,
    desc: 'Tablet — two floating windows stacked, the focused one in front',
    // 1024x768 (landscape tablet). App.jsx picks its layout purely on window
    // width (<768px → MobileStack), so a tablet gets the FULL DesktopCanvas —
    // real windows, real tiling — rather than the phone stack. That is the
    // whole point of the shot: the same windowing works on a tablet, which is
    // not obvious from a phone screenshot where windows become fullscreen
    // cards.
    //
    // Stacked, not side-by-side. Two half-screen tiles filled the frame edge to
    // edge and read as a split-screen dashboard; two overlapping windows on
    // visible wallpaper read as the desktop this actually is. Two rather than
    // the desktop's three: a third 1024-wide cascade step would push the front
    // window off the tablet.
    //
    // Both windows stay left of x=772, where the ambient widget column starts
    // (fixed right-3 w-60). The column is ~294px tall, so at 768px of height it
    // sits fully in frame beside the stack rather than being sliced down the
    // middle by a window edge.
    viewport: { width: 1024, height: 768 },
    async drive(page) {
      // File Explorer behind, Activity Monitor in front. Activity Monitor is the
      // one that fills its frame at this size — four coloured stat cards, then a
      // process table — while File Explorer's exposed top-left strip (Places
      // sidebar + path bar) is instantly recognisable as a different app.
      await launchApp(page, 'File Explorer', { maximize: false })
      await placeWindow(page, 'File Explorer', { x: 24, y: 58, width: 660, height: 400 })

      await launchApp(page, 'Activity Monitor', { maximize: false })
      await placeWindow(page, 'Activity Monitor', { x: 92, y: 140, width: 668, height: 420 })

      await page.waitForTimeout(400)
      await assertStacked(page, [
        { title: 'File Explorer', x: 24, y: 58, width: 660, height: 400 },
        { title: 'Activity Monitor', x: 92, y: 140, width: 668, height: 420 },
      ])
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
    const p = spawn(cmd, args, { cwd: FRONTEND, stdio: 'inherit', ...opts })
    p.on('exit', (code) => (code === 0 ? resolve() : reject(new Error(`${cmd} exited ${code}`))))
    p.on('error', reject)
  })
}

async function captureTheme(browser, theme, overrides, results) {
  const suffix = theme === 'light' ? '-light' : ''
  for (const shot of SHOTS) {
    if (process.env.SHOT && shot.name !== process.env.SHOT) continue
    if (theme === 'light' && !shot.light) continue
    // FRESH context per shot: the shell persists open-window state to
    // localStorage, so a shared context would restore prior shots' windows.
    // Isolation guarantees each shot starts from a clean desktop.
    // A shot may request its own (narrower) viewport — e.g. `mobile`, which
    // needs <768px to flip the shell's useViewport() media query and render
    // MobileStack instead of DesktopCanvas (see src/App.jsx `useDesktop`).
    // Existing shots omit `viewport` and get the standard desktop VIEWPORT.
    // Derive "is this the PHONE shell?" from the actual breakpoint, not merely
    // from having a custom viewport. src/App.jsx flips to MobileStack purely on
    // width < 768px, so a tablet-sized override (1024x768) still renders the
    // full DesktopCanvas. Keying off `!!shot.viewport` made every custom-size
    // shot wait for the mobile-only root marker, so the tablet shot hung until
    // it timed out.
    const shotViewport = shot.viewport || VIEWPORT
    const isPhoneShot = shotViewport.width < 768
    const context = await browser.newContext({
      viewport: shotViewport,
      // Per-shot override (see SHOTS above): lighter views opt into dsf:2 for
      // retina crispness; heavy maximized apps (App Hub, Dashboard, Instances,
      // tiled multi-window) stay at the default 1 — headless Chromium
      // intermittently captures a BLACK GPU frame for those at 2x.
      deviceScaleFactor: shot.dsf || 1,
      isMobile: isPhoneShot,
      // Tablets are touch devices too, so touch follows any viewport override.
      hasTouch: !!shot.viewport,
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
      // Activity Monitor's telemetry WebSocket. Send the whole walk up front,
      // back to back: the component appends every frame it receives to its
      // history buffer, so this fills the sparklines with a real trace before
      // the capture instead of the single flat reading it used to get. The last
      // frame is also what the gauges read, so the numbers and the graphs agree.
      await page.routeWebSocket(/\/api\/telemetry/, (ws) => {
        // Paced, not blasted. Sending all 140 frames synchronously on connect
        // queued 140 React state updates at once and starved the main thread —
        // enough that a tap on the phone dock landed without the app ever
        // responding to it, which cost a while to track down because the tap
        // itself reported success. Same failure shape as the ResizeObserver
        // render loop: the UI is not broken, it is simply too busy to answer.
        //
        // A short interval fills the sparklines just as well, since the capture
        // waits on real content anyway.
        let i = 0
        const t = setInterval(() => {
          if (i >= DEMO_TELEMETRY_SERIES.length) { clearInterval(t); return }
          try { ws.send(JSON.stringify(DEMO_TELEMETRY_SERIES[i++])) } catch { clearInterval(t) }
        }, 12)
        ws.onClose(() => clearInterval(t))
      })
      // Accept — and then hold open, silently — the shell's two ambient event
      // streams. installBackend mocks the REST surface but not these, so they
      // reached the vite preview server, were refused, and reconnected in a
      // tight loop: every capture logged HUNDREDS of "WebSocket connection
      // failed" errors. Holding them open is also the honest state — on a real
      // box these connect — and it takes a needless reconnect storm out of the
      // page while the shots are being driven.
      await page.routeWebSocket(/\/api\/(notifications|peering)\/stream/, (ws) => {
        ws.onMessage(() => { /* nothing to answer */ })
      })
      await page.goto(BASE_URL, { waitUntil: 'domcontentloaded' })
      // The desktop TopBar's "Applications" button doesn't exist in
      // MobileStack — wait for that layout's own root marker instead.
      if (isPhoneShot) {
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
    // FRONTEND, not REPO_ROOT: the vite project (and its dist/) live in
    // frontend/. Run from the repo root and preview serves a directory with no
    // index.html, so the page loads without a #root and every shot times out
    // waiting for a shell that was never mounted.
    cwd: FRONTEND, stdio: 'ignore', detached: false,
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

  // COVERAGE ASSERTION.
  //
  // Without this, a run that captures NOTHING exits 0: SHOT could name a shot
  // that does not exist, a bad edit could empty SHOTS, or a refactor could make
  // every entry skip. The summary then reads "0 captured, 0 failed" and the
  // caller treats stale docs assets as freshly regenerated.
  //
  // Expected count is derived from SHOTS rather than hard-coded, so adding or
  // removing a shot needs no edit here — but capturing fewer than expected,
  // for any reason, is a failure.
  const expected = process.env.SHOT
    ? SHOTS.filter((s) => s.name === process.env.SHOT).length +
      SHOTS.filter((s) => s.name === process.env.SHOT && s.light).length
    : SHOTS.length + SHOTS.filter((s) => s.light).length

  if (process.env.SHOT && expected === 0) {
    console.error(
      `\nSHOT="${process.env.SHOT}" matched no shot. Known shots:\n` +
      SHOTS.map((s) => `  ${s.name}`).join('\n'),
    )
    process.exit(1)
  }

  if (ok.length !== expected) {
    console.error(
      `\nCoverage check FAILED: captured ${ok.length} image(s), expected ${expected}.` +
      `\nThe docs assets on disk are NOT a complete regeneration — do not treat them as current.`,
    )
    process.exit(1)
  }

  console.log(`Coverage check OK: ${ok.length}/${expected} expected images captured.`)
  process.exit(failed.length ? 1 : 0)
}

main().catch((err) => { console.error('Fatal:', err); process.exit(1) })
