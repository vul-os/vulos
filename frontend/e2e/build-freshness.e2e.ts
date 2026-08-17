import { test, expect } from '@playwright/test'
import { readdirSync, statSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve, join, relative } from 'node:path'

/**
 * The suite must be testing the CURRENT source, not a bundle from earlier.
 *
 * playwright.config.js sets `reuseExistingServer: !process.env.CI`, so locally,
 * if anything is already listening on the preview port, the whole
 * `npm run build && vite preview` command is SKIPPED and every test runs against
 * whatever dist/ happens to contain. It is a real speed win and it is also a way
 * to get a green run for code you have not built.
 *
 * That produced a false green in the sibling vulos-cloud repo: a change that put
 * white text on a light button at 3.63:1 passed a full-suite run, because the run
 * reused a server whose dist/ predated the change. The gate that should have
 * caught it was working perfectly and was handed the wrong bytes.
 *
 * A stale run is the worst failure to have, because it is indistinguishable from
 * success — and this suite has 18 spec files whose results all depend on the
 * bundle being current. So this one checks the bundle.
 */

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')

/**
 * The directory actually being SERVED.
 *
 * This file used to hard-code `dist/`, which made it a check about the
 * filesystem while the browser reads a socket. scripts/e2e-isolated.mjs builds
 * into a private outDir and serves that, precisely so a run cannot be handed
 * another agent's bundle — and against such a run the old check inspected a
 * `dist/` nobody was serving and reported on it either way. It failed loudly
 * there (a stale shared dist/), which was luck: had the shared dist/ been fresh
 * it would have reported PASS about bytes no browser ever fetched.
 *
 * So the subject comes from the harness. Default `dist` keeps the plain
 * `npm run test:e2e` path unchanged.
 */
const OUT_DIR = process.env.E2E_OUT_DIR || 'dist'

/** Newest mtime under a directory, and which file it was. */
function newest(dir: string): { mtime: number; file: string | null } {
  let best: { mtime: number; file: string | null } = { mtime: 0, file: null }
  const walk = (d: string) => {
    let entries
    try {
      entries = readdirSync(d, { withFileTypes: true })
    } catch {
      return
    }
    for (const e of entries) {
      const p = join(d, e.name)
      if (e.isDirectory()) {
        walk(p)
        continue
      }
      const m = statSync(p).mtimeMs
      if (m > best.mtime) best = { mtime: m, file: relative(ROOT, p) }
    }
  }
  walk(dir)
  return best
}

test('the served bundle is not older than the source', () => {
  const src = newest(resolve(ROOT, 'src'))
  const dist = newest(resolve(ROOT, OUT_DIR))

  expect(src.file, 'no source files were found; this check would pass vacuously').not.toBeNull()
  expect(dist.file, `no ${OUT_DIR}/ was found — nothing was built for these tests to run against`).not.toBeNull()

  // Two seconds covers a build writing its own output. A real edit leaves minutes.
  const staleBySeconds = (src.mtime - dist.mtime) / 1000
  expect(
    staleBySeconds,
    `${OUT_DIR}/ is ${staleBySeconds.toFixed(0)}s older than src/.\n` +
      `  newest source: ${src.file}\n` +
      `  newest built:  ${dist.file}\n` +
      'playwright.config.js reuses an existing preview server locally, which SKIPS the ' +
      'build — so every result in this run may describe a bundle that predates your ' +
      'changes. Run `npm run build`, or stop the preview server so playwright rebuilds.',
  ).toBeLessThan(2)
})

/**
 * A second, independent check, because mtimes can lie — a checkout, a touch or a
 * copy moves them without changing content.
 *
 * `--accent` is a good witness: it is declared in src/index.css and its literal
 * value is emitted into the built CSS. If the source value appears in none of the
 * built stylesheets, the bundle was produced from different source.
 */
test('a token from the source appears in the built bundle', () => {
  const css = readFileSync(resolve(ROOT, 'src/index.css'), 'utf8')
  const accent = /--accent:\s*(#[0-9a-fA-F]{6})/.exec(css)
  expect(accent, 'could not read --accent from src/index.css; this check needs updating').not.toBeNull()

  const wanted = accent![1].toLowerCase()
  const assets = resolve(ROOT, `${OUT_DIR}/assets`)
  let found = false
  let files = 0
  for (const f of readdirSync(assets)) {
    if (!f.endsWith('.css')) continue
    files++
    if (readFileSync(join(assets, f), 'utf8').toLowerCase().includes(wanted)) {
      found = true
      break
    }
  }
  expect(files, 'no built CSS was found, so this check would pass vacuously').toBeGreaterThan(0)
  expect(
    found,
    `--accent is ${wanted} in src/index.css and appears in none of the ${files} built ` +
      `stylesheets — ${OUT_DIR}/ was produced from different source than the one on disk.`,
  ).toBe(true)
})
