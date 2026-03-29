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
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-faint)', fontSize: 13 }}>
        Connecting to system telemetry...
      </div>
    )
  }

  const cpuVal = Math.round(stats?.cpu || 0)
  const memVal = Math.round(stats?.mem_percent || 0)

  return (
    <div style={styles.container}>
      <div style={styles.header}>
        <span style={styles.title}>Activity Monitor</span>
        <span style={{ ...styles.dot, background: connected ? '#22c55e' : '#ef4444' }} />
      </div>

      <div style={styles.graphGrid}>
        <GraphCard
          label="CPU"
          value={`${cpuVal}%`}
          sub={`${stats?.num_cpu || '\u2014'} cores`}
          subRight={`Load ${stats?.load_avg || '\u2014'}`}
          data={history.map(h => h.cpu)}
          color="#3b82f6"
          colorFill="rgba(59,130,246,0.12)"
        />
        <GraphCard
          label="Memory"
          value={`${memVal}%`}
          sub={`${fmtBytes(stats?.mem_used)} used`}
          subRight={`${fmtBytes(stats?.mem_total)} total`}
          data={history.map(h => h.mem)}
          color="#a855f7"
          colorFill="rgba(168,85,247,0.12)"
        />
      </div>

      <div style={styles.statsGrid}>
        <StatCard label="Temp" value={stats?.temp > 0 ? `${Math.round(stats.temp)}\u00B0` : '\u2014'} />
        <StatCard label="Battery" value={stats?.battery >= 0 ? `${stats.battery}%${stats.charging ? ' +' : ''}` : '\u2014'} />
        <StatCard label="Net RX" value={fmtBytes(stats?.net_rx)} />
        <StatCard label="Net TX" value={fmtBytes(stats?.net_tx)} />
        <StatCard label="Uptime" value={stats?.uptime || '\u2014'} />
        <StatCard label="Host" value={stats?.hostname || '\u2014'} />
      </div>

      <div style={styles.section}>
        <div style={styles.sectionLabel}>Processes ({apps?.length || 0})</div>
        {apps?.length === 0 && <div style={{ fontSize: 12, color: 'var(--text-dim)' }}>No apps running</div>}
        {apps?.map(app => (
          <div key={app.id} style={styles.processRow}>
            <div style={styles.processName}>
              <span style={{ ...styles.processDot, background: app.running ? '#22c55e' : '#ef4444' }} />
              {app.id}
            </div>
            <div style={styles.processInfo}>
              <span>:{app.host_port}</span>
              {app.traffic && <span>{fmtBytes(app.traffic.rx_bytes)}</span>}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

function GraphCard({ label, value, sub, subRight, data, color, colorFill }) {
  return (
    <div style={styles.card}>
      <div style={styles.cardHeader}>
        <div>
          <span style={{ fontSize: 11, color: 'var(--text-faint)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{label}</span>
          <div style={{ fontSize: 22, fontWeight: 600, color: 'var(--text-primary)', lineHeight: 1.1, marginTop: 2 }}>{value}</div>
        </div>
      </div>
      <div style={{ flex: 1, minHeight: 0 }}>
        <AreaGraph data={data} color={color} fill={colorFill} />
      </div>
      <div style={styles.cardFooter}>
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
    <div ref={ref} style={{ width: '100%', height: '100%', position: 'relative' }}>
      <svg width={w} height={h} style={{ display: 'block' }}>
        {/* Grid */}
        {gridLines.map((y, i) => (
          <line key={i} x1={0} y1={y} x2={w} y2={y} stroke="var(--graph-grid)" strokeWidth={1} />
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

function StatCard({ label, value }) {
  return (
    <div style={styles.stat}>
      <div style={{ fontSize: 10, color: 'var(--text-ghost)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{label}</div>
      <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginTop: 2 }}>{value}</div>
    </div>
  )
}

const styles = {
  container: {
    display: 'flex',
    flexDirection: 'column',
    height: '100%',
    padding: 16,
    gap: 12,
    overflow: 'hidden',
  },
  header: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    flexShrink: 0,
  },
  title: {
    fontSize: 14,
    fontWeight: 600,
    color: 'var(--text-primary)',
  },
  dot: {
    width: 6,
    height: 6,
    borderRadius: '50%',
  },
  graphGrid: {
    display: 'grid',
    gridTemplateColumns: '1fr 1fr',
    gap: 10,
    flex: 1,
    minHeight: 0,
  },
  card: {
    display: 'flex',
    flexDirection: 'column',
    background: 'var(--bg-surface)',
    borderRadius: 10,
    border: '1px solid var(--border-default)',
    padding: 12,
    minHeight: 0,
    overflow: 'hidden',
  },
  cardHeader: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'flex-start',
    marginBottom: 8,
    flexShrink: 0,
  },
  cardFooter: {
    display: 'flex',
    justifyContent: 'space-between',
    fontSize: 10,
    color: 'var(--text-ghost)',
    marginTop: 6,
    flexShrink: 0,
  },
  statsGrid: {
    display: 'grid',
    gridTemplateColumns: 'repeat(6, 1fr)',
    gap: 8,
    flexShrink: 0,
  },
  stat: {
    background: 'var(--bg-surface)',
    borderRadius: 8,
    border: '1px solid var(--border-default)',
    padding: '8px 10px',
  },
  section: {
    flexShrink: 0,
    maxHeight: 140,
    overflowY: 'auto',
  },
  sectionLabel: {
    fontSize: 11,
    color: 'var(--text-ghost)',
    textTransform: 'uppercase',
    letterSpacing: '0.05em',
    marginBottom: 6,
  },
  processRow: {
    display: 'flex',
    justifyContent: 'space-between',
    alignItems: 'center',
    padding: '5px 0',
    borderBottom: '1px solid var(--border-subtle)',
    fontSize: 12,
  },
  processName: {
    display: 'flex',
    alignItems: 'center',
    gap: 6,
    color: 'var(--text-secondary)',
  },
  processDot: {
    width: 5,
    height: 5,
    borderRadius: '50%',
    flexShrink: 0,
  },
  processInfo: {
    display: 'flex',
    gap: 10,
    color: 'var(--text-dim)',
    fontSize: 11,
  },
}
