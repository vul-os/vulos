import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { execSync } from 'node:child_process'
import { resolve } from 'node:path'

// Every API the screenshot harness mocks must exist in the real server.
//
// # Why
//
// The screenshots are published in docs/ and on the marketing site, so they are
// a claim about what the software does. A mock is a fixture, and a fixture can
// describe an endpoint that was never built — at which point the picture shows a
// UI fed by an API nobody can call.
//
// That is not hypothetical here. The terminal shot ran `vulos status` and
// `vulos apps ls --published` for months; neither command has ever existed, and
// the comment above it called the session honest. This checks the same class of
// claim on the HTTP side, where it is checkable.
//
// # How it matches, and why that took three attempts
//
// Routes register as `mux.HandleFunc("GET /api/thing", …)` — the method is
// INSIDE the string. Grepping for `"/api/thing` finds nothing, which reported
// ten real endpoints as missing. Path parameters are a second trap: the harness
// mocks a concrete `/api/apps/lilmail/domain`, and the server declares
// `/api/apps/{id}/domain`.
//
// So the matcher below is validated against a route known to exist and a route
// known not to, before it is trusted with anything. A detector that has not been
// shown to fail is not evidence.

const HARNESS = resolve(__dirname, '../../scripts/screenshots.mjs')
const BACKEND = resolve(__dirname, '../../../backend')

/** Read every Go source file under the backend, once. */
function backendSource(): string {
  // `grep -rh` over .go is far cheaper than walking the tree in JS, and this
  // only needs the registration lines.
  // Two registration shapes exist and both serve real traffic:
  //   mux.HandleFunc("GET /api/thing", …)   — method + exact path
  //   mux.HandleFunc("/api/pim/", …)        — no method, SUBTREE (Go ServeMux
  //                                            treats a trailing slash as a
  //                                            prefix), used by the proxies
  // Reading only the first shape called the whole /api/pim/* surface fake.
  return execSync(
    `grep -rhoE '"((GET|POST|PUT|DELETE|PATCH) )?/api/[^"]*"' --include=*.go . || true`,
    { cwd: BACKEND, encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 },
  )
}

/** The concrete routes the screenshot harness serves. */
function mockedRoutes(): string[] {
  const src = readFileSync(HARNESS, 'utf8')
  const found = src.match(/'(?:GET|POST|PUT|DELETE|PATCH) \/api\/[^']*'/g) ?? []
  return [...new Set(found.map((s) => s.replace(/'/g, '')))]
}

/**
 * Does the server declare a route matching this concrete path?
 *
 * A declared `{param}` segment matches any single concrete segment, which is
 * what lets `/api/apps/lilmail/domain` match `/api/apps/{id}/domain`.
 */
function serverDeclares(route: string, declarations: string): boolean {
  const [method, path] = route.split(' ')
  const segs = path.split('/')
  for (const decl of declarations.split('\n')) {
    const m = decl.replace(/"/g, '').trim()
    if (!m) continue

    // The bare "/api/" registration is the FALLBACK — routes_apifallback.go,
    // which exists to answer 404/405 for paths nothing else claimed. Counting
    // it as a declaration makes every conceivable /api path "exist" and this
    // whole check vacuous. It is the one subtree that must not match.
    if (m === '/api/') continue
    const hasMethod = /^(GET|POST|PUT|DELETE|PATCH) /.test(m)
    const dMethod = hasMethod ? m.split(' ')[0] : null
    const dPath = hasMethod ? m.split(' ')[1] : m
    // A method-less registration answers every method, as ServeMux does.
    if (dMethod && dMethod !== method) continue

    // A trailing slash is a SUBTREE match, not an exact one.
    if (dPath.endsWith('/') && path.startsWith(dPath)) return true

    const dSegs = dPath.split('/')
    if (dSegs.length !== segs.length) continue
    if (dSegs.every((d, i) => d === segs[i] || /^\{.*\}$/.test(d))) return true
  }
  return false
}

describe('screenshot harness mocks only real endpoints', () => {
  const declarations = backendSource()

  it('the matcher itself works', () => {
    // Validated before it is trusted. The first two versions of this check
    // reported ten live endpoints as missing, because they assumed a string
    // shape the server does not use.
    expect(
      declarations.length,
      'no route declarations were read from the backend at all — the matcher would then call every mock fake',
    ).toBeGreaterThan(500)

    expect(serverDeclares('GET /api/relayconfig', declarations)).toBe(true)
    expect(serverDeclares('GET /api/apps/lilmail/domain', declarations)).toBe(true) // {id} route
    expect(serverDeclares('GET /api/definitely/not/real', declarations)).toBe(false)
    expect(serverDeclares('DELETE /api/relayconfig', declarations)).toBe(false) // right path, wrong method
    // Subtree proxy: /api/pim/ is registered with a trailing slash and no
    // method, and serves everything beneath it. Missing this called the entire
    // mail/calendar/contacts surface fabricated.
    expect(serverDeclares('GET /api/pim/contacts/cards', declarations)).toBe(true)
  })

  it('every mocked route exists in the server', () => {
    const routes = mockedRoutes()

    // A harness that stopped mocking anything would make this pass by finding
    // nothing to check.
    expect(routes.length, 'no mocked routes were found in screenshots.mjs').toBeGreaterThan(10)

    const missing = routes.filter((r) => !serverDeclares(r, declarations))
    expect(
      missing,
      `the screenshots depict a UI fed by endpoints the server does not have:\n${missing.join('\n')}`,
    ).toEqual([])
  })
})
