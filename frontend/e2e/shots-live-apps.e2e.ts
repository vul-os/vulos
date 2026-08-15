// shots-live-apps.e2e.ts — screenshots of the process-backed apps, taken while
// those apps are ACTUALLY RUNNING.
//
// WHY THIS EXISTS
// ---------------
// The founder booted a real box, found the bundled apps failing, and asked:
// "in screenshots these all work, why when I'm trying is it not?"
//
// The answer was that every product screenshot came from a mocked backend. The
// marketing generator (scripts/screenshots.mjs) and the whole Playwright suite
// install e2e/mock-backend.js, which answers `POST /api/apps/launch` with
// `{ok:true}` and never starts anything. Those shots proved the shell RENDERS
// GIVEN DATA. They could not, even in principle, say whether an app RUNS —
// which is exactly the gap the founder fell into. At the time the shots were
// taken no bundled app could run at all (build.sh copied from a path that had
// moved, so /opt/vulos/apps/ shipped empty and the gateway answered
// `{"error":"app not running"}`), and the screenshots were green anyway.
//
// This spec is the opposite of that harness, and deliberately so:
//
//   * It imports NOTHING from mock-backend.js. There is no page.route, no
//     fulfil table, no fixture. Every byte the browser renders crossed a real
//     socket from a real subprocess started with the app's own manifest
//     `command`.
//   * Each capture is followed by a LIVENESS PROOF: the app process is killed
//     and the same URL is re-fetched from inside the page with cache:'no-store'.
//     The fetch must fail. A fixture-backed or cached page would keep serving,
//     so a shot can only be produced by a process that was genuinely up at the
//     moment of capture and genuinely gone once it was stopped.
//   * Output goes to docs/screenshots/live-apps/ — a directory separate from
//     the fixture-backed docs/screenshots/, so a reader looking at a folder of
//     PNGs a month from now can tell which are which without reading any code.
//     scripts/check-screenshot-provenance.py enforces that split.
//
// WHAT THIS DOES AND DOES NOT PROVE
// ---------------------------------
// PROVES: the app's own `command` starts, binds a port, serves its real UI, and
// that UI renders. This runs on macOS and Linux alike — the app servers are
// plain Python stdlib HTTP servers with no platform dependency.
//
// DOES NOT PROVE: the box's routing around the app. On a real box each app is
// launched into its own network namespace and reached through the gateway; that
// path is Linux-only (`ip netns`) and is NOT exercised here. It has its own
// coverage — scripts/check-apps-run.py for start-and-serve, and the container
// run that verified namespace reclaim. A green run here means "the app runs and
// renders", not "the box's gateway routes to it".
//
// VULOS_API is pointed at 127.0.0.1:1 (a definitely-dead address), matching
// scripts/check-apps-run.py, so no capture can quietly depend on some other
// process happening to hold port 8080 on the developer's machine. Where an app
// shows a degraded/offline state as a result, that state is REAL, not staged.

import { test, expect } from '@playwright/test'
import { spawn, type ChildProcess } from 'node:child_process'
import fs from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const FRONTEND = path.resolve(HERE, '..')
const REPO_ROOT = path.resolve(FRONTEND, '..')
const APPS_DIR = path.join(FRONTEND, 'apps')
const OUT_DIR =
  process.env.LIVE_SHOT_OUT || path.join(REPO_ROOT, 'docs', 'screenshots', 'live-apps')

// Generous because the host is routinely under heavy build load; polled tightly
// so a fast start still finishes fast.
const BOOT_TIMEOUT_MS = Number(process.env.VULOS_APP_BOOT_TIMEOUT || 40) * 1000
const POLL_MS = 50

const VIEWPORT = { width: 1280, height: 800 }

interface LiveApp {
  id: string
  dir: string
  command: string
  name: string
}

