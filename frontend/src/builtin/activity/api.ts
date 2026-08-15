// The Activity Monitor's trust boundary.
//
// Two things live here rather than in the component:
//
//  1. The FETCH DISCIPLINE. Every /api/* service in this OS answers 5xx with a
//     JSON error body that parses cleanly. A component doing
//     `fetch(u).then(r => r.json()).then(setRows).catch(() => {})` therefore
//     gets an OBJECT on failure, coerces it to an empty list, and renders its
//     designed empty state — so a dead backend showed this app's "No processes
//     found" panel and told the user their box was running nothing. Which is
//     never true: a box always has processes. loadJSON checks r.ok first and
//     surfaces the failure as a failure.
//
//  2. The NARROWING. Responses are `unknown` at the boundary and are converted
//     field by field, matching the isRecord()/normalize*() pattern used in
//     src/lib/offlineAuth.ts and src/builtin/drive/Drive.tsx.

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

const str = (v: unknown): string | undefined => (typeof v === 'string' ? v : undefined)
const num = (v: unknown): number | undefined => (typeof v === 'number' ? v : undefined)

/** Everything that can go wrong with a request, as one shape the UI can render. */
export interface ApiFailure {
  /** HTTP status, or 0 when the request never completed. */
  status: number
  /** Human-readable message, taken from the server's `error` field when present. */
  message: string
  /** Stable machine code when the server sent one (e.g. proc_unavailable). */
  code?: string
}

export type Result<T> = { ok: true; value: T } | { ok: false; error: ApiFailure }

/**
 * loadJSON fetches and narrows, and NEVER turns a failure into empty data.
 *
 * The r.ok check is the whole point. Without it a 503 whose body is
 * `{"error":"cannot read /proc"}` flows into a list-shaped narrower, comes out
 * as `[]`, and the UI cheerfully reports zero processes.
 */
export async function loadJSON<T>(url: string, narrow: (x: unknown) => T): Promise<Result<T>> {
  let res: Response
  try {
    res = await fetch(url, { credentials: 'include' })
  } catch (e) {
    return { ok: false, error: { status: 0, message: e instanceof Error ? e.message : 'network error' } }
  }
  let body: unknown = null
  try {
    body = await res.json()
  } catch {
    body = null
  }
  if (!res.ok) {
    const rec = isRecord(body) ? body : {}
    return {
      ok: false,
      error: {
        status: res.status,
        message: str(rec.error) || `request failed (${res.status})`,
        code: str(rec.code),
      },
    }
  }
  return { ok: true, value: narrow(body) }
}

async function postJSON(url: string, payload: unknown): Promise<Result<Record<string, unknown>>> {
  let res: Response
  try {
    res = await fetch(url, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    })
  } catch (e) {
    return { ok: false, error: { status: 0, message: e instanceof Error ? e.message : 'network error' } }
  }
  let body: unknown = null
  try {
    body = await res.json()
  } catch {
    body = null
  }
  const rec = isRecord(body) ? body : {}
  if (!res.ok) {
    return {
      ok: false,
      error: { status: res.status, message: str(rec.error) || `request failed (${res.status})`, code: str(rec.code) },
    }
  }
  return { ok: true, value: rec }
}

// ── processes ───────────────────────────────────────────────────────────────

export interface ProcessInfo {
  pid: number
  name?: string
  command?: string
  user?: string
  state?: string
  cpu?: number
  mem_rss?: number
  threads?: number
  /**
   * start is the process's start time in clock ticks since boot — the OTHER
   * half of its identity. A pid alone is a stale handle: once a process is
   * reaped the kernel may hand its number to something else, so the server
   * refuses any kill request that does not echo the start time the client saw.
   * A row without one cannot be ended, and the UI must not offer to.
   */
  start?: number
  protected?: boolean
  protected_reason?: string
}

export function toProcess(x: unknown): ProcessInfo {
  const r = isRecord(x) ? x : {}
  return {
    pid: num(r.pid) ?? 0,
    name: str(r.name),
    command: str(r.command),
    user: str(r.user),
    state: str(r.state),
    cpu: num(r.cpu),
    mem_rss: num(r.mem_rss),
    threads: num(r.threads),
    start: num(r.start),
    protected: typeof r.protected === 'boolean' ? r.protected : undefined,
    protected_reason: str(r.protected_reason),
  }
}

