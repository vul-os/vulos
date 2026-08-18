import { useState, useEffect, useRef, useCallback } from 'react'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

/** A filesystem mount from GET /api/disks. */
interface Mount {
  mount_point: string
  device: string
  fs_type: string
  used_mb: number
  free_mb: number
  total_mb: number
  percent: number
}

function isMount(x: unknown): x is Mount {
  return isRecord(x)
    && typeof x.mount_point === 'string'
    && typeof x.device === 'string'
    && typeof x.fs_type === 'string'
    && typeof x.used_mb === 'number'
    && typeof x.free_mb === 'number'
    && typeof x.total_mb === 'number'
    && typeof x.percent === 'number'
}

/** A directory-breakdown row from GET /api/disks/breakdown. */
interface BreakdownEntry {
  name: string
  path: string
  size_mb: number
}

function isBreakdownEntry(x: unknown): x is BreakdownEntry {
  return isRecord(x)
    && typeof x.name === 'string'
    && typeof x.path === 'string'
    && typeof x.size_mb === 'number'
}

const COLORS = [
  '#3b82f6', '#8b5cf6', '#ec4899', '#f97316', '#eab308',
  '#22c55e', '#06b6d4', '#6366f1', '#f43f5e', '#14b8a6',
  '#a855f7', '#84cc16', '#0ea5e9', '#d946ef', '#f59e0b',
  '#10b981', '#6d28d9', '#e11d48', '#0891b2', '#65a30d',
]

function fmtSize(mb: number | null | undefined): string {
  if (mb == null) return '—'
  if (mb >= 1024) return (mb / 1024).toFixed(1) + ' GB'
  return mb + ' MB'
}

interface DonutSegment {
  label: string
  value: number
  color: string
}

