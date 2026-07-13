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

const REPO = resolve(import.meta.dirname, '../..')

const SKIP_DIRS = new Set(['node_modules', 'dist', '.git', 'test-results'])

function walk(dir, exts, out = []) {
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
function backendRoutes() {
  const routes = new Set()
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
function frontendPaths() {
  const calls = new Map()
  for (const file of walk(join(REPO, 'src'), ['.js', '.jsx'])) {
    if (file.includes('__tests__') || /\.test\.jsx?$/.test(file)) continue
    const src = readFileSync(file, 'utf8')
    for (const m of src.matchAll(/['"`](\/api\/[^'"`\s]*)['"`]/g)) {
      const raw = m[1]
      if (raw === '/api/' || raw.includes('...')) continue
      const path = raw
        .replace(/\$\{[^}]*\}/g, '{}') // `${id}` → single-segment wildcard
        .replace(/\?.*$/, '') // drop query string
        .replace(/\/+$/, '') // drop trailing slash
      if (!calls.has(path)) calls.set(path, new Set())
      calls.get(path).add(relative(REPO, file))
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
function served(path, routes) {
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
      if (!served(path, routes)) missing.push(`${path}  ← ${[...files].join(', ')}`)
    }
    expect(missing.sort()).toEqual([])
  })
})
