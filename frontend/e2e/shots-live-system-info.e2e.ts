// shots-live-system-info.e2e.ts — the fifteenth live app shot, taken on a REAL
// BOX rather than on the machine running the tests.
//
// WHY THIS FILE IS SEPARATE FROM shots-live-apps.e2e.ts
// -----------------------------------------------------
// The other fourteen process-backed apps are photographed by
// shots-live-apps.e2e.ts, which spawns each app's own `command` as a local
// subprocess. For fourteen of them the host is irrelevant: Notes renders an
// empty editor and Calculator renders a keypad wherever they run.
//
// System Info is the one app whose ENTIRE SURFACE IS THE MACHINE IT RUNS ON.
// Run it under that harness and the capture publishes the capture host's real
// hostname, kernel, disk and uptime. Done on a laptop that is a privacy leak
// (`pcs-MacBook-Air.local`, 792.8/926.4 GB, published into a public repo);
// cropping was rejected, because a doctored shot of a system-information app
// is a staged shot of the one app whose whole job is to be accurate. So the
// shot was simply absent, behind a self-expiring exemption in
// scripts/check-screenshot-provenance.py.
//
// A Docker container was the obvious next idea and was MEASURED (2026-08-16,
// debian trixie under OrbStack) before being rejected — it is a worse lie, not
// a smaller one:
//
//     hostname   aff034300851        a container ID, not a box name
//     kernel     6.17.8-orbstack-…   the developer's VM kernel
//     uptime     1d 14h 6m           the VM's uptime, not the box's
//     mem        7995 MB             the VM's allocation
//     storage    327 GB / 108 GB     the laptop's docker store, one layer down
//     disks      /etc/resolv.conf, /etc/hostname, /etc/hosts listed AS DISKS,
//                because Docker bind-mounts them from /dev/vdb1 and the app's
//                mount filter passes anything under /dev/
//     network    interfaces with NO addresses (no iproute2 in the image)
//
// Four of the app's panels would have been wrong in ways a reader cannot tell
// apart from "the app is broken", while still implying "this is your Vulos
// box". That is the founder's original complaint wearing different clothes.
//
// WHAT THIS HARNESS DOES INSTEAD
// ------------------------------
// It photographs the app on a real Vulos OS box, through the box's own path:
//
//   * the box is a booted Vulos OS image (QEMU/HVF, see the header of
//     scripts/baremetal-smoke.sh for the boot line; VULOS_BOX_URL points at the
//     forwarded backend port),
//   * the app is started by THE BOX'S OWN LAUNCHER via POST /api/apps/launch,
//     which resolves the command from the installed manifest and runs it inside
//     a per-app network namespace (`ip netns`) — the Linux-only path that
//     shots-live-apps.e2e.ts explicitly does NOT exercise,
//   * every number on screen is read by the app from THAT machine's /proc and
//     /sys, and is cross-checked here against what the box's own backend
//     reports for the same machine.
//
// Nothing is mocked. This file imports nothing from mock-backend.js and
// installs no page.route.
//
// WHY THE APP ORIGIN AND NOT THE GATEWAY PATH
// -------------------------------------------
// The capture is taken at the app's OWN origin root (VULOS_BOX_APP_URL), which
// is what the other fourteen live shots do too — they point the browser at the
// port the app really bound.
//
// It is not taken through the shell's `/app/<id>/` gateway path, and the reason
// is a REAL DEFECT found while building this harness, recorded here rather than
// hidden by the choice of URL:
//
//   frontend/apps/system-info/index.html fetches ABSOLUTE paths (`/api/info`,
//   `/api/disks`, `/api/network`, `/api/live`). With app origins disabled —
//   which is the DEFAULT (GET /api/apps/origins → {"enabled":false}), and what
//   src/core/AppOrigins.ts falls back to — the shell opens an app at
//   `/app/<id>/` on the shell's own origin. Those absolute fetches then resolve
//   against the SHELL's origin, hit the box's backend instead of the app, and
//   every panel renders its empty state: "No storage data available", "No
//   active interfaces detected", 0%. Verified on the box on 2026-08-16: the
//   gateway serves `/app/system-info/api/info` correctly, so the routing is
//   fine — it is the app's own URLs that are wrong.
//
// So a gateway-path capture would have photographed that bug, and a reader
// could not have told it apart from "System Info does not work". The bug is
// filed instead of framed; the shot shows the app, which is the claim this
// directory makes about every image in it.
//
// THE LIVENESS PROOF
// ------------------
// After the capture the app is stopped through POST /api/apps/stop and the very
// same gateway URL is re-fetched from inside the page with cache:'no-store'.
// It must stop answering 200. A fixture-backed or cached page would keep
// serving, so the image can only have come from a process that was genuinely up.
//
// THE HOST-IDENTITY PROOF
// -----------------------
// The whole reason this file exists is that the shot must not be of the
// developer's machine, so that is asserted rather than trusted: the hostname
// the box reports must differ from the hostname of the machine running the
// test. A capture that quietly fell back to localhost fails here instead of
// being published.

