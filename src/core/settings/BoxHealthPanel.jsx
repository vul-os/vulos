import { useState, useEffect, useCallback } from 'react'
import { useTelemetry } from '../useTelemetry'

// ---------------------------------------------------------------------------
// Box Health — owner-facing live status of your sovereign server.
//
// Surfaces the health/resource data the backend already collects into one clean
// status view:
//   • /api/health         → the degraded-checks probe (data-dir writable, free
//                            disk, sync lag) with an overall OK/degraded banner.
//   • /api/telemetry (WS) → live CPU %, memory, load avg, uptime (via useTelemetry).
//   • /api/system/info    → one-shot storage + host details.
//
// This is the operator's "is my box healthy?" glance. It complements the
// About page (static device info) with a live, health-focused view.
// ---------------------------------------------------------------------------

function Section({ title, desc, children }) {
  return (
    <div>
      <header className="mb-5 pb-4 border-b border-[var(--border-default)]">
        <h2 className="text-xl font-semibold tracking-tight text-[var(--text-primary)]">{title}</h2>
        {desc && <p className="mt-1 text-sm text-[var(--text-tertiary)] leading-relaxed">{desc}</p>}
      </header>
      {children}
    </div>
  )
}

function humanMB(mb) {
  if (mb == null) return '—'
  return mb >= 1024 ? (mb / 1024).toFixed(1) + ' GB' : Math.round(mb) + ' MB'
}

