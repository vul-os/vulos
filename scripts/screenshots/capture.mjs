#!/usr/bin/env node
/**
 * vulos-management — React console screenshotter
 *
 * Captures the OSS control plane's React console SPA (web/dist — the "Vulos
 * Workspace": auth + user console + operator admin) into
 * docs/assets/screenshots/ for the README gallery.
 *
 * How it works (fully repeatable, privacy-safe — no real data ever touched):
 *
 *   1. Serves the built SPA (web/dist) over a throwaway localhost static server
 *      with SPA fallback: hashed assets under /console/assets/* are served from
 *      disk, every other /console/* path returns index.html so the hand-rolled
 *      router can resolve it.
 *   2. Drives headless Chromium (Playwright) with EVERY /api/** request
 *      intercepted and answered from an in-memory fixture bank of FABRICATED
 *      demo data (a small account + fleet + audit trail + an operator). Nothing
 *      hits a real backend; the management binary is never run; no operator data
 *      is ever captured. Commercial/billing surfaces stay empty (the OSS build
 *      resolves @vulos/commercial-seam to the NoOp seam) — correct for OSS.
 *   3. Shoots each page at retina and writes docs/assets/screenshots/<name>.png.
 *
 * The console ships a single deliberate dark "instrument-panel" theme (the
 * index.html theme bootstrap defaults to dark), so each page is captured in its
 * one canonical look.
 *
 * Usage:
 *   npm install && npx playwright install chromium   # first time
 *   npm run screenshots                              # or: node scripts/screenshots/capture.mjs
 *
 * Requires: web/dist built (`npm --prefix web run build`) and Chromium (Playwright).
 */

