import { useState, useEffect, useRef, type Dispatch, type SetStateAction } from 'react'
import { useTelemetry } from '../../core/useTelemetry'

const HISTORY_LEN = 120

function fmtBytes(b: number | undefined): string {
  if (!b || b <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0, v = b
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i > 0 ? 1 : 0)} ${units[i]}`
}

// ── narrow-untrusted-JSON helpers ───────────────────────────────────────────
// useTelemetry()'s `stats` and the raw fetch() responses below are all
// `unknown` at the trust boundary (matches the isRecord() pattern in
// src/lib/offlineAuth.ts and the fuller normalize*() helpers in
// src/builtin/drive/Drive.tsx) — narrowed here rather than trusted.
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

// ProcessInfo — a row from GET /api/system/processes.
interface ProcessInfo {
  pid: number
  name?: string
  command?: string
  user?: string
  state?: string
  cpu?: number
  mem_rss?: number
  threads?: number
}

function toProcess(x: unknown): ProcessInfo {
  const r = isRecord(x) ? x : {}
  return {
    pid: typeof r.pid === 'number' ? r.pid : 0,
    name: typeof r.name === 'string' ? r.name : undefined,
    command: typeof r.command === 'string' ? r.command : undefined,
    user: typeof r.user === 'string' ? r.user : undefined,
    state: typeof r.state === 'string' ? r.state : undefined,
    cpu: typeof r.cpu === 'number' ? r.cpu : undefined,
    mem_rss: typeof r.mem_rss === 'number' ? r.mem_rss : undefined,
    threads: typeof r.threads === 'number' ? r.threads : undefined,
  }
}
function toProcesses(x: unknown): ProcessInfo[] {
  return Array.isArray(x) ? x.map(toProcess) : []
}

// ProcessSortKey — the ProcessTable columns that support click-to-sort.
type ProcessSortKey = 'pid' | 'name' | 'user' | 'state' | 'cpu' | 'mem_rss' | 'threads'

// NetConn — a row from GET /api/system/network.
interface NetConn {
  proto?: string
  local_addr?: string
  local_port?: number
  remote_addr?: string
  remote_port?: number
  state?: string
  process?: string
}

function toNetConn(x: unknown): NetConn {
  const r = isRecord(x) ? x : {}
  return {
    proto: typeof r.proto === 'string' ? r.proto : undefined,
    local_addr: typeof r.local_addr === 'string' ? r.local_addr : undefined,
    local_port: typeof r.local_port === 'number' ? r.local_port : undefined,
    remote_addr: typeof r.remote_addr === 'string' ? r.remote_addr : undefined,
    remote_port: typeof r.remote_port === 'number' ? r.remote_port : undefined,
    state: typeof r.state === 'string' ? r.state : undefined,
    process: typeof r.process === 'string' ? r.process : undefined,
  }
}
function toNetConns(x: unknown): NetConn[] {
  return Array.isArray(x) ? x.map(toNetConn) : []
}

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

const TABS: ('processes' | 'network')[] = ['processes', 'network']

export default function ActivityMonitor() {
  const { stats: rawStats, connected } = useTelemetry()
  const stats = toTelemetryStats(rawStats)
  const [history, setHistory] = useState<HistoryPoint[]>([])
  const [processes, setProcesses] = useState<ProcessInfo[]>([])
  const [netConns, setNetConns] = useState<NetConn[]>([])
  const [expanded, setExpanded] = useState<GraphId | null>(null)
  const [tab, setTab] = useState<'processes' | 'network'>('processes')
  const [sortCol, setSortCol] = useState<ProcessSortKey>('cpu')
  const [sortAsc, setSortAsc] = useState(false)
  const [search, setSearch] = useState('')

  useEffect(() => {
    if (stats) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setHistory(prev => {
        const next = [...prev, {
          cpu: stats.cpu || 0,
          mem: stats.mem_percent || 0,
          rx: stats.net_rx || 0,
          tx: stats.net_tx || 0,
          disk_r: stats.disk_read || 0,
          disk_w: stats.disk_write || 0,
          t: Date.now(),
        }]
        return next.slice(-HISTORY_LEN)
      })
    }
  }, [stats])

  // Poll processes + network
  useEffect(() => {
    const poll = () => {
      fetch('/api/system/processes').then(r => r.json()).then((data: unknown) => setProcesses(toProcesses(data))).catch(() => {})
      fetch('/api/system/network').then(r => r.json()).then((data: unknown) => setNetConns(toNetConns(data))).catch(() => {})
    }
    poll()
    const id = setInterval(poll, 3000)
    return () => clearInterval(id)
  }, [])

  if (!connected) {
    return (
      <div className="flex items-center justify-center h-full text-neutral-500 text-sm">
        <span className="w-4 h-4 spinner mr-2" />
        Connecting to system telemetry...
      </div>
    )
  }

  const cpuVal = Math.round(stats?.cpu || 0)
  const memVal = Math.round(stats?.mem_percent || 0)

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

  return (
    <div className="flex flex-col h-full bg-neutral-950 text-neutral-100 overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-2.5 shrink-0 border-b border-neutral-800/40">
        <div className="flex items-center gap-3">
          <h1 className="text-sm font-semibold tracking-tight">Activity Monitor</h1>
          <span className="text-[12px] text-neutral-600 font-mono">{stats?.hostname || ''}</span>
        </div>
        <div className="flex items-center gap-3">
          {(stats?.temp ?? 0) > 0 && (
            <span className="text-[12px] text-neutral-500 font-mono">{Math.round(stats?.temp ?? 0)}{'°'}C</span>
          )}
          {(stats?.battery ?? -1) >= 0 && (
            <span className="text-[12px] text-neutral-500 font-mono">{stats?.battery}%{stats?.charging ? ' +' : ''}</span>
          )}
          <span className="text-[12px] text-neutral-600 font-mono">up {stats?.uptime || '—'}</span>
          <span className={`w-1.5 h-1.5 rounded-full ${connected ? 'bg-emerald-500' : 'bg-red-500'}`} />
        </div>
      </div>

      {/* Graphs section */}
      <div className="shrink-0 p-3 pb-0">
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
      <div className="flex items-center justify-between px-4 pt-3 pb-1.5 shrink-0">
        <div className="flex items-center gap-0.5">
          {TABS.map(t => (
            <button
              key={t}
              onClick={() => setTab(t)}
              aria-pressed={tab === t}
              className={`px-3 py-1.5 text-[12px] font-medium rounded-md transition-colors ${tab === t ? 'text-neutral-100 bg-neutral-800' : 'text-neutral-500 hover:text-neutral-300 hover:bg-neutral-800/40'}`}
              style={tab === t ? { boxShadow: 'inset 0 -2px 0 var(--accent)' } : undefined}
            >
              {t === 'processes' ? `Processes (${processes.length})` : `Network (${netConns.length})`}
            </button>
          ))}
        </div>
        <input
          type="text" placeholder="Filter..." value={search}
          onChange={e => setSearch(e.target.value)}
          className="bg-neutral-900 border border-neutral-800/60 rounded-md px-2.5 py-1.5 text-[12px] text-neutral-300 placeholder-neutral-600 w-28 sm:w-40 outline-none focus:border-neutral-600 transition-colors"
        />
      </div>

      {/* List */}
      <div className="flex-1 min-h-0 px-3 pb-3">
        {tab === 'processes' ? (
          <ProcessTable
            processes={processes} search={search}
            sortCol={sortCol} setSortCol={setSortCol}
            sortAsc={sortAsc} setSortAsc={setSortAsc}
          />
        ) : (
          <NetworkTable conns={netConns} search={search} />
        )}
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
}

function ProcessTable({ processes, search, sortCol, setSortCol, sortAsc, setSortAsc }: ProcessTableProps) {
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
    { key: 'state', label: 'State', w: '65px', align: '' },
    { key: 'cpu', label: 'CPU %', w: '60px', align: 'text-right' },
    { key: 'mem_rss', label: 'Memory', w: '70px', align: 'text-right' },
    { key: 'threads', label: 'Threads', w: '50px', align: 'text-right' },
  ]
  const gridTemplate = cols.map(c => c.w).join(' ')

  return (
    <div className="flex flex-col h-full min-h-0 rounded-lg border border-neutral-800/60 bg-neutral-900/40 overflow-hidden">
      {/* Scroll region — horizontal on narrow screens, vertical always */}
      <div className="flex-1 min-h-0 overflow-auto">
        <div className="min-w-[560px]">
          {/* Header */}
          <div className="grid gap-2 px-3 py-1.5 text-[12px] uppercase tracking-wider text-neutral-600 border-b border-neutral-800/40 sticky top-0 z-10 bg-neutral-900/95 backdrop-blur-sm" style={{ gridTemplateColumns: gridTemplate }}>
            {cols.map(c => (
              <span
                key={c.key}
                className={`cursor-pointer select-none hover:text-neutral-400 transition-colors ${sortCol === c.key ? 'text-neutral-200' : ''} ${c.align}`}
                onClick={() => handleSort(c.key)}
              >
                {c.label}{sortCol === c.key ? (sortAsc ? ' ▲' : ' ▼') : ''}
              </span>
            ))}
          </div>
          {/* Rows */}
          {sorted.length === 0 && (
            <div className="text-xs text-neutral-600 p-6 text-center">No processes found</div>
          )}
          {sorted.map(p => (
            <div key={p.pid} className="grid gap-2 items-center px-3 py-1 text-[12px] border-b border-neutral-800/20 hover:bg-neutral-800/30 transition-colors" style={{ gridTemplateColumns: gridTemplate }}>
              <span className="text-neutral-500 font-mono">{p.pid}</span>
              <span className="text-neutral-300 truncate" title={p.command}>{p.name}</span>
              <span className="text-neutral-500 truncate">{p.user}</span>
              <StateIndicator state={p.state} />
              <span className="text-right font-mono text-neutral-400">{Number(p.cpu) < 0.1 ? '0.0' : p.cpu?.toFixed(1)}</span>
              <span className="text-right text-neutral-500 font-mono">{fmtBytes(p.mem_rss)}</span>
              <span className="text-right text-neutral-500">{p.threads}</span>
            </div>
          ))}
        </div>
      </div>
      {/* Footer */}
      <div className="flex items-center justify-between px-3 py-1.5 text-[12px] text-neutral-600 border-t border-neutral-800/40 shrink-0 bg-neutral-900/80">
        <span>{sorted.length} process{sorted.length !== 1 ? 'es' : ''}</span>
        <span>Total threads: {sorted.reduce((s, p) => s + (p.threads || 0), 0)}</span>
      </div>
    </div>
  )
}

function StateIndicator({ state }: { state?: string }) {
  const colors: Record<string, string> = {
    running: 'bg-emerald-500',
    sleeping: 'bg-blue-500/40',
    'disk sleep': 'bg-amber-500',
    zombie: 'bg-red-500',
    stopped: 'bg-neutral-500',
    idle: 'bg-neutral-700',
  }
  return (
    <span className="flex items-center gap-1">
      <span className={`inline-block w-1.5 h-1.5 rounded-full ${colors[state ?? ''] || 'bg-neutral-600'}`} />
      <span className="text-[12px] truncate">{state}</span>
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
    { key: 'state', label: 'State', w: '90px' },
    { key: 'process', label: 'Process', w: '1fr' },
  ]
  const gridTemplate = cols.map(c => c.w).join(' ')

  return (
    <div className="flex flex-col h-full min-h-0 rounded-lg border border-neutral-800/60 bg-neutral-900/40 overflow-hidden">
      <div className="flex-1 min-h-0 overflow-auto">
        <div className="min-w-[560px]">
          <div className="grid gap-2 px-3 py-1.5 text-[12px] uppercase tracking-wider text-neutral-600 border-b border-neutral-800/40 sticky top-0 z-10 bg-neutral-900/95 backdrop-blur-sm" style={{ gridTemplateColumns: gridTemplate }}>
            {cols.map(c => <span key={c.key}>{c.label}</span>)}
          </div>
          {filtered.length === 0 && (
            <div className="text-xs text-neutral-600 p-6 text-center">No connections</div>
          )}
          {filtered.map((c, i) => (
            <div key={i} className="grid gap-2 items-center px-3 py-1 text-[12px] border-b border-neutral-800/20 hover:bg-neutral-800/30 transition-colors" style={{ gridTemplateColumns: gridTemplate }}>
              <span className="text-neutral-500 font-mono uppercase">{c.proto}</span>
              <span className="text-neutral-300 font-mono truncate">{c.local_addr}:{c.local_port}</span>
              <span className="text-neutral-500 font-mono truncate">
                {c.remote_addr === '0.0.0.0' && c.remote_port === 0 ? '*' : `${c.remote_addr}:${c.remote_port}`}
              </span>
              <span className={`text-[12px] ${c.state === 'ESTABLISHED' ? 'text-emerald-400' : c.state === 'LISTEN' ? 'text-blue-400' : c.state === 'TIME_WAIT' ? 'text-amber-400' : 'text-neutral-500'}`}>
                {c.state}
              </span>
              <span className="text-neutral-500 truncate">{c.process || '—'}</span>
            </div>
          ))}
        </div>
      </div>
      <div className="flex items-center justify-between px-3 py-1.5 text-[12px] text-neutral-600 border-t border-neutral-800/40 shrink-0 bg-neutral-900/80">
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
      className={`flex flex-col rounded-xl border ${borderColor} bg-gradient-to-b ${bgGlow} to-transparent overflow-hidden cursor-pointer transition-all hover:brightness-110 h-full ${expanded ? '' : ''}`}
    >
      <div className={`flex ${expanded ? 'gap-6' : 'flex-col'} h-full`}>
        {/* Info side */}
        <div className={`flex flex-col ${expanded ? 'w-44 shrink-0 p-3 justify-center' : 'px-3 pt-2.5 pb-1 shrink-0'}`}>
          <span className="text-[12px] text-neutral-500 uppercase tracking-widest font-medium">{label}</span>
          <span className={`font-semibold text-neutral-100 leading-tight ${expanded ? 'text-2xl mt-1' : compact ? 'text-base' : 'text-xl'}`}>{value}</span>
          {expanded && details && (
            <div className="mt-3 space-y-1">
              {details.map(d => (
                <div key={d.label} className="flex justify-between text-[12px]">
                  <span className="text-neutral-500">{d.label}</span>
                  <span className="text-neutral-300 font-mono">{d.value}</span>
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
function AreaGraph({ data, color, fill, autoScale }: { data: number[], color: string, fill: string, autoScale?: boolean }) {
  const ref = useRef<HTMLDivElement>(null)
  const [size, setSize] = useState({ w: 200, h: 80 })

  useEffect(() => {
    if (!ref.current) return
    const ro = new ResizeObserver(entries => {
      const { width, height } = entries[0].contentRect
      if (width > 0 && height > 0) setSize({ w: width, h: height })
    })
    ro.observe(ref.current)
    return () => ro.disconnect()
  }, [])

  const { w, h } = size
  const padTop = 2, padBot = 2
  const graphH = h - padTop - padBot
  const maxVal = autoScale ? Math.max(...data, 1) : 100
  const pts: [number, number][] = []

  for (let i = 0; i < data.length; i++) {
    const x = (i / (HISTORY_LEN - 1)) * w
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
