import { useState, useEffect } from 'react'

const COLORS = [
  '#3b82f6', '#8b5cf6', '#ec4899', '#f97316', '#eab308',
  '#22c55e', '#06b6d4', '#6366f1', '#f43f5e', '#14b8a6',
  '#a855f7', '#84cc16', '#0ea5e9', '#d946ef', '#f59e0b',
  '#10b981', '#6d28d9', '#e11d48', '#0891b2', '#65a30d',
]

function fmtSize(mb) {
  if (mb == null) return '—'
  if (mb >= 1024) return (mb / 1024).toFixed(1) + ' GB'
  return mb + ' MB'
}

function PieChart({ segments, size = 200 }) {
  const cx = size / 2, cy = size / 2, r = size / 2 - 8
  const total = segments.reduce((s, seg) => s + seg.value, 0)
  if (total === 0) return null

  let angle = -Math.PI / 2
  const paths = []

  for (let i = 0; i < segments.length; i++) {
    const seg = segments[i]
    const sweep = (seg.value / total) * Math.PI * 2
    if (sweep < 0.005) continue

    const x1 = cx + r * Math.cos(angle)
    const y1 = cy + r * Math.sin(angle)
    const x2 = cx + r * Math.cos(angle + sweep)
    const y2 = cy + r * Math.sin(angle + sweep)
    const large = sweep > Math.PI ? 1 : 0

    paths.push(
      <path key={i}
        d={`M${cx},${cy} L${x1},${y1} A${r},${r} 0 ${large} 1 ${x2},${y2} Z`}
        fill={seg.color}
        stroke="#0c0c0c" strokeWidth="1.5"
        className="transition-opacity hover:opacity-80 cursor-pointer"
      >
        <title>{seg.label}: {fmtSize(seg.value)}</title>
      </path>
    )
    angle += sweep
  }

  return (
    <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`}>
      {paths}
      {/* Center hole for donut style */}
      <circle cx={cx} cy={cy} r={r * 0.5} fill="#0c0c0c" />
      <text x={cx} y={cy - 6} textAnchor="middle" fill="#e5e5e5" fontSize="16" fontWeight="600">
        {fmtSize(total)}
      </text>
      <text x={cx} y={cy + 12} textAnchor="middle" fill="#666" fontSize="11">
        total
      </text>
    </svg>
  )
}

export default function DiskUsage() {
  const [mounts, setMounts] = useState(null)
  const [selectedMount, setSelectedMount] = useState(null)
  const [breakdown, setBreakdown] = useState(null)
  const [breakdownPath, setBreakdownPath] = useState('/')
  const [loading, setLoading] = useState(true)
  const [breakdownLoading, setBreakdownLoading] = useState(false)

  useEffect(() => {
    fetch('/api/disks').then(r => r.json()).then(d => {
      setMounts(d.mounts || [])
      if (d.mounts?.length) setSelectedMount(d.mounts[0])
      setLoading(false)
    }).catch(() => setLoading(false))
  }, [])

  const loadBreakdown = (path) => {
    setBreakdownPath(path)
    setBreakdownLoading(true)
    fetch('/api/disks/breakdown?path=' + encodeURIComponent(path))
      .then(r => r.json())
      .then(d => { setBreakdown(d); setBreakdownLoading(false) })
      .catch(() => setBreakdownLoading(false))
  }

  useEffect(() => {
    if (selectedMount) loadBreakdown(selectedMount.mount_point)
  }, [selectedMount])

  // Pie segments for directory breakdown
  const breakdownSegments = (breakdown || []).map((d, i) => ({
    label: d.name,
    value: d.size_mb,
    color: COLORS[i % COLORS.length],
  }))

  // If there's remaining space not accounted for, add "Other"
  if (selectedMount && breakdown) {
    const accounted = breakdown.reduce((s, d) => s + d.size_mb, 0)
    const remaining = selectedMount.used_mb - accounted
    if (remaining > 0) {
      breakdownSegments.push({ label: 'Other', value: remaining, color: '#333' })
    }
  }

  // Pie segments for mount overview (used vs free)
  const mountSegments = selectedMount ? [
    { label: 'Used', value: selectedMount.used_mb, color: '#3b82f6' },
    { label: 'Free', value: selectedMount.free_mb, color: '#1e293b' },
  ] : []

  return (
    <div className="h-full flex flex-col bg-neutral-950 text-neutral-200">
      {/* Header */}
      <div className="shrink-0 border-b border-neutral-800/50 px-5 pt-4 pb-3">
        <h1 className="text-base font-semibold">Disk Usage</h1>
        <p className="text-xs text-neutral-500 mt-0.5">Storage analyzer</p>
      </div>

      <div className="flex-1 overflow-y-auto p-5">
        {loading && <div className="text-sm text-neutral-500 py-8 text-center">Scanning filesystems...</div>}

        {mounts && (
          <div className="flex flex-col lg:flex-row gap-6">
            {/* Left: mount selector + overview pie */}
            <div className="lg:w-72 shrink-0 space-y-4">
              {/* Mount selector */}
              <div>
                <h3 className="text-xs uppercase tracking-wider text-neutral-500 mb-2">Filesystems</h3>
                <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
                  {mounts.map(m => (
                    <button key={m.mount_point}
                      onClick={() => setSelectedMount(m)}
                      className={`w-full text-left px-4 py-3 transition-colors ${
                        selectedMount?.mount_point === m.mount_point
                          ? 'bg-neutral-800/60' : 'bg-neutral-900/40 hover:bg-neutral-900/60'}`}>
                      <div className="flex items-center justify-between">
                        <div className="min-w-0">
                          <div className="text-sm font-mono truncate">{m.mount_point}</div>
                          <div className="text-[11px] text-neutral-600 mt-0.5">{m.device} · {m.fs_type}</div>
                        </div>
                        <span className={`text-xs shrink-0 ml-2 ${
                          m.percent > 90 ? 'text-red-400' : m.percent > 70 ? 'text-amber-400' : 'text-neutral-400'
                        }`}>{Math.round(m.percent)}%</span>
                      </div>
                      {/* Usage bar */}
                      <div className="mt-2 w-full h-1.5 bg-neutral-800 rounded-full overflow-hidden">
                        <div className={`h-full rounded-full ${
                          m.percent > 90 ? 'bg-red-500' : m.percent > 70 ? 'bg-amber-500' : 'bg-blue-500'
                        }`} style={{ width: `${Math.min(m.percent, 100)}%` }} />
                      </div>
                      <div className="text-[10px] text-neutral-600 mt-1">
                        {fmtSize(m.used_mb)} of {fmtSize(m.total_mb)}
                      </div>
                    </button>
                  ))}
                </div>
              </div>

              {/* Overview pie */}
              {selectedMount && (
                <div className="flex flex-col items-center">
                  <PieChart segments={mountSegments} size={180} />
                  <div className="flex gap-4 mt-3 text-[11px]">
                    <span className="flex items-center gap-1.5">
                      <span className="w-2.5 h-2.5 rounded-sm bg-blue-500" />
                      Used {fmtSize(selectedMount.used_mb)}
                    </span>
                    <span className="flex items-center gap-1.5">
                      <span className="w-2.5 h-2.5 rounded-sm" style={{ background: '#1e293b' }} />
                      Free {fmtSize(selectedMount.free_mb)}
                    </span>
                  </div>
                </div>
              )}
            </div>

            {/* Right: directory breakdown */}
            <div className="flex-1 min-w-0">
              <div className="flex items-center justify-between mb-3">
                <div>
                  <h3 className="text-xs uppercase tracking-wider text-neutral-500">Directory Breakdown</h3>
                  <div className="text-[11px] text-neutral-600 mt-0.5 font-mono flex items-center gap-1">
                    {breakdownPath !== '/' && breakdownPath !== selectedMount?.mount_point && (
                      <button onClick={() => {
                        const parent = breakdownPath.replace(/\/[^/]+\/?$/, '') || '/'
                        loadBreakdown(parent)
                      }} className="text-blue-400 hover:text-blue-300 mr-1">&larr;</button>
                    )}
                    {breakdownPath}
                  </div>
                </div>
              </div>

              {breakdownLoading && <div className="text-sm text-neutral-500 py-8 text-center">Scanning...</div>}

              {!breakdownLoading && breakdown && (
                <div className="flex flex-col lg:flex-row gap-5">
                  {/* Breakdown pie */}
                  {breakdownSegments.length > 0 && (
                    <div className="shrink-0 flex justify-center">
                      <PieChart segments={breakdownSegments} size={200} />
                    </div>
                  )}

                  {/* Breakdown list */}
                  <div className="flex-1 min-w-0">
                    <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
                      {(breakdown || []).map((d, i) => (
                        <button key={d.path}
                          onClick={() => loadBreakdown(d.path)}
                          className="w-full text-left flex items-center gap-3 px-4 py-2.5 bg-neutral-900/40 hover:bg-neutral-900/60 transition-colors">
                          <span className="w-3 h-3 rounded-sm shrink-0"
                            style={{ background: COLORS[i % COLORS.length] }} />
                          <div className="min-w-0 flex-1">
                            <div className="text-sm truncate">{d.name}</div>
                          </div>
                          <span className="text-xs text-neutral-400 shrink-0">{fmtSize(d.size_mb)}</span>
                          {selectedMount && d.size_mb > 0 && (
                            <div className="w-16 h-1 bg-neutral-800 rounded-full overflow-hidden shrink-0">
                              <div className="h-full rounded-full"
                                style={{
                                  width: `${Math.min(d.size_mb / selectedMount.total_mb * 100, 100)}%`,
                                  background: COLORS[i % COLORS.length],
                                }} />
                            </div>
                          )}
                        </button>
                      ))}
                    </div>

                    {breakdown?.length === 0 && (
                      <p className="text-sm text-neutral-500 py-4 text-center">Empty or not accessible</p>
                    )}
                  </div>
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