import { chromium } from 'playwright'
import { createServer } from 'node:http'
import { readFileSync, existsSync, statSync, mkdirSync, readdirSync, unlinkSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const ROOT = path.resolve(__dirname, '..', '..')
const DIST = path.join(ROOT, 'web', 'dist')
const PNG_OUT = path.join(ROOT, 'docs', 'assets', 'screenshots')

if (!existsSync(path.join(DIST, 'index.html'))) {
  console.error(`\n  web/dist is not built. Run:  npm --prefix web run build\n`)
  process.exit(1)
}

// ── Fabricated demo data ───────────────────────────────────────────────────────
// Timestamps are computed relative to "now" so the relative-time labels
// ("3m ago") render naturally. All identifiers/emails are invented.
const now = Date.now()
const ago = (mins) => new Date(now - mins * 60_000).toISOString()

const DEMO_ACCOUNT = { id: '01HQ9Z7VULOSACCT0DEMO001', email: 'ada@example.org', email_verified: true }

const DEMO_DEVICES = [
  { ulid: '01HQZX3NKB0X0RTHERN0DE01', name: 'northstar', version: '0.9.4', channel: 'stable', status: 'online',   health: 'online',   last_heartbeat: ago(2),    crash_count: 0, decommissioned: false },
  { ulid: '01HQZX3NKB0X0MERIDIAN002', name: 'meridian',  version: '0.9.4', channel: 'stable', status: 'online',   health: 'online',   last_heartbeat: ago(1),    crash_count: 0, decommissioned: false },
  { ulid: '01HQZX3NKB0X0ATELIER0003', name: 'atelier',   version: '0.9.3', channel: 'beta',   status: 'degraded', health: 'degraded', last_heartbeat: ago(41),   crash_count: 2, decommissioned: false },
  { ulid: '01HQZX3NKB0X0COTTAGE0004', name: 'cottage',   version: '0.9.4', channel: 'stable', status: 'online',   health: 'online',   last_heartbeat: ago(6),    crash_count: 0, decommissioned: false },
  { ulid: '01HQZX3NKB0X0SPARE00T005', name: 'spare-t470', version: '0.8.9', channel: 'stable', status: 'offline', health: 'offline', last_heartbeat: ago(4820), crash_count: 1, decommissioned: true },
]

const DEMO_ACCOUNT_STATUS = {
  overall: 'operational',
  relay: { health: 'ok', bytes: 41_083_218_944, sessions: 128, period: 'Jul 2026' },
  boxes: [
    { name: 'northstar', ulid: '01HQZX3NKB0X0RTHERN0DE01', reachable: true,  last_seen: ago(2),  health: 'operational' },
    { name: 'meridian',  ulid: '01HQZX3NKB0X0MERIDIAN002', reachable: true,  last_seen: ago(1),  health: 'operational' },
    { name: 'atelier',   ulid: '01HQZX3NKB0X0ATELIER0003', reachable: false, last_seen: ago(41), health: 'degraded' },
  ],
  services: [
    { name: 'Relay PoP — eu-central', status: 'operational' },
    { name: 'Relay PoP — af-south',   status: 'operational' },
    { name: 'OS routing directory',   status: 'operational' },
    { name: 'Object storage (BYOB)',  status: 'degraded' },
  ],
  events: [
    { at: ago(2),   kind: 'heartbeat', text: 'northstar checked in — healthy' },
    { at: ago(18),  kind: 'enroll',    text: 'meridian joined the fleet' },
    { at: ago(41),  kind: 'health',    text: 'atelier reported degraded storage' },
    { at: ago(140), kind: 'relay',     text: 'Relay failover: af-south PoP promoted' },
    { at: ago(360), kind: 'key',       text: 'API key “CI deploy key” issued' },
  ],
}

const auditEntry = (seq, ts, actor, action, target, metadata) => ({ seq, id: `aud_${seq}`, ts, actor, action, target, metadata })
const DEMO_ORG_AUDIT = {
  entries: [
    auditEntry(812, ago(6),   'ada@example.org',   'device_enrolled',       'meridian',            { channel: 'stable', method: 'device_code' }),
    auditEntry(811, ago(52),  'ada@example.org',   'api_key_created',       'CI deploy key',       { scopes: 'relay.send' }),
    auditEntry(810, ago(190), 'ada@example.org',   'member_added',          'lin@example.org',     { role: 'admin' }),
    auditEntry(809, ago(240), 'system',            'role_changed',          'lin@example.org',     { from: 'member', to: 'admin' }),
    auditEntry(808, ago(300), 'ada@example.org',   'webhook_created',       'hooks/vulos',         null),
    auditEntry(807, ago(900), 'ada@example.org',   'device_decommissioned', 'spare-t470',          null),
    auditEntry(806, ago(1440),'system',            'billing_noop_recorded', 'relay_gb',            { gb: '41' }),
    auditEntry(805, ago(2880),'ada@example.org',   'member_invited',        'sam@example.org',     { role: 'member' }),
  ],
  count: 8, limit: 50, next_cursor: '',
}

const DEMO_DEV_KEYS = [
  { id: 'key_01', name: 'CI deploy key',   scopes: ['relay.send'],                     products: ['relay'],         created_at: ago(52 * 60) },
  { id: 'key_02', name: 'Office bot',       scopes: ['documents.read', 'documents.write'], products: ['office'],    created_at: ago(9 * 1440) },
  { id: 'key_03', name: 'Backup exporter',  scopes: ['drive.read'],                     products: ['files', 'drive'], created_at: ago(30 * 1440) },
]
const DEMO_WEBHOOKS = [
  { id: 'wh_01', url: 'https://ops.example.org/hooks/vulos', state: 'active' },
  { id: 'wh_02', url: 'https://ci.example.org/vulos/deploy', state: 'active' },
]

const DEMO_COMPLIANCE = {
  requests: [
    { id: 'req_02', kind: 'export',  status: 'completed', note: 'Quarterly personal archive', created_at: ago(3 * 1440) },
    { id: 'req_01', kind: 'export',  status: 'pending',   note: '', created_at: ago(20) },
  ],
}

const DEMO_SUPPORT_STATUS = {
  ulid: '01HQZX3NKB0X0RTHERN0DE01', sampled_at: ago(3), has_data: true,
  signals: [
    { signal: 'cpu_load',    severity: 'ok',   value: 0.34, unit: '',    detail: '4-core, 15m avg' },
    { signal: 'memory_used', severity: 'ok',   value: 61.2, unit: '%',   detail: '9.8 / 16 GB' },
    { signal: 'disk_used',   severity: 'warn', value: 82.0, unit: '%',   detail: '410 / 500 GB' },
    { signal: 'relay_rtt',   severity: 'ok',   value: 24,   unit: 'ms',  detail: 'eu-central PoP' },
    { signal: 'uptime',      severity: 'ok',   value: 37,   unit: 'days', detail: 'since 0.9.4' },
    { signal: 'temp',        severity: 'ok',   value: 48,   unit: '°C',  detail: 'package' },
  ],
  alerts: [
    { id: 'al_1', signal: 'disk_used', severity: 'warn', detail: 'Root volume above 80% — prune snapshots or expand storage.', fired_at: ago(120) },
  ],
}

// ── Operator (super-admin) fixtures ─────────────────────────────────────────────
const opAudit = (seq, ts, actor, action, target, metadata) => ({ seq, id: `sa_${seq}`, ts, actor, action, target, metadata })
const DEMO_SA_DASHBOARD = {
  account_count: 1284, superadmin_count: 3,
  recent_audit: [
    opAudit(4021, ago(3),  'ada@example.org', 'admin.login',           'console',         { mfa: 'webauthn' }),
    opAudit(4020, ago(14), 'sam@example.org', 'account.suspend',       'wiley@example.org', { reason: 'abuse' }),
    opAudit(4019, ago(38), 'system',          'relay.autoscale',       'af-south',        { from: '2', to: '3' }),
    opAudit(4018, ago(64), 'lin@example.org', 'account.reset_2fa',     'joan@example.org', null),
    opAudit(4017, ago(96), 'system',          'audit.chain_verified',  'nightly',         { rows: '128401' }),
  ],
}
const DEMO_SA_ACCOUNTS = [
  { id: '01HQ9Z7VULOSACCT0DEMO001', email: 'ada@example.org',   suspended: false, created_at: ago(400 * 1440) },
  { id: '01HQ9Z7VULOSACCT0DEMO002', email: 'lin@example.org',   suspended: false, created_at: ago(300 * 1440) },
  { id: '01HQ9Z7VULOSACCT0DEMO003', email: 'sam@example.org',   suspended: false, created_at: ago(210 * 1440) },
  { id: '01HQ9Z7VULOSACCT0DEMO004', email: 'joan@example.org',  suspended: false, created_at: ago(120 * 1440) },
  { id: '01HQ9Z7VULOSACCT0DEMO005', email: 'wiley@example.org', suspended: true,  created_at: ago(60 * 1440) },
  { id: '01HQ9Z7VULOSACCT0DEMO006', email: 'nils@example.org',  suspended: false, created_at: ago(12 * 1440) },
]
const DEMO_SA_AUDIT = {
  rows: [
    opAudit(4021, ago(3),   'ada@example.org', 'admin.login',        'console',          { ip: '198.51.100.24', mfa: 'webauthn' }),
    opAudit(4020, ago(14),  'sam@example.org', 'account.suspend',    'wiley@example.org', { reason: 'abuse' }),
    opAudit(4019, ago(38),  'system',          'relay.autoscale',    'af-south',         { from: '2', to: '3' }),
    opAudit(4018, ago(64),  'lin@example.org', 'account.reset_2fa',  'joan@example.org',  null),
    opAudit(4017, ago(96),  'system',          'audit.chain_verified','nightly',         { rows: '128401' }),
    opAudit(4016, ago(150), 'ada@example.org', 'reserved_handle.add','postmaster',       null),
    opAudit(4015, ago(220), 'system',          'secret.rotated',     'session_key',      { age_days: '30' }),
  ],
}
const DEMO_SA_SECURITY = {
  waf: [
    { ID: 'w1', RuleID: 'SQLI-04', Pattern: "' OR 1=1 --", ClientIP: '203.0.113.9',  Path: '/api/auth/login', Ts: ago(9) },
    { ID: 'w2', RuleID: 'XSS-11',  Pattern: '<script>',     ClientIP: '198.51.100.7', Path: '/api/org/audit',  Ts: ago(52) },
    { ID: 'w3', RuleID: 'TRAV-02', Pattern: '../../etc',    ClientIP: '203.0.113.44', Path: '/api/account/export', Ts: ago(140) },
  ],
  bots: [
    { ID: 'b1', ClientIP: '203.0.113.9',  Score: 0.94, Reason: 'headless-signature + rate', Ts: ago(9) },
    { ID: 'b2', ClientIP: '192.0.2.55',   Score: 0.72, Reason: 'datacenter ASN', Ts: ago(70) },
  ],
  stepups: [
    { ID: 's1', UserID: '01HQ9Z7VULOSACCT0DEMO004', ClientIP: '198.51.100.7', RiskScore: 0.66, Ts: ago(31) },
  ],
  ato: [
    { ID: 'a1', UserID: '01HQ9Z7VULOSACCT0DEMO005', Action: 'impossible_travel', ClientIP: '203.0.113.44', Ts: ago(48) },
  ],
  honeypot: [
    { ID: 'h1', Email: 'admin@vulos.local', ClientIP: '192.0.2.55', Ts: ago(88) },
  ],
  egress: [
    { ID: 'e1', UserID: '01HQ9Z7VULOSACCT0DEMO006', BytesHr: 8_400_000_000, Baseline: 900_000_000, Sigma: 6.2, Ts: ago(210) },
  ],
}

// ── Fixture router — resolves an /api/** pathname to a response ─────────────────
// `ctx` carries per-capture flags: { authed, operator }.
function resolveApi(pathname, ctx) {
  const J = (body, status = 200) => ({ status, body })
  const NO = (status) => ({ status, body: null })

  if (pathname === '/api/auth/me') return ctx.authed ? J(DEMO_ACCOUNT) : NO(401)
  if (pathname === '/api/superadmin/whoami') return ctx.operator ? J({ operator: true }) : NO(403)

  if (pathname === '/api/fleet/devices')     return J({ items: DEMO_DEVICES })
  if (pathname === '/api/account/status')    return J(DEMO_ACCOUNT_STATUS)
  if (pathname === '/api/org/audit')         return J(DEMO_ORG_AUDIT)
  if (pathname === '/api/developer/keys')    return J(DEMO_DEV_KEYS)
  if (pathname === '/api/developer/webhooks')return J(DEMO_WEBHOOKS)
  if (pathname === '/api/developer/mcp-servers') return J({ servers: [] })
  if (pathname === '/api/compliance/requests')   return J(DEMO_COMPLIANCE)
  if (pathname === '/api/support/status')    return J(DEMO_SUPPORT_STATUS)

  if (pathname === '/api/superadmin/dashboard') return ctx.operator ? J(DEMO_SA_DASHBOARD) : NO(403)
  if (pathname === '/api/superadmin/accounts')  return ctx.operator ? J(DEMO_SA_ACCOUNTS)  : NO(403)
  if (pathname === '/api/superadmin/audit')     return ctx.operator ? J(DEMO_SA_AUDIT)     : NO(403)
  if (pathname === '/api/superadmin/security')  return ctx.operator ? J(DEMO_SA_SECURITY)  : NO(403)

  // Any other API: a benign empty 200 keeps the SPA from erroring.
  return J({})
}

// ── Page catalogue: route, flags and README caption ─────────────────────────────
const PAGES = [
  { name: 'login',           route: '/login',           authed: false, title: 'Sign in',            desc: 'One Vulos account for the OS, the apps and the console — email + password, an optional passkey, and social sign-in when the deployment configures it.',
    prepare: async (page) => {
      // Fill the form with fabricated demo credentials so the primary CTA shows
      // its live (enabled) accent state in the gallery, not the empty/disabled one.
      await page.fill('#login-email', 'ada@example.org').catch(() => {})
      await page.fill('#login-password', 'correct-horse-battery-staple').catch(() => {})
      await page.locator('#login-email').blur().catch(() => {})
    } },
  { name: 'dashboard',       route: '/',                title: 'Dashboard',          desc: 'The console cockpit — fleet + relay stat tiles, quick actions and the recent-activity feed. Billing surfaces stay absent under the NoOp seam.' },
  { name: 'boxes',           route: '/boxes',           title: 'Boxes',              desc: 'The machines running your Vulos OS: per-box version, channel, health and last-seen, as a responsive card grid.' },
  { name: 'devices',         route: '/devices',         title: 'Devices',            desc: 'Every enrolled device as a manageable table — health pills, last heartbeat and a decommission action.' },
  { name: 'account-status',  route: '/account-status',  title: 'Account status',     desc: 'Per-account health: box reachability, relay usage/health, provisioned services and recent events.' },
  { name: 'telemetry',       route: '/status',          title: 'Telemetry',          desc: 'Per-device support diagnostics — pick a box and read its latest health signals and open alerts.' },
  { name: 'enroll',          route: '/enroll',          title: 'Enroll a box',       desc: 'Bind a new box to your account from the device code it shows at boot, with a plain how-it-works explainer.' },
  { name: 'developer',       route: '/developer',       title: 'Developer',          desc: 'Issue scoped API keys, register webhooks and (soon) MCP servers for building on your control plane.' },
  { name: 'auditlog',        route: '/audit',           title: 'Audit log',          desc: 'Who did what in your organisation — expandable, tamper-evident rows with actor / action / target filters.' },
  { name: 'privacy',         route: '/privacy',         title: 'Privacy & data',     desc: 'Export or erase your account data and track the compliance requests you have filed.' },
  { name: 'admin-dashboard', route: '/admin',           operator: true, title: 'Operator — Dashboard', desc: 'The operator cockpit: account / super-admin counts and the most recent platform audit rows.' },
  { name: 'admin-accounts',  route: '/admin/accounts',  operator: true, title: 'Operator — Accounts',  desc: 'Operator account search with an inline detail drawer — session state, flags and transactions.' },
  { name: 'admin-audit',     route: '/admin/audit',     operator: true, title: 'Operator — Audit',     desc: 'The platform-wide, hash-chained audit trail (every tenant + system) with actor / action filters.' },
  { name: 'admin-security',  route: '/admin/security',  operator: true, title: 'Operator — Security',  desc: 'Defensive telemetry: WAF hits, bot flags, step-up challenges, ATO anomalies, honeypot + egress.' },
]

// ── Static server for the built SPA (SPA fallback under /console/) ──────────────
const MIME = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8', '.svg': 'image/svg+xml', '.woff2': 'font/woff2',
  '.woff': 'font/woff', '.json': 'application/json', '.png': 'image/png', '.ico': 'image/x-icon',
}
function serve() {
  const indexHtml = readFileSync(path.join(DIST, 'index.html'))
  const srv = createServer((req, res) => {
    let rel = decodeURIComponent(req.url.split('?')[0])
    // Assets live at /console/assets/* → dist/assets/*
    if (rel.startsWith('/console/')) rel = rel.slice('/console'.length)
    if (rel === '/' || rel === '') { res.setHeader('Content-Type', MIME['.html']); res.end(indexHtml); return }
    const full = path.join(DIST, rel.replace(/^\/+/, ''))
    if (full.startsWith(DIST) && existsSync(full) && statSync(full).isFile()) {
      res.setHeader('Content-Type', MIME[path.extname(full)] ?? 'application/octet-stream')
      res.end(readFileSync(full))
      return
    }
    // SPA fallback — any unknown path renders the shell.
    res.setHeader('Content-Type', MIME['.html'])
    res.end(indexHtml)
  })
  return new Promise((resolve) => srv.listen(0, '127.0.0.1', () => resolve({ srv, port: srv.address().port })))
}

