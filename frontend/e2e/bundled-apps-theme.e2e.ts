// Does a bundled app actually render in the theme the shell is in?
//
// WHY THIS TEST EXISTS
// --------------------
// Measured before this change: zero of the fifteen bundled apps carried a
// data-theme rule or a prefers-color-scheme query, and apps/_shared/
// vulos-tokens.css — which claimed in its own header to mirror src/index.css —
// had zero theme rules and one hardcoded dark palette. An app was dark no
// matter what the shell was.
//
// The Go guard (backend/internal/docsref/appthemetokens_test.go) proves the
// STRUCTURE: the resolver and the tokens are present in every app, identical to
// their source, correctly ordered, and complete in both themes. Structure is
// not colour. Only a browser can say what a rule actually computes to, and the
// specific way this can be structurally perfect and still wrong is ordinary
// CSS: some later rule out-specifies the tokens, or the attribute is stamped
// after first paint. So this measures getComputedStyle on the real file.
//
// THE SUBJECT
// -----------
// apps/browser/index.html, loaded off disk exactly as shipped. It was chosen
// because it is the one bundled app that already resolves every colour through
// the shared tokens — zero local palette, one stray hex in the whole file — so
// what it proves is the token pipeline itself rather than one app's CSS. The
// other fourteen still carry local palettes that (by the ordering the guard
// pins) deliberately win; converting them is per-app design work and is
// reported as a backlog, not asserted here.
//
// WHAT REMAINS UNPROVEN HERE
// --------------------------
// That the shell SENDS the theme. AppBridge.appFrameSrc() appends `__vulos_s`
// to the frame URL but does not yet append `__vulos_t`; that one-line change is
// shell-owned. This spec supplies the fragment itself, so it proves the
// receiving half works and that the sender has a well-defined contract to meet.
//
// NO SHARED PORT: binds an ephemeral port of its own, never the suite's
// preview server or baseURL.
//
// SHOWN CAPABLE OF FAILING (chromium, 7/7 green clean):
//   - delete the [data-theme="light"] block from vulos-tokens.css and re-sync →
//     "an explicit Light ... BEATS the dark system preference" and the live
//     switch both fail. Structure alone would not have caught it: the Go guard
//     only sees the source file, which is why this one measures computed style.
//   - drop the value validation in vulos-theme.js (stamp whatever arrives) →
//     "a junk theme value is ignored rather than stamped" fails.

import { test, expect, type Page } from '@playwright/test'
import http from 'node:http'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const APP_DIR = path.join(HERE, '..', 'apps', 'browser')

// The two --bg-base values in apps/_shared/vulos-tokens.css, as the browser
// serialises them. If the palette moves these move with it and the test fails
// loudly rather than silently measuring nothing.
const DARK_BG = 'rgb(8, 9, 12)' // #08090c
const LIGHT_BG = 'rgb(255, 255, 255)' // #ffffff
const DARK_TEXT = 'rgb(242, 244, 248)' // #f2f4f8
const LIGHT_TEXT = 'rgb(18, 20, 26)' // #12141a

let server: http.Server
let port = 0

