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

function DonutChart({ segments, size = 160, label, sublabel }) {
  const cx = size / 2, cy = size / 2
  const outerR = size / 2 - 4
  const innerR = outerR * 0.62
  const total = segments.reduce((s, seg) => s + seg.value, 0)
  if (total === 0) return null

  let angle = -Math.PI / 2
  const paths = []

  for (let i = 0; i < segments.length; i++) {
    const seg = segments[i]
    const sweep = (seg.value / total) * Math.PI * 2
    if (sweep < 0.003) continue

    const ox1 = cx + outerR * Math.cos(angle)
    const oy1 = cy + outerR * Math.sin(angle)
    const ox2 = cx + outerR * Math.cos(angle + sweep)
    const oy2 = cy + outerR * Math.sin(angle + sweep)
    const ix1 = cx + innerR * Math.cos(angle + sweep)
    const iy1 = cy + innerR * Math.sin(angle + sweep)
    const ix2 = cx + innerR * Math.cos(angle)
    const iy2 = cy + innerR * Math.sin(angle)
    const large = sweep > Math.PI ? 1 : 0

    paths.push(
      <path key={i}
        d={`M${ox1},${oy1} A${outerR},${outerR} 0 ${large} 1 ${ox2},${oy2} L${ix1},${iy1} A${innerR},${innerR} 0 ${large} 0 ${ix2},${iy2} Z`}
        style={{ fill: seg.color, stroke: 'var(--bg-base)' }}
        strokeWidth="1.5"
        className="transition-opacity duration-(--motion-base) hover:opacity-80 cursor-pointer"
      >
        <title>{seg.label}: {fmtSize(seg.value)}</title>
      </path>
    )
    angle += sweep
  }

  return (
    <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} className="shrink-0">
      {paths}
      <text x={cx} y={cy - 4} textAnchor="middle" style={{ fill: 'var(--text-primary)' }} fontSize="14" fontWeight="600">
        {label || fmtSize(total)}
      </text>
      {sublabel && (
        <text x={cx} y={cy + 12} textAnchor="middle" style={{ fill: 'var(--text-faint)' }} fontSize="10">
          {sublabel}
        </text>
      )}
    </svg>
  )
}

function usageTone(percent) {
  if (percent > 90) return { bar: 'bg-danger', text: 'text-danger' }
  if (percent > 70) return { bar: 'bg-warning', text: 'text-warning' }
  return { bar: 'accent-bg', text: 'accent-text' }
}

function UsageBar({ percent, className = '' }) {
  const { bar } = usageTone(percent)
  return (
    <div className={`w-full h-1.5 bg-neutral-800 rounded-full overflow-hidden ${className}`}>
      <div className={`h-full rounded-full transition-all duration-(--motion-slow) ease-(--ease-out) ${bar}`}
        style={{ width: `${Math.min(percent, 100)}%` }} />
    </div>
  )
}

