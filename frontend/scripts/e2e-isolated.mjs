#!/usr/bin/env node
/**
 * e2e-isolated.mjs — run the E2E suite against a bundle this process built, and
 * PROVE that is what the browser got.
 *
 * # Why the two guards already here are not enough
 *
 * `e2e/assert-correct-app.js` compares the served <title> against this repo's
 * index.html. That catches a sibling REPO on the port (the marketing-site
 * incident it was written for). It does not catch a sibling AGENT: another
 * checkout of *this* repo serves the identical title, so the guard passes and
 * the suite runs against somebody else's code.
 *
 * `e2e/build-freshness.e2e.ts` compares mtimes under `dist/`. It reads the
 * filesystem while the BROWSER reads a socket. If the server on the port is
 * another agent's preview, the check inspects a dist/ nobody is serving and
 * reports fresh. Both guards are correct about the question they ask; neither
 * asks whether the bytes under test are the bytes just built.
 *
 * Playwright's `reuseExistingServer` is what makes this more than theory: a
 * `vite preview` that dies with EADDRINUSE does not fail the run, it silently
 * hands the suite to whoever already holds the port. A private --outDir alone
 * does not help, because the port is the thing that is shared.
 *
 * # What this does instead
 *
 *   1. builds into a PRIVATE outDir (nobody else writes there)
 *   2. serves it on a PRIVATE port (nobody else listens there)
 *   3. reads the hashed entry bundle out of the freshly built index.html
 *   4. fetches / over HTTP and requires the SERVED html to name that exact hash
 *   5. fetches the bundle over HTTP and requires a planted marker to be ABSENT
 *   6. only then runs Playwright against that port
 *
 * Step 4 is the provenance check: a hash is content-addressed, so another
 * agent's build of different source cannot produce it. Step 5 is the control
 * for step 4 — run with --plant-mutation and the marker must APPEAR, which is
 * what proves the check can see the served JavaScript at all. A check that
 * cannot fail is the failure mode this repository keeps finding.
 *
 * Usage:
 *   node scripts/e2e-isolated.mjs [--plant-mutation] [-- <playwright args>]
 */

import { spawn, spawnSync } from 'node:child_process'
import { readFileSync, writeFileSync, rmSync, existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve, join } from 'node:path'

import { hasContentHash } from './entryHash.mjs'

const FE = resolve(dirname(fileURLToPath(import.meta.url)), '..')

const argv = process.argv.slice(2)
const PLANT = argv.includes('--plant-mutation')
const pwArgs = argv.includes('--') ? argv.slice(argv.indexOf('--') + 1) : []

// The marker is a string that cannot occur by accident and that survives
// minification because it is a string literal in shipped code.
const MARKER = 'VULOS_E2E_PROVENANCE_MARKER_9f3a1c'

// Private by construction, not by convention: the PID makes both unique to this
// process, so two agents running this script at the same time cannot collide.
const UNIQ = process.pid
const OUT_DIR = `dist-e2e-${UNIQ}`
const PORT = 41000 + (UNIQ % 2000)

const log = (...a) => console.log('[e2e-isolated]', ...a)
const fail = (msg) => { console.error('\n[e2e-isolated] FAILED\n' + msg + '\n'); process.exitCode = 1 }

// The file the mutation is planted into. index.html is deliberate: it is the
// one input whose output we can locate without parsing the bundle graph, and
// the marker still has to survive the real build to reach the served page.
const PLANT_FILE = join(FE, 'src', 'main.tsx')
const PLANT_LINE = `\n// e2e provenance probe\nconsole.debug('${MARKER}')\n`

let planted = false
function plant() {
  const src = readFileSync(PLANT_FILE, 'utf8')
  if (src.includes(MARKER)) throw new Error('src/main.tsx already contains the marker — refusing to plant a second one')
  writeFileSync(PLANT_FILE, src + PLANT_LINE)
  planted = true
  log('planted marker into src/main.tsx')
}
function unplant() {
  if (!planted) return
  const src = readFileSync(PLANT_FILE, 'utf8')
  if (!src.includes(PLANT_LINE)) { planted = false; return }
  // Removed by CONTENT, not by trailing-suffix. The first version stripped the
  // last PLANT_LINE.length characters only when the file still ENDED with the
  // marker — and this repository has several agents editing concurrently, so a
  // write that landed underneath this process left the marker stranded in
  // src/main.tsx with the revert reporting success. Matching the exact inserted
  // text is both safer (it cannot truncate an unrelated edit) and correct when
  // the marker is no longer last.
  writeFileSync(PLANT_FILE, src.replace(PLANT_LINE, ''))
  planted = false
  log('reverted marker')
}

let server = null
function cleanup() {
  unplant()
  if (server && !server.killed) { try { process.kill(-server.pid, 'SIGKILL') } catch { /* already gone */ } }
  try { rmSync(join(FE, OUT_DIR), { recursive: true, force: true }) } catch { /* best effort */ }
}
process.on('exit', cleanup)
process.on('SIGINT', () => { cleanup(); process.exit(130) })

