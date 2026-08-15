import { useState, useEffect, useRef, useCallback, type Dispatch, type SetStateAction } from 'react'
import { useTelemetry } from '../../core/useTelemetry'
import {
  loadJSON, toProcesses, toNetConns, toAppList, signalProcess, closeApp,
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
}

export default function ActivityMonitor() {
  const { stats: rawStats, connected } = useTelemetry()
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
  const [expanded, setExpanded] = useState<GraphId | null>(null)
  const [tab, setTab] = useState<Tab>('processes')
  const [sortCol, setSortCol] = useState<ProcessSortKey>('cpu')
  const [sortAsc, setSortAsc] = useState(false)
  const [search, setSearch] = useState('')
  const [selectedPid, setSelectedPid] = useState<number | null>(null)
  const [pending, setPending] = useState<PendingAction | null>(null)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<{ tone: 'ok' | 'bad'; text: string } | null>(null)

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
    // eslint-disable-next-line react-hooks/set-state-in-effect
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
  const poll = useCallback(() => {
    loadJSON('/api/system/processes', toProcesses).then(r => {
      if (r.ok) { setProcesses(r.value); setProcError(null) } else { setProcError(r.error) }
    })
    loadJSON('/api/system/network', toNetConns).then(r => {
      if (r.ok) { setNetConns(r.value); setNetError(null) } else { setNetError(r.error) }
    })
    loadJSON('/api/proc/apps', toAppList).then(r => {
      if (r.ok) { setApps(r.value); setAppError(null) } else { setAppError(r.error) }
    })
  }, [])

  useEffect(() => {
    poll()
    const id = setInterval(poll, 3000)
    return () => clearInterval(id)
  }, [poll])

  // Plain function, not useCallback: it is passed to a dialog that only exists
  // while an action is pending, so nothing downstream is memoized on it, and
  // the React Compiler handles the rest.
  const runPending = async () => {
    if (!pending) return
    setBusy(true)
    const res = pending.kind === 'process' && pending.proc
      ? await signalProcess(pending.proc, pending.mode)
      : await closeApp(pending.appID || '', pending.mode === 'force')
    setBusy(false)
    setPending(null)
    if (res.ok) {
      const outcome = typeof res.value.outcome === 'string' ? res.value.outcome : 'done'
      // OUTCOMES ARE NOT ALL SUCCESS. `survived` means the signal was
      // delivered and the process is still there — a task in uninterruptible
      // sleep cannot be killed until its I/O returns. Reporting that as a win
      // would leave the user believing they fixed a stuck disk.
      const survived = outcome === 'survived'
      setNotice({
        tone: survived ? 'bad' : 'ok',
        text: survived
          ? `${pending.label} could not be ended — it is blocked in the kernel (state ${String(res.value.state ?? '?')}), usually waiting on storage. Signals are not delivered until that returns.`
          : `${pending.label}: ${outcome.replace(/_/g, ' ')}`,
      })
      setSelectedPid(null)
    } else {
      setNotice({ tone: 'bad', text: `${pending.label}: ${res.error.message}` })
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
  const cpuVal = Math.round(stats?.cpu || 0)
  const memVal = Math.round(stats?.mem_percent || 0)
  const selected = processes.find(p => p.pid === selectedPid) || null

  const graphsById: Record<GraphId, GraphSpec> = {
    cpu: {
      id: 'cpu', label: 'CPU', value: `${cpuVal}%`,
      details: [
        { label: 'Usage', value: `${cpuVal}%` },
        { label: 'Cores', value: stats?.num_cpu || '—' },
        { label: 'Load', value: stats?.load_avg || '—' },
        { label: 'Threads', value: processes.reduce((sum, p) => sum + (p.threads || 0), 0) },
        { label: 'Processes', value: processes.length },
      ],
      data: history.map(h => h.cpu),
      color: '#3b82f6', fill: 'rgba(59,130,246,0.12)',
      border: 'border-blue-500/20', glow: 'from-blue-500/5',
    },
    memory: {
      id: 'memory', label: 'Memory', value: `${memVal}%`,
      details: [
        { label: 'Used', value: fmtBytes(stats?.mem_used) },
        { label: 'Total', value: fmtBytes(stats?.mem_total) },
        { label: 'Free', value: fmtBytes((stats?.mem_total || 0) - (stats?.mem_used || 0)) },
        { label: 'Swap', value: stats?.swap_used ? fmtBytes(stats.swap_used) : '—' },
        { label: 'Cached', value: stats?.mem_cached ? fmtBytes(stats.mem_cached) : '—' },
      ],
      data: history.map(h => h.mem),
      color: '#a855f7', fill: 'rgba(168,85,247,0.12)',
      border: 'border-purple-500/20', glow: 'from-purple-500/5',
    },
    network: {
      id: 'network', label: 'Network', value: fmtBytes((stats?.net_rx || 0) + (stats?.net_tx || 0)) + '/s',
      details: [
        { label: 'Receiving', value: fmtBytes(stats?.net_rx) + '/s' },
        { label: 'Sending', value: fmtBytes(stats?.net_tx) + '/s' },
        { label: 'Connections', value: netConns.length },
        { label: 'Listening', value: netConns.filter(c => c.state === 'LISTEN').length },
        { label: 'Established', value: netConns.filter(c => c.state === 'ESTABLISHED').length },
      ],
      data: history.map(h => (h.rx || 0) + (h.tx || 0)),
      color: '#22c55e', fill: 'rgba(34,197,94,0.12)',
      border: 'border-green-500/20', glow: 'from-green-500/5',
      autoScale: true,
    },
    disk: {
      id: 'disk', label: 'Disk', value: fmtBytes((stats?.disk_read || 0) + (stats?.disk_write || 0)) + '/s',
      details: [
        { label: 'Read', value: fmtBytes(stats?.disk_read) + '/s' },
        { label: 'Write', value: fmtBytes(stats?.disk_write) + '/s' },
        { label: 'Used', value: stats?.disk_used ? fmtBytes(stats.disk_used) : '—' },
        { label: 'Total', value: stats?.disk_total ? fmtBytes(stats.disk_total) : '—' },
        { label: 'Usage', value: stats?.disk_percent ? `${Math.round(stats.disk_percent)}%` : '—' },
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
          <div
            role="status"
            className="flex items-start gap-2 rounded-md px-3 py-2 text-[12px] mb-1 border"
            style={{
              borderColor: notice.tone === 'ok' ? 'var(--status-success)' : 'var(--status-danger)',
              color: 'var(--text-primary)',
            }}
          >
            <span
              className="mt-1 inline-block w-1.5 h-1.5 rounded-full shrink-0"
              style={{ background: notice.tone === 'ok' ? 'var(--status-success)' : 'var(--status-danger)' }}
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
            selectedPid={selectedPid} setSelectedPid={setSelectedPid}
          />
        ) : tab === 'apps' ? (
          <AppTable
            apps={apps} search={search}
            onClose={(a, mode) => setPending({
              kind: 'app', mode, appID: a.app_id, label: a.name || a.app_id,
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
function ActionBar({ selected, onQuit, onForce }: {
  selected: ProcessInfo | null
  onQuit: () => void
  onForce: () => void
}) {
  // Three reasons a row cannot be ended, and each gets its own sentence. A
  // single disabled button with no explanation reads as a broken feature.
  const reason = !selected
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
function ConfirmDialog({ pending, busy, onCancel, onConfirm }: {
  pending: PendingAction
  busy: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  const force = pending.mode === 'force'
  return (
    <div
      className="absolute inset-0 z-50 flex items-center justify-center p-4"
      style={{ background: 'var(--overlay)' }}
      role="dialog"
      aria-modal="true"
      aria-label={force ? 'Confirm force quit' : 'Confirm quit'}
    >
      <div className="w-full max-w-sm rounded-xl border border-neutral-700 bg-neutral-900 p-4 shadow-2xl">
        <h2 className="text-sm font-semibold text-neutral-100">
          {force ? 'Force quit' : 'Quit'} {pending.label}?
        </h2>
        <p className="mt-2 text-[12px] text-neutral-300 leading-relaxed">
          {force
            ? 'It will be ended immediately and will not get a chance to save. Anything unsaved is lost.'
            : 'It will be asked to exit and given 5 seconds to finish up. If it has not exited by then it will be ended.'}
        </p>
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
            onClick={onConfirm}
            disabled={busy}
            className="px-3 py-1.5 text-[12px] font-medium rounded-md transition-colors disabled:opacity-50 border"
            style={force
              ? { background: '#b91c1c', borderColor: '#b91c1c', color: '#ffffff' }
              : { borderColor: 'var(--border-emphasis)', color: 'var(--text-primary)' }}
          >
            {busy ? 'Working…' : force ? 'Force quit' : 'Quit'}
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
  selectedPid: number | null
  setSelectedPid: Dispatch<SetStateAction<number | null>>
}

function ProcessTable({
  processes, search, sortCol, setSortCol, sortAsc, setSortAsc, selectedPid, setSelectedPid,
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

  const sorted = [...filtered].sort((a, b) => {
    const rawA = a[sortCol]
    const rawB = b[sortCol]
    // Every sortable column is either always-string or always-number (plus
    // undefined when a row omits it) — never mixed. String columns compare
    // case-insensitively, missing values sorting as ''. Numeric columns run
    // through Number(), whose ToNumber(undefined) === NaN reproduces the
    // original untyped code's raw `va < vb` behaviour (any comparison against
    // undefined is false, so missing values never reorder relative to a
    // defined value on either side) without defaulting a missing value to 0.
    if (typeof rawA === 'string') {
      const va = rawA.toLowerCase()
      const vb = (typeof rawB === 'string' ? rawB : '').toLowerCase()
      if (va < vb) return sortAsc ? -1 : 1
      if (va > vb) return sortAsc ? 1 : -1
      return 0
    }
    const va = Number(rawA)
    const vb = Number(rawB)
    if (va < vb) return sortAsc ? -1 : 1
    if (va > vb) return sortAsc ? 1 : -1
    return 0
  })

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
        <div className="min-w-[590px]">
          {/* Header */}
          <div className="grid gap-2 px-3 py-1.5 text-[12px] uppercase tracking-wider text-neutral-400 border-b border-neutral-700 sticky top-0 z-10 bg-neutral-900 backdrop-blur-sm" style={{ gridTemplateColumns: gridTemplate }}>
            {cols.map(c => (
              <button
                key={c.key}
                type="button"
                aria-label={`Sort by ${c.label}`}
                className={`cursor-pointer select-none hover:text-neutral-200 transition-colors text-left ${sortCol === c.key ? 'text-neutral-100' : ''} ${c.align}`}
                onClick={() => handleSort(c.key)}
              >
                {c.label}{sortCol === c.key ? (sortAsc ? ' ▲' : ' ▼') : ''}
              </button>
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
          {sorted.map(p => (
            <div
              key={p.pid}
              role="row"
              tabIndex={0}
              aria-selected={selectedPid === p.pid}
              onClick={() => setSelectedPid(prev => (prev === p.pid ? null : p.pid))}
              onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setSelectedPid(prev => (prev === p.pid ? null : p.pid)) } }}
              className={`grid gap-2 items-center px-3 py-1 text-[12px] border-b border-neutral-800/40 cursor-pointer transition-colors ${
                selectedPid === p.pid ? 'bg-neutral-800' : 'hover:bg-neutral-800/40'
              }`}
              style={gridStyle(gridTemplate, selectedPid === p.pid)}
            >
              <span className="text-neutral-400 font-mono">{p.pid}</span>
              <span className="text-neutral-200 truncate" title={p.command}>{p.name}</span>
              <span className="text-neutral-400 truncate">{p.user}</span>
              <StateIndicator state={p.state} />
              <span className="text-right font-mono text-neutral-300">{Number(p.cpu) < 0.1 ? '0.0' : p.cpu?.toFixed(1)}</span>
              <span className="text-right text-neutral-400 font-mono">{fmtBytes(p.mem_rss)}</span>
              <span className="text-right text-neutral-400">{p.threads}</span>
            </div>
          ))}
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
function AppTable({ apps, search, onClose }: {
  apps: AppStatus[]
  search: string
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
  const gridTemplate = '1fr 90px 150px 80px 130px'

  return (
    <div className="flex flex-col h-full min-h-0 rounded-lg border border-neutral-700 bg-neutral-900/40 overflow-hidden">
      <div className="flex-1 min-h-0 overflow-auto">
        <div className="min-w-[560px]">
          <div className="grid gap-2 px-3 py-1.5 text-[12px] uppercase tracking-wider text-neutral-400 border-b border-neutral-700 sticky top-0 z-10 bg-neutral-900" style={{ gridTemplateColumns: gridTemplate }}>
            {cols.map((c, i) => <span key={i} className={c.align}>{c.label}</span>)}
          </div>
          {filtered.length === 0 && (
            <div className="text-xs text-neutral-400 p-6 text-center">
              No apps are running on this box right now.
            </div>
          )}
          {filtered.map(a => (
            <div key={a.app_id} className="grid gap-2 items-center px-3 py-1.5 text-[12px] border-b border-neutral-800/40" style={{ gridTemplateColumns: gridTemplate }}>
              <span className="text-neutral-200 truncate">{a.name || a.app_id}</span>
              <span className="text-neutral-400">{a.kind}</span>
              <RespBadge r={a.responding} />
              <span className="text-right text-neutral-400 font-mono">{a.mem_rss ? fmtBytes(a.mem_rss) : '—'}</span>
              <span className="flex justify-end gap-1.5">
                {a.closable && (
                  <>
                    <button
                      onClick={() => onClose(a, 'quit')}
                      className="px-2 py-1 text-[12px] rounded border border-neutral-600 text-neutral-100 hover:bg-neutral-800 transition-colors"
                    >
                      Close
                    </button>
                    <button
                      onClick={() => onClose(a, 'force')}
                      className="px-2 py-1 text-[12px] rounded border transition-colors"
                      style={{ borderColor: 'var(--status-danger)', color: 'var(--text-primary)' }}
                    >
                      Force
                    </button>
                  </>
                )}
              </span>
            </div>
          ))}
        </div>
      </div>
      <div className="px-3 py-1.5 text-[12px] text-neutral-400 border-t border-neutral-700 shrink-0 bg-neutral-900/80">
        Built-in windows are not listed: they run inside this browser tab, so the box cannot probe them.
      </div>
    </div>
  )
}

/**
 * RespBadge renders a responsiveness answer WITHOUT flattening it.
 *
 * Four states, four appearances, and the two that are not measurements say so
 * in plain words rather than borrowing the look of the two that are. A badge
 * that guesses is worse than no badge: it is a confident claim about the one
 * thing the user came here to find out.
 */
function RespBadge({ r }: { r: Responsiveness }) {
  const look: Record<string, { edge: string; text: string }> = {
    responding: { edge: 'var(--status-success)', text: 'Responding' },
    not_responding: { edge: 'var(--status-danger)', text: 'Not responding' },
    unknown: { edge: 'var(--border-emphasis)', text: 'Cannot tell' },
    not_applicable: { edge: 'var(--border-default)', text: 'Not applicable' },
  }
  const l = look[r.status] || look.unknown
  return (
    <span
      className="inline-flex items-center gap-1.5 rounded px-1.5 py-0.5 border text-[12px] w-fit"
      style={{ borderColor: l.edge, color: 'var(--text-primary)' }}
      title={r.detail ? `${r.detail} (method: ${r.method})` : `method: ${r.method}`}
    >
      <span className="inline-block w-1.5 h-1.5 rounded-full shrink-0" style={{ background: l.edge }} aria-hidden="true" />
      {l.text}
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
