/**
 * DiskMap — visual representation of a physical block device.
 * Shows the disk as a horizontal bar divided into partitions,
 * with a legend below each segment.
 */

function fmtSize(bytes) {
  if (bytes == null || bytes <= 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = bytes, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i >= 2 ? 1 : 0)} ${units[i]}`
}

const PART_COLORS = [
  '#6366f1', '#8b5cf6', '#3b82f6', '#06b6d4',
  '#22c55e', '#eab308', '#f97316', '#ef4444',
]

const UNALLOC_COLOR = '#1e1e2e'

export default function DiskMap({ disk, compact = false }) {
  if (!disk) return null

  const totalBytes = disk.size_bytes || 0
  const partitions = disk.partitions || []

  // Build segments: alternating allocated / unallocated
  const segments = []
  let cursor = 0

  const sorted = [...partitions].sort((a, b) => (a.start_bytes || 0) - (b.start_bytes || 0))

  sorted.forEach((p, idx) => {
    const start = p.start_bytes || 0
    const size = p.size_bytes || 0
    if (start > cursor) {
      segments.push({
        type: 'free',
        start: cursor,
        size: start - cursor,
        label: 'Free',
        color: UNALLOC_COLOR,
      })
    }
    segments.push({
      type: 'partition',
      start,
      size,
      label: p.name || `Part ${idx + 1}`,
      fstype: p.fstype || '',
      mount: p.mountpoint || '',
      color: PART_COLORS[idx % PART_COLORS.length],
      partition: p,
    })
    cursor = start + size
  })

  if (totalBytes > 0 && cursor < totalBytes) {
    segments.push({
      type: 'free',
      start: cursor,
      size: totalBytes - cursor,
      label: 'Free',
      color: UNALLOC_COLOR,
    })
  }

  if (compact) {
    return <CompactBar segments={segments} totalBytes={totalBytes} />
  }

  return (
    <div className="space-y-2">
      <FullBar segments={segments} totalBytes={totalBytes} />
      <Legend segments={segments} totalBytes={totalBytes} />
    </div>
  )
}

function FullBar({ segments, totalBytes }) {
  if (totalBytes <= 0) {
    return (
      <div className="h-8 rounded bg-neutral-800 flex items-center justify-center">
        <span className="text-[10px] text-neutral-600">No partition data</span>
      </div>
    )
  }

  return (
    <div
      className="h-8 rounded overflow-hidden flex"
      style={{ background: UNALLOC_COLOR }}
      role="img"
      aria-label="Disk partition map"
    >
      {segments.map((seg, i) => {
        const pct = totalBytes > 0 ? (seg.size / totalBytes * 100) : 0
        if (pct < 0.1) return null
        return (
          <div
            key={i}
            title={`${seg.label}${seg.fstype ? ` (${seg.fstype})` : ''}: ${fmtSize(seg.size)}`}
            className="h-full flex items-center justify-center overflow-hidden transition-opacity hover:opacity-80"
            style={{ width: `${pct}%`, background: seg.color }}
          >
            {pct > 8 && (
              <span className="text-[9px] text-white/70 font-mono truncate px-0.5 select-none">
                {seg.label}
              </span>
            )}
          </div>
        )
      })}
    </div>
  )
}

function CompactBar({ segments, totalBytes }) {
  if (totalBytes <= 0) {
    return <div className="h-2 rounded bg-neutral-800 w-full" />
  }
  return (
    <div className="h-2 rounded overflow-hidden flex w-full">
      {segments.map((seg, i) => {
        const pct = (seg.size / totalBytes * 100)
        if (pct < 0.1) return null
        return (
          <div
            key={i}
            title={`${seg.label}: ${fmtSize(seg.size)}`}
            className="h-full"
            style={{ width: `${pct}%`, background: seg.color }}
          />
        )
      })}
    </div>
  )
}

function Legend({ segments, totalBytes }) {
  const visible = segments.filter(s => {
    const pct = totalBytes > 0 ? s.size / totalBytes * 100 : 0
    return pct > 0.5
  })

  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 pl-0.5">
      {visible.map((seg, i) => (
        <div key={i} className="flex items-center gap-1.5">
          <span
            className="shrink-0 w-2.5 h-2.5 rounded-sm"
            style={{ background: seg.color }}
          />
          <span className="text-[10px] text-neutral-500">
            {seg.label}
            {seg.fstype ? ` · ${seg.fstype}` : ''}
            {` · ${fmtSize(seg.size)}`}
          </span>
        </div>
      ))}
    </div>
  )
}