function DonutChart({ segments, size = 160, label, sublabel }: { segments: DonutSegment[]; size?: number; label?: string; sublabel?: string }) {
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

function usageTone(percent: number): { bar: string; text: string } {
  if (percent > 90) return { bar: 'bg-danger', text: 'text-danger' }
  if (percent > 70) return { bar: 'bg-warning', text: 'text-warning' }
  return { bar: 'accent-bg', text: 'accent-text' }
}

function UsageBar({ percent, className = '' }: { percent: number; className?: string }) {
  const { bar } = usageTone(percent)
  return (
    <div className={`w-full h-1.5 bg-neutral-800 rounded-full overflow-hidden ${className}`}>
      <div className={`h-full rounded-full transition-all duration-(--motion-slow) ease-(--ease-out) ${bar}`}
        style={{ width: `${Math.min(percent, 100)}%` }} />
    </div>
  )
}

function Spinner({ className = 'w-6 h-6' }: { className?: string }) {
  return <div className={`spinner rounded-full ${className}`} role="status" aria-label="Loading" />
}

export default function DiskUsage() {
  const [mounts, setMounts] = useState<Mount[] | null>(null)
  const [selectedMount, setSelectedMount] = useState<Mount | null>(null)
  const [breakdown, setBreakdown] = useState<BreakdownEntry[] | null>(null)
  const [breakdownPath, setBreakdownPath] = useState('/')
  const [loading, setLoading] = useState(true)
  const [breakdownLoading, setBreakdownLoading] = useState(false)
  const [mountsError, setMountsError] = useState<string | null>(null)
  const [breakdownError, setBreakdownError] = useState<string | null>(null)

  // Only the NEWEST scan may write. `du` on a big tree is slow, so clicking a
  // large directory and then Up (or a small sibling) used to let the slow first
  // answer land last: the breadcrumb read one path while the list under it was
  // another, and clicking a row then scanned a path unrelated to what was on
  // screen. A stale failure could likewise paint "Could not measure this
  // directory" over a scan that had just succeeded.
  const scanSeq = useRef(0)

  const loadBreakdown = useCallback((path: string) => {
    const seq = ++scanSeq.current
    setBreakdownPath(path)
    setBreakdownLoading(true)
    setBreakdownError(null)
    // Same shape as the mounts fetch below: a rejected request left `breakdown`
    // null with `breakdownLoading` false, and all three render branches below
    // test `breakdown &&` — so the pane rendered as nothing at all. A non-ok
    // response fared no better: it parsed, failed Array.isArray, and became []
    // which reads as "Empty or not accessible".
    fetch('/api/disks/breakdown?path=' + encodeURIComponent(path))
      .then(r => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((d: unknown) => {
        if (seq !== scanSeq.current) return
        setBreakdown(Array.isArray(d) ? d.filter(isBreakdownEntry) : [])
        setBreakdownLoading(false)
      })
      .catch((e: unknown) => {
        if (seq !== scanSeq.current) return
        setBreakdownError(e instanceof Error ? e.message : String(e))
        setBreakdownLoading(false)
      })
  }, [])

  useEffect(() => {
    // BUILTIN-5: neither half of this was safe. A 500 with a JSON body parsed
    // cleanly and produced an empty `mounts` list, so a dead disk service
    // rendered "No volumes found" — telling the user their box has no
    // filesystems. A rejected fetch took the `.catch`, which cleared `loading`
    // but left `mounts` null, and `mounts?.length === 0` is false for null, so
    // the rail rendered as nothing at all: no spinner, no message, no reason.
    fetch('/api/disks')
      .then(r => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((d: unknown) => {
        const list = isRecord(d) && Array.isArray(d.mounts) ? d.mounts.filter(isMount) : []
        setMounts(list)
        if (list.length) {
          setSelectedMount(list[0])
          loadBreakdown(list[0].mount_point)
        }
        setLoading(false)
      })
      .catch((e: unknown) => {
        setMountsError(e instanceof Error ? e.message : String(e))
        setLoading(false)
      })
  }, [loadBreakdown])

  // The scan is asked for by the click, not by an effect watching the selection.
  // `mounts` is fetched once and never replaced, so a sidebar button hands back
  // the SAME object every time: re-selecting the volume you are already on is a
  // no-op write that React bails out of, and an effect keyed on `selectedMount`
  // never re-ran. That made the sidebar dead after drilling in — having gone
  // /home -> /home/user -> /home/user/Downloads via the rows, clicking /home to
  // get back to the volume did nothing at all, and the only way up was one press
  // of "Up" per level.
  const selectMount = (m: Mount) => {
    setSelectedMount(m)
    loadBreakdown(m.mount_point)
  }

  const breakdownSegments: DonutSegment[] = (breakdown || []).map((d, i) => ({
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

  const mountSegments: DonutSegment[] = selectedMount ? [
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
            <h2 className="text-[12px] uppercase tracking-wider text-neutral-500 font-semibold">Volumes</h2>
          </div>
          <div className="flex-1 overflow-y-auto">
            {loading && !mounts && !mountsError && (
              <div className="flex flex-col items-center gap-2 px-3 py-8 text-neutral-500">
                <Spinner className="w-5 h-5" />
                <span className="text-[12px]">Scanning...</span>
              </div>
            )}
            {mountsError && (
              <div role="alert" className="px-3 py-6 text-[12px] text-center">
                <div className="text-neutral-300">Could not read volumes</div>
                <div className="text-neutral-500 mt-1 break-words">
                  The box did not answer ({mountsError}).
                </div>
              </div>
            )}
            {mounts?.length === 0 && !loading && !mountsError && (
              <div className="px-3 py-6 text-[12px] text-neutral-600 text-center">No volumes found</div>
            )}
            {mounts?.map(m => {
              const active = selectedMount?.mount_point === m.mount_point
              const { text: pctColor } = usageTone(m.percent)
              return (
                <button key={m.mount_point}
                  onClick={() => selectMount(m)}
                  aria-pressed={active}
                  style={active ? { borderColor: 'var(--accent)', background: 'var(--accent-soft)' } : undefined}
                  className={`w-full text-left px-3 py-2.5 transition-colors duration-(--motion-fast) border-l-2 ${
                    active
                      ? ''
                      : 'border-transparent hover:bg-neutral-900/60'
                  }`}>
                  <div className="flex items-center justify-between gap-1">
                    <span className="text-xs font-mono truncate">{m.mount_point}</span>
                    <span className={`text-[12px] shrink-0 tabular-nums font-medium ${pctColor}`}>{Math.round(m.percent)}%</span>
                  </div>
                  <div className="text-[12px] text-neutral-600 mt-0.5 truncate font-mono">{m.device}</div>
                  <UsageBar percent={m.percent} className="mt-1.5" />
                  <div className="text-[12px] text-neutral-600 mt-1 tabular-nums">
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
              {/* Top section: donut + mount info.
                  @container: on a narrow window (a phone-fullscreen app, or a
                  desktop window snapped to a quarter-tile) the donut and the
                  Used/Free/Total row used to stay side-by-side down to zero
                  width, so the 3-column grid ran out of room, its uppercase
                  labels overflowed their cells edge-to-edge ("USEDFREETOTAL")
                  and the values overlapped the same way. Below `@xs` (20rem)
                  the donut stacks above the info column instead, which gives
                  Used/Free/Total the full content width to lay out in. This
                  reads by CONTAINER width, not viewport width, because the
                  cramped case above was a narrow desktop window just as much
                  as a phone — see Assistant.tsx for the same @container
                  pattern in this codebase. */}
              <div className="shrink-0 p-4 pb-3 border-b border-neutral-800/40 @container">
                <div className="flex flex-col @xs:flex-row items-center gap-3 @xs:gap-5">
                  <DonutChart
                    segments={mountSegments}
                    size={110}
                    label={`${Math.round(selectedMount.percent)}%`}
                    sublabel="used"
                  />
                  <div className="min-w-0 w-full @xs:flex-1 text-center @xs:text-left">
                    <h1 className="text-sm font-semibold truncate">{selectedMount.mount_point}</h1>
                    <div className="text-[12px] text-neutral-500 mt-0.5 font-mono truncate">
                      {selectedMount.device} &middot; {selectedMount.fs_type}
                    </div>
                    <div className="mt-3 grid grid-cols-3 gap-1.5 @xs:gap-3">
                      <div className="min-w-0">
                        <div className="text-[12px] uppercase tracking-wider text-neutral-600 font-semibold truncate">Used</div>
                        <div className="text-xs font-medium accent-text tabular-nums mt-0.5 truncate">{fmtSize(selectedMount.used_mb)}</div>
                      </div>
                      <div className="min-w-0">
                        <div className="text-[12px] uppercase tracking-wider text-neutral-600 font-semibold truncate">Free</div>
                        <div className="text-xs font-medium text-neutral-400 tabular-nums mt-0.5 truncate">{fmtSize(selectedMount.free_mb)}</div>
                      </div>
                      <div className="min-w-0">
                        <div className="text-[12px] uppercase tracking-wider text-neutral-600 font-semibold truncate">Total</div>
                        <div className="text-xs font-medium text-neutral-300 tabular-nums mt-0.5 truncate">{fmtSize(selectedMount.total_mb)}</div>
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
                  <span className="text-[12px] text-neutral-500 font-mono truncate">{breakdownPath}</span>
                </div>
                <span className="text-[12px] uppercase tracking-wider text-neutral-600 font-semibold shrink-0">Breakdown</span>
              </div>

              {/* Directory breakdown list (scrollable) */}
              <div className="flex-1 overflow-y-auto min-h-0 px-4 pb-3">
                {breakdownLoading && (
                  <div className="flex flex-col items-center gap-2 text-neutral-500 py-8">
                    <Spinner className="w-5 h-5" />
                    <span className="text-xs">Scanning directory...</span>
                  </div>
                )}
                {!breakdownLoading && breakdownError && (
                  <div role="alert" className="flex flex-col items-center gap-2 text-neutral-500 py-8 px-4 text-center">
                    <span aria-hidden="true" className="text-xl leading-none text-neutral-700">⚠</span>
                    <span className="text-xs text-neutral-300">Could not measure this directory</span>
                    <span className="text-[11px] text-neutral-500 break-words">
                      The box did not answer ({breakdownError}).
                    </span>
                  </div>
                )}
                {!breakdownLoading && !breakdownError && breakdown && breakdown.length === 0 && (
                  <div className="flex flex-col items-center gap-2 text-neutral-600 py-8">
                    <svg className="w-8 h-8 text-neutral-700" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.4}>
                      <path strokeLinecap="round" strokeLinejoin="round" d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z" />
                    </svg>
                    <span className="text-xs">Empty or not accessible</span>
                  </div>
                )}
                {!breakdownLoading && !breakdownError && breakdown && breakdown.length > 0 && (
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
                          <span className="text-xs truncate flex-1 min-w-0 group-hover:text-[var(--text-primary)] transition-colors">
                            {d.name}
                          </span>
                          <span className="text-[12px] text-neutral-500 shrink-0 tabular-nums">
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