test.beforeAll(async () => {
  const index = fs.readFileSync(path.join(APP_DIR, 'index.html'), 'utf8')
  // Guard the guard: if the app ever loses the shared blocks this spec would
  // otherwise measure a page that simply has no tokens and report nothing.
  for (const must of ['data-vulos-shared="vulos-theme.js"', 'data-vulos-shared="vulos-tokens.css"']) {
    if (!index.includes(must)) throw new Error(`apps/browser/index.html is missing ${must}`)
  }
  server = http.createServer((req, res) => {
    const rel = (req.url || '/').split(/[?#]/)[0]
    const file = rel === '/' ? 'index.html' : rel.replace(/^\/+/, '')
    // Containment: never serve outside the app directory.
    const abs = path.resolve(APP_DIR, file)
    if (!abs.startsWith(path.resolve(APP_DIR) + path.sep)) {
      res.writeHead(403).end()
      return
    }
    fs.readFile(abs, (err, buf) => {
      if (err) {
        res.writeHead(404).end()
        return
      }
      const type = abs.endsWith('.svg') ? 'image/svg+xml' : 'text/html; charset=utf-8'
      res.writeHead(200, { 'content-type': type })
      res.end(buf)
    })
  })
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', r))
  port = (server.address() as { port: number }).port
})

test.afterAll(async () => {
  await new Promise<void>((r) => server.close(() => r()))
})

async function paint(page: Page) {
  return page.evaluate(() => {
    const cs = getComputedStyle(document.body)
    return {
      attr: document.documentElement.getAttribute('data-theme'),
      bg: cs.backgroundColor,
      fg: cs.color,
    }
  })
}

test.describe('system prefers dark', () => {
  test.use({ colorScheme: 'dark' })

  test('with no theme from the shell, the app follows the system', async ({ page }) => {
    await page.goto(`http://127.0.0.1:${port}/`)
    const p = await paint(page)
    // The attribute's ABSENCE is the meaningful state: it is what lets the
    // prefers-color-scheme layer govern. Stamping a guess would disable it.
    expect(p.attr, 'nothing told the app a theme, so nothing should be stamped').toBeNull()
    expect(p.bg).toBe(DARK_BG)
    expect(p.fg).toBe(DARK_TEXT)
  })

  test('an explicit Light from the shell BEATS the dark system preference', async ({ page }) => {
    // This is the whole point. ThemeProvider has four modes and three of them
    // can disagree with the system; an app that could only read
    // prefers-color-scheme would render light-on-dark furniture in those.
    await page.goto(`http://127.0.0.1:${port}/#__vulos_t=light`)
    const p = await paint(page)
    expect(p.attr).toBe('light')
    expect(p.bg).toBe(LIGHT_BG)
    expect(p.fg).toBe(LIGHT_TEXT)
  })

  test('the seed the bridge already sends does not disturb the theme', async ({ page }) => {
    // AppBridge.appFrameSrc() puts `__vulos_s=<seed>` in this same fragment, so
    // the parser has to find __vulos_t alongside it — and must not be fooled by
    // a key that merely ends in the same characters.
    await page.goto(`http://127.0.0.1:${port}/#__vulos_s=abc%3D%3D&__vulos_t=light&x__vulos_t=dark`)
    expect((await paint(page)).attr).toBe('light')
  })

  test('a junk theme value is ignored rather than stamped', async ({ page }) => {
    await page.goto(`http://127.0.0.1:${port}/#__vulos_t=chartreuse`)
    const p = await paint(page)
    expect(p.attr, 'an unrecognised value must fall back, not be written through').toBeNull()
    expect(p.bg).toBe(DARK_BG)
  })
})

test.describe('system prefers light', () => {
  test.use({ colorScheme: 'light' })

  test('with no theme from the shell, the app follows the system', async ({ page }) => {
    await page.goto(`http://127.0.0.1:${port}/`)
    const p = await paint(page)
    expect(p.attr).toBeNull()
    expect(p.bg).toBe(LIGHT_BG)
    expect(p.fg).toBe(LIGHT_TEXT)
  })

  test('an explicit Dark from the shell BEATS the light system preference', async ({ page }) => {
    // The asymmetric half: without an explicit [data-theme="dark"] block the
    // prefers-color-scheme:light fallback would win here and an explicit Dark
    // choice would be silently ignored.
    await page.goto(`http://127.0.0.1:${port}/#__vulos_t=dark`)
    const p = await paint(page)
    expect(p.attr).toBe('dark')
    expect(p.bg).toBe(DARK_BG)
    expect(p.fg).toBe(DARK_TEXT)
  })

  test('switching the theme repaints the frame without reloading it', async ({ page }) => {
    await page.goto(`http://127.0.0.1:${port}/#__vulos_t=dark`)
    expect((await paint(page)).bg).toBe(DARK_BG)

    // Mark the document. Changing ONLY the fragment must not reload it — that
    // is why the fragment was chosen as the carrier, and it is what makes a
    // live theme switch in Settings free for every open app.
    await page.evaluate(() => {
      ;(window as unknown as { __alive: number }).__alive = 42
    })
    await page.evaluate(() => {
      window.location.hash = '__vulos_t=light'
    })
    await expect.poll(async () => (await paint(page)).attr).toBe('light')

    const p = await paint(page)
    expect(p.bg).toBe(LIGHT_BG)
    const survived = await page.evaluate(() => (window as unknown as { __alive?: number }).__alive)
    expect(survived, 'the document reloaded; the fragment channel would cost every app its state').toBe(42)
  })
})
