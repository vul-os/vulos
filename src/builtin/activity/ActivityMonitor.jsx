import { useState, useEffect, useRef, useCallback } from 'react'
import { useTelemetry } from '../../core/useTelemetry'

const HISTORY_LEN = 120

function fmtBytes(b) {
  if (!b || b <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0, v = b
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i > 0 ? 1 : 0)} ${units[i]}`
}

function fmtDuration(secs) {
  if (!secs) return '—'
  const d = Math.floor(secs / 86400)
  const h = Math.floor((secs % 86400) / 3600)
  const m = Math.floor((secs % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

export default function ActivityMonitor() {
  const { stats, connected } = useTelemetry()
  const [history, setHistory] = useState([])
  const [processes, setProcesses] = useState([])
  const [netConns, setNetConns] = useState([])
  const [expanded, setExpanded] = useState(null) // 'cpu' | 'memory' | 'network' | 'disk'
  const [tab, setTab] = useState('processes')
  const [sortCol, setSortCol] = useState('cpu')
  const [sortAsc, setSortAsc] = useState(false)
  const [search, setSearch] = useState('')

  useEffect(() => {
    if (stats) {
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
      fetch('/api/system/processes').then(r => r.json()).then(setProcesses).catch(() => {})
      fetch('/api/system/network').then(r => r.json()).then(setNetConns).catch(() => {})
    }
    poll()
    const id = setInterval(poll, 3000)
    return () => clearInterval(id)
  }, [])

  if (!connected) {
    return (
      <div className="flex items-center justify-center h-full text-neutral-500 text-sm">
        <span className="w-4 h-4 border-2 border-neutral-700 border-t-blue-500 rounded-full animate-spin mr-2" />
        Connecting to system telemetry...
      </div>
    )
  }

  const cpuVal = Math.round(stats?.cpu || 0)
  const memVal = Math.round(stats?.mem_percent || 0)

  const graphs = [
    {
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
    {
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
    {
      id: 'network', label: 'Network', value: fmtBytes((stats?.net_rx || 0) + (stats?.net_tx || 0)) + '/s',
      details: [
        { label: 'Receiving', value: fmtBytes(stats?.net_rx) + '/s' },
        { label: 'Sending', value: fmtBytes(stats?.net_tx) + '/s' },
        { label: 'Connections', value: netConns?.length || 0 },
        { label: 'Listening', value: (netConns || []).filter(c => c.state === 'LISTEN').length },
        { label: 'Established', value: (netConns || []).filter(c => c.state === 'ESTABLISHED').length },
      ],
      data: history.map(h => (h.rx || 0) + (h.tx || 0)),
      color: '#22c55e', fill: 'rgba(34,197,94,0.12)',
      border: 'border-green-500/20', glow: 'from-green-500/5',
      autoScale: true,
    },
    {
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
  ]

  const expandedGraph = expanded ? graphs.find(g => g.id === expanded) : null
  const otherGraphs = expanded ? graphs.filter(g => g.id !== expanded) : []

  return (
    <div className="flex flex-col h-full bg-neutral-950 text-neutral-100 overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-2.5 shrink-0 border-b border-neutral-800/40">
        <div className="flex items-center gap-3">
          <h1 className="text-sm font-semibold tracking-tight">Activity Monitor</h1>
          <span className="text-[10px] text-neutral-600 font-mono">{stats?.hostname || ''}</span>
        </div>
        <div className="flex items-center gap-3">
          {stats?.temp > 0 && (
            <span className="text-[10px] text-neutral-500 font-mono">{Math.round(stats.temp)}{'\u00B0'}C</span>
          )}
          {stats?.battery >= 0 && (
            <span className="text-[10px] text-neutral-500 font-mono">{stats.battery}%{stats.charging ? ' +' : ''}</span>
          )}
          <span className="text-[10px] text-neutral-600 font-mono">up {stats?.uptime || '—'}</span>
          <span className={`w-1.5 h-1.5 rounded-full ${connected ? 'bg-emerald-500' : 'bg-red-500'}`} />
        </div>
      </div>

      {/* Graphs section */}
      <div className="shrink-0 p-3 pb-0">
        {!expanded ? (
          /* Default: 4 compact graph cards */
          <div className="grid grid-cols-4 gap-2" style={{ height: 140 }}>
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
          {['processes', 'network'].map(t => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`px-3 py-1 text-[11px] font-medium rounded-md transition-colors ${tab === t ? 'bg-neutral-800 text-neutral-100' : 'text-neutral-500 hover:text-neutral-300 hover:bg-neutral-800/40'}`}
            >
              {t === 'processes' ? `Processes (${processes.length})` : `Network (${netConns?.length || 0})`}
            </button>
          ))}
        </div>
        <input
          type="text" placeholder="Filter..." value={search}
          onChange={e => setSearch(e.target.value)}
          className="bg-neutral-900 border border-neutral-800/60 rounded-md px-2.5 py-1 text-[11px] text-neutral-300 placeholder-neutral-600 w-40 outline-none focus:border-neutral-600 transition-colors"
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
function ProcessTable({ processes, search, sortCol, setSortCol, sortAsc, setSortAsc }) {
  const handleSort = (col) => {
    if (sortCol === col) setSortAsc(!sortAsc)
    else { setSortCol(col); setSortAsc(false) }
  }

  const filtered = (processes || []).filter(p => {
    if (!search) return true
    const q = search.toLowerCase()
    return p.name?.toLowerCase().includes(q) || p.command?.toLowerCase().includes(q) || String(p.pid).includes(q) || p.user?.toLowerCase().includes(q)
  })

  const sorted = [...filtered].sort((a, b) => {
    let va = a[sortCol], vb = b[sortCol]
    if (typeof va === 'string') { va = va.toLowerCase(); vb = (vb || '').toLowerCase() }
    if (va < vb) return sortAsc ? -1 : 1
    if (va > vb) return sortAsc ? 1 : -1
    return 0
  })

  const cols = [
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
      {/* Header */}
      <div className="grid gap-2 px-3 py-1.5 text-[10px] uppercase tracking-wider text-neutral-600 border-b border-neutral-800/40 shrink-0 bg-neutral-900/80" style={{ gridTemplateColumns: gridTemplate }}>
        {cols.map(c => (
          <span
            key={c.key}
            className={`cursor-pointer select-none hover:text-neutral-400 ${sortCol === c.key ? 'text-neutral-300' : ''} ${c.align}`}
            onClick={() => handleSort(c.key)}
          >
            {c.label}{sortCol === c.key ? (sortAsc ? ' \u25B2' : ' \u25BC') : ''}
          </span>
        ))}
      </div>
      {/* Rows */}
      <div className="flex-1 overflow-y-auto min-h-0">
        {sorted.length === 0 && (
          <div className="text-xs text-neutral-600 p-3">No processes found</div>
        )}
        {sorted.map(p => (
          <div key={p.pid} className="grid gap-2 items-center px-3 py-1 text-[11px] border-b border-neutral-800/20 hover:bg-neutral-800/30 transition-colors" style={{ gridTemplateColumns: gridTemplate }}>
            <span className="text-neutral-500 font-mono">{p.pid}</span>
            <span className="text-neutral-300 truncate" title={p.command}>{p.name}</span>
            <span className="text-neutral-500 truncate">{p.user}</span>
            <StateIndicator state={p.state} />
            <span className="text-right font-mono text-neutral-400">{p.cpu < 0.1 ? '0.0' : p.cpu?.toFixed(1)}</span>
            <span className="text-right text-neutral-500 font-mono">{fmtBytes(p.mem_rss)}</span>
            <span className="text-right text-neutral-500">{p.threads}</span>
          </div>
        ))}
      </div>
      {/* Footer */}
      <div className="flex items-center justify-between px-3 py-1.5 text-[10px] text-neutral-600 border-t border-neutral-800/40 shrink-0 bg-neutral-900/80">
        <span>{sorted.length} process{sorted.length !== 1 ? 'es' : ''}</span>
        <span>Total threads: {sorted.reduce((s, p) => s + (p.threads || 0), 0)}</span>
      </div>
    </div>
  )
}

function StateIndicator({ state }) {
  const colors = {
    running: 'bg-emerald-500',
    sleeping: 'bg-blue-500/40',
    'disk sleep': 'bg-amber-500',
    zombie: 'bg-red-500',
    stopped: 'bg-neutral-500',
    idle: 'bg-neutral-700',
  }
  return (
    <span className="flex items-center gap-1">
      <span className={`inline-block w-1.5 h-1.5 rounded-full ${colors[state] || 'bg-neutral-600'}`} />
      <span className="text-[10px] truncate">{state}</span>
    </span>
  )
}

/* ── Network Table ── */
function NetworkTable({ conns, search }) {
  const filtered = (conns || []).filter(c => {
    if (!search) return true
    const q = search.toLowerCase()
    return c.proto?.includes(q) || c.local_addr?.includes(q) || c.remote_addr?.includes(q) ||
           String(c.local_port).includes(q) || (c.process || '').toLowerCase().includes(q) ||
           c.state?.toLowerCase().includes(q)
  })

  const cols = [
    { key: 'proto', label: 'Protocol', w: '55px' },
    { key: 'local', label: 'Local Address', w: '1fr' },
    { key: 'remote', label: 'Remote Address', w: '1fr' },
    { key: 'state', label: 'State', w: '90px' },
    { key: 'process', label: 'Process', w: '1fr' },
  ]
  const gridTemplate = cols.map(c => c.w).join(' ')

  return (
    <div className="flex flex-col h-full min-h-0 rounded-lg border border-neutral-800/60 bg-neutral-900/40 overflow-hidden">
      <div className="grid gap-2 px-3 py-1.5 text-[10px] uppercase tracking-wider text-neutral-600 border-b border-neutral-800/40 shrink-0 bg-neutral-900/80" style={{ gridTemplateColumns: gridTemplate }}>
        {cols.map(c => <span key={c.key}>{c.label}</span>)}
      </div>
      <div className="flex-1 overflow-y-auto min-h-0">
        {filtered.length === 0 && (
          <div className="text-xs text-neutral-600 p-3">No connections</div>
        )}
        {filtered.map((c, i) => (
          <div key={i} className="grid gap-2 items-center px-3 py-1 text-[11px] border-b border-neutral-800/20 hover:bg-neutral-800/30 transition-colors" style={{ gridTemplateColumns: gridTemplate }}>
            <span className="text-neutral-500 font-mono uppercase">{c.proto}</span>
            <span className="text-neutral-300 font-mono truncate">{c.local_addr}:{c.local_port}</span>
            <span className="text-neutral-500 font-mono truncate">
              {c.remote_addr === '0.0.0.0' && c.remote_port === 0 ? '*' : `${c.remote_addr}:${c.remote_port}`}
            </span>
            <span className={`text-[10px] ${c.state === 'ESTABLISHED' ? 'text-emerald-400' : c.state === 'LISTEN' ? 'text-blue-400' : c.state === 'TIME_WAIT' ? 'text-amber-400' : 'text-neutral-500'}`}>
              {c.state}
            </span>
            <span className="text-neutral-500 truncate">{c.process || '—'}</span>
          </div>
        ))}
      </div>
      <div className="flex items-center justify-between px-3 py-1.5 text-[10px] text-neutral-600 border-t border-neutral-800/40 shrink-0 bg-neutral-900/80">
        <span>{filtered.length} connection{filtered.length !== 1 ? 's' : ''}</span>
        <span>
          {(filtered).filter(c => c.state === 'LISTEN').length} listening,{' '}
          {(filtered).filter(c => c.state === 'ESTABLISHED').length} established
        </span>
      </div>
    </div>
  )
}

/* ── Graph Card ── */
function GraphCard({ label, value, details, data, color, colorFill, borderColor, bgGlow, autoScale, compact, expanded, onClick }) {
  return (
    <div
      onClick={onClick}
      className={`flex flex-col rounded-xl border ${borderColor} bg-gradient-to-b ${bgGlow} to-transparent overflow-hidden cursor-pointer transition-all hover:brightness-110 h-full ${expanded ? '' : ''}`}
    >
      <div className={`flex ${expanded ? 'gap-6' : 'flex-col'} h-full`}>
        {/* Info side */}
        <div className={`flex flex-col ${expanded ? 'w-44 shrink-0 p-3 justify-center' : 'px-3 pt-2.5 pb-1 shrink-0'}`}>
          <span className="text-[10px] text-neutral-500 uppercase tracking-widest font-medium">{label}</span>
          <span className={`font-semibold text-neutral-100 leading-tight ${expanded ? 'text-2xl mt-1' : compact ? 'text-base' : 'text-xl'}`}>{value}</span>
          {expanded && details && (
            <div className="mt-3 space-y-1">
              {details.map(d => (
                <div key={d.label} className="flex justify-between text-[11px]">
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
function AreaGraph({ data, color, fill, autoScale }) {
  const ref = useRef(null)
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
  const pts = []

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
          <line key={i} x1={0} y1={y} x2={w} y2={y} stroke="rgba(255,255,255,0.03)" strokeWidth={1} />
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

function smoothPath(pts) {
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
