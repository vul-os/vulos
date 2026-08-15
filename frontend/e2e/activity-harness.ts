import { expect, type Page } from '@playwright/test'
import { installBackend } from './mock-backend.js'

/**
 * Shared setup for the activity-* specs.
 *
 * Two things the generic harness does not provide:
 *
 *  1. Process/network/app fixtures. mock-backend's catch-all fulfils an
 *     unmocked /api call with `{}`, which the Activity Monitor narrows to an
 *     empty list — so without these the specs would be measuring the empty
 *     state and calling it the app.
 *  2. A telemetry WebSocket. useTelemetry opens ws://host/api/telemetry, and
 *     page.route cannot answer a WebSocket. fakeTelemetry replaces the
 *     constructor so the graphs have data to draw.
 */

export const PROCESSES = [
  { pid: 1, name: 'init', command: '/sbin/init', user: 'root', state: 'sleeping', cpu: 0.1, mem_rss: 8_400_000, mem_pct: 0.1, threads: 1, start: 5, ppid: 0, pgid: 1, protected: true, protected_reason: 'pid 1 is the system init; ending it stops the box' },
  { pid: 2, name: 'kthreadd', command: 'kthreadd', user: 'root', state: 'sleeping', cpu: 0, mem_rss: 0, mem_pct: 0, threads: 1, start: 5, ppid: 0, pgid: 0, protected: true, protected_reason: 'kernel threads do not receive signals' },
  { pid: 812, name: 'vulos-server', command: '/opt/vulos/bin/vulos serve', user: 'vulos', state: 'running', cpu: 12.4, mem_rss: 148_000_000, mem_pct: 3.6, threads: 24, start: 61200, ppid: 1, pgid: 812, protected: true, protected_reason: 'this is the Vulos server process' },
  { pid: 1044, name: 'postgres', command: '/usr/lib/postgresql/16/bin/postgres -D /var/lib/postgresql', user: 'postgres', state: 'sleeping', cpu: 2.1, mem_rss: 96_400_000, mem_pct: 2.3, threads: 9, start: 61980, ppid: 1, pgid: 1044 },
  { pid: 2277, name: 'gimp', command: '/usr/bin/gimp-2.10', user: 'ada', state: 'running', cpu: 98.6, mem_rss: 812_000_000, mem_pct: 19.8, threads: 14, start: 88410, ppid: 812, pgid: 2277 },
  { pid: 2301, name: 'cp', command: 'cp -a /mnt/photos /backup', user: 'ada', state: 'disk sleep', cpu: 0, mem_rss: 2_100_000, mem_pct: 0.1, threads: 1, start: 91002, ppid: 2277, pgid: 2301 },
  { pid: 3110, name: 'node', command: 'node /srv/notes/server.js', user: 'ada', state: 'sleeping', cpu: 0.7, mem_rss: 64_200_000, mem_pct: 1.6, threads: 11, start: 92140, ppid: 812, pgid: 3110 },
  { pid: 3400, name: 'defunct-worker', command: 'defunct-worker', user: 'ada', state: 'zombie', cpu: 0, mem_rss: 0, mem_pct: 0, threads: 1, start: 93000, ppid: 3110, pgid: 3110 },
]

export const NETWORK = [
  { proto: 'tcp', local_addr: '0.0.0.0', local_port: 8080, remote_addr: '0.0.0.0', remote_port: 0, state: 'LISTEN', pid: 812, process: 'vulos-server', inode: 918273 },
  { proto: 'tcp', local_addr: '192.168.1.40', local_port: 8080, remote_addr: '192.168.1.19', remote_port: 54112, state: 'ESTABLISHED', pid: 812, process: 'vulos-server', inode: 918290 },
  { proto: 'tcp', local_addr: '127.0.0.1', local_port: 5432, remote_addr: '0.0.0.0', remote_port: 0, state: 'LISTEN', pid: 1044, process: 'postgres', inode: 918311 },
  { proto: 'tcp6', local_addr: '[::]', local_port: 22, remote_addr: '[::]', remote_port: 0, state: 'LISTEN', pid: 640, process: 'sshd', inode: 918400 },
  { proto: 'udp', local_addr: '0.0.0.0', local_port: 5353, remote_addr: '0.0.0.0', remote_port: 0, state: '', pid: 0, process: '', inode: 918455 },
  { proto: 'tcp', local_addr: '192.168.1.40', local_port: 49820, remote_addr: '140.82.121.4', remote_port: 443, state: 'TIME_WAIT', pid: 0, process: '', inode: 0 },
]

export const APPS = {
  apps: [
    { app_id: 'notes', name: 'notes', kind: 'process', running: true, pid: 3110, mem_rss: 64_200_000, closable: true,
      responding: { status: 'responding', method: 'http_probe', detail: 'HTTP 200 in 41ms', checked_ms: 41 } },
    { app_id: 'ledger', name: 'ledger', kind: 'process', running: true, pid: 3115, mem_rss: 38_000_000, closable: true,
      responding: { status: 'not_responding', method: 'http_probe', detail: 'no reply within 3s', checked_ms: 3000 } },
    { app_id: 'gimp-session', name: 'GIMP', kind: 'stream', running: true, pid: 0, mem_rss: 0, closable: true,
      responding: { status: 'unknown', method: 'none', detail: 'streamed X11 apps need _NET_WM_PING, which this box does not implement yet' } },
  ],
}