/** Every directory under frontend/apps/ whose app.json declares a `command`. */
function discoverApps(): LiveApp[] {
  const out: LiveApp[] = []
  for (const id of fs.readdirSync(APPS_DIR).sort()) {
    const dir = path.join(APPS_DIR, id)
    const manifestPath = path.join(dir, 'app.json')
    if (!fs.existsSync(manifestPath)) continue
    const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'))
    if (!manifest.command) continue // static app, no process to run
    out.push({ id, dir, command: manifest.command, name: manifest.name || id })
  }
  return out
}

/** Directories carrying an app.json at all, `command` or not. */
function countManifests(): number {
  return fs
    .readdirSync(APPS_DIR)
    .filter((id) => fs.existsSync(path.join(APPS_DIR, id, 'app.json'))).length
}

const APPS = discoverApps()

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer()
    srv.on('error', reject)
    srv.listen(0, '127.0.0.1', () => {
      const port = (srv.address() as net.AddressInfo).port
      srv.close(() => resolve(port))
    })
  })
}

interface Started {
  proc: ChildProcess
  port: number
  home: string
  log: string
}

// Node's global fetch (undici) sets IP_TOS on the socket and intermittently
// throws `setTypeOfService EINVAL` against 127.0.0.1 on macOS — a flake in the
// probe, not in the app, which failed a different pair of apps on every run.
// node:http does not touch IP_TOS, so these two helpers are stable. The
// liveness proof itself deliberately still runs in the PAGE (Chromium's fetch),
// because what must be proven is that the browser could reach the app.
function nodeGet(port: number): Promise<number> {
  return new Promise((resolve, reject) => {
    const req = http.get(
      { host: '127.0.0.1', port, path: '/', timeout: 10_000 },
      (res) => {
        res.resume()
        res.on('end', () => resolve(res.statusCode ?? 0))
      },
    )
    req.on('timeout', () => req.destroy(new Error('timeout')))
    req.on('error', reject)
  })
}

/** Resolves true once nothing is listening on the port. */
function portRefused(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const sock = net.connect({ host: '127.0.0.1', port })
    const done = (refused: boolean) => {
      sock.destroy()
      resolve(refused)
    }
    sock.setTimeout(5_000)
    sock.on('connect', () => done(false))
    sock.on('timeout', () => done(false))
    sock.on('error', () => done(true))
  })
}

/**
 * Start one app for real. Nothing is mocked: a real subprocess is spawned with
 * the manifest's own command, in a throwaway HOME so the capture cannot write
 * into the developer's real ~/.vulos.
 */
async function startApp(app: LiveApp): Promise<Started> {
  const port = await freePort()
  const home = fs.mkdtempSync(path.join(os.tmpdir(), `vulos-liveshot-${app.id}-`))
  const logPath = path.join(home, 'app.log')
  const logFd = fs.openSync(logPath, 'w')

  const argv = app.command.split(/\s+/)
  const proc = spawn(argv[0], argv.slice(1), {
    cwd: app.dir,
    env: {
      ...process.env,
      HOME: home,
      PORT: String(port),
      VULOS_PORT: String(port),
      VULOS_API: 'http://127.0.0.1:1',
      VULOS_APP_SECRET: 'shots-live-apps',
    },
    stdio: ['ignore', logFd, logFd],
    detached: true,
  })

  const deadline = Date.now() + BOOT_TIMEOUT_MS
  let lastErr = 'never became reachable'
  while (Date.now() < deadline) {
    if (proc.exitCode !== null) {
      const tail = fs.readFileSync(logPath, 'utf8').slice(-600).trim()
      throw new Error(
        `${app.id}: process exited early rc=${proc.exitCode}: ${tail || '(no output)'}`,
      )
    }
    try {
      const status = await nodeGet(port)
      if (status === 200) return { proc, port, home, log: logPath }
      lastErr = `GET / returned ${status}, want 200`
    } catch (e) {
      lastErr = String(e)
    }
    await new Promise((r) => setTimeout(r, POLL_MS))
  }
  throw new Error(`${app.id}: ${lastErr} within ${BOOT_TIMEOUT_MS}ms`)
}