async function main() {
  if (PLANT) plant()

  log(`building into ${OUT_DIR} (private)`)
  const build = spawnSync('npx', ['vite', 'build', '--outDir', OUT_DIR, '--emptyOutDir'], {
    cwd: FE, encoding: 'utf8', env: { ...process.env },
  })
  if (build.status !== 0) {
    return fail(`vite build exited ${build.status}\n${build.stdout}\n${build.stderr}`)
  }

  // The hashed entry bundle, read from the build we just produced.
  const builtHtmlPath = join(FE, OUT_DIR, 'index.html')
  if (!existsSync(builtHtmlPath)) return fail(`no ${OUT_DIR}/index.html — the build produced nothing to serve`)
  const builtHtml = readFileSync(builtHtmlPath, 'utf8')
  const entry = (builtHtml.match(/src="([^"]*\/assets\/[^"]*\.js)"/) || [])[1]
  if (!entry) return fail(`could not find a hashed entry bundle in ${OUT_DIR}/index.html — this check would prove nothing`)
  // A content hash is what makes this a provenance check rather than a name
  // check: /assets/index.js would be identical across every agent's build.
  //
  // The predicate lives in ./entryHash.mjs so it can be tested — the version
  // inline here rejected any Vite digest containing a hyphen, which is a fifth
  // of them, deterministically per tree, while reading as a build failure. See
  // that file and src/__tests__/entryHash.test.ts.
  if (!hasContentHash(entry)) {
    return fail(`entry ${entry} carries no content hash; any other build would produce the same name and this check would be decorative`)
  }
  log(`built entry: ${entry}`)

  log(`serving ${OUT_DIR} on private port ${PORT}`)
  server = spawn('npx', ['vite', 'preview', '--outDir', OUT_DIR, '--port', String(PORT), '--strictPort'], {
    cwd: FE, detached: true, stdio: ['ignore', 'pipe', 'pipe'],
  })
  let serverErr = ''
  server.stderr.on('data', (d) => { serverErr += d })
  server.stdout.on('data', () => {})

  const base = `http://localhost:${PORT}`
  let up = false
  for (let i = 0; i < 60; i++) {
    try { await fetch(base + '/'); up = true; break } catch { await new Promise((r) => setTimeout(r, 500)) }
  }
  // strictPort makes EADDRINUSE fatal instead of silently sliding to the next
  // port — which is the whole reason a "running" server can be the wrong one.
  if (!up) return fail(`nothing answered on ${base} within 30s.\n${serverErr}`)

  // ── PROOF 1: the served page names the bundle this process just built ──────
  const servedHtml = await (await fetch(base + '/')).text()
  if (!servedHtml.includes(entry)) {
    const servedEntry = (servedHtml.match(/src="([^"]*\/assets\/[^"]*\.js)"/) || [])[1]
    return fail(
      `The served index.html does NOT reference this build's bundle.\n` +
      `  expected: ${entry}\n` +
      `  served:   ${servedEntry}\n` +
      `Something else is answering on ${base}. Every result from this run would\n` +
      `be about another build's code.`,
    )
  }
  log(`PROOF 1 ok — served index.html references ${entry}`)

  // ── PROOF 2: the served JS does or does not carry the marker ──────────────
  const servedJs = await (await fetch(base + entry)).text()
  const hasMarker = servedJs.includes(MARKER)
  log(`served bundle: ${servedJs.length} bytes, marker ${hasMarker ? 'PRESENT' : 'absent'}`)

  if (PLANT && !hasMarker) {
    return fail(
      `A marker was planted in src/main.tsx and is ABSENT from the served bundle.\n` +
      `That means this check cannot see the JavaScript the browser runs, so its\n` +
      `"marker absent" result in a normal run proves nothing.`,
    )
  }
  if (!PLANT && hasMarker) {
    return fail(`The provenance marker is in the served bundle but nothing planted it — the tree is dirty.`)
  }
  log(`PROOF 2 ok — marker ${PLANT ? 'present under --plant-mutation (the check can see served JS)' : 'absent, as it must be in a clean run'}`)

  if (PLANT) {
    log('--plant-mutation: proofs verified, not running the suite')
    return
  }

  // ── run the suite against the port we proved ──────────────────────────────
  log(`running Playwright against ${base}`)
  const pw = spawnSync('npx', ['playwright', 'test', ...pwArgs], {
    cwd: FE, encoding: 'utf8', stdio: 'inherit',
    // E2E_PORT points both the config's baseURL and assert-correct-app at OUR
    // server; reuseExistingServer then adopts it instead of building again.
    // E2E_OUT_DIR points build-freshness.e2e.ts at the directory actually being
    // served. Without it that spec inspects the shared dist/, which is not what
    // this run put on the wire.
    env: { ...process.env, E2E_PORT: String(PORT), E2E_OUT_DIR: OUT_DIR },
  })
  if (pw.status !== 0) process.exitCode = pw.status ?? 1
}

main().catch((e) => fail(e.stack || String(e)))