// ── Capture ─────────────────────────────────────────────────────────────────────
async function main() {
  mkdirSync(PNG_OUT, { recursive: true })

  // Remove stale PNGs (the old Go-template admin shots) so the gallery only ever
  // holds the current React-console set.
  for (const f of readdirSync(PNG_OUT)) {
    if (f.endsWith('.png')) unlinkSync(path.join(PNG_OUT, f))
  }

  const { srv, port } = await serve()
  const base = `http://127.0.0.1:${port}`
  const browser = await chromium.launch({ headless: true })
  const results = []

  for (const p of PAGES) {
    const ctx = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      deviceScaleFactor: 2,
      colorScheme: 'dark',
    })
    const flags = { authed: p.authed !== false, operator: !!p.operator }
    // Intercept every API call and answer from the fixture bank.
    await ctx.route('**/api/**', (routeReq) => {
      const url = new URL(routeReq.request().url())
      const { status, body } = resolveApi(url.pathname, flags)
      routeReq.fulfill({
        status,
        contentType: 'application/json',
        body: body == null ? '' : JSON.stringify(body),
      })
    })

    const page = await ctx.newPage()
    page.on('pageerror', () => {})
    const url = `${base}/console${p.route === '/' ? '/' : p.route}`
    try {
      await page.goto(url, { waitUntil: 'networkidle', timeout: 20_000 })
      await page.waitForTimeout(500)
      if (p.prepare) { await p.prepare(page); await page.waitForTimeout(200) }
      const out = path.join(PNG_OUT, `${p.name}.png`)
      await page.screenshot({ path: out, fullPage: true })
      console.log(`  ✓ ${p.name}.png`)
      results.push({ ...p, status: 'ok' })
    } catch (err) {
      console.warn(`  ✗ ${p.name}: ${err.message}`)
      results.push({ ...p, status: 'failed', error: err.message })
    }
    await ctx.close()
  }

  await browser.close()
  srv.close()

  // Gallery index for docs/assets/screenshots.
  const idx = [
    '# docs/assets/screenshots',
    '',
    'Generated by `npm run screenshots` (`scripts/screenshots/capture.mjs`).',
    '',
    'Each image is the **real** React console SPA (`web/dist` — the Vulos',
    'Workspace: auth + user console + operator admin), driven headlessly with',
    'every `/api/**` request answered from a bank of **fabricated** demo data —',
    'no backend is run and no operator data is ever captured. Retina',
    '(1440-wide @2x). The console ships a single deliberate dark instrument-panel',
    'theme. Billing surfaces are absent by design (the OSS build resolves the',
    'commercial seam to its NoOp implementation).',
    '',
    '| Image | Surface |',
    '|-------|---------|',
    ...results.map((r) => `| \`${r.name}.png\` | ${r.title} — ${r.desc} |`),
    '',
    'Regenerate: `npm run screenshots` (after `npm --prefix web run build`).',
  ].join('\n')
  writeFileSync(path.join(PNG_OUT, 'README.md'), idx + '\n')

  const ok = results.filter((r) => r.status === 'ok').length
  console.log(`\nDone — ${ok}/${results.length} captured → ${path.relative(ROOT, PNG_OUT)}/`)
  if (ok !== results.length) process.exit(1)
}

main().catch((e) => { console.error('Fatal:', e); process.exit(1) })
