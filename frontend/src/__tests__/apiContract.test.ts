/**
 * apiContract.test.js — the frontend may only call /api/ routes the box serves.
 *
 * The box answers an unmatched /api/* call with a real JSON 404 (see
 * backend/cmd/server/routes_apifallback.go). Before that terminal handler
 * existed, the SPA catch-all answered them 200 text/html, so a call to a route
 * the box never registered looked like SUCCESS to every client that checks only
 * `res.ok` — and a whole family of dead/lying UI accumulated behind that: a
 * post-signup wizard whose 2FA step hit CP-only routes, per-app resource bars
 * polling a mistyped path, an instance "Remove" button that reported success
 * while the server did nothing.
 *
 * A 404 makes those failures loud at runtime; this suite makes them loud at
 * TEST time. It parses the routes the Go backend actually registers and the
 * literal /api/ paths the frontend actually fetches, and fails on any frontend
 * path no backend route can serve.
 *
 * When this fails, the fix is one of:
 *   • the route name is wrong        → correct the client, or
 *   • the box cannot do this at all  → drop the affordance (do not ship a
 *                                      button that lies), or
 *   • the box SHOULD do this         → register the route.
 */

import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve, relative } from 'node:path'

// The web tier lives in frontend/, so this file is <repo>/frontend/src/__tests__:
// FRONTEND is two up, REPO is three. The backend walk needs the repo root; the
// frontend walk needs frontend/src.
const FRONTEND = resolve(import.meta.dirname, '../..')
const REPO = resolve(import.meta.dirname, '../../..')

const SKIP_DIRS = new Set(['node_modules', 'dist', '.git', 'test-results'])

function walk(dir: string, exts: string[], out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (SKIP_DIRS.has(entry)) continue
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) walk(path, exts, out)
    else if (exts.some(ext => path.endsWith(ext))) out.push(path)
  }
  return out
}

/**
 * backendRoutes parses every /api/ pattern the Go server registers on a mux
 * (`mux.HandleFunc("GET /api/instances/{ulid}/status", …)`). Test files are
 * excluded: a route that exists only in a Go test does not serve a browser.
 *
 * The terminal "/api/" fallback is excluded too — it is the 404/405 handler
 * itself, and treating it as a route would match every path and defeat the
 * whole check.
 */
function backendRoutes(): string[] {
  const routes = new Set<string>()
  for (const file of walk(join(REPO, 'backend'), ['.go'])) {
    if (file.endsWith('_test.go')) continue
    const src = readFileSync(file, 'utf8')
    const re = /(?:HandleFunc|Handle)\(\s*"((?:[A-Z]+ )?\/api\/[^"]*)"/g
    for (const m of src.matchAll(re)) {
      const pattern = m[1]
      const path = pattern.includes(' ') ? pattern.split(' ')[1] : pattern
      if (path === '/api/') continue
      routes.add(path)
    }
  }
  return [...routes]
}

/**
 * frontendPaths collects the literal /api/ paths the shipped frontend fetches,
 * mapped to the files that call them. Template placeholders (`${id}`) collapse
 * to a `{}` wildcard so they can be matched against the backend's `{ulid}`.
 *
 * Two shapes are not concrete call paths and are skipped: the bare "/api/"
 * prefix (used by offlineQueue's `path.startsWith('/api/')` guard) and prose
 * containing an ellipsis (`/api/...` in a doc comment).
 */