function Spinner({ className = 'w-6 h-6' }) {
  return <div className={`spinner rounded-full ${className}`} role="status" aria-label="Loading" />
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
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (selectedMount) loadBreakdown(selectedMount.mount_point)
  }, [selectedMount])

  const breakdownSegments = (breakdown || []).map((d, i) => ({
    label: d.name,
    value: d.size_mb,
    color: COLORS[i % COLORS.length],
  }))

  if (selectedMount && breakdown) {
    const accounted = breakdown.reduce((s, d) => s + d.size_mb, 0)
    const remaining = selectedMount.used_mb - accounted
    if (remaining > 0) {
      breakdownSegments.push({ label: 'Other', value: remaining, color: 'var(--border-strong)' })
    }
  }

  const mountSegments = selectedMount ? [
    { label: 'Used', value: selectedMount.used_mb, color: 'var(--accent)' },
    { label: 'Free', value: selectedMount.free_mb, color: 'var(--border-default)' },
  ] : []

  const canGoUp = breakdownPath !== '/' && breakdownPath !== selectedMount?.mount_point

  return (
    <div className="h-full flex flex-col bg-neutral-950 text-neutral-200 overflow-hidden">
      {/* Sidebar + Detail layout */}
      <div className="flex-1 flex min-h-0">

        {/* Sidebar: filesystem list */}
        <div className="w-36 sm:w-52 shrink-0 flex flex-col border-r border-neutral-800/50 bg-neutral-950/80">
          <div className="shrink-0 px-3 pt-3 pb-2">
            <h2 className="text-[11px] uppercase tracking-wider text-neutral-500 font-semibold">Volumes</h2>
          </div>
          <div className="flex-1 overflow-y-auto">
            {loading && !mounts && (
              <div className="flex flex-col items-center gap-2 px-3 py-8 text-neutral-500">
                <Spinner className="w-5 h-5" />
                <span className="text-[11px]">Scanning...</span>
              </div>
            )}
            {mounts?.length === 0 && !loading && (
              <div className="px-3 py-6 text-[11px] text-neutral-600 text-center">No volumes found</div>
            )}
            {mounts?.map(m => {
              const active = selectedMount?.mount_point === m.mount_point
              const { text: pctColor } = usageTone(m.percent)
              return (
                <button key={m.mount_point}
                  onClick={() => setSelectedMount(m)}
                  aria-pressed={active}
                  style={active ? { borderColor: 'var(--accent)', background: 'var(--accent-soft)' } : undefined}
                  className={`w-full text-left px-3 py-2.5 transition-colors duration-(--motion-fast) border-l-2 ${
                    active
                      ? ''
                      : 'border-transparent hover:bg-neutral-900/60'
                  }`}>
                  <div className="flex items-center justify-between gap-1">
                    <span className="text-xs font-mono truncate">{m.mount_point}</span>
                    <span className={`text-[10px] shrink-0 tabular-nums font-medium ${pctColor}`}>{Math.round(m.percent)}%</span>
                  </div>
                  <div className="text-[10px] text-neutral-600 mt-0.5 truncate font-mono">{m.device}</div>
                  <UsageBar percent={m.percent} className="mt-1.5" />
                  <div className="text-[10px] text-neutral-600 mt-1 tabular-nums">
                    {fmtSize(m.used_mb)} / {fmtSize(m.total_mb)}
                  </div>
                </button>
              )
            })}
          </div>
        </div>

        {/* Detail pane */}
        <div className="flex-1 flex flex-col min-w-0 min-h-0">
          {!selectedMount ? (
            <div className="flex-1 flex flex-col items-center justify-center gap-3 text-neutral-500 px-6 text-center">
              {loading ? (
                <>
                  <Spinner className="w-7 h-7" />
                  <span className="text-sm">Loading volumes...</span>
                </>
              ) : (
                <>
                  <svg className="w-10 h-10 text-neutral-700" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.4}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M4 7v10a2 2 0 002 2h12a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H6a2 2 0 00-2 2z" />
                  </svg>
                  <span className="text-sm">No filesystem selected</span>
                </>
              )}
            </div>
          ) : (
            <>
              {/* Top section: donut + mount info */}
              <div className="shrink-0 p-4 pb-3 border-b border-neutral-800/40">
                <div className="flex items-center gap-5">
                  <DonutChart
                    segments={mountSegments}
                    size={110}
                    label={`${Math.round(selectedMount.percent)}%`}
                    sublabel="used"
                  />
                  <div className="min-w-0 flex-1">
                    <h1 className="text-sm font-semibold truncate">{selectedMount.mount_point}</h1>
                    <div className="text-[11px] text-neutral-500 mt-0.5 font-mono truncate">
                      {selectedMount.device} &middot; {selectedMount.fs_type}
                    </div>
                    <div className="mt-3 grid grid-cols-3 gap-3">
                      <div>
                        <div className="text-[10px] uppercase tracking-wider text-neutral-600 font-semibold">Used</div>
                        <div className="text-xs font-medium accent-text tabular-nums mt-0.5">{fmtSize(selectedMount.used_mb)}</div>
                      </div>
                      <div>
                        <div className="text-[10px] uppercase tracking-wider text-neutral-600 font-semibold">Free</div>
                        <div className="text-xs font-medium text-neutral-400 tabular-nums mt-0.5">{fmtSize(selectedMount.free_mb)}</div>
                      </div>
                      <div>
                        <div className="text-[10px] uppercase tracking-wider text-neutral-600 font-semibold">Total</div>
                        <div className="text-xs font-medium text-neutral-300 tabular-nums mt-0.5">{fmtSize(selectedMount.total_mb)}</div>
                      </div>
                    </div>
                    <UsageBar percent={selectedMount.percent} className="mt-2.5" />
                  </div>
                </div>
              </div>

              {/* Directory breakdown header */}
              <div className="shrink-0 px-4 pt-3 pb-2 flex items-center justify-between">
                <div className="flex items-center gap-2 min-w-0">
                  {canGoUp && (
                    <button onClick={() => {
                      const parent = breakdownPath.replace(/\/[^/]+\/?$/, '') || '/'
                      loadBreakdown(parent)
                    }} className="accent-text hover-accent-text text-xs shrink-0 flex items-center gap-1 rounded-md px-1.5 py-0.5 hover-accent-bg-soft transition-colors duration-(--motion-fast)">
                      &larr; Up
                    </button>
                  )}
                  <span className="text-[11px] text-neutral-500 font-mono truncate">{breakdownPath}</span>
                </div>
                <span className="text-[10px] uppercase tracking-wider text-neutral-600 font-semibold shrink-0">Breakdown</span>
              </div>

              {/* Directory breakdown list (scrollable) */}
              <div className="flex-1 overflow-y-auto min-h-0 px-4 pb-3">
                {breakdownLoading && (
                  <div className="flex flex-col items-center gap-2 text-neutral-500 py-8">
                    <Spinner className="w-5 h-5" />
                    <span className="text-xs">Scanning directory...</span>
                  </div>
                )}
                {!breakdownLoading && breakdown && breakdown.length === 0 && (
                  <div className="flex flex-col items-center gap-2 text-neutral-600 py-8">
                    <svg className="w-8 h-8 text-neutral-700" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.4}>
                      <path strokeLinecap="round" strokeLinejoin="round" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z" />
                    </svg>
                    <span className="text-xs">Empty or not accessible</span>
                  </div>
                )}
                {!breakdownLoading && breakdown && breakdown.length > 0 && (
                  <div className="space-y-px rounded-lg overflow-hidden border border-neutral-800/40">
                    {breakdown.map((d, i) => {
                      const pct = selectedMount.total_mb > 0
                        ? (d.size_mb / selectedMount.total_mb * 100)
                        : 0
                      return (
                        <button key={d.path}
                          onClick={() => loadBreakdown(d.path)}
                          className="w-full text-left flex items-center gap-2.5 px-3 py-2 bg-neutral-900/30 hover:bg-neutral-800/40 transition-colors group">
                          <span className="w-2.5 h-2.5 rounded-sm shrink-0"
                            style={{ background: COLORS[i % COLORS.length] }} />
                          <span className="text-xs truncate flex-1 min-w-0 group-hover:text-white transition-colors">
                            {d.name}
                          </span>
                          <span className="text-[11px] text-neutral-500 shrink-0 tabular-nums">
                            {fmtSize(d.size_mb)}
                          </span>
                          <div className="w-14 h-1 bg-neutral-800 rounded-full overflow-hidden shrink-0">
                            <div className="h-full rounded-full transition-all"
                              style={{
                                width: `${Math.min(pct, 100)}%`,
                                background: COLORS[i % COLORS.length],
                              }} />
                          </div>
                        </button>
                      )
                    })}
                  </div>
                )}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