/**
 * fakeTelemetry replaces window.WebSocket for the telemetry URL only.
 *
 * page.route cannot fulfil a WebSocket, so without this useTelemetry never
 * connects. Scoped by URL rather than replacing WebSocket wholesale: the shell
 * opens other sockets, and a blanket stub would quietly change what else the
 * page does while claiming to test only this app.
 */
export async function fakeTelemetry(page: Page) {
  await page.addInitScript(() => {
    const Real = window.WebSocket
    class FakeTelemetrySocket {
      onopen: (() => void) | null = null
      onmessage: ((e: { data: string }) => void) | null = null
      onclose: (() => void) | null = null
      onerror: (() => void) | null = null
      readyState = 1
      private timer: ReturnType<typeof setInterval> | undefined
      private t = 0
      constructor() {
        setTimeout(() => {
          this.onopen?.()
          this.push()
          this.timer = setInterval(() => this.push(), 300)
        }, 10)
      }
      private push() {
        this.t += 1
        const wave = (p: number, lo: number, hi: number) =>
          lo + (hi - lo) * (0.5 + 0.5 * Math.sin(this.t / p))
        this.onmessage?.({
          data: JSON.stringify({
            cpu: wave(6, 8, 74),
            mem_percent: wave(11, 38, 63),
            mem_used: 6_900_000_000,
            mem_total: 16_800_000_000,
            mem_cached: 2_100_000_000,
            swap_used: 340_000_000,
            net_rx: wave(4, 40_000, 900_000),
            net_tx: wave(5, 10_000, 320_000),
            disk_read: wave(7, 0, 4_400_000),
            disk_write: wave(9, 0, 1_800_000),
            disk_used: 210_000_000_000,
            disk_total: 512_000_000_000,
            disk_percent: 41,
            num_cpu: 8,
            load_avg: '1.42 1.18 0.94',
            uptime: '3d 4h 12m',
            hostname: 'ada-box',
            temp: 47,
            battery: 88,
            charging: true,
          }),
        })
      }
      close() { clearInterval(this.timer); this.readyState = 3; this.onclose?.() }
      send() { /* the telemetry socket is read-only */ }
    }
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(window as any).WebSocket = function (url: string, protocols?: string | string[]) {
      if (String(url).includes('/api/telemetry')) return new FakeTelemetrySocket()
      return new Real(url, protocols)
    }
  })
}

export interface BootOptions {
  theme?: 'dark' | 'light'
  telemetry?: boolean
  /** Extra/overriding mock-backend routes, keyed "METHOD /path". */
  overrides?: Record<string, unknown>
}

/** Boots the shell with the activity fixtures in place and opens the app. */
export async function bootActivity(page: Page, opts: BootOptions = {}) {
  const theme = opts.theme ?? 'dark'
  if (opts.telemetry !== false) await fakeTelemetry(page)

  await page.addInitScript(t => {
    localStorage.setItem('vulos-theme', t)
  }, theme)

  const json = (body: unknown, status = 200) => ({
    status, contentType: 'application/json', body: JSON.stringify(body),
  })

  await installBackend(page, {
    'GET /api/system/processes': json(PROCESSES),
    'GET /api/system/network': json(NETWORK),
    'GET /api/proc/apps': json(APPS),
    ...(opts.overrides ?? {}),
  })

  await page.goto('/')

  // The boot marker DIFFERS BY VIEWPORT. useViewport switches to MobileStack
  // below 768px, and that tree has no Applications control — it has a system
  // nav. Waiting only for the desktop marker is why the first phone-width run
  // of these specs timed out for twenty seconds and reported a layout failure
  // that was really a harness failure.
  await Promise.race([
    page.getByTitle('Applications').waitFor({ timeout: 25_000 }),
    page.locator('nav[aria-label="System navigation"]').waitFor({ timeout: 25_000 }),
  ])
  await page.evaluate(t => document.documentElement.setAttribute('data-theme', t), theme)

  // ⌘K is the same launch lane on both shells.
  const palette = page.getByPlaceholder(/Search apps/)
  await expect(async () => {
    await page.keyboard.press('Meta+k')
    await expect(palette).toBeVisible({ timeout: 1000 })
  }).toPass({ timeout: 20_000 })
  await palette.fill('Activity')
  await page.keyboard.press('Enter')
  await page.locator('[data-app="activity"]').waitFor({ timeout: 20_000 })
  // Let the poll land and the graphs accumulate a few samples.
  await page.waitForTimeout(1800)
}

/**
 * maximise fills the desktop window frame, so the table gets the whole
 * viewport as a user working in it would have.
 *
 * A no-op on the mobile shell, which has no window frame — expressed as a
 * count check rather than a try/catch, because a catch would also swallow the
 * case where the desktop frame exists and the double-click silently fails.
 */
export async function maximise(page: Page) {
  const bar = page.locator('.vwin-titlebar')
  if (await bar.count() === 0) return
  await bar.first().dblclick()
  await page.waitForTimeout(400)
}

/** The Activity Monitor's root, so assertions cannot drift onto the shell. */
export const APP = '[data-app="activity"]'