/** Kill the app's whole process group and wait for the port to go dead. */
async function stopApp(s: Started): Promise<void> {
  try {
    process.kill(-s.proc.pid!, 'SIGKILL')
  } catch {
    try {
      s.proc.kill('SIGKILL')
    } catch {
      /* already gone */
    }
  }
  const deadline = Date.now() + 10_000
  while (Date.now() < deadline) {
    if (await portRefused(s.port)) return // genuinely dead
    await new Promise((r) => setTimeout(r, POLL_MS))
  }
}

fs.mkdirSync(OUT_DIR, { recursive: true })

// COVERAGE ASSERTION. Without this, deleting or renaming every app directory
// would make this file collect zero tests and report a clean pass — the exact
// "collection is not execution" failure that has bitten this repo before. The
// floor is pinned to the 15 bundled process-backed apps that were verified
// launching; adding an app must raise it deliberately, not silently.
const EXPECTED_LIVE_APPS = 15

test('every process-backed app is covered by a live screenshot', () => {
  expect(APPS.length).toBe(EXPECTED_LIVE_APPS)
  // Every manifest except the one static template must be process-backed.
  expect(countManifests()).toBe(EXPECTED_LIVE_APPS + 1)
  expect(APPS.map((a) => a.id)).toContain('notes')
})

for (const app of APPS) {
  test(`live: ${app.id}`, async ({ page }) => {
    // Boot budget plus render and the liveness proof, on a loaded host.
    test.setTimeout(BOOT_TIMEOUT_MS + 60_000)

    const started = await startApp(app)
    const url = `http://127.0.0.1:${started.port}/`
    try {
      await page.setViewportSize(VIEWPORT)

      // No page.route anywhere in this file: this navigation is answered by the
      // app's own process over a real socket.
      const response = await page.goto(url, { waitUntil: 'load' })
      expect(response, `${app.id}: no response for ${url}`).not.toBeNull()
      expect(response!.status(), `${app.id}: GET / status`).toBe(200)
      expect(new URL(response!.url()).port).toBe(String(started.port))

      // The app really rendered something, rather than serving a blank shell.
      await page.waitForLoadState('domcontentloaded')
      await expect
        .poll(
          async () => page.evaluate(() => document.body?.innerText?.trim().length ?? 0),
          { timeout: 15_000, message: `${app.id}: body never rendered any text` },
        )
        .toBeGreaterThan(0)
      // Settle animations/fonts before the capture.
      await page.waitForTimeout(600)

      await page.screenshot({ path: path.join(OUT_DIR, `${app.id}.png`) })

      // ── LIVENESS PROOF ────────────────────────────────────────────────────
      // The process must still be up at capture time...
      expect(started.proc.exitCode, `${app.id}: process died before capture`).toBeNull()
      // Probe the BARE url: several app servers match `self.path == "/"`
      // exactly and answer 404 to `/?cachebuster=…`. `cache:'no-store'` already
      // forces a network round trip, so no query string is needed.
      const alive = await page.evaluate(
        async (u) => {
          try {
            const r = await fetch(u, { cache: 'no-store' })
            return r.status
          } catch {
            return -1
          }
        },
        url,
      )
      expect(alive, `${app.id}: app was not serving at capture time`).toBe(200)

      // ...and once it is killed the very same fetch must FAIL. This is what
      // separates this shot from a mocked one: a fixture would still answer.
      await stopApp(started)
      const afterKill = await page.evaluate(
        async (u) => {
          try {
            const r = await fetch(u, { cache: 'no-store' })
            return r.status
          } catch {
            return -1
          }
        },
        url,
      )
      expect(
        afterKill,
        `${app.id}: the URL still served after the app was killed — this shot is NOT backed by a live process`,
      ).toBe(-1)
    } finally {
      await stopApp(started)
      fs.rmSync(started.home, { recursive: true, force: true })
    }
  })
}