function humanBytes(n) {
  if (n == null) return '—'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${u[i]}`
}

// Bar — a labelled utilization meter that turns amber/red as it fills.
function Bar({ label, pct, right }) {
  const p = Math.max(0, Math.min(100, pct || 0))
  const tone = p > 85 ? 'bg-[var(--status-danger)]' : p > 65 ? 'bg-[var(--status-warning)]' : 'bg-[var(--accent)]'
  const readout = right ?? `${Math.round(p)}%`
  return (
    <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
      <span className="text-xs text-[var(--text-muted)]">{label}</span>
      <div className="flex items-center gap-2">
        <div
          className="w-32 h-1.5 bg-[var(--bg-elevated)] rounded-full overflow-hidden"
          role="progressbar"
          aria-label={`${label} utilization`}
          aria-valuenow={Math.round(p)}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuetext={typeof readout === 'string' ? readout : undefined}
        >
          <div className={`h-full rounded-full transition-all ${tone}`} style={{ width: `${p}%` }} />
        </div>
        <span className="text-xs text-[var(--text-tertiary)] w-16 text-right">{readout}</span>
      </div>
    </div>
  )
}

function InfoRow({ label, value }) {
  return (
    <div className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)]">
      <span className="text-xs text-[var(--text-muted)]">{label}</span>
      <span className="text-sm text-[var(--text-secondary)]">{value ?? '—'}</span>
    </div>
  )
}

// checkTone maps a health check's string ("ok: …" | "degraded: …") to a dot color.
function checkTone(v) {
  if (typeof v !== 'string') return 'bg-[var(--bg-active)]'
  return v.startsWith('degraded') ? 'bg-[var(--status-danger)]' : 'bg-[var(--status-success)]'
}

export default function BoxHealthPanel() {
  const { stats, connected } = useTelemetry()
  const [health, setHealth] = useState(null)
  const [sys, setSys] = useState(null)
  const [healthErr, setHealthErr] = useState(false)

  const loadHealth = useCallback(() => {
    // /api/health returns 200 (ok) or 503 (degraded) — both carry a JSON body.
    fetch('/api/health', { credentials: 'include' })
      .then(async r => { setHealth(await r.json()); setHealthErr(false) })
      .catch(() => setHealthErr(true))
  }, [])

  useEffect(() => {
    loadHealth()
    fetch('/api/system/info', { credentials: 'include' }).then(r => r.json()).then(setSys).catch(() => {})
    const id = setInterval(() => { loadHealth() }, 15000)
    return () => clearInterval(id)
  }, [loadHealth])

  const degraded = health?.status && health.status !== 'ok'
  const storagePct = sys?.storage_total_mb ? (sys.storage_used_mb / sys.storage_total_mb) * 100 : 0

  return (
    <Section title="Box Health">
      <p className="text-xs text-[var(--text-faint)] mb-5 leading-relaxed">
        The live status of your sovereign server — resource usage and the built-in
        health checks. For a machine-readable feed, a Prometheus <span className="font-mono">/metrics</span> endpoint
        is available to the box owner.
      </p>

      {/* Overall status banner */}
      <div
        role="status"
        aria-live="polite"
        className={`rounded-xl border px-4 py-3 mb-5 ${
        healthErr ? 'border-[var(--border-strong)] bg-[var(--bg-elevated)] text-[var(--text-secondary)]'
          : degraded ? 'border-danger-soft bg-[var(--status-danger-soft)] text-[var(--status-danger)]'
          : 'border-success-soft bg-[var(--status-success-soft)] text-[var(--status-success)]'
      }`}>
        <div className="flex items-center gap-2">
          <span className={`inline-block w-2 h-2 rounded-full ${
            healthErr ? 'bg-[var(--bg-active)]' : degraded ? 'bg-[var(--status-danger)]' : 'bg-[var(--status-success)]'
          }`} aria-hidden="true" />
          <span className="text-sm font-medium">
            {healthErr ? 'Health status unavailable' : degraded ? 'Attention needed' : 'All systems healthy'}
          </span>
        </div>
        {health?.timestamp && !healthErr && (
          <p className="text-[11px] mt-1 opacity-80">Checked {new Date(health.timestamp).toLocaleTimeString()}</p>
        )}
      </div>

      {/* Live resource usage */}
      <h3 className="text-sm font-medium text-[var(--text-secondary)] mb-2">
        Resources
        <span className={`ml-2 text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded ${
          connected ? 'bg-[var(--status-success-soft)] text-[var(--status-success)]' : 'bg-[var(--bg-elevated)] text-[var(--text-muted)]'
        }`}>{connected ? 'live' : 'offline'}</span>
      </h3>
      <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)] mb-5">
        <Bar label="CPU" pct={stats?.cpu} />
        <Bar label="Memory" pct={stats?.mem_percent}
          right={stats ? `${humanBytes(stats.mem_used)} / ${humanBytes(stats.mem_total)}` : '—'} />
        <Bar label="Storage" pct={storagePct}
          right={sys ? `${humanMB(sys.storage_used_mb)} / ${humanMB(sys.storage_total_mb)}` : '—'} />
        <InfoRow label="Load average" value={stats?.load_avg} />
        <InfoRow label="Uptime" value={stats?.uptime || sys?.uptime} />
        {stats?.temp > 0 && <InfoRow label="Temperature" value={`${Math.round(stats.temp)}°C`} />}
        <InfoRow label="Hostname" value={stats?.hostname || sys?.hostname} />
      </div>

      {/* Health checks breakdown */}
      {health?.checks && (
        <>
          <h3 className="text-sm font-medium text-[var(--text-secondary)] mb-2">Health checks</h3>
          <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)]">
            {Object.entries(health.checks).map(([name, value]) => (
              <div key={name} className="flex items-center justify-between px-4 py-2.5 bg-[var(--bg-surface)] gap-3">
                <span className="text-xs text-[var(--text-tertiary)] flex items-center gap-2">
                  <span className={`inline-block w-1.5 h-1.5 rounded-full ${checkTone(value)}`} aria-hidden="true" />
                  {name.replace(/_/g, ' ')}
                </span>
                <span className="text-xs text-[var(--text-muted)] text-right truncate max-w-[55%]" title={value}>{value}</span>
              </div>
            ))}
          </div>
        </>
      )}
    </Section>
  )
}
