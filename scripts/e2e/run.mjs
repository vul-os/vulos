#!/usr/bin/env node
/**
 * vulos-management — console E2E (Playwright, headless Chromium)
 *
 * Drives the REAL built console SPA (web/dist) end-to-end in a browser against a
 * stateful in-memory mock of the management JSON API. No Go backend is run; the
 * management binary's role-separation + last-admin guard are proven exhaustively
 * by the Go tests (pkg/superadmin, pkg/cpserver, test/consoleboot). THIS test
 * proves the browser-facing half:
 *
 *   1. SIGN-IN — the self-host login page renders and a submit authenticates
 *      (POST /api/auth/login), landing on the authed console.
 *   2. ROLE SEPARATION (UI) — a signed-in PORTAL USER (whoami → 403) sees NO
 *      Operator nav group and its user surfaces render; an OPERATOR (whoami → 200)
 *      sees the Operator group and its admin surfaces render.
 *   3. ADMIN TEAM — grant admin by email, revoke it, and the last-admin guard
 *      (both the UI hiding the final Revoke and the server 409 error surfacing).
 *
 * Every scenario asserts ZERO uncaught page errors.
 *
 * Usage:
 *   npm --prefix web run build                       # build web/dist first
 *   npm --prefix scripts/e2e install                 # first time (+ chromium)
 *   npm --prefix scripts/e2e run e2e
 */

import { chromium } from 'playwright'
import { createServer } from 'node:http'
import { readFileSync, existsSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const ROOT = path.resolve(__dirname, '..', '..')
const DIST = path.join(ROOT, 'web', 'dist')

if (!existsSync(path.join(DIST, 'index.html'))) {
  console.error(`\n  web/dist is not built. Run:  npm --prefix web run build\n`)
  process.exit(1)
}

// ── tiny assert harness ─────────────────────────────────────────────────────────
let passed = 0
const failures = []
function check(cond, msg) {
  if (cond) { passed++; return }
  failures.push(msg)
  console.error(`  ✗ ${msg}`)
}
function ok(msg) { passed++; console.log(`  ✓ ${msg}`) }

// ── static server for the built SPA (SPA fallback under /console/) ──────────────
const MIME = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8', '.svg': 'image/svg+xml', '.woff2': 'font/woff2',
  '.woff': 'font/woff', '.json': 'application/json', '.png': 'image/png', '.ico': 'image/x-icon',
}
function serve() {
  const indexHtml = readFileSync(path.join(DIST, 'index.html'))
  const srv = createServer((req, res) => {
    let rel = decodeURIComponent(req.url.split('?')[0])
    if (rel.startsWith('/console/')) rel = rel.slice('/console'.length)
    if (rel === '/' || rel === '') { res.setHeader('Content-Type', MIME['.html']); res.end(indexHtml); return }
    const full = path.join(DIST, rel.replace(/^\/+/, ''))
    if (full.startsWith(DIST) && existsSync(full) && statSync(full).isFile()) {
      res.setHeader('Content-Type', MIME[path.extname(full)] ?? 'application/octet-stream')
      res.end(readFileSync(full))
      return
    }
    res.setHeader('Content-Type', MIME['.html'])
    res.end(indexHtml)
  })
  return new Promise((resolve) => srv.listen(0, '127.0.0.1', () => resolve({ srv, port: srv.address().port })))
}

// ── stateful mock backend ───────────────────────────────────────────────────────
const USER = { id: '01HQ9Z7VULOSACCT0DEMO001', email: 'ada@example.org', email_verified: true }
const ago = (m) => new Date(Date.now() - m * 60000).toISOString()

/** newState builds one scenario's mutable API state. */
function newState({ authed = false, operator = false, admins, revokeAlways409 = false } = {}) {
  return {
    authed,
    operator,
    revokeAlways409,
    admins: admins ?? [
      { account_id: 'acc-root', email: 'root@example.org', promoted_at: ago(4000), promoted_by: '', promoted_by_email: '', bootstrap: true },
    ],
  }
}