export function toProcesses(x: unknown): ProcessInfo[] {
  return Array.isArray(x) ? x.map(toProcess) : []
}

// ── network ─────────────────────────────────────────────────────────────────

export interface NetConn {
  proto?: string
  local_addr?: string
  local_port?: number
  remote_addr?: string
  remote_port?: number
  state?: string
  process?: string
  /** The owning process, or 0 when the socket has none (TIME_WAIT, or another user's). */
  pid?: number
  /** The socket inode. A join key, never a pid — they used to be the same field. */
  inode?: number
}

export function toNetConn(x: unknown): NetConn {
  const r = isRecord(x) ? x : {}
  return {
    proto: str(r.proto),
    local_addr: str(r.local_addr),
    local_port: num(r.local_port),
    remote_addr: str(r.remote_addr),
    remote_port: num(r.remote_port),
    state: str(r.state),
    process: str(r.process),
    pid: num(r.pid),
    inode: num(r.inode),
  }
}

export function toNetConns(x: unknown): NetConn[] {
  return Array.isArray(x) ? x.map(toNetConn) : []
}

// ── apps + responsiveness ───────────────────────────────────────────────────

/**
 * RespStatus is four-valued, and the fourth values matter as much as the first
 * two. `unknown` means Vulos has no mechanism to ask — a gap it could close.
 * `not_applicable` means the question is a category error. Collapsing either
 * into "responding" would turn an absence of information into a reassurance.
 */
export type RespStatus = 'responding' | 'not_responding' | 'unknown' | 'not_applicable'

export interface Responsiveness {
  status: RespStatus
  /** How the answer was reached: http_probe, proc_state, client_side, none. */
  method: string
  /** The evidence, or the reason there is none. Always shown on hover. */
  detail: string
  checked_ms?: number
}

export function toResponsiveness(x: unknown): Responsiveness {
  const r = isRecord(x) ? x : {}
  const s = str(r.status)
  const status: RespStatus =
    s === 'responding' || s === 'not_responding' || s === 'not_applicable' ? s : 'unknown'
  return { status, method: str(r.method) || 'none', detail: str(r.detail) || '', checked_ms: num(r.checked_ms) }
}

export interface AppStatus {
  app_id: string
  name?: string
  kind: string
  running: boolean
  pid?: number
  mem_rss?: number
  responding: Responsiveness
  closable: boolean
}

export function toAppStatus(x: unknown): AppStatus {
  const r = isRecord(x) ? x : {}
  return {
    app_id: str(r.app_id) || '',
    name: str(r.name),
    kind: str(r.kind) || 'process',
    running: r.running === true,
    pid: num(r.pid),
    mem_rss: num(r.mem_rss),
    responding: toResponsiveness(r.responding),
    closable: r.closable === true,
  }
}

export function toAppList(x: unknown): AppStatus[] {
  const r = isRecord(x) ? x : {}
  return Array.isArray(r.apps) ? r.apps.map(toAppStatus) : []
}

// ── actions ─────────────────────────────────────────────────────────────────

export type SignalMode = 'quit' | 'force'

/**
 * signalProcess ends a process.
 *
 * `start` is REQUIRED by the server and is passed from the row the user
 * selected. If it is missing the request is not sent at all — a kill request
 * carrying only a pid is a request to signal whatever now holds that number.
 */
export async function signalProcess(
  p: Pick<ProcessInfo, 'pid' | 'start'>,
  mode: SignalMode,
): Promise<Result<Record<string, unknown>>> {
  if (typeof p.start !== 'number') {
    return {
      ok: false,
      error: {
        status: 0,
        message: 'this process has no recorded start time, so it cannot be ended safely — refresh the list',
      },
    }
  }
  return postJSON('/api/system/processes/signal', { pid: p.pid, start: p.start, mode })
}

export function closeApp(appID: string, force: boolean): Promise<Result<Record<string, unknown>>> {
  return postJSON('/api/proc/apps/close', { app_id: appID, force })
}
