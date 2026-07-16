#!/usr/bin/env node
/**
 * vulos-management — admin-console screenshotter
 *
 * Captures the OSS control plane's server-rendered operator console into
 * docs/assets/screenshots/ for the README gallery.
 *
 * How it works (fully repeatable, no real data ever touched):
 *
 *   1. Runs the Go dump harness
 *        SCREENSHOT_DUMP=1 SCREENSHOT_OUT=<tmp> go test ./pkg/superadmin ./pkg/security -run ScreenshotDump
 *      which renders the REAL console templates + admin.css this repo ships —
 *      the exact same NewPages handlers cmd/server mounts — against in-memory
 *      SQLite seeded with fabricated demo accounts/orgs/usage. Nothing on disk,
 *      no network, no operator data.
 *   2. Serves that HTML directory over a throwaway localhost static server.
 *   3. Drives headless Chromium (Playwright) to shoot each page at retina.
 *
 * The whole console — including the security and DDoS dashboards — ships a
 * single deliberate dark instrument-panel theme (admin.css is dark-only) and does
 * not theme dynamically, so each page is captured in its one canonical look.
 *
 * Usage:
 *   npm install && npx playwright install chromium   # first time
 *   npm run screenshots                              # or: node scripts/screenshots/capture.mjs
 *
 * Requires Go on PATH (to render the pages) and Chromium (via Playwright).
 */

import { chromium } from 'playwright'
import { createServer } from 'node:http'
import { readFileSync, existsSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const ROOT = path.resolve(__dirname, '..', '..')
const PNG_OUT = path.join(ROOT, 'docs', 'assets', 'screenshots')

// ── Page catalogue: order, theme (native — not a toggle) and captions ──────────
const PAGES = [
  { name: 'dashboard',        theme: 'dark',  title: 'Operator console',     desc: 'The central cockpit — fleet health beacon, account / super-admin counts, open-incident state and the live hash-chained audit feed, with jump cards to every section.' },
  { name: 'accounts',         theme: 'dark',  title: 'Accounts',             desc: 'Account search + admin — suspend, force-reset and reset 2FA, with every action audit-logged.' },
  { name: 'account-detail',   theme: 'dark',  title: 'Account detail',       desc: 'A single account: verification / TOTP / session state, entitlements and recorded usage.' },
  { name: 'analytics',        theme: 'dark',  title: 'Analytics',            desc: 'Users, DAU / MAU and per-product 7-day usage trends drawn as inline SVG sparklines. Usage signals only.' },
  { name: 'orgs',             theme: 'dark',  title: 'Organisations',        desc: 'Organisation directory — members, seats, tier and suspension state.' },
  { name: 'org-detail',       theme: 'dark',  title: 'Org detail',           desc: 'One organisation: member roster, roles and a usage summary.' },
  { name: 'relay',            theme: 'dark',  title: 'Relay health',         desc: 'Relay data-plane health — per-region PoP status and throughput. Operational signals only.' },
  { name: 'incidents',        theme: 'dark',  title: 'Incidents',            desc: 'Status-page incidents and scheduled maintenance windows.' },
  { name: 'migrations',       theme: 'dark',  title: 'Migrations',           desc: 'Aggregate schema state polled across every product in the suite.' },
  { name: 'auditlog',         theme: 'dark',  title: 'Audit log',            desc: 'The tamper-evident, hash-chained audit log with actor / action filters.' },
  { name: 'reserved-handles', theme: 'dark',  title: 'Reserved handles',     desc: 'Reserved-username registry (postmaster, abuse, brand handles …).' },
  { name: 'maintenance',      theme: 'dark',  title: 'Maintenance',          desc: 'Operator tools — verify the audit chain, check secret rotation.' },
  { name: 'login',            theme: 'dark',  title: 'Operator login',       desc: 'The console sign-in: password + TOTP, then a WebAuthn hardware-key step-up.' },
  { name: 'security-dashboard', theme: 'dark', title: 'Security dashboard', file: 'security-dashboard.html', desc: 'WAF hits, bot scores, step-up / ATO events, CT-log certs, egress anomalies and honeypot hits.' },
]

const fileFor = (p) => p.file ?? `admin-${p.name}.html`

// ── Step 1: render the console HTML via the Go dump harness ────────────────────
function dumpHTML() {
  const htmlDir = mkdtempSync(path.join(tmpdir(), 'vulos-mgmt-ss-'))
  console.log('  rendering console templates via Go dump harness …')
  execFileSync(
    'go',
    ['test', './pkg/superadmin', './pkg/security', '-run', 'ScreenshotDump', '-count=1'],
    { cwd: ROOT, stdio: 'inherit', env: { ...process.env, SCREENSHOT_DUMP: '1', SCREENSHOT_OUT: htmlDir } },
  )
  return htmlDir
}

// ── Step 2: minimal static file server over the dumped HTML dir ────────────────
const MIME = { '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8', '.js': 'text/javascript; charset=utf-8' }
function serve(dir) {
  const srv = createServer((req, res) => {
    const rel = decodeURIComponent(req.url.split('?')[0]).replace(/^\/+/, '')
    const full = path.join(dir, rel)
    if (!full.startsWith(dir) || !existsSync(full)) { res.statusCode = 404; res.end('not found'); return }
    res.setHeader('Content-Type', MIME[path.extname(full)] ?? 'application/octet-stream')
    res.end(readFileSync(full))
  })
  return new Promise((resolve) => srv.listen(0, '127.0.0.1', () => resolve({ srv, port: srv.address().port })))
}

// ── Step 3: capture ────────────────────────────────────────────────────────────
async function main() {
  mkdirSync(PNG_OUT, { recursive: true })
  const htmlDir = dumpHTML()
  const { srv, port } = await serve(htmlDir)
  const base = `http://127.0.0.1:${port}`

  const browser = await chromium.launch({ headless: true })
  const results = []
  for (const p of PAGES) {
    const ctx = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      deviceScaleFactor: 2,
      colorScheme: p.theme,
    })
    const page = await ctx.newPage()
    page.on('pageerror', () => {})
    const url = `${base}/${fileFor(p)}`
    try {
      await page.goto(url, { waitUntil: 'networkidle', timeout: 15_000 })
      await page.waitForTimeout(250)
      const out = path.join(PNG_OUT, `${p.name}.png`)
      await page.screenshot({ path: out, fullPage: true })
      console.log(`  ✓ ${p.name}.png  (${p.theme})`)
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
    'Each image is the **real** server-rendered console page (the same',
    '`pkg/superadmin` / `pkg/security` templates + CSS the control plane serves),',
    'seeded with fabricated demo data by the Go dump harness — no operator data is',
    'ever captured. Retina (1440-wide @2x). The whole console — including the',
    'security dashboard — ships a single deliberate dark instrument-panel theme.',
    '',
    '| Image | Surface |',
    '|-------|---------|',
    ...results.map((r) => `| \`${r.name}.png\` | ${r.title} — ${r.desc} |`),
    '',
    'Regenerate: `npm run screenshots`',
  ].join('\n')
  writeFileSync(path.join(PNG_OUT, 'README.md'), idx + '\n')

  const ok = results.filter((r) => r.status === 'ok').length
  console.log(`\nDone — ${ok}/${results.length} captured → ${path.relative(ROOT, PNG_OUT)}/`)
  if (ok !== results.length) process.exit(1)
}

main().catch((e) => { console.error('Fatal:', e); process.exit(1) })