import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = path.resolve(HERE, '..', '..')
const OUT_DIR =
  process.env.LIVE_SHOT_OUT || path.join(REPO_ROOT, 'docs', 'screenshots', 'live-apps')

const APP_ID = 'system-info'
const VIEWPORT = { width: 1280, height: 800 }

// A booted Vulos OS box, and an admin session cookie on it. Absent them there
// is no box to photograph, and this spec has nothing it could honestly do —
// see the file header for how the box is booted and provisioned.
const BOX_URL = (process.env.VULOS_BOX_URL || '').replace(/\/$/, '')
const BOX_SESSION = process.env.VULOS_BOX_SESSION || ''
// The APP's own origin on that box, as reachable from this machine. See
// "WHY THE APP ORIGIN AND NOT THE GATEWAY PATH" in the header: the app must be
// served at the ROOT of an origin or its own /api/* calls do not reach it.
const APP_ORIGIN = (process.env.VULOS_BOX_APP_URL || '').replace(/\/$/, '')

interface BoxInfo {
  hostname: string
  kernel: string
  device_model: string
}

interface Reply {
  status: number
  body: string
}

/**
 * One request to the box's backend.
 *
 * node:http rather than Node's global fetch: undici sets IP_TOS on the socket
 * and intermittently resets against a QEMU-forwarded 127.0.0.1 port on macOS
 * (`ECONNRESET` in the first draft of this file, on a box that was serving
 * perfectly). node:http does not touch IP_TOS. shots-live-apps.e2e.ts hit the
 * same flake and made the same choice. The liveness proof itself deliberately
 * still runs in the PAGE, because what must be proven is that the BROWSER
 * could reach the app.
 */
function boxRequest(method: string, p: string, session: string, body?: unknown): Promise<Reply> {
  return new Promise((resolve, reject) => {
    const target = new URL(`${BOX_URL}${p}`)
    const payload = body === undefined ? undefined : Buffer.from(JSON.stringify(body))
    const req = http.request(
      {
        method,
        host: target.hostname,
        port: target.port,
        path: target.pathname + target.search,
        timeout: 30_000,
        headers: {
          Cookie: `vulos_session=${session}`,
          'Content-Type': 'application/json',
          ...(payload ? { 'Content-Length': String(payload.length) } : {}),
        },
      },
      (res) => {
        let out = ''
        res.setEncoding('utf8')
        res.on('data', (c) => (out += c))
        res.on('end', () => resolve({ status: res.statusCode ?? 0, body: out }))
      },
    )
    req.on('timeout', () => req.destroy(new Error('timeout')))
    req.on('error', reject)
    if (payload) req.write(payload)
    req.end()
  })
}

/**
 * A plain GET against the app's own origin on the box (same node:http reason).
 *
 * A transport error resolves as status 0 rather than rejecting: this is a
 * PROBE, and "nothing is listening yet" is a normal answer while an app is
 * starting. It is never an assertion on its own — every caller either polls for
 * 200 with a deadline or checks the body, so a permanently dead app still
 * fails, it just fails on the timeout instead of on the first refused socket.
 */
function originStatus(p = '/'): Promise<Reply> {
  return new Promise((resolve) => {
    const target = new URL(`${APP_ORIGIN}${p}`)
    const req = http.get(
      { host: target.hostname, port: target.port, path: target.pathname, timeout: 15_000 },
      (res) => {
        let out = ''
        res.setEncoding('utf8')
        res.on('data', (c) => (out += c))
        res.on('end', () => resolve({ status: res.statusCode ?? 0, body: out }))
      },
    )
    req.on('timeout', () => req.destroy(new Error('timeout')))
    req.on('error', () => resolve({ status: 0, body: '' }))
  })
}