/** handle resolves one intercepted /api/** request against the scenario state. */
async function handle(route, st) {
  const request = route.request()
  const url = new URL(request.url())
  const p = url.pathname
  const method = request.method()
  const J = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
  const empty = (status) => route.fulfill({ status, contentType: 'application/json', body: '' })

  // ── auth ──
  if (p === '/api/auth/me') return st.authed ? J(USER) : empty(401)
  if (p === '/api/auth/login' && method === 'POST') { st.authed = true; return J({ user: USER }) }
  if (p === '/api/auth/logout') { st.authed = false; return empty(204) }

  // ── operator probe (drives whether the Operator nav group shows) ──
  if (p === '/api/superadmin/whoami') return st.operator ? J({ ok: true, account_id: USER.id }) : empty(403)

  // ── admin team API ──
  if (p === '/api/superadmin/admins' && method === 'GET') {
    if (!st.operator) return empty(403)
    return J({ admins: st.admins, count: st.admins.length })
  }
  if (p === '/api/superadmin/admins' && method === 'POST') {
    if (!st.operator) return empty(403)
    let body = {}
    try { body = JSON.parse(request.postData() || '{}') } catch { /* ignore */ }
    const email = String(body.email || '').toLowerCase().trim()
    if (email === 'ghost@nowhere.example') return J({ error: 'no account found for that email — the person must sign up first' }, 422)
    const row = { account_id: 'acc-' + email, email, promoted_at: ago(0), promoted_by: 'acc-root', promoted_by_email: 'root@example.org', bootstrap: false }
    if (!st.admins.some((a) => a.email === email)) st.admins.push(row)
    return J(row, 201)
  }
  if (p.startsWith('/api/superadmin/admins/') && method === 'DELETE') {
    if (!st.operator) return empty(403)
    if (st.revokeAlways409 || st.admins.length <= 1) {
      return J({ error: 'cannot revoke the last remaining admin' }, 409)
    }
    const id = decodeURIComponent(p.slice('/api/superadmin/admins/'.length))
    st.admins = st.admins.filter((a) => a.account_id !== id)
    return empty(204)
  }

  // ── operator read surfaces ──
  if (p === '/api/superadmin/dashboard') return st.operator ? J({ account_count: 3, superadmin_count: st.admins.length, recent_audit: [] }) : empty(403)
  if (p === '/api/superadmin/accounts') return st.operator ? J({ accounts: [] }) : empty(403)
  if (p === '/api/superadmin/audit') return st.operator ? J({ rows: [], count: 0 }) : empty(403)
  if (p === '/api/superadmin/security') return st.operator ? J({ waf: [], bots: [], stepups: [], ato: [], honeypot: [], egress: [], ct: [] }) : empty(403)

  // ── portal-user read surfaces ──
  if (p === '/api/fleet/devices') return J({ items: [] })
  if (p === '/api/account/status') return J({ overall: 'operational', relay: {}, boxes: [], services: [], events: [] })
  if (p === '/api/org/audit') return J({ entries: [], count: 0, limit: 50, next_cursor: '' })
  if (p === '/api/developer/keys') return J([])
  if (p === '/api/developer/webhooks') return J([])
  if (p === '/api/developer/mcp-servers') return J({ servers: [] })
  if (p === '/api/compliance/requests') return J({ requests: [] })
  if (p === '/api/support/status') return J({ has_data: false, signals: [], alerts: [] })

  // benign default keeps the SPA from erroring on any other call
  return J({})
}

// ── scenario driver ─────────────────────────────────────────────────────────────
async function withContext(browser, base, st, fn) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' })
  await ctx.route('**/api/**', (route) => handle(route, st))
  const page = await ctx.newPage()
  const pageErrors = []
  page.on('pageerror', (e) => pageErrors.push(e.message))
  page.on('dialog', (d) => d.accept()) // auto-accept the revoke confirm()
  try {
    await fn(page, base)
  } finally {
    check(pageErrors.length === 0, `zero uncaught page errors (got ${pageErrors.length}: ${pageErrors.join(' | ')})`)
    await ctx.close()
  }
}

const url = (base, route) => `${base}/console${route === '/' ? '/' : route}`