function frontendPaths(): Map<string, Set<string>> {
  const calls = new Map<string, Set<string>>()
  for (const file of walk(join(FRONTEND, 'src'), ['.js', '.jsx', '.ts', '.tsx'])) {
    if (file.includes('__tests__') || /\.test\.jsx?$/.test(file)) continue
    const src = readFileSync(file, 'utf8')
    for (const m of src.matchAll(/['"`](\/api\/[^'"`\s]*)['"`]/g)) {
      const raw = m[1]
      if (raw === '/api/' || raw.includes('...')) continue
      const path = raw
        .replace(/\$\{[^}]*\}/g, '{}') // `${id}` → single-segment wildcard
        .replace(/\?.*$/, '') // drop query string
        .replace(/\/+$/, '') // drop trailing slash
      let files = calls.get(path)
      if (!files) {
        files = new Set<string>()
        calls.set(path, files)
      }
      files.add(relative(REPO, file))
    }
  }
  return calls
}

/**
 * served reports whether any backend route can serve path. A backend `{param}`
 * segment absorbs any single segment; a frontend `{}` wildcard is NOT allowed
 * to absorb a literal backend segment (that laxity would let a call to
 * `/api/instances/${id}` be "served" by `POST /api/instances/provision`).
 */
function served(path: string, routes: string[]): boolean {
  const want = path.split('/')
  return routes.some(route => {
    if (route.endsWith('/')) {
      // Go prefix pattern: serves everything below it.
      return path.startsWith(route)
    }
    const have = route.split('/')
    if (have.length !== want.length) return false
    return have.every((seg, i) => seg.startsWith('{') || seg === want[i])
  })
}

/**
 * Paths the frontend deliberately PROBES rather than calls — a capability the
 * box may or may not expose, where absence is a designed, handled outcome
 * rather than a broken affordance.
 *
 * This is deliberately not a general escape hatch, and the test below is built
 * so it cannot become one. Two rules keep it honest:
 *
 *   1. A declaration must carry a reason naming where the design is recorded.
 *   2. A declared path must be GENUINELY UNSERVED. The moment the box registers
 *      it, this entry is stale and the suite fails until it is deleted — so the
 *      list cannot quietly outlive the gap it documents.
 *
 * The distinction being drawn is real: "calls a route the box does not serve"
 * is a lying button, while "asks whether the box can do X, and does nothing
 * when it cannot" is how an optional capability has to be written. Collapsing
 * the two would either forbid capability probes or excuse dead affordances.
 */
const OPTIONAL_CAPABILITIES: Record<string, string> = {
  '/api/widgets/fetch':
    'Widget network broker. widgetNet() returns null when the box does not ' +
    'expose it and never falls back to a direct browser call, so no box today ' +
    'reaches a third party. See roadmap/WIDGETS.md § "Stocks and the network".',
}

describe('frontend → box API contract', () => {
  it('parses a plausible number of routes from both sides', () => {
    // Guards the parser itself: a regex that silently stops matching would make
    // every assertion below vacuously pass.
    expect(backendRoutes().length).toBeGreaterThan(100)
    expect(frontendPaths().size).toBeGreaterThan(50)
  })

  it('calls no /api/ route the box does not register', () => {
    const routes = backendRoutes()
    const missing = []
    for (const [path, files] of frontendPaths()) {
      if (path in OPTIONAL_CAPABILITIES) continue
      if (!served(path, routes)) missing.push(`${path}  ← ${[...files].join(', ')}`)
    }
    expect(missing.sort()).toEqual([])
  })

  // The two guards that stop OPTIONAL_CAPABILITIES becoming a place to hide a
  // missing route. Without them the list above would weaken the suite it sits
  // in — which is this repo's single most common defect shape.
  it('every optional capability carries a reason', () => {
    for (const [path, reason] of Object.entries(OPTIONAL_CAPABILITIES)) {
      expect(reason.length, `${path} needs a reason naming where it is designed`)
        .toBeGreaterThan(40)
    }
  })

  it('no optional capability is actually served — a shipped one must be deleted from the list', () => {
    const routes = backendRoutes()
    const stale = Object.keys(OPTIONAL_CAPABILITIES).filter(p => served(p, routes))
    expect(
      stale.sort(),
      'the box now registers these, so they are ordinary routes: delete the OPTIONAL_CAPABILITIES entries',
    ).toEqual([])
  })
})