test.describe('live: system-info, on a real box', () => {
  test.skip(
    !BOX_URL || !BOX_SESSION || !APP_ORIGIN,
    'needs a booted Vulos OS box: set VULOS_BOX_URL, VULOS_BOX_SESSION and ' +
      'VULOS_BOX_APP_URL (see this file’s header)',
  )

  test('captured on a real box, started by the box, and proved live', async ({ page }) => {
    test.setTimeout(180_000)

    const api = (method: string, p: string, body?: unknown) =>
      boxRequest(method, p, BOX_SESSION, body)

    // ── The machine is a BOX, not this machine ────────────────────────────
    const infoRes = await api('GET', '/api/system/info')
    expect(infoRes.status, `GET /api/system/info on ${BOX_URL}`).toBe(200)
    const info = JSON.parse(infoRes.body) as BoxInfo
    expect(info.hostname, 'the box reported no hostname').toBeTruthy()
    // THE point of this file. If the "box" turns out to be the machine running
    // the tests, the capture is the privacy leak this harness exists to avoid,
    // and it must fail rather than be published.
    expect(
      info.hostname.split('.')[0].toLowerCase(),
      'the box reports THIS machine’s hostname — the capture would publish the developer’s laptop',
    ).not.toBe(os.hostname().split('.')[0].toLowerCase())
    console.log(
      `[live-shot] box: hostname=${info.hostname} kernel=${info.kernel} model=${info.device_model}`,
    )

    // ── The BOX's own launcher starts the app ─────────────────────────────
    const launch = await api('POST', '/api/apps/launch', { app_id: APP_ID })
    expect(launch.status, `POST /api/apps/launch: ${launch.body}`).toBe(200)
    const launched = JSON.parse(launch.body) as { url: string; running: boolean }
    expect(launched.running, `the box refused to run ${APP_ID}: ${launch.body}`).toBe(true)
    // The gateway path the BOX chose, not one this test picked.
    expect(launched.url, 'the box returned no gateway URL').toContain(APP_ID)
    const url = `${APP_ORIGIN}/`

    try {
      // The app was started inside its own network namespace; it needs a moment
      // to bind before anything can reach it.
      await expect
        .poll(async () => (await originStatus()).status, {
          timeout: 60_000,
          message: `${APP_ID}: the box never served ${url}`,
        })
        .toBe(200)

      // The app is serving THIS box: its own /api/info must agree with what the
      // box's backend independently reports for the same machine. Two separate
      // readers of the same /proc, cross-checked — so a shot of some OTHER
      // machine's dashboard cannot be published under this box's name.
      const appInfo = JSON.parse((await originStatus('/api/info')).body) as BoxInfo
      expect(
        appInfo.hostname,
        `${APP_ID}: the app reports host ${appInfo.hostname}, the box reports ${info.hostname}`,
      ).toBe(info.hostname)
      expect(appInfo.kernel, `${APP_ID}: kernel disagrees with the box`).toBe(info.kernel)

      await page.setViewportSize(VIEWPORT)

      const response = await page.goto(url, { waitUntil: 'load' })
      expect(response, `${APP_ID}: no response for ${url}`).not.toBeNull()
      expect(response!.status(), `${APP_ID}: GET ${url} status`).toBe(200)

      // The app really rendered, and it rendered THE BOX: the hostname the
      // backend reported must be on screen. This is what stops a blank shell,
      // an error page, or some other machine's dashboard being published.
      await expect
        .poll(async () => page.evaluate(() => document.body?.innerText ?? ''), {
          timeout: 30_000,
          message: `${APP_ID}: the box hostname ${info.hostname} never appeared on screen`,
        })
        .toContain(info.hostname)
      await page.waitForTimeout(1_200) // let the live gauges take a reading

      fs.mkdirSync(OUT_DIR, { recursive: true })
      await page.screenshot({ path: path.join(OUT_DIR, `${APP_ID}.png`) })

      // ── LIVENESS PROOF ──────────────────────────────────────────────────
      // The probe runs in the PAGE (Chromium's fetch), because what has to be
      // proven is that THE BROWSER could reach the app — the same thing that
      // produced the pixels.
      //
      // It carries its own 6s deadline, and a timeout counts as "not serving".
      // That is not a loosening: it is the shape a stopped app actually takes
      // here. When the box tears the app's network namespace down, the DNAT to
      // 10.200.67.2 is left pointing at an address that no longer routes, so
      // packets are DROPPED rather than refused and the fetch hangs instead of
      // erroring. Without the deadline the proof timed out with no verdict on a
      // genuinely dead app. A cached or mocked response, which is the thing
      // this proof exists to exclude, answers instantly in either shape.
      const probe = (u: string) =>
        page.evaluate(async (target) => {
          try {
            const r = await fetch(target, { cache: 'no-store', signal: AbortSignal.timeout(6_000) })
            return r.status
          } catch {
            return -1
          }
        }, u)

      expect(await probe(url), `${APP_ID}: app was not serving at capture time`).toBe(200)

      // ...and once the box stops it, the very same URL must stop answering.
      const stopped = await api('POST', '/api/apps/stop', { app_id: APP_ID })
      expect(stopped.status, `POST /api/apps/stop: ${stopped.body}`).toBe(200)

      await expect
        .poll(() => probe(url), {
          timeout: 60_000,
          message: `${APP_ID}: the URL still served after the box stopped the app — this shot is NOT backed by a live process`,
        })
        .not.toBe(200)
    } finally {
      await api('POST', '/api/apps/stop', { app_id: APP_ID }).catch(() => {})
    }
  })
})
