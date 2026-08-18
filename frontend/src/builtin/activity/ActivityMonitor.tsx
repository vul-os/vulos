import { useState, useEffect, useRef, useCallback, type Dispatch, type SetStateAction, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { useTelemetry } from '../../core/useTelemetry'
import { useAuth } from '../../auth/AuthProvider'
import {
  loadJSON, toProcesses, toNetConns, toAppList, signalProcess, closeApp, processKey,
  type ProcessInfo, type NetConn, type AppStatus, type ApiFailure,
  type Responsiveness, type SignalMode,
} from './api'

const HISTORY_LEN = 120

function fmtBytes(b: number | undefined): string {
  if (!b || b <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0, v = b
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i > 0 ? 1 : 0)} ${units[i]}`
}

// ── narrow-untrusted-JSON helpers ───────────────────────────────────────────
// useTelemetry()'s `stats` is `unknown` at the trust boundary (matches the
// isRecord() pattern in src/lib/offlineAuth.ts and the fuller normalize*()
// helpers in src/builtin/drive/Drive.tsx) — narrowed here rather than trusted.
// The fetched payloads are narrowed in ./api alongside their error handling.
function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// TelemetryStats — the fields this monitor reads off useTelemetry()'s live WS
// payload. Mirrors (and extends, for the network/disk/battery detail rows
// this view also renders) the narrower TelemetryStats in
// src/core/settings/BoxHealthPanel.tsx.
interface TelemetryStats {
  cpu?: number
  mem_percent?: number
  mem_used?: number
  mem_total?: number
  mem_cached?: number
  swap_used?: number
  net_rx?: number
  net_tx?: number
  disk_read?: number
  disk_write?: number
  disk_used?: number
  disk_total?: number
  disk_percent?: number
  num_cpu?: number
  load_avg?: string
  uptime?: string
  hostname?: string
  temp?: number
  battery?: number
  charging?: boolean
}

function toTelemetryStats(x: unknown): TelemetryStats | null {
  if (!isRecord(x)) return null
  return {
    cpu: typeof x.cpu === 'number' ? x.cpu : undefined,
    mem_percent: typeof x.mem_percent === 'number' ? x.mem_percent : undefined,
    mem_used: typeof x.mem_used === 'number' ? x.mem_used : undefined,
    mem_total: typeof x.mem_total === 'number' ? x.mem_total : undefined,
    mem_cached: typeof x.mem_cached === 'number' ? x.mem_cached : undefined,
    swap_used: typeof x.swap_used === 'number' ? x.swap_used : undefined,
    net_rx: typeof x.net_rx === 'number' ? x.net_rx : undefined,
    net_tx: typeof x.net_tx === 'number' ? x.net_tx : undefined,
    disk_read: typeof x.disk_read === 'number' ? x.disk_read : undefined,
    disk_write: typeof x.disk_write === 'number' ? x.disk_write : undefined,
    disk_used: typeof x.disk_used === 'number' ? x.disk_used : undefined,
    disk_total: typeof x.disk_total === 'number' ? x.disk_total : undefined,
    disk_percent: typeof x.disk_percent === 'number' ? x.disk_percent : undefined,
    num_cpu: typeof x.num_cpu === 'number' ? x.num_cpu : undefined,
    load_avg: typeof x.load_avg === 'string' ? x.load_avg : undefined,
    uptime: typeof x.uptime === 'string' ? x.uptime : undefined,
    hostname: typeof x.hostname === 'string' ? x.hostname : undefined,
    temp: typeof x.temp === 'number' ? x.temp : undefined,
    battery: typeof x.battery === 'number' ? x.battery : undefined,
    charging: typeof x.charging === 'boolean' ? x.charging : undefined,
  }
}

// HistoryPoint — one ~3s-cadence sample kept for the sparkline graphs (see
// HISTORY_LEN above), derived from TelemetryStats each time a WS frame lands.
interface HistoryPoint {
  cpu: number
  mem: number
  rx: number
  tx: number
  disk_r: number
  disk_w: number
  t: number
}

// ProcessSortKey — the ProcessTable columns that support click-to-sort.
type ProcessSortKey = 'pid' | 'name' | 'user' | 'state' | 'cpu' | 'mem_rss' | 'threads'

// GraphId — the four sparkline cards; also the `expanded` selection state.
type GraphId = 'cpu' | 'memory' | 'network' | 'disk'

interface GraphDetail {
  label: string
  value: string | number
}

/**
 * UNKNOWN is what a card shows in place of a number it does not have.
 *
 * # Why `0` is the worst possible placeholder here
 *
 * Every value on these cards LOOKS like a measurement, because on a working
 * box it is one. `0` is therefore not a neutral default and not a smaller kind
 * of "unknown" — it is a specific, confident, and reassuring claim: an idle
 * CPU, a box with no processes, a machine with no network connections.
 *
 * It is also stated at exactly the moment it is most wrong. `stats` is null
 * until the telemetry socket delivers a frame, and the socket is most likely
 * to be down when the box is in trouble — which is the moment a user opens
 * this app. So a wedged box pegged at 100% rendered "CPU 0%", and a user
 * reading that concludes the box is fine and the app is broken, or worse, that
 * whatever they came to investigate is not the problem.
 *
 * This is the defect class this repo hits most often: a surface stating a
 * measured-looking value it never measured. The rule enforced below is that a
 * derived value is known only when EVERY input it derives from is known.
 */
const UNKNOWN = '—'

/** A percentage, or UNKNOWN when the reading is absent. */
function pctOf(v: number | undefined): string {
  return typeof v === 'number' ? `${Math.round(v)}%` : UNKNOWN
}

/** A byte count, or UNKNOWN when the reading is absent. */
function bytesOf(v: number | undefined): string {
  return typeof v === 'number' ? fmtBytes(v) : UNKNOWN
}

/** A per-second byte rate, or UNKNOWN when the reading is absent. */
function rateOf(v: number | undefined): string {
  return typeof v === 'number' ? `${fmtBytes(v)}/s` : UNKNOWN
}

/**
 * The sum of two readings as a rate — UNKNOWN unless BOTH are present.
 *
 * Not "sum what we have": a total that silently counts one direction of
 * traffic is a wrong number rather than a partial one, and nothing on screen
 * distinguishes it from a correct one.
 */
function sumRateOf(a: number | undefined, b: number | undefined): string {
  return typeof a === 'number' && typeof b === 'number' ? `${fmtBytes(a + b)}/s` : UNKNOWN
}

/** A count derived from a fetched list — UNKNOWN unless that feed is current. */
function countOf(known: boolean, n: number): string | number {
  return known ? n : UNKNOWN
}

interface GraphSpec {
  id: GraphId
  label: string
  value: string
  details: GraphDetail[]
  data: number[]
  color: string
  fill: string
  border: string
  glow: string
  autoScale?: boolean
}

type Tab = 'processes' | 'apps' | 'network'
const TABS: Tab[] = ['processes', 'apps', 'network']

/** What a confirmation dialog is about to do. */
interface PendingAction {
  kind: 'process' | 'app'
  mode: SignalMode
  label: string
  /** Set for kind === 'process'. */
  proc?: ProcessInfo
  /** Set for kind === 'app'. */
  appID?: string
  /**
   * True when this row's close ends a whole STREAMED SESSION rather than one
   * program. The server resolves app_id against the appnet launcher first and
   * falls through to stream.Pool.Stop, which tears down the display, the
   * window manager and everything drawing on it — so the dialog has to say
   * that, and cannot borrow the single-app wording.
   */
  session?: boolean
  /**
   * True when the reason for ending it is that the DISPLAY stopped answering.
   * The dialog then has one more thing to say, and it is the sentence this
   * whole feature turns on: ending the session does not repair this app,
   * because this app was never the thing that failed.
   */
  displayStalled?: boolean
}

export default function ActivityMonitor() {
  const { stats: rawStats, connected } = useTelemetry()
  const { profile } = useAuth()
  const stats = toTelemetryStats(rawStats)
  const [history, setHistory] = useState<HistoryPoint[]>([])
  const [processes, setProcesses] = useState<ProcessInfo[]>([])
  const [netConns, setNetConns] = useState<NetConn[]>([])
  const [apps, setApps] = useState<AppStatus[]>([])
  // One failure slot per feed. A single shared slot would let a healthy
  // network poll clear the message from a failing process poll, which is the
  // same class of bug as swallowing the error outright.
  const [procError, setProcError] = useState<ApiFailure | null>(null)
  const [netError, setNetError] = useState<ApiFailure | null>(null)
  const [appError, setAppError] = useState<ApiFailure | null>(null)
  // "Has this feed EVER produced a reading?" — distinct from "is it empty".
  // Before the first response `processes` is [] and `netConns` is [], and a
  // count taken from them is a statement about a box nobody has asked yet.
  const [procLoaded, setProcLoaded] = useState(false)
  const [netLoaded, setNetLoaded] = useState(false)
  const [expanded, setExpanded] = useState<GraphId | null>(null)
  const [tab, setTab] = useState<Tab>('processes')
  const [sortCol, setSortCol] = useState<ProcessSortKey>('cpu')
  const [sortAsc, setSortAsc] = useState(false)
  const [search, setSearch] = useState('')
  // The selection is an IDENTITY, not a pid. See processKey: a selection held
  // as a bare number silently re-aims at whatever inherits that number, and
  // the kill it then forms is internally consistent, so the server's
  // (pid, starttime) check has nothing to object to.
  const [selectedKey, setSelectedKey] = useState<string | null>(null)
  const [pending, setPending] = useState<PendingAction | null>(null)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<OutcomeReport | null>(null)

  // Keyed on rawStats — the WEBSOCKET PAYLOAD — not on the narrowed `stats`.
  //
  // `stats` is toTelemetryStats(rawStats), a fresh object built on every
  // render. As an effect dependency it is never Object.is-equal to the
  // previous one, so the effect ran after EVERY render, appended a sample,
  // and the state change re-rendered, which ran the effect again. A
  // self-sustaining loop, and the same shape as the ResizeObserver bug
  // documented on nextGraphSize below — which was found, fixed, and then
  // survived here in the component's other effect.
  //
  // It was visible in a screenshot only as graphs that looked a little wrong:
  // the 120-sample history saturated within about two seconds of opening the
  // app, so all four sparklines drew a full-width FLAT line at whatever the
  // current value happened to be. That reads as "a quiet box", not as a bug.
  //
  // rawStats is the parsed frame from the socket: a new object per MESSAGE and
  // stable between messages, which is precisely the cadence a sample should
  // follow.
  useEffect(() => {
    if (!isRecord(rawStats)) return
    const s = toTelemetryStats(rawStats)
    if (!s) return
    setHistory(prev => {
      const next = [...prev, {
        cpu: s.cpu || 0,
        mem: s.mem_percent || 0,
        rx: s.net_rx || 0,
        tx: s.net_tx || 0,
        disk_r: s.disk_read || 0,
        disk_w: s.disk_write || 0,
        t: Date.now(),
      }]
      return next.slice(-HISTORY_LEN)
    })
  }, [rawStats])

  // Poll processes, apps and network.
  //
  // Each result is applied through its OWN success/failure pair. The previous
  // version swallowed every failure with `.catch(() => {})` after calling
  // r.json() unconditionally — and because every /api/* service answers 5xx
  // with a body that parses cleanly, a dead backend produced a parsed error
  // object, an empty list, and this app's designed "No processes found" panel.
  // pollSeq makes a response's ORDER OF ARRIVAL irrelevant.
  //
  // Three requests go out every three seconds and nothing guaranteed they came
  // back in that order. A slow poll landing after a newer one overwrote fresh
  // rows with older ones, and the display then showed a past state of the box
  // as the present one — indefinitely, since nothing corrects it until the
  // next poll happens to be well-ordered.
  //
  // That is not cosmetic on this screen. The rows carry the (pid, starttime)
  // identity the kill path is built on, and a superseded listing can contain a
  // process that has since exited. Selecting from it produces a request the
  // server refuses — the safe outcome, but the user was shown a box that no
  // longer exists and given no reason for the refusal.
  //
  // Every response now names the poll it belongs to and is dropped unless it
  // is the newest. The counter is also bumped on unmount, which discards
  // in-flight responses instead of setting state on a component that is gone.
  const pollSeq = useRef(0)

  const poll = useCallback(() => {
    const seq = ++pollSeq.current
    const current = () => seq === pollSeq.current
    loadJSON('/api/system/processes', toProcesses).then(r => {
      if (!current()) return
      if (r.ok) { setProcesses(r.value); setProcError(null); setProcLoaded(true) } else { setProcError(r.error) }
    })
    loadJSON('/api/system/network', toNetConns).then(r => {
      if (!current()) return
      if (r.ok) { setNetConns(r.value); setNetError(null); setNetLoaded(true) } else { setNetError(r.error) }
    })
    loadJSON('/api/proc/apps', toAppList).then(r => {
      if (!current()) return
      if (r.ok) { setApps(r.value); setAppError(null) } else { setAppError(r.error) }
    })
  }, [])

  useEffect(() => {
    poll()
    const id = setInterval(poll, 3000)
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the rule guards against a ref to a DOM NODE going stale before cleanup. This ref is a counter, and reading its LATEST value at cleanup is the whole point: bumping it invalidates whichever poll is in flight at unmount. Copying it into a variable inside the effect, as the rule suggests, would capture the value at mount and invalidate nothing.
    return () => { clearInterval(id); pollSeq.current++ }
  }, [poll])

  // Plain function, not useCallback: it is passed to a dialog that only exists
  // while an action is pending, so nothing downstream is memoized on it, and
  // the React Compiler handles the rest.
  const runPending = async () => {
    if (!pending) return
    setBusy(true)
    // An explicit branch, not a `kind === 'process' && proc` condition with an
    // app-close fallthrough. That form sent a malformed PROCESS action to
    // /api/proc/apps/close with an empty app_id — the wrong endpoint for the
    // wrong kind of target, silently, in the kill path. Unreachable today, but
    // the types allow it and a wrong-target dispatch is not a thing to leave
    // sitting one refactor away.
    let res
    if (pending.kind === 'process') {
      if (!pending.proc) {
        setBusy(false)
        setPending(null)
        setNotice({ tone: 'bad', text: `${pending.label}: this row lost its identity before the request was sent — refresh the list` })
        return
      }
      res = await signalProcess(pending.proc, pending.mode)
    } else {
      res = await closeApp(pending.appID || '', pending.mode === 'force')
    }
    setBusy(false)
    setPending(null)
    if (res.ok) {
      setNotice(describeOutcome(pending.label, res.value))
      setSelectedKey(null)
    } else {
      setNotice({ tone: 'bad', text: `${pending.label}: ${res.error.message}` })
      // A 409 means the pid the user selected now belongs to a DIFFERENT
      // process. The server says "refresh the list" precisely because a retry
      // is the wrong response, so the selection is dropped here rather than
      // left armed over the stranger that inherited the number. Without this
      // the buttons stay enabled against a row the server just refused, and
      // the second click — with the stranger's own starttime now echoed back —
      // is the one that succeeds.
      if (res.error.code === 'identity_mismatch') setSelectedKey(null)
    }
    poll()
  }

  // NOT gated on `connected`.
  //
  // This used to return a full-window "Connecting to system telemetry..."
  // spinner whenever the WebSocket was down, which made the whole app unusable
  // for a reason that affects only the four graphs. Processes, apps and
  // network arrive over ordinary HTTP and are unaffected — and the moment a
  // user most wants a process list is when something on the box has stopped
  // answering, which is also when the telemetry socket is most likely to be
  // the thing that stopped. The one screen that could have told them what was
  // wrong was the screen that refused to render.
  //
  // The graphs say so for themselves below; the tables carry on.
  // A fetched count is only current while its feed is. `procError !== null`
  // means the last attempt failed, so the rows still in state are stale — and
  // a stale count presented as a live one is the same claim as a fabricated
  // one. The tab labels already show "(—)" on a failed feed; these agree.
  const procKnown = procLoaded && procError === null
  const netKnown = netLoaded && netError === null

  // Ending a process is admin-only, and the box enforces that — POST
  // /api/system/processes/signal and /api/proc/apps/close both answer 403 to
  // anyone else. What follows is an AFFORDANCE, not a second gate: it decides
  // what to OFFER, never what to allow. Deleting it would cost no security and
  // would restore a UI that invites the most dangerous action in the OS,
  // accepts a confirmation for it, and only then reports that it was never
  // permitted.
  //
  // It fails toward OFFERING. `profile` is null while the session is loading
  // and AuthProfile types `role` as unknown, so the control is withdrawn only
  // when the answer is positively "not an admin". Guessing the other way would
  // show a real admin dead buttons for as long as the profile takes to arrive,
  // and a dead button with no explanation is indistinguishable from a broken
  // feature — while guessing this way costs a non-admin nothing worse than the
  // 403 they would have got anyway.
  const role = typeof profile?.role === 'string' ? profile.role : null
  const mayEnd = role === null || role === 'admin'
  // Matching on the full key is what makes a recycled pid DESELECT rather than
  // re-aim. When the number is handed to a new process the key no longer
  // matches, find() misses, and the action bar falls back to "Select a process
  // to end it" instead of quietly pointing at a stranger.
  const selected = processes.find(p => processKey(p) === selectedKey) || null

  const graphsById: Record<GraphId, GraphSpec> = {
    cpu: {
      id: 'cpu', label: 'CPU', value: pctOf(stats?.cpu),
      details: [
        { label: 'Usage', value: pctOf(stats?.cpu) },
        { label: 'Cores', value: stats?.num_cpu ?? UNKNOWN },
        { label: 'Load', value: stats?.load_avg || UNKNOWN },
        { label: 'Threads', value: countOf(procKnown, processes.reduce((sum, p) => sum + (p.threads || 0), 0)) },
        { label: 'Processes', value: countOf(procKnown, processes.length) },
      ],
      data: history.map(h => h.cpu),
      color: '#3b82f6', fill: 'rgba(59,130,246,0.12)',
      border: 'border-blue-500/20', glow: 'from-blue-500/5',
    },
    memory: {
      id: 'memory', label: 'Memory', value: pctOf(stats?.mem_percent),
      details: [
        { label: 'Used', value: bytesOf(stats?.mem_used) },
        { label: 'Total', value: bytesOf(stats?.mem_total) },
        // Free is DERIVED, so it needs both operands. The old form defaulted
        // each to 0 and rendered "0 B" free — which reads as a box out of
        // memory, the opposite of the "no reading yet" it actually meant.
        {
          label: 'Free',
          value: typeof stats?.mem_total === 'number' && typeof stats?.mem_used === 'number'
            ? fmtBytes(stats.mem_total - stats.mem_used)
            : UNKNOWN,
        },
        { label: 'Swap', value: bytesOf(stats?.swap_used) },
        { label: 'Cached', value: bytesOf(stats?.mem_cached) },
      ],
      data: history.map(h => h.mem),
      color: '#a855f7', fill: 'rgba(168,85,247,0.12)',
      border: 'border-purple-500/20', glow: 'from-purple-500/5',
    },
    network: {
      id: 'network', label: 'Network', value: sumRateOf(stats?.net_rx, stats?.net_tx),
      details: [
        { label: 'Receiving', value: rateOf(stats?.net_rx) },
        { label: 'Sending', value: rateOf(stats?.net_tx) },
        { label: 'Connections', value: countOf(netKnown, netConns.length) },
        { label: 'Listening', value: countOf(netKnown, netConns.filter(c => c.state === 'LISTEN').length) },
        { label: 'Established', value: countOf(netKnown, netConns.filter(c => c.state === 'ESTABLISHED').length) },
      ],
      data: history.map(h => (h.rx || 0) + (h.tx || 0)),
      color: '#22c55e', fill: 'rgba(34,197,94,0.12)',
      border: 'border-green-500/20', glow: 'from-green-500/5',
      autoScale: true,
    },
    disk: {
      id: 'disk', label: 'Disk', value: sumRateOf(stats?.disk_read, stats?.disk_write),
      details: [
        { label: 'Read', value: rateOf(stats?.disk_read) },
        { label: 'Write', value: rateOf(stats?.disk_write) },
        { label: 'Used', value: bytesOf(stats?.disk_used) },
        { label: 'Total', value: bytesOf(stats?.disk_total) },
        { label: 'Usage', value: pctOf(stats?.disk_percent) },
      ],
      data: history.map(h => (h.disk_r || 0) + (h.disk_w || 0)),
      color: '#f59e0b', fill: 'rgba(245,158,11,0.12)',
      border: 'border-amber-500/20', glow: 'from-amber-500/5',
      autoScale: true,
    },
  }
  const graphs: GraphSpec[] = [graphsById.cpu, graphsById.memory, graphsById.network, graphsById.disk]

  const expandedGraph = expanded ? graphsById[expanded] : null
  const otherGraphs = expanded ? graphs.filter(g => g.id !== expanded) : []

  const tabError = tab === 'processes' ? procError : tab === 'apps' ? appError : netError
  const tabCount = tab === 'processes' ? processes.length : tab === 'apps' ? apps.length : netConns.length

  // `relative` on the root: the confirmation overlay is absolute inset-0 and
  // must be contained by THIS app, not by whatever positioned ancestor the
  // window manager happens to provide. It looked right either way, which is
  // the kind of accident that stops being right when the frame changes.
  return (
    <div className="relative flex flex-col h-full bg-neutral-950 text-neutral-100 overflow-hidden" data-app="activity">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-2.5 shrink-0 border-b border-neutral-800/40">
        {/* neutral-400, not neutral-500. The header sits on bg-neutral-950,
            which is #0a0a0a on dark, and neutral-500 measured 4.18:1 there —
            under the 4.5 floor for the hostname, temperature, battery and
            uptime, all of which are real copy a user reads. The neutral scale
            is remapped per theme, so one class holds in both. */}
        <div className="flex items-center gap-3 min-w-0">
          <h1 className="text-sm font-semibold tracking-tight shrink-0">Activity Monitor</h1>
          <span className="text-[12px] text-neutral-400 font-mono truncate">{stats?.hostname || ''}</span>
        </div>
        <div className="flex items-center gap-3 shrink-0">
          {(stats?.temp ?? 0) > 0 && (
            <span className="text-[12px] text-neutral-400 font-mono hidden sm:inline">{Math.round(stats?.temp ?? 0)}{'°'}C</span>
          )}
          {(stats?.battery ?? -1) >= 0 && (
            <span className="text-[12px] text-neutral-400 font-mono hidden sm:inline">{stats?.battery}%{stats?.charging ? ' +' : ''}</span>
          )}
          <span className="text-[12px] text-neutral-400 font-mono">up {stats?.uptime || '—'}</span>
          <span className={`w-1.5 h-1.5 rounded-full ${connected ? 'bg-emerald-500' : 'bg-red-500'}`} />
        </div>
      </div>

      {!connected && (
        <div className="shrink-0 px-3 pt-3">
          <div
            role="status"
            className="rounded-md border px-3 py-2 text-[12px]"
            style={{ borderColor: 'var(--status-warning)', color: 'var(--text-primary)' }}
          >
            Live resource graphs are not updating — the telemetry connection to your box is down.
            The lists below are fetched separately and are still current.
          </div>
        </div>
      )}

      {/* Graphs section.
          data-samples is a TEST SEAM, and it exists because the defect it
          guards is invisible in a screenshot except as a graph that looks
          slightly wrong: a saturated history renders as a full-width flat
          line, which is exactly what a quiet box also looks like. Exposing
          the count makes "the history filled up in two seconds" assertable. */}
      <div className="shrink-0 p-3 pb-0" data-samples={history.length}>
        {!expanded ? (
          /* Default: 4 compact graph cards — 2×2 on mobile, single row on desktop */
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 auto-rows-[118px] sm:auto-rows-auto sm:h-[140px]">
            {graphs.map(g => (
              <GraphCard
                key={g.id}
                label={g.label} value={g.value}
                data={g.data}
                color={g.color} colorFill={g.fill}
                borderColor={g.border} bgGlow={g.glow}
                autoScale={g.autoScale}
                compact
                onClick={() => setExpanded(g.id)}
              />
            ))}
          </div>
        ) : (
          /* Expanded: main graph large, other 3 small below */
          <div className="flex flex-col gap-2">
            <div style={{ height: 180 }}>
              {expandedGraph && (
                <GraphCard
                  label={expandedGraph.label} value={expandedGraph.value}
                  details={expandedGraph.details}
                  data={expandedGraph.data}
                  color={expandedGraph.color} colorFill={expandedGraph.fill}
                  borderColor={expandedGraph.border} bgGlow={expandedGraph.glow}
                  autoScale={expandedGraph.autoScale}
                  onClick={() => setExpanded(null)}
                  expanded
                />
              )}
            </div>
            <div className="grid grid-cols-3 gap-2" style={{ height: 80 }}>
              {otherGraphs.map(g => (
                <GraphCard
                  key={g.id}
                  label={g.label} value={g.value}
                  data={g.data}
                  color={g.color} colorFill={g.fill}
                  borderColor={g.border} bgGlow={g.glow}
                  autoScale={g.autoScale}
                  compact
                  onClick={() => setExpanded(g.id)}
                />
              ))}
            </div>
          </div>
        )}
      </div>

      {/* Tabs + search */}
      {/* Stacked below sm.
          Side by side, the filter input took a fixed 96px off a 390px row and
          the Network tab rendered as "Network (6" with its closing bracket
          under the input. It was still reachable by scrolling the tab strip,
          which is exactly why it read as a rendering fault rather than a
          layout one — the control worked and looked broken. */}
      <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2 px-4 pt-3 pb-1.5 shrink-0">
        <div className="flex items-center gap-0.5 min-w-0 overflow-x-auto">
          {TABS.map(t => (
            <button
              key={t}
              onClick={() => setTab(t)}
              aria-pressed={tab === t}
              className={`px-3 py-1.5 text-[12px] font-medium rounded-md transition-colors whitespace-nowrap ${tab === t ? 'text-neutral-100 bg-neutral-800' : 'text-neutral-400 hover:text-neutral-200 hover:bg-neutral-800/40'}`}
              style={tab === t ? { boxShadow: 'inset 0 -2px 0 var(--accent)' } : undefined}
            >
              {/* A count of 0 beside a failed feed is a claim the app cannot
                  support — it is the same "no processes" lie in miniature. A
                  failed tab shows a dash instead. */}
              {t === 'processes' ? `Processes ${procError ? '(—)' : `(${processes.length})`}`
                : t === 'apps' ? `Apps ${appError ? '(—)' : `(${apps.length})`}`
                : `Network ${netError ? '(—)' : `(${netConns.length})`}`}
            </button>
          ))}
        </div>
        <input
          type="text" placeholder="Filter..." value={search}
          aria-label="Filter rows"
          onChange={e => setSearch(e.target.value)}
          className="bg-neutral-900 border border-neutral-700 rounded-md px-2.5 py-1.5 text-[12px] text-neutral-200 placeholder-neutral-400 w-full sm:w-40 shrink-0 outline-none focus:border-neutral-500 transition-colors"
        />
      </div>

      {/* Action bar — only on the Processes tab, where an action is possible */}
      {tab === 'processes' && (
        <ActionBar
          selected={selected}
          mayEnd={mayEnd}
          onQuit={() => selected && setPending({
            kind: 'process', mode: 'quit', proc: selected,
            label: `${selected.name || 'process'} (${selected.pid})`,
          })}
          onForce={() => selected && setPending({
            kind: 'process', mode: 'force', proc: selected,
            label: `${selected.name || 'process'} (${selected.pid})`,
          })}
        />
      )}

      {notice && (
        <div className="px-3 shrink-0">
          {/* Colour lives in the BORDER and the dot, never in the text.
              --status-success and --status-warning are 3.0:1 against this
              theme's light surfaces, so a coloured label here fails WCAG AA on
              light while looking fine on dark — which is how a contrast bug
              ships. The text uses --text-primary, which the theme derives
              against its own surfaces and holds at ~17:1 in both. */}
          {/* A failed or costly action is an ALERT, not a status. `status` is
              polite: a screen reader finishes what it is saying and may never
              announce it at all if focus moves. "I could not end that" and "I
              destroyed your unsaved work" are both interruptions by nature. */}
          <div
            role={notice.tone === 'ok' ? 'status' : 'alert'}
            className="flex items-start gap-2 rounded-md px-3 py-2 text-[12px] mb-1 border"
            style={{ borderColor: noticeTone(notice.tone), color: 'var(--text-primary)' }}
          >
            <span
              className="mt-1 inline-block w-1.5 h-1.5 rounded-full shrink-0"
              style={{ background: noticeTone(notice.tone) }}
              aria-hidden="true"
            />
            <span className="flex-1">{notice.text}</span>
            <button onClick={() => setNotice(null)} style={{ color: 'var(--text-secondary)' }} aria-label="Dismiss">×</button>
          </div>
        </div>
      )}

      {/* List */}
      <div className="flex-1 min-h-0 px-3 pb-3">
        {tabError ? (
          <FeedError error={tabError} onRetry={poll} />
        ) : tab === 'processes' ? (
          <ProcessTable
            processes={processes} search={search}
            sortCol={sortCol} setSortCol={setSortCol}
            sortAsc={sortAsc} setSortAsc={setSortAsc}
            selectedKey={selectedKey} setSelectedKey={setSelectedKey}
          />
        ) : tab === 'apps' ? (
          <AppTable
            apps={apps} search={search} mayEnd={mayEnd}
            onClose={(a, mode) => setPending({
              kind: 'app', mode, appID: a.app_id, label: a.name || a.app_id,
              session: a.kind === 'stream',
              displayStalled: a.responding.status === 'display_not_responding',
            })}
          />
        ) : (
          <NetworkTable conns={netConns} search={search} />
        )}
        {tabError === null && tabCount === 0 && tab !== 'network' && null}
      </div>

      {pending && (
        <ConfirmDialog
          pending={pending}
          busy={busy}
          onCancel={() => setPending(null)}
          onConfirm={runPending}
        />
      )}
    </div>
  )
}

/* ── Failure panel ── */
/**
 * FeedError is what the user sees INSTEAD of an empty table when a feed fails.
 *
 * The distinction it exists to preserve: "your box is running nothing" and
 * "this app could not read your box" are different sentences, and only one of
 * them is ever true. The previous version could only say the first.
 */
function FeedError({ error, onRetry }: { error: ApiFailure; onRetry: () => void }) {
  return (
    <div
      className="flex flex-col items-center justify-center h-full gap-3 rounded-lg border p-6 text-center"
      style={{ borderColor: 'var(--status-danger)' }}
    >
      <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Could not read this from your box</span>
      <span className="text-neutral-300 text-[12px] max-w-md">{error.message}</span>
      <span className="text-neutral-400 text-[12px] font-mono">
        {error.status > 0 ? `HTTP ${error.status}` : 'no response'}{error.code ? ` · ${error.code}` : ''}
      </span>
      <button
        onClick={onRetry}
        className="mt-1 px-3 py-1.5 text-[12px] rounded-md border border-neutral-600 text-neutral-200 hover:bg-neutral-800 transition-colors"
      >
        Try again
      </button>
    </div>
  )
}

/* ── Action bar ── */
function ActionBar({ selected, mayEnd, onQuit, onForce }: {
  selected: ProcessInfo | null
  mayEnd: boolean
  onQuit: () => void
  onForce: () => void
}) {
  // Four reasons a row cannot be ended, and each gets its own sentence. A
  // single disabled button with no explanation reads as a broken feature.
  //
  // The permission check comes FIRST because it is the only one that does not
  // depend on the selection: telling a non-admin to "select a process to end
  // it" walks them into an action the box will refuse.
  const reason = !mayEnd
    ? 'Only an administrator can end processes on this box'
    : !selected
      ? 'Select a process to end it'
      : selected.protected
        ? selected.protected_reason || 'this process is protected'
        : typeof selected.start !== 'number'
          ? 'no start time recorded for this process — refresh the list'
          : null
  const canAct = selected !== null && reason === null

  return (
    <div className="flex items-center gap-2 px-4 pb-2 shrink-0 flex-wrap">
      <button
        onClick={onQuit}
        disabled={!canAct}
        title={reason || 'Ask the process to exit (SIGTERM), then force it after 5 seconds'}
        className="px-3 py-1.5 text-[12px] font-medium rounded-md border border-neutral-600 text-neutral-100 enabled:hover:bg-neutral-800 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
      >
        Quit
      </button>
      <button
        onClick={onForce}
        disabled={!canAct}
        title={reason || 'End it immediately (SIGKILL). Unsaved work is lost.'}
        className="px-3 py-1.5 text-[12px] font-medium rounded-md border disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
        style={{ borderColor: 'var(--status-danger)', color: 'var(--text-primary)' }}
      >
        Force Quit
      </button>
      <span className="text-[12px] text-neutral-400 min-w-0 truncate">
        {reason ?? `${selected?.name} (${selected?.pid})`}
      </span>
    </div>
  )
}

/* ── Confirm dialog ── */
/**
 * The last thing standing between a click and a SIGKILL, so it has to behave
 * like a dialog and not merely look like one.
 *
 * It declared `aria-modal="true"` and enforced none of what that promises.
 * What that cost, concretely:
 *
 *   - Focus stayed wherever it was — on the Quit button in the action bar,
 *     behind the overlay. A keyboard user pressing Enter to "confirm" instead
 *     re-triggered Quit, and a screen-reader user was never told a dialog had
 *     opened at all, because nothing moved focus into it.
 *   - Tab walked straight out of the dialog and through the process table
 *     underneath, which `aria-modal` had already told assistive technology to
 *     ignore. The user could reach controls that were being announced as
 *     absent.
 *   - Escape did nothing. Escape is how every other dialog in this OS is
 *     dismissed, and the one place it was missing is the one place where the
 *     alternative to dismissing is destroying someone's unsaved work.
 *   - On cancel, focus was dropped to the document body, so the next Tab
 *     restarted from the top of the app rather than from the row the user had
 *     been working with.
 *
 * All four are handled below. The confirm button takes initial focus rather
 * than Cancel: this dialog is only ever reached by a deliberate click on an
 * already-disabled-unless-valid control, and the destructive button is the one
 * the user came for — but Escape and Cancel are both one keystroke away, which
 * is what makes that safe.
 */
function ConfirmDialog({ pending, busy, onCancel, onConfirm }: {
  pending: PendingAction
  busy: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  const force = pending.mode === 'force'
  const session = pending.session === true
  const panelRef = useRef<HTMLDivElement>(null)
  const confirmRef = useRef<HTMLButtonElement>(null)
  // Captured at mount, restored on unmount: whatever had focus when the dialog
  // opened is where the user was, and it is where they should be returned to
  // however the dialog closes.
  const returnTo = useRef<HTMLElement | null>(null)

  useEffect(() => {
    returnTo.current = document.activeElement as HTMLElement | null
    confirmRef.current?.focus()
    const restore = returnTo.current
    return () => {
      // Only if it is still in the document — the row the user came from may
      // have been unmounted by the poll that ran after the kill.
      if (restore && document.contains(restore)) restore.focus()
    }
  }, [])

  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      if (!busy) onCancel()
      return
    }
    if (e.key !== 'Tab') return
    // The focus trap. Querying on each keypress rather than caching: the
    // dialog's own buttons become disabled while a kill is in flight, and a
    // cached list would keep offering a control that is no longer focusable.
    const focusable = panelRef.current?.querySelectorAll<HTMLElement>(
      'button:not([disabled]), [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
    )
    if (!focusable || focusable.length === 0) return
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault()
      first.focus()
    }
  }

  return (
    <div
      className="absolute inset-0 z-50 flex items-center justify-center p-4"
      style={{ background: 'var(--overlay)' }}
      role="dialog"
      aria-modal="true"
      onKeyDown={onKeyDown}
      aria-label={session ? 'Confirm end session' : force ? 'Confirm force quit' : 'Confirm quit'}
    >
      <div ref={panelRef} className="w-full max-w-sm rounded-xl border border-neutral-700 bg-neutral-900 p-4 shadow-2xl">
        <h2 className="text-sm font-semibold text-neutral-100">
          {session ? 'End session' : force ? 'Force quit' : 'Quit'} {pending.label}?
        </h2>
        <p className="mt-2 text-[12px] text-neutral-300 leading-relaxed">
          {session
            ? 'The display, the window manager and everything drawing on it stop at once. Anything unsaved is lost, and the session will not come back on its own — it has to be started again.'
            : force
              ? 'It will be ended immediately and will not get a chance to save. Anything unsaved is lost.'
              : 'It will be asked to exit and given 5 seconds to finish up. If it has not exited by then it will be ended.'}
        </p>
        {/* The sentence this whole feature turns on, and the reason it is here
            rather than on a hover: the user reached this dialog because a badge
            said something was not responding, and if they leave believing they
            have just repaired THIS APP they will conclude the feature does not
            work when the picture is still frozen. */}
        {pending.displayStalled && (
          <p
            className="mt-2 text-[12px] leading-relaxed"
            style={{ color: 'var(--status-warning-text)' }}
          >
            This will not repair {pending.label}: the screen it draws on is what stopped
            answering, and every app in the session is equally silent. Ending the session
            clears the stuck display — it is not a verdict on this app.
          </p>
        )}
        <div className="mt-4 flex justify-end gap-2">
          <button
            onClick={onCancel}
            disabled={busy}
            className="px-3 py-1.5 text-[12px] rounded-md border border-neutral-600 text-neutral-200 enabled:hover:bg-neutral-800 disabled:opacity-40 transition-colors"
          >
            Cancel
          </button>
          {/* A SOLID fill, not a tint, and a fixed pair.
              A destructive confirmation is the one control that must not lose
              its emphasis to a theme, and a translucent red tint composites
              differently over a dark and a light surface — the same pair that
              measured 5.6:1 on dark measured 1.4:1 on light. #b91c1c with
              white is 6.47:1 regardless of what is behind it, because nothing
              shows through. */}
          <button
            ref={confirmRef}
            onClick={onConfirm}
            disabled={busy}
            className="px-3 py-1.5 text-[12px] font-medium rounded-md transition-colors disabled:opacity-50 border"
            style={force
              ? { background: '#b91c1c', borderColor: '#b91c1c', color: '#ffffff' }
              : { borderColor: 'var(--border-emphasis)', color: 'var(--text-primary)' }}
          >
            {busy ? 'Working…' : session ? 'End session' : force ? 'Force quit' : 'Quit'}
          </button>
        </div>
      </div>
    </div>
  )
}

/* ── Process Table ── */
interface ProcessTableProps {
  processes: ProcessInfo[]
  search: string
  sortCol: ProcessSortKey
  setSortCol: Dispatch<SetStateAction<ProcessSortKey>>
  sortAsc: boolean
  setSortAsc: Dispatch<SetStateAction<boolean>>
  selectedKey: string | null
  setSelectedKey: Dispatch<SetStateAction<string | null>>
}

function ProcessTable({
  processes, search, sortCol, setSortCol, sortAsc, setSortAsc, selectedKey, setSelectedKey,
}: ProcessTableProps) {
  const handleSort = (col: ProcessSortKey) => {
    if (sortCol === col) setSortAsc(!sortAsc)
    else { setSortCol(col); setSortAsc(false) }
  }

  const filtered = processes.filter(p => {
    if (!search) return true
    const q = search.toLowerCase()
    return p.name?.toLowerCase().includes(q) || p.command?.toLowerCase().includes(q) || String(p.pid).includes(q) || p.user?.toLowerCase().includes(q)
  })

  const sorted = [...filtered].sort(compareProcesses(sortCol, sortAsc))

  // Roving-tabindex focus position. Kept as an index into `sorted` rather than
  // an identity, because it is about WHERE THE CURSOR IS in the list on screen,
  // not about which process is selected — those are different things, and
  // conflating them is what makes a list jump when it re-sorts.
  const [focusIdx, setFocusIdx] = useState(0)
  const rowRefs = useRef<(HTMLDivElement | null)[]>([])

  const moveFocus = (to: number) => {
    const i = Math.max(0, Math.min(sorted.length - 1, to))
    setFocusIdx(i)
    rowRefs.current[i]?.focus()
  }

  const onRowKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>, i: number, k: string) => {
    switch (e.key) {
      case 'ArrowDown': e.preventDefault(); moveFocus(i + 1); break
      case 'ArrowUp': e.preventDefault(); moveFocus(i - 1); break
      case 'Home': e.preventDefault(); moveFocus(0); break
      case 'End': e.preventDefault(); moveFocus(sorted.length - 1); break
      case 'Enter':
      case ' ':
        e.preventDefault()
        setSelectedKey(prev => (prev === k ? null : k))
        break
    }
  }

  const cols: { key: ProcessSortKey, label: string, w: string, align: string }[] = [
    { key: 'pid', label: 'PID', w: '55px', align: '' },
    { key: 'name', label: 'Process Name', w: '1fr', align: '' },
    { key: 'user', label: 'User', w: '70px', align: '' },
    { key: 'state', label: 'State', w: '95px', align: '' },
    { key: 'cpu', label: 'CPU %', w: '60px', align: 'text-right' },
    { key: 'mem_rss', label: 'Memory', w: '70px', align: 'text-right' },
    { key: 'threads', label: 'Threads', w: '50px', align: 'text-right' },
  ]
  const gridTemplate = cols.map(c => c.w).join(' ')

  return (
    <div className="flex flex-col h-full min-h-0 rounded-lg border border-neutral-700 bg-neutral-900/40 overflow-hidden">
      {/* Scroll region — horizontal on narrow screens, vertical always */}
      <div className="flex-1 min-h-0 overflow-auto">
        {/*
          role="grid", and the rows below are role="row" INSIDE it.
          A bare role="row" was already there with no table, grid or rowgroup
          above it. That is not untidy markup, it is invalid: a row outside a
          grid has nowhere to belong, so assistive technology cannot report
          position ("row 4 of 312"), cannot expose the column a cell sits in,
          and aria-selected — which this listing sets on every row — is only
          defined for a row within a grid or treegrid. The selection state that
          decides which process is about to be killed was being announced by
          nothing.

          grid rather than table because these rows are SELECTABLE and
          interactive; a plain table's rows support neither.
        */}
        <div role="grid" aria-label="Processes" aria-rowcount={sorted.length} className="min-w-[590px]">
          {/* Header */}
          <div role="row" className="grid gap-2 px-3 py-1.5 text-[12px] uppercase tracking-wider text-neutral-400 border-b border-neutral-700 sticky top-0 z-10 bg-neutral-900 backdrop-blur-sm" style={{ gridTemplateColumns: gridTemplate }}>
            {cols.map(c => (
              <span
                key={c.key}
                role="columnheader"
                aria-sort={sortCol === c.key ? (sortAsc ? 'ascending' : 'descending') : 'none'}
              >
                <button
                  type="button"
                  aria-label={`Sort by ${c.label}`}
                  className={`cursor-pointer select-none hover:text-neutral-200 transition-colors text-left w-full ${sortCol === c.key ? 'text-neutral-100' : ''} ${c.align}`}
                  onClick={() => handleSort(c.key)}
                >
                  {c.label}{sortCol === c.key ? (sortAsc ? ' ▲' : ' ▼') : ''}
                </button>
              </span>
            ))}
          </div>
          {/* Rows */}
          {sorted.length === 0 && (
            <div className="text-xs text-neutral-400 p-6 text-center">
              {processes.length === 0
                ? 'No processes reported. A box always has processes, so this usually means the list has not loaded yet.'
                : 'No processes match this filter'}
            </div>
          )}
          {sorted.map((p, i) => {
            const k = processKey(p)
            // The React key is the identity too, not the pid. Keyed on pid, a
            // recycled number reuses the same DOM node and the row simply
            // changes its text under the highlight; keyed on identity, React
            // unmounts the old row and mounts a new one, so the change is a
            // change rather than a substitution.
            const locked = p.protected === true
            const why = p.protected_reason || 'this process is protected'
            return (
            <div
              key={k}
              role="row"
              aria-rowindex={i + 1}
              // Roving tabindex: exactly ONE row is a tab stop, and the arrow
              // keys move between them. Every row carried tabIndex={0} before,
              // which on a box running three hundred processes meant three
              // hundred presses of Tab to get past this table — the keyboard
              // equivalent of the list being unusable.
              tabIndex={i === focusIdx ? 0 : -1}
              ref={el => { rowRefs.current[i] = el }}
              aria-selected={selectedKey === k}
              onFocus={() => setFocusIdx(i)}
              onClick={() => setSelectedKey(prev => (prev === k ? null : k))}
              onKeyDown={e => onRowKeyDown(e, i, k)}
              className={`grid gap-2 items-center px-3 py-1 text-[12px] border-b border-neutral-800/40 cursor-pointer transition-colors ${
                selectedKey === k ? 'bg-neutral-800' : 'hover:bg-neutral-800/40'
              }`}
              style={gridStyle(gridTemplate, selectedKey === k)}
            >
              <span role="gridcell" className="text-neutral-400 font-mono">{p.pid}</span>
              <span role="gridcell" className="text-neutral-200 truncate flex items-center gap-1.5 min-w-0" title={p.command}>
                <span className="truncate">{p.name}</span>
                {/*
                  A protected row said nothing until it was selected, so the
                  only way to discover that init or the Vulos server cannot be
                  ended was to pick it and read the disabled button's excuse.
                  The mark is a LOCK GLYPH plus an accessible name, not a
                  colour: colour alone is not information, and this listing is
                  read by people who cannot see it at all.
                */}
                {locked && (
                  <span
                    className="shrink-0 text-neutral-400"
                    title={why}
                    aria-label={`Protected: ${why}`}
                  >
                    🔒
                  </span>
                )}
              </span>
              <span role="gridcell" className="text-neutral-400 truncate">{p.user}</span>
              <span role="gridcell"><StateIndicator state={p.state} /></span>
              <span role="gridcell" className="text-right font-mono text-neutral-300">{Number(p.cpu) < 0.1 ? '0.0' : p.cpu?.toFixed(1)}</span>
              <span role="gridcell" className="text-right text-neutral-400 font-mono">{fmtBytes(p.mem_rss)}</span>
              <span role="gridcell" className="text-right text-neutral-400">{p.threads}</span>
            </div>
            )
          })}
        </div>
      </div>
      {/* Footer */}
      <div className="flex items-center justify-between gap-2 px-3 py-1.5 text-[12px] text-neutral-400 border-t border-neutral-700 shrink-0 bg-neutral-900/80">
        <span>{sorted.length} process{sorted.length !== 1 ? 'es' : ''}</span>
        <span className="truncate">Total threads: {sorted.reduce((s, p) => s + (p.threads || 0), 0)}</span>
      </div>
    </div>
  )
}

function gridStyle(gridTemplateColumns: string, selected: boolean) {
  return selected
    ? { gridTemplateColumns, boxShadow: 'inset 2px 0 0 var(--accent)' }
    : { gridTemplateColumns }
}

/**
 * StateIndicator shows the kernel's state letter for a process, expanded, with
 * the honest caveat on hover.
 *
 * It deliberately does NOT say "Not Responding". A process state is not an
 * event-loop measurement: a program pinning a core is running and responsive,
 * and one at 0% blocked on a lock is neither. The state most often mistaken
 * for frozen is D — uninterruptible sleep — which is usually a stalled disk or
 * a wedged network mount rather than an application fault at all, and is also
 * the one state where Force Quit genuinely cannot work, because the signal is
 * not delivered until the I/O returns. So D gets a warning colour and an
 * explanation, not a verdict.
 */
function StateIndicator({ state }: { state?: string }) {
  const meta: Record<string, { dot: string; note: string }> = {
    running: { dot: 'bg-emerald-500', note: 'Running on a CPU' },
    sleeping: { dot: 'bg-blue-400', note: 'Waiting on an event — the normal state for an idle program' },
    'disk sleep': { dot: 'bg-amber-400', note: 'Uninterruptible sleep: blocked in the kernel, usually waiting on storage. Signals are not delivered until it returns, so it cannot be force-quit.' },
    zombie: { dot: 'bg-red-500', note: 'Already exited, waiting for its parent to collect it. It cannot be killed again.' },
    stopped: { dot: 'bg-neutral-400', note: 'Suspended by a signal or a debugger — not hung' },
    idle: { dot: 'bg-neutral-500', note: 'Idle kernel task' },
  }
  const m = meta[state ?? ''] || { dot: 'bg-neutral-500', note: `Kernel state ${state ?? '?'}` }
  return (
    <span className="flex items-center gap-1 min-w-0" title={m.note}>
      <span className={`inline-block w-1.5 h-1.5 rounded-full shrink-0 ${m.dot}`} />
      <span className="text-[12px] truncate">{state}</span>
    </span>
  )
}

/* ── App Table ── */
/**
 * AppTable is the "not responding" surface, and the reason it is a separate
 * tab from Processes: only SOME of what this box runs can be asked whether it
 * is servicing its event loop, and the answer must carry how it was reached.
 */
function AppTable({ apps, search, mayEnd, onClose }: {
  apps: AppStatus[]
  search: string
  mayEnd: boolean
  onClose: (a: AppStatus, mode: SignalMode) => void
}) {
  const filtered = apps.filter(a => {
    if (!search) return true
    const q = search.toLowerCase()
    return a.app_id.toLowerCase().includes(q) || (a.name || '').toLowerCase().includes(q) || a.kind.includes(q)
  })
  const cols: { label: string, align?: string }[] = [
    { label: 'App' }, { label: 'Kind' }, { label: 'Responding' },
    { label: 'Memory', align: 'text-right' }, { label: '' },
  ]
  // 240px for the verdict, not 150. The session-stalled row carries a sentence
  // under its badge saying the app may be fine, and that sentence is the whole
  // point of the state — at 150px it wrapped to four lines and pushed the row
  // to twice the height of its neighbours.
  const gridTemplate = '1fr 90px 240px 80px 140px'

  return (
    <div className="flex flex-col h-full min-h-0 rounded-lg border border-neutral-700 bg-neutral-900/40 overflow-hidden">
      <div className="flex-1 min-h-0 overflow-auto">
        <div className="min-w-[660px]">
          <div className="grid gap-2 px-3 py-1.5 text-[12px] uppercase tracking-wider text-neutral-400 border-b border-neutral-700 sticky top-0 z-10 bg-neutral-900" style={{ gridTemplateColumns: gridTemplate }}>
            {cols.map((c, i) => <span key={i} className={c.align}>{c.label}</span>)}
          </div>
          {filtered.length === 0 && (
            <div className="text-xs text-neutral-400 p-6 text-center">
              No apps are running on this box right now.
            </div>
          )}
          {filtered.map(a => <AppRow key={a.app_id} a={a} gridTemplate={gridTemplate} mayEnd={mayEnd} onClose={onClose} />)}
        </div>
      </div>
      <div className="px-3 py-1.5 text-[12px] text-neutral-400 border-t border-neutral-700 shrink-0 bg-neutral-900/80">
        Built-in windows are not listed: they run inside this browser tab, so the box cannot probe them.
      </div>
    </div>
  )
}

/**
 * AppRow is one app, and the row where a wrong verb does real damage.
 *
 * # The two buttons that were the same button
 *
 * A `stream` row's app_id is a STREAM SESSION id. The server resolves it
 * against the appnet launcher first, fails, and falls through to
 * stream.Pool.Stop — which ends the display, the window manager and everything
 * drawing on it. `force` shortens only the appnet path's grace window, so both
 * "Close" and "Force" reached the identical call. Two controls that do the
 * same thing while implying different amounts of violence is a lie about the
 * box, so a streamed row now offers ONE action, named for what it does.
 *
 * # Why it is not disabled when the display has stalled
 *
 * Force-quitting cannot fix a stalled display, and the tempting fix is to grey
 * the control out. That is worse: ending the session IS the only remedy this
 * box can currently reach for a wedged X server — it is half of the restart —
 * and a disabled button with no alternative reads as a broken feature. So the
 * action stays live and the CONSEQUENCE is spelled out where the user cannot
 * miss it, in the confirmation, rather than being implied by a badge colour.
 *
 * The affordance this row still wants is "Restart session": stop, then relaunch
 * with the same command and geometry. It is deliberately NOT built here as a
 * stop-then-launch pair from the client, which would be a parallel path that
 * loses the command line, the env and the owner. It needs one endpoint in
 * services/stream; see the report accompanying this change.
 */
function AppRow({ a, gridTemplate, mayEnd, onClose }: {
  a: AppStatus
  gridTemplate: string
  mayEnd: boolean
  onClose: (a: AppStatus, mode: SignalMode) => void
}) {
  const session = a.kind === 'stream'
  const stalled = a.responding.status === 'display_not_responding'
  // The app's own name, for the accessible names below. Every one of these
  // buttons reads "Close" or "Force" as its label, so a screen-reader user
  // moving through the list heard "button, button, button" beside a column of
  // names they could not associate with them — on controls that destroy work.
  // The visible label stays short; the accessible name carries the target.
  const who = a.name || a.app_id
  const denied = 'Only an administrator can end apps on this box'
  return (
    <div className="grid gap-2 items-center px-3 py-1.5 text-[12px] border-b border-neutral-800/40" style={{ gridTemplateColumns: gridTemplate }}>
      <span className="text-neutral-200 truncate">{who}</span>
      <span className="text-neutral-400">{a.kind}</span>
      <RespBadge r={a.responding} />
      <span className="text-right text-neutral-400 font-mono">{a.mem_rss ? fmtBytes(a.mem_rss) : '—'}</span>
      <span className="flex justify-end gap-1.5">
        {a.closable && (session ? (
          <button
            onClick={() => onClose(a, 'force')}
            disabled={!mayEnd}
            aria-label={`End the session running ${who}`}
            title={!mayEnd ? denied : stalled
              ? 'Ends the whole session. It will not repair this app — the display it draws on is what stopped answering.'
              : 'Ends the whole session: the display, the window manager and everything drawing on it.'}
            className="px-2 py-1 text-[12px] rounded border transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
            style={{ borderColor: 'var(--status-danger)', color: 'var(--text-primary)' }}
          >
            End session
          </button>
        ) : (
          <>
            <button
              onClick={() => onClose(a, 'quit')}
              disabled={!mayEnd}
              aria-label={`Close ${who}`}
              title={!mayEnd ? denied : 'Ask it to exit, then end it if it does not'}
              className="px-2 py-1 text-[12px] rounded border border-neutral-600 text-neutral-100 enabled:hover:bg-neutral-800 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
            >
              Close
            </button>
            <button
              onClick={() => onClose(a, 'force')}
              disabled={!mayEnd}
              aria-label={`Force quit ${who}`}
              title={!mayEnd ? denied : 'End it immediately. Unsaved work is lost.'}
              className="px-2 py-1 text-[12px] rounded border transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
              style={{ borderColor: 'var(--status-danger)', color: 'var(--text-primary)' }}
            >
              Force
            </button>
          </>
        ))}
      </span>
    </div>
  )
}

/** How one responsiveness answer is worded and toned. */
export interface RespPresentation {
  /** The tone token. Border and dot ONLY — never the text; see the notice
   *  comment above for why a coloured label fails AA on the light theme. */
  tone: string
  /** The badge label. */
  label: string
  /** A visible sentence beside the badge, or '' when the label says it all. */
  note: string
  /** The token the note is drawn in. --status-*-text, never --status-*. */
  noteTone: string
}

/**
 * describeResponding turns one answer into the words a user reads.
 *
 * Exported, and a pure function, because the WORDING is the part of this
 * feature that can be wrong while every pixel renders: a badge that quietly
 * implies "this app is broken" when the display stalled teaches the user to
 * distrust the badge, and no layout or contrast gate can see that.
 *
 * The five answers, and what each one has to convey:
 *
 *  responding              this app's own event loop answered. Plain.
 *  not_responding          THIS APP was asked and stayed silent while its
 *                          display kept answering. The one state that may
 *                          blame the app, and the only one shown in danger.
 *  display_not_responding  the DISPLAY stopped answering. Every app in that
 *                          session is equally silent and the one being looked
 *                          at may be perfectly healthy, so the label names the
 *                          SESSION and the note says the app may be fine.
 *                          Warning, not danger: this is not a verdict on the
 *                          row it sits on.
 *  unknown                 nobody answered because nobody could be asked. Two
 *                          different reasons reach it and they are NOT the
 *                          same fact, so they do not get the same sentence:
 *                          `x11_ping` means the display answered, its windows
 *                          were enumerated, and none of them implement
 *                          _NET_WM_PING — there was nothing to ask. Any other
 *                          method means Vulos has no mechanism at all here
 *                          (the Wayland path, a session that is not running).
 *                          Flattening those into one badge would present
 *                          "asked and got silence" and "nobody to ask" as the
 *                          same measurement, which is the mistake the whole
 *                          Status type exists to prevent.
 *  not_applicable          the question is a category error.
 *
 * A status this build does not know arrives here as `unknown` carrying its
 * original name (see toResponsiveness), and is worded as the box knowing
 * something this version does not — which is true, and is not a verdict.
 */
/** What the user is told after the server accepted a signal request. */
export interface OutcomeReport { tone: 'ok' | 'warn' | 'bad'; text: string }

/**
 * noticeTone maps a tone to a token. Colour lives in the BORDER and the dot,
 * never in the text — --status-warning is around 3:1 against this theme's
 * light surfaces, so a coloured label would fail WCAG AA on light while
 * looking fine on dark, which is how a contrast bug ships. The text uses
 * --text-primary, which the theme derives against its own surfaces.
 */
function noticeTone(tone: OutcomeReport['tone']): string {
  return tone === 'ok' ? 'var(--status-success)'
    : tone === 'warn' ? 'var(--status-warning)'
      : 'var(--status-danger)'
}

/**
 * describeOutcome turns a signal result into a sentence about WHAT WAS SENT.
 *
 * # The escalation is the part worth saying
 *
 * A quit request is a sequence, not a flag: SIGTERM, wait out the grace
 * period, then SIGKILL if the process is still there. The server performs that
 * sequence and reports it honestly — `signals` comes back as ["SIGTERM"] or
 * ["SIGTERM","SIGKILL"], with `elapsed_ms` alongside.
 *
 * The previous version read `outcome` and threw `signals` away, so a user who
 * clicked **Quit** on an editor that ignored SIGTERM was told, five seconds
 * later, `notes (4242): killed`. Every word of that is true and the important
 * one is missing: the polite request was refused and the box destroyed unsaved
 * work to carry it out. That is the single fact a person needs in order to know
 * whether to expect their document back, and it was already on the wire.
 *
 * So an escalation says so, and says how long it waited. It is `warn` rather
 * than `ok` because the outcome differs from what was asked for, and rather
 * than `bad` because the process did end — the action succeeded, expensively.
 *
 * A force quit that used SIGKILL alone is `ok`: it did exactly what the user
 * chose, having been told in the dialog what that costs.
 */
/**
 * compareProcesses builds the process table's comparator.
 *
 * # The old one was not an ordering
 *
 * It ran missing values through `Number()`, and `Number(undefined)` is NaN.
 * Every comparison against NaN is false, so both `<` and `>` fell through and
 * the comparator returned 0 — "these are equal". That makes a row with no
 * value equal to EVERY row, while the rows it is equal to are not equal to
 * each other:
 *
 *     cmp(noThreads, 1) === 0      // "equal"
 *     cmp(noThreads, 900) === 0    // "equal"
 *     cmp(1, 900) === -1           // but these are not
 *
 * That is intransitive, and a comparator that is not a consistent ordering has
 * no defined result: V8's sort is free to produce a different arrangement for
 * the same rows depending on the order they arrived in, which on a list that
 * re-polls every three seconds means the table can reshuffle under the
 * pointer. On this screen the row under the pointer is the one about to be
 * killed.
 *
 * It was also asymmetric. The branch was chosen by the type of the LEFT
 * operand only, so a string-vs-number pair compared as strings in one
 * direction and as numbers in the other, and cmp(a,b) and cmp(b,a) could agree
 * on a sign instead of opposing.
 *
 * # What replaces it
 *
 * A total order, in three tiers:
 *
 *  1. Rows WITHOUT a value for the sorted column sort last, in both
 *     directions. They form one equivalence class at the end rather than a
 *     value that compares equal to everything everywhere — which is what makes
 *     the result transitive. Keeping them last regardless of direction is the
 *     same convention as SQL's NULLS LAST, and it is the useful one: reversing
 *     "CPU" should swap the busy and idle processes, not bury them under rows
 *     that reported nothing.
 *  2. Present values compare by type — strings case-insensitively, numbers
 *     numerically — with the direction applied.
 *  3. Ties break on pid, which is unique per listing. This is what makes the
 *     output depend only on the SET of rows and not on the order they were
 *     fetched in, so the assertion in the tests can be about the ordering
 *     itself rather than about one lucky example.
 */
// eslint-disable-next-line react-refresh/only-export-components -- compareProcesses is exported so TRANSITIVITY can be asserted as a property over every triple. That is the defect the previous comparator had, and it is invisible in any single sorted example a component test could inspect.
export function compareProcesses(
  col: ProcessSortKey,
  asc: boolean,
): (a: ProcessInfo, b: ProcessInfo) => number {
  // "Missing" covers undefined, null and NaN. NaN is the one that mattered:
  // it is a number by type and unordered by behaviour, so it has to be
  // classified here rather than left to the comparisons below.
  const missing = (v: unknown): boolean =>
    v === undefined || v === null || (typeof v === 'number' && Number.isNaN(v))

  return (a, b) => {
    const va = a[col]
    const vb = b[col]
    const ma = missing(va)
    const mb = missing(vb)
    if (ma && mb) return a.pid - b.pid
    if (ma) return 1
    if (mb) return -1

    let d = 0
    if (typeof va === 'string' || typeof vb === 'string') {
      // Both sides coerced the SAME way, so the branch cannot depend on which
      // operand happens to be on the left.
      d = String(va).toLowerCase().localeCompare(String(vb).toLowerCase())
    } else {
      d = Number(va) - Number(vb)
    }
    if (d !== 0) return asc ? d : -d
    // The tie-break is NOT reversed with the sort direction. Reversing it
    // would make the two directions disagree about the order of equal rows,
    // which is a reshuffle the user reads as movement that did not happen.
    return a.pid - b.pid
  }
}

// eslint-disable-next-line react-refresh/only-export-components -- describeOutcome is exported so the ESCALATION REPORT can be asserted directly. What the user is told after a kill is the half of this feature that can be wrong while every pixel renders, and a test that re-implements the wording proves nothing about the wording that ships.
export function describeOutcome(label: string, v: Record<string, unknown>): OutcomeReport {
  const outcome = typeof v.outcome === 'string' ? v.outcome : 'done'
  const signals = Array.isArray(v.signals)
    ? v.signals.filter((s): s is string => typeof s === 'string')
    : []
  const sent = signals.length > 0 ? signals.join(', then ') : 'a signal'
  const ms = typeof v.elapsed_ms === 'number' ? v.elapsed_ms : null
  const waited = ms !== null && ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : ms !== null ? `${ms}ms` : null

  // OUTCOMES ARE NOT ALL SUCCESS. `survived` means the signal was delivered
  // and the process is STILL there — a task in uninterruptible sleep cannot be
  // killed until its I/O returns. Reporting that as a win would leave the user
  // believing they fixed a stuck disk.
  if (outcome === 'survived') {
    return {
      tone: 'bad',
      text: `${label} could not be ended — ${sent} was delivered and it is still there, blocked in the kernel (state ${String(v.state ?? '?')}), usually waiting on storage. Signals are not delivered until that returns.`,
    }
  }
  if (outcome === 'already_gone') {
    return { tone: 'ok', text: `${label} had already exited before the request was sent.` }
  }
  if (outcome === 'terminated') {
    return {
      tone: 'ok',
      text: `${label} was asked to quit (SIGTERM) and exited${waited ? ` in ${waited}` : ''}. It had the chance to save.`,
    }
  }
  if (outcome === 'killed') {
    const escalated = signals.includes('SIGTERM') && signals.includes('SIGKILL')
    if (escalated) {
      return {
        tone: 'warn',
        text: `${label} ignored the request to quit, so it was force-ended: SIGTERM, then SIGKILL${waited ? ` after ${waited}` : ''}. Unsaved work in it is lost.`,
      }
    }
    return { tone: 'ok', text: `${label} was force-ended immediately (SIGKILL). Unsaved work in it is lost.` }
  }
  return { tone: 'ok', text: `${label}: ${outcome.replace(/_/g, ' ')}` }
}

// eslint-disable-next-line react-refresh/only-export-components -- describeResponding is exported so the WORDING can be asserted directly. It is the half of this feature that can be wrong while everything renders, and a test that re-implements it proves nothing.
export function describeResponding(r: Responsiveness): RespPresentation {
  switch (r.status) {
    case 'responding':
      return { tone: 'var(--status-success)', label: 'Responding', note: '', noteTone: 'var(--text-secondary)' }
    case 'not_responding':
      return { tone: 'var(--status-danger)', label: 'Not responding', note: '', noteTone: 'var(--text-secondary)' }
    case 'display_not_responding':
      return {
        tone: 'var(--status-warning)',
        label: 'Session not responding',
        note: 'This app may be fine — the session it runs in stopped answering.',
        noteTone: 'var(--status-warning-text)',
      }
    case 'not_applicable':
      return { tone: 'var(--border-default)', label: 'Not applicable', note: '', noteTone: 'var(--text-secondary)' }
    default:
      return {
        tone: 'var(--border-emphasis)',
        label: 'Cannot tell',
        note: r.unrecognised
          ? 'Your box reported something this version does not understand.'
          : r.method === 'x11_ping'
            ? 'This app offers no way to be asked.'
            : '',
        noteTone: 'var(--text-secondary)',
      }
  }
}

/**
 * RespBadge renders a responsiveness answer WITHOUT flattening it.
 *
 * Five states and five appearances. The colour is the border and the dot, and
 * the words carry the meaning — a badge that guesses is worse than no badge,
 * because it is a confident claim about the one thing the user came here to
 * find out.
 */
function RespBadge({ r }: { r: Responsiveness }) {
  const l = describeResponding(r)
  const hover = r.detail ? `${r.detail} (method: ${r.method})` : `method: ${r.method}`
  return (
    <span className="flex flex-col gap-0.5 min-w-0">
      <span
        className="inline-flex items-center gap-1.5 rounded px-1.5 py-0.5 border text-[12px] w-fit"
        style={{ borderColor: l.tone, color: 'var(--text-primary)' }}
        title={hover}
      >
        <span className="inline-block w-1.5 h-1.5 rounded-full shrink-0" style={{ background: l.tone }} aria-hidden="true" />
        {l.label}
      </span>
      {l.note && (
        <span className="text-[12px] leading-snug" style={{ color: l.noteTone }}>{l.note}</span>
      )}
    </span>
  )
}

/* ── Network Table ── */
function NetworkTable({ conns, search }: { conns: NetConn[], search: string }) {
  const filtered = conns.filter(c => {
    if (!search) return true
    const q = search.toLowerCase()
    return c.proto?.includes(q) || c.local_addr?.includes(q) || c.remote_addr?.includes(q) ||
           String(c.local_port).includes(q) || (c.process || '').toLowerCase().includes(q) ||
           c.state?.toLowerCase().includes(q)
  })

  const cols: { key: string, label: string, w: string }[] = [
    { key: 'proto', label: 'Protocol', w: '76px' },
    { key: 'local', label: 'Local Address', w: '1fr' },
    { key: 'remote', label: 'Remote Address', w: '1fr' },
    { key: 'state', label: 'State', w: '118px' },
    { key: 'pid', label: 'PID', w: '60px' },
    { key: 'process', label: 'Process', w: '1fr' },
  ]
  const gridTemplate = cols.map(c => c.w).join(' ')

  return (
    <div className="flex flex-col h-full min-h-0 rounded-lg border border-neutral-700 bg-neutral-900/40 overflow-hidden">
      <div className="flex-1 min-h-0 overflow-auto">
        <div className="min-w-[620px]">
          <div className="grid gap-2 px-3 py-1.5 text-[12px] uppercase tracking-wider text-neutral-400 border-b border-neutral-700 sticky top-0 z-10 bg-neutral-900 backdrop-blur-sm" style={{ gridTemplateColumns: gridTemplate }}>
            {cols.map(c => <span key={c.key}>{c.label}</span>)}
          </div>
          {filtered.length === 0 && (
            <div className="text-xs text-neutral-400 p-6 text-center">No connections</div>
          )}
          {filtered.map((c, i) => (
            <div key={i} className="grid gap-2 items-center px-3 py-1 text-[12px] border-b border-neutral-800/40 hover:bg-neutral-800/40 transition-colors" style={{ gridTemplateColumns: gridTemplate }}>
              <span className="text-neutral-400 font-mono uppercase">{c.proto}</span>
              <span className="text-neutral-200 font-mono truncate">{c.local_addr}:{c.local_port}</span>
              <span className="text-neutral-400 font-mono truncate">
                {c.remote_addr === '0.0.0.0' && c.remote_port === 0 ? '*' : `${c.remote_addr}:${c.remote_port}`}
              </span>
              {/* Same rule as the badges: the colour is a dot, the label is
                  --text-primary. Tailwind's emerald-300/blue-300/amber-300 are
                  pale by design and were never remapped for the light theme —
                  only the neutral scale is — so they measured fine on dark and
                  would have failed on light. */}
              <span className="text-[12px] flex items-center gap-1.5 min-w-0" style={{ color: 'var(--text-primary)' }}>
                <span
                  className="inline-block w-1.5 h-1.5 rounded-full shrink-0"
                  style={{ background:
                    c.state === 'ESTABLISHED' ? 'var(--status-success)'
                    : c.state === 'LISTEN' ? 'var(--accent)'
                    : c.state === 'TIME_WAIT' ? 'var(--status-warning)'
                    : 'var(--text-ghost)' }}
                  aria-hidden="true"
                />
                <span className="truncate">{c.state}</span>
              </span>
              {/* A socket with no owning process shows —, not a stand-in
                  number. This column used to carry the socket INODE, which on
                  a busy box looks exactly like a plausible pid. */}
              <span className="text-neutral-400 font-mono">{c.pid ? c.pid : '—'}</span>
              <span className="text-neutral-400 truncate">{c.process || '—'}</span>
            </div>
          ))}
        </div>
      </div>
      <div className="flex items-center justify-between px-3 py-1.5 text-[12px] text-neutral-400 border-t border-neutral-700 shrink-0 bg-neutral-900/80">
        <span>{filtered.length} connection{filtered.length !== 1 ? 's' : ''}</span>
        <span>
          {filtered.filter(c => c.state === 'LISTEN').length} listening,{' '}
          {filtered.filter(c => c.state === 'ESTABLISHED').length} established
        </span>
      </div>
    </div>
  )
}

/* ── Graph Card ── */
interface GraphCardProps {
  label: string
  value: string
  details?: GraphDetail[]
  data: number[]
  color: string
  colorFill: string
  borderColor: string
  bgGlow: string
  autoScale?: boolean
  compact?: boolean
  expanded?: boolean
  onClick: () => void
}

function GraphCard({ label, value, details, data, color, colorFill, borderColor, bgGlow, autoScale, compact, expanded, onClick }: GraphCardProps) {
  return (
    <div
      onClick={onClick}
      className={`flex flex-col rounded-xl border ${borderColor} bg-gradient-to-b ${bgGlow} to-transparent overflow-hidden cursor-pointer transition-all hover:brightness-110 h-full`}
    >
      <div className={`flex ${expanded ? 'gap-6' : 'flex-col'} h-full`}>
        {/* Info side */}
        <div className={`flex flex-col ${expanded ? 'w-44 shrink-0 p-3 justify-center' : 'px-3 pt-2.5 pb-1 shrink-0'}`}>
          <span className="text-[12px] text-neutral-400 uppercase tracking-widest font-medium">{label}</span>
          <span className={`font-semibold text-neutral-100 leading-tight ${expanded ? 'text-2xl mt-1' : compact ? 'text-base' : 'text-xl'}`}>{value}</span>
          {expanded && details && (
            <div className="mt-3 space-y-1">
              {details.map(d => (
                <div key={d.label} className="flex justify-between text-[12px]">
                  <span className="text-neutral-400">{d.label}</span>
                  <span className="text-neutral-200 font-mono">{d.value}</span>
                </div>
              ))}
            </div>
          )}
        </div>
        {/* Graph side */}
        <div className={`flex-1 min-h-0 min-w-0 ${expanded ? 'pr-3 py-3' : 'px-2 pb-2'}`}>
          <AreaGraph data={data} color={color} fill={colorFill} autoScale={autoScale} />
        </div>
      </div>
    </div>
  )
}

/* ── Area Graph ── */
/**
 * nextGraphSize reconciles an observed size against the current one, returning
 * the PREVIOUS OBJECT UNCHANGED when nothing moved.
 *
 * The identity matters, not just the values: React bails out of a state update
 * only when the next value is Object.is-equal to the current one. Returning a
 * fresh `{ w, h }` each time — which this did — guarantees a re-render on every
 * ResizeObserver callback, and since the re-render resizes the observed SVG,
 * the observer fires again. Four graphs, four self-sustaining loops.
 *
 * Exported so the regression is testable: a test can assert the same reference
 * comes back, which is the exact property that stops the loop.
 */
// eslint-disable-next-line react-refresh/only-export-components -- nextGraphSize is exported so a test can exercise the REAL identity-preserving comparison that stops the resize loop, rather than a re-implementation of it.
export function nextGraphSize(prev: { w: number, h: number }, width: number, height: number): { w: number, h: number } {
  return prev.w === width && prev.h === height ? prev : { w: width, h: height }
}

function AreaGraph({ data, color, fill, autoScale }: { data: number[], color: string, fill: string, autoScale?: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  const [size, setSize] = useState({ w: 200, h: 80 })

  // Only set state when the size ACTUALLY changed.
  //
  // This used to call setSize({ w, h }) on every observation. A fresh object is
  // never Object.is-equal to the previous one, so React could not bail out and
  // re-rendered on every callback; the re-render resized the SVG, which fed the
  // observer again — a self-sustaining render loop, one per graph, four graphs.
  //
  // It did not merely burn CPU. It starved the main thread badly enough that
  // OTHER windows' React.lazy chunks never finished committing, so they sat on
  // their "Loading..." Suspense fallback indefinitely. That is the long-standing
  // blank-window bug: it looked like a race, but it never resolved with time,
  // and it followed whichever window was opened alongside Activity Monitor. Two
  // windows shipped empty in a screenshot because of it.
  useEffect(() => {
    if (!ref.current) return
    const ro = new ResizeObserver(entries => {
      const { width, height } = entries[0].contentRect
      if (width <= 0 || height <= 0) return
      setSize(prev => nextGraphSize(prev, width, height))
    })
    ro.observe(ref.current)
    return () => ro.disconnect()
  }, [])

  const { w, h } = size
  const padTop = 2, padBot = 2
  const graphH = h - padTop - padBot
  const maxVal = autoScale ? Math.max(...data, 1) : 100
  const pts: [number, number][] = []

  // Newest sample at the RIGHT edge, history extending left, which is how
  // every system monitor scrolls and what a reader expects "now" to mean.
  //
  // Left-anchored, a freshly opened window drew its handful of samples as a
  // stub in the leftmost few percent with the rest of the card blank, which
  // reads as a broken graph rather than as a graph that has not filled yet.
  // The empty region is still honest — it is time this window has not
  // observed — it is now on the side where "the past" belongs.
  for (let i = 0; i < data.length; i++) {
    const x = w - ((data.length - 1 - i) / (HISTORY_LEN - 1)) * w
    const y = padTop + graphH - (Math.min(data[i], maxVal) / maxVal) * graphH
    pts.push([x, y])
  }

  const linePath = smoothPath(pts)
  const areaPath = pts.length >= 2
    ? linePath + ` L ${pts[pts.length - 1][0]},${h} L ${pts[0][0]},${h} Z`
    : ''

  const gridLines = [25, 50, 75].map(pct => padTop + graphH - (pct / 100) * graphH)

  return (
    <div ref={ref} className="w-full h-full">
      <svg width={w} height={h} className="block">
        {gridLines.map((y, i) => (
          <line key={i} x1={0} y1={y} x2={w} y2={y} stroke="var(--graph-grid)" strokeWidth={1} strokeOpacity={0.7} />
        ))}
        {pts.length >= 2 && (
          <>
            <path d={areaPath} fill={fill} />
            <path d={linePath} fill="none" stroke={color} strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round" />
          </>
        )}
        {pts.length > 0 && (
          <circle cx={pts[pts.length - 1][0]} cy={pts[pts.length - 1][1]} r={2.5} fill={color} />
        )}
      </svg>
    </div>
  )
}

function smoothPath(pts: [number, number][]): string {
  if (pts.length < 2) return ''
  if (pts.length === 2) return `M ${pts[0][0]},${pts[0][1]} L ${pts[1][0]},${pts[1][1]}`
  let d = `M ${pts[0][0]},${pts[0][1]}`
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[Math.max(0, i - 1)]
    const p1 = pts[i]
    const p2 = pts[i + 1]
    const p3 = pts[Math.min(pts.length - 1, i + 2)]
    const cp1x = p1[0] + (p2[0] - p0[0]) / 6
    const cp1y = p1[1] + (p2[1] - p0[1]) / 6
    const cp2x = p2[0] - (p3[0] - p1[0]) / 6
    const cp2y = p2[1] - (p3[1] - p1[1]) / 6
    d += ` C ${cp1x},${cp1y} ${cp2x},${cp2y} ${p2[0]},${p2[1]}`
  }
  return d
}