async function main() {
  const { srv, port } = await serve()
  const base = `http://127.0.0.1:${port}`
  const browser = await chromium.launch({ headless: true })

  // ── Scenario 1: SIGN-IN ────────────────────────────────────────────────────
  console.log('\n[1] sign-in')
  await withContext(browser, base, newState({ authed: false }), async (page) => {
    await page.goto(url(base, '/login'), { waitUntil: 'networkidle', timeout: 30000 })
    const emailField = page.locator('#login-email')
    check(await emailField.count() > 0, 'login page renders the email field')
    check(await page.locator('#login-password').count() > 0, 'login page renders the password field')
    await emailField.fill('ada@example.org')
    await page.locator('#login-password').fill('correct-horse-battery-staple-9')
    const loginPost = page.waitForRequest((r) => r.url().includes('/api/auth/login') && r.method() === 'POST', { timeout: 15000 })
    await page.locator('button[type="submit"]').first().click()
    await loginPost
    ok('submitting the form issues POST /api/auth/login')
    // Land on the authed console (URL leaves /login).
    await page.waitForFunction(() => !location.pathname.endsWith('/login'), null, { timeout: 15000 }).catch(() => {})
    check(!page.url().endsWith('/login'), 'after sign-in the app navigates off /login into the console')
  })

  // ── Scenario 2: PORTAL USER (non-operator) — no Operator nav, surfaces render ─
  console.log('\n[2] portal user (role separation in the UI)')
  await withContext(browser, base, newState({ authed: true, operator: false }), async (page) => {
    await page.goto(url(base, '/'), { waitUntil: 'networkidle', timeout: 30000 })
    await page.waitForTimeout(600)
    const bodyText = await page.locator('body').innerText()
    check(!/Operator/i.test(bodyText), 'a portal user sees NO "Operator" nav group')
    // Deep-linking to an admin route must not surface operator data — the admin
    // gate view ("operator access required") is shown instead.
    await page.goto(url(base, '/admin/admins'), { waitUntil: 'networkidle', timeout: 30000 })
    await page.waitForTimeout(600)
    const adminText = (await page.locator('body').innerText()).toLowerCase()
    check(/operator access required|access required|not.*operator/.test(adminText),
      'a portal user deep-linking to /admin/admins gets the access-required gate, not admin data')
    // A user surface renders.
    await page.goto(url(base, '/devices'), { waitUntil: 'networkidle', timeout: 30000 })
    await page.waitForTimeout(400)
    check((await page.locator('body').innerText()).length > 0, 'the Devices user surface renders')
  })

  // ── Scenario 3: OPERATOR — Operator nav present, admin surfaces render ───────
  console.log('\n[3] operator surfaces')
  await withContext(browser, base, newState({ authed: true, operator: true }), async (page) => {
    await page.goto(url(base, '/'), { waitUntil: 'networkidle', timeout: 30000 })
    await page.waitForTimeout(700)
    const navText = await page.locator('body').innerText()
    check(/Operator/i.test(navText), 'an operator sees the "Operator" nav group')
    for (const [route, label] of [['/admin', 'dashboard'], ['/admin/audit', 'audit'], ['/admin/security', 'security']]) {
      await page.goto(url(base, route), { waitUntil: 'networkidle', timeout: 30000 })
      await page.waitForTimeout(400)
      check((await page.locator('body').innerText()).length > 20, `operator ${label} surface renders`)
    }
  })

  // ── Scenario 4: ADMIN TEAM — grant, revoke, last-admin guard ─────────────────
  console.log('\n[4] admin team: grant / revoke / last-admin guard')
  await withContext(browser, base, newState({ authed: true, operator: true }), async (page) => {
    await page.goto(url(base, '/admin/admins'), { waitUntil: 'networkidle', timeout: 30000 })
    await page.waitForTimeout(600)

    // Start: exactly one (bootstrap) admin → the last-admin guard hides Revoke.
    check((await page.locator('text=last admin').count()) > 0, 'with one admin the "last admin" guard label is shown (Revoke hidden)')
    check((await page.getByRole('button', { name: 'Revoke' }).count()) === 0, 'no Revoke button when only one admin remains')

    // Grant a new admin by email.
    await page.locator('#grant-email').fill('teammate@example.org')
    await page.getByRole('button', { name: /Grant admin/i }).click()
    await page.waitForTimeout(700)
    // Assert on the LIST ROW (not whole-body text — the success notice banner also
    // mentions the email, which would pollute a body-level check).
    const teammateRow = () => page.locator('.op-trow', { hasText: 'teammate@example.org' })
    check((await teammateRow().count()) > 0, 'granted admin appears in the list after grant')
    check((await page.getByRole('button', { name: 'Revoke' }).count()) > 0, 'Revoke buttons appear once more than one admin exists')

    // Revoke the granted admin — target ITS row, not the first (bootstrap) one.
    await teammateRow().getByRole('button', { name: 'Revoke' }).click()
    await page.waitForTimeout(700)
    check((await teammateRow().count()) === 0, 'revoked admin is gone from the list after revoke')
    check((await page.locator('text=last admin').count()) > 0, 'back to the last-admin guard after revoking down to one')
  })

  // ── Scenario 4b: server-side last-admin 409 surfaces as an honest error ──────
  console.log('\n[4b] last-admin server guard (409) surfaces in the UI')
  await withContext(browser, base, newState({
    authed: true, operator: true, revokeAlways409: true,
    admins: [
      { account_id: 'acc-root', email: 'root@example.org', promoted_at: ago(4000), promoted_by: '', promoted_by_email: '', bootstrap: true },
      { account_id: 'acc-two', email: 'two@example.org', promoted_at: ago(10), promoted_by: 'acc-root', promoted_by_email: 'root@example.org', bootstrap: false },
    ],
  }), async (page) => {
    await page.goto(url(base, '/admin/admins'), { waitUntil: 'networkidle', timeout: 30000 })
    await page.waitForTimeout(600)
    await page.getByRole('button', { name: 'Revoke' }).first().click()
    await page.waitForTimeout(700)
    const txt = (await page.locator('body').innerText()).toLowerCase()
    check(/last remaining admin/.test(txt), 'a server 409 shows the "cannot revoke the last remaining admin" message')
  })

  await browser.close()
  srv.close()

  console.log(`\n${failures.length === 0 ? 'PASS' : 'FAIL'} — ${passed} checks passed, ${failures.length} failed`)
  if (failures.length) { for (const f of failures) console.error(`  - ${f}`); process.exit(1) }
}

main().catch((e) => { console.error('Fatal:', e); process.exit(1) })
