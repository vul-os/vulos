import { useState, useEffect, useRef } from 'react'
import { useTelemetry } from '../../core/useTelemetry'

const HISTORY_LEN = 60

function fmtBytes(b) {
  if (!b || b <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0, v = b
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i > 0 ? 1 : 0)} ${units[i]}`
}

export default function ActivityMonitor() {
  const { stats, connected } = useTelemetry()
  const [history, setHistory] = useState([])
  const [apps, setApps] = useState([])

  useEffect(() => {
    if (stats) {
      setHistory(prev => {
        const next = [...prev, { cpu: stats.cpu || 0, mem: stats.mem_percent || 0, t: Date.now() }]
        return next.slice(-HISTORY_LEN)
      })
    }
  }, [stats])

  useEffect(() => {
    const poll = () => fetch('/api/apps/running').then(r => r.json()).then(setApps).catch(() => {})
    poll()
    const id = setInterval(poll, 5000)
    return () => clearInterval(id)
  }, [])

  if (!connected) {
    return (
      <div className="flex items-center justify-center h-full text-neutral-500 text-sm">
        Connecting to system telemetry...
      </div>
    )
  }

  const cpuVal = Math.round(stats?.cpu || 0)
  const memVal = Math.round(stats?.mem_percent || 0)

  return (
    <div className="flex flex-col h-full p-4 gap-3 overflow-hidden bg-neutral-950 text-neutral-100">
      {/* Header */}
      <div className="flex items-center justify-between shrink-0">
        <div className="flex items-center gap-3">
          <h1 className="text-sm font-semibold text-neutral-100 tracking-tight">Activity Monitor</h1>
          <span className="text-[10px] text-neutral-500 font-mono">{stats?.hostname || ''}</span>
        </div>
        <div className="flex items-center gap-2">
          {stats?.uptime && (
            <span className="text-[10px] text-neutral-600 font-mono">up {stats.uptime}</span>
          )}
          <span className={`w-1.5 h-1.5 rounded-full ${connected ? 'bg-emerald-500' : 'bg-red-500'}`} />
        </div>
      </div>

      {/* Graph cards */}
      <div className="grid grid-cols-2 gap-2.5 flex-1 min-h-0">
        <GraphCard
          label="CPU"
          value={`${cpuVal}%`}
          sub={`${stats?.num_cpu || '\u2014'} cores`}
          subRight={`Load ${stats?.load_avg || '\u2014'}`}
          data={history.map(h => h.cpu)}
          color="#3b82f6"
          colorFill="rgba(59,130,246,0.12)"
          borderColor="border-blue-500/20"
          bgGlow="from-blue-500/5 to-transparent"
        />
        <GraphCard
          label="Memory"
          value={`${memVal}%`}
          sub={`${fmtBytes(stats?.mem_used)} used`}
          subRight={`${fmtBytes(stats?.mem_total)} total`}
          data={history.map(h => h.mem)}
          color="#a855f7"
          colorFill="rgba(168,85,247,0.12)"
          borderColor="border-purple-500/20"
          bgGlow="from-purple-500/5 to-transparent"
        />
      </div>

      {/* Stats grid */}
      <div className="grid grid-cols-3 sm:grid-cols-6 gap-1.5 shrink-0">
        <StatCard icon="&#x1F321;" label="Temp" value={stats?.temp > 0 ? `${Math.round(stats.temp)}\u00B0` : '\u2014'} />
        <StatCard icon="&#x1F50B;" label="Battery" value={stats?.battery >= 0 ? `${stats.battery}%${stats.charging ? ' +' : ''}` : '\u2014'} />
        <StatCard icon="&#x25BC;" label="RX" value={fmtBytes(stats?.net_rx)} />
        <StatCard icon="&#x25B2;" label="TX" value={fmtBytes(stats?.net_tx)} />
        <StatCard icon="&#x23F1;" label="Uptime" value={stats?.uptime || '\u2014'} />
        <StatCard icon="&#x1F4BB;" label="Host" value={stats?.hostname || '\u2014'} />
      </div>

      {/* Process list */}
      <div className="flex flex-col flex-1 min-h-0 shrink">
        <div className="text-[11px] text-neutral-500 uppercase tracking-wider mb-1.5 shrink-0 font-medium">
          Processes ({apps?.length || 0})
        </div>
        <div className="flex-1 min-h-0 overflow-y-auto rounded-lg border border-neutral-800/60 bg-neutral-900/50">
          {apps?.length === 0 && (
            <div className="text-xs text-neutral-600 p-3">No apps running</div>
          )}
          {/* Table header */}
          {apps?.length > 0 && (
            <div className="grid grid-cols-[1fr_80px_60px_80px] gap-2 px-3 py-1.5 text-[10px] uppercase tracking-wider text-neutral-600 border-b border-neutral-800/40 sticky top-0 bg-neutral-900/90 backdrop-blur-sm">
              <span>Name</span>
              <span className="text-right">Port</span>
              <span className="text-center">Status</span>
              <span className="text-right">Traffic</span>
            </div>
          )}
          {apps?.map(app => (
            <div key={app.id} className="grid grid-cols-[1fr_80px_60px_80px] gap-2 items-center px-3 py-2 text-xs border-b border-neutral-800/30 hover:bg-neutral-800/30 transition-colors">
              <span className="text-neutral-300 truncate font-medium">{app.id}</span>
              <span className="text-right text-neutral-500 font-mono text-[11px]">:{app.host_port}</span>
              <span className="flex justify-center">
                <span className={`inline-block w-1.5 h-1.5 rounded-full ${app.running ? 'bg-emerald-500' : 'bg-red-500'}`} />
              </span>
              <span className="text-right text-neutral-500 text-[11px]">
                {app.traffic ? fmtBytes(app.traffic.rx_bytes) : '\u2014'}
              </span>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

function GraphCard({ label, value, sub, subRight, data, color, colorFill, borderColor, bgGlow }) {
  return (
    <div className={`flex flex-col rounded-xl border ${borderColor} bg-gradient-to-b ${bgGlow} p-3 min-h-0 overflow-hidden`}>
      <div className="flex justify-between items-start mb-2 shrink-0">
        <div>
          <span className="text-[10px] text-neutral-500 uppercase tracking-widest font-medium">{label}</span>
          <div className="text-xl font-semibold text-neutral-100 leading-tight mt-0.5">{value}</div>
        </div>
      </div>
      <div className="flex-1 min-h-0">
        <AreaGraph data={data} color={color} fill={colorFill} />
      </div>
      <div className="flex justify-between text-[10px] text-neutral-600 mt-1.5 shrink-0">
        <span>{sub}</span>
        <span>{subRight}</span>
      </div>
    </div>
  )
}

function AreaGraph({ data, color, fill }) {
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
  const padTop = 2
  const padBot = 2
  const graphH = h - padTop - padBot

  // Always show full 0-100% scale
  const maxVal = 100
  const pts = []

  for (let i = 0; i < data.length; i++) {
    const x = (i / (HISTORY_LEN - 1)) * w
    const y = padTop + graphH - (Math.min(data[i], maxVal) / maxVal) * graphH
    pts.push([x, y])
  }

  // Smooth path using cardinal spline
  const linePath = smoothPath(pts)
  const areaPath = pts.length >= 2
    ? linePath + ` L ${pts[pts.length - 1][0]},${h} L ${pts[0][0]},${h} Z`
    : ''

  // Grid lines at 25%, 50%, 75%
  const gridLines = [25, 50, 75].map(pct => padTop + graphH - (pct / 100) * graphH)

  return (
    <div ref={ref} className="w-full h-full relative">
      <svg width={w} height={h} className="block">
        {/* Grid */}
        {gridLines.map((y, i) => (
          <line key={i} x1={0} y1={y} x2={w} y2={y} stroke="rgba(255,255,255,0.04)" strokeWidth={1} />
        ))}
        {/* Filled area */}
        {pts.length >= 2 && (
          <>
            <path d={areaPath} fill={fill} />
            <path d={linePath} fill="none" stroke={color} strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round" />
          </>
        )}
        {/* Current value dot */}
        {pts.length > 0 && (
          <circle cx={pts[pts.length - 1][0]} cy={pts[pts.length - 1][1]} r={3} fill={color} />
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

function StatCard({ icon, label, value }) {
  return (
    <div className="bg-neutral-900/60 border border-neutral-800/40 rounded-lg px-2.5 py-1.5 min-w-0">
      <div className="text-[10px] text-neutral-600 uppercase tracking-wider flex items-center gap-1">
        <span className="text-[9px] leading-none">{icon}</span>
        {label}
      </div>
      <div className="text-xs text-neutral-300 mt-0.5 truncate">{value}</div>
    </div>
  )
}
