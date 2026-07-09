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

function Section({ title, children }) {
  return <div><h2 className="text-lg font-medium mb-4">{title}</h2>{children}</div>
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
  const tone = p > 85 ? 'bg-red-500' : p > 65 ? 'bg-amber-500' : 'bg-blue-500'
  const readout = right ?? `${Math.round(p)}%`
  return (
    <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
      <span className="text-xs text-neutral-500">{label}</span>
      <div className="flex items-center gap-2">
        <div
          className="w-32 h-1.5 bg-neutral-800 rounded-full overflow-hidden"
          role="progressbar"
          aria-label={`${label} utilization`}
          aria-valuenow={Math.round(p)}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuetext={typeof readout === 'string' ? readout : undefined}
        >
          <div className={`h-full rounded-full transition-all ${tone}`} style={{ width: `${p}%` }} />
        </div>
        <span className="text-xs text-neutral-400 w-16 text-right">{readout}</span>
      </div>
    </div>
  )
}

function InfoRow({ label, value }) {
  return (
    <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
      <span className="text-xs text-neutral-500">{label}</span>
      <span className="text-sm text-neutral-300">{value ?? '—'}</span>
    </div>
  )
}

// checkTone maps a health check's string ("ok: …" | "degraded: …") to a dot color.
function checkTone(v) {
  if (typeof v !== 'string') return 'bg-neutral-500'
  return v.startsWith('degraded') ? 'bg-red-400' : 'bg-green-400'
}

export default function BoxHealthPanel() {
  const { stats, connected } = useTelemetry()
  const [health, setHealth] = useState(null)
  const [sys, setSys] = useState(null)
  const [healthErr, setHealthErr] = useState(false)
  // SFU host (Meet Phase 2): opt-in on the box. `null` until probed; `false`
  // when the endpoint isn't wired (feature off) — we then hide the section.
  const [sfu, setSfu] = useState(null)

  const loadHealth = useCallback(() => {
    // /api/health returns 200 (ok) or 503 (degraded) — both carry a JSON body.
    fetch('/api/health', { credentials: 'include' })
      .then(async r => { setHealth(await r.json()); setHealthErr(false) })
      .catch(() => setHealthErr(true))
  }, [])

  const loadSfu = useCallback(() => {
    // /api/meethost/status is mounted only when VULOS_SFU_HOST is enabled; a
    // 404/network error means the feature is off, so hide the section entirely.
    fetch('/api/meethost/status', { credentials: 'include' })
      .then(r => (r.ok ? r.json() : null))
      .then(s => setSfu(s || false))
      .catch(() => setSfu(false))
  }, [])

  useEffect(() => {
    loadHealth()
    loadSfu()
    fetch('/api/system/info', { credentials: 'include' }).then(r => r.json()).then(setSys).catch(() => {})
    const id = setInterval(() => { loadHealth(); loadSfu() }, 15000)
    return () => clearInterval(id)
  }, [loadHealth, loadSfu])

  const degraded = health?.status && health.status !== 'ok'
  const storagePct = sys?.storage_total_mb ? (sys.storage_used_mb / sys.storage_total_mb) * 100 : 0

  return (
    <Section title="Box Health">
      <p className="text-xs text-neutral-600 mb-5 leading-relaxed">
        The live status of your sovereign server — resource usage and the built-in
        health checks. For a machine-readable feed, a Prometheus <span className="font-mono">/metrics</span> endpoint
        is available to the box owner.
      </p>

      {/* Overall status banner */}
      <div
        role="status"
        aria-live="polite"
        className={`rounded-xl border px-4 py-3 mb-5 ${
        healthErr ? 'border-neutral-700/50 bg-neutral-800/40 text-neutral-300'
          : degraded ? 'border-red-800/40 bg-red-900/30 text-red-400'
          : 'border-green-800/40 bg-green-900/30 text-green-400'
      }`}>
        <div className="flex items-center gap-2">
          <span className={`inline-block w-2 h-2 rounded-full ${
            healthErr ? 'bg-neutral-400' : degraded ? 'bg-red-400' : 'bg-green-400'
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
      <h3 className="text-sm font-medium text-neutral-300 mb-2">
        Resources
        <span className={`ml-2 text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded ${
          connected ? 'bg-green-900/30 text-green-500' : 'bg-neutral-800/50 text-neutral-500'
        }`}>{connected ? 'live' : 'offline'}</span>
      </h3>
      <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50 mb-5">
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
          <h3 className="text-sm font-medium text-neutral-300 mb-2">Health checks</h3>
          <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
            {Object.entries(health.checks).map(([name, value]) => (
              <div key={name} className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40 gap-3">
                <span className="text-xs text-neutral-400 flex items-center gap-2">
                  <span className={`inline-block w-1.5 h-1.5 rounded-full ${checkTone(value)}`} aria-hidden="true" />
                  {name.replace(/_/g, ' ')}
                </span>
                <span className="text-xs text-neutral-500 text-right truncate max-w-[55%]" title={value}>{value}</span>
              </div>
            ))}
          </div>
        </>
      )}

      {/* Self-host Meet SFU (Phase 2) — shown only when the operator enabled it. */}
      {sfu && (
        <>
          <h3 className="text-sm font-medium text-neutral-300 mb-2 mt-5">Meet SFU host</h3>
          <p className="text-[11px] text-neutral-600 mb-2 leading-relaxed">
            Big calls that outgrow the peer mesh escalate to media on your own box — no third-party
            SFU. Media rides your infra; recording and transcription stay local.
          </p>
          <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
            <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
              <span className="text-xs text-neutral-500">Status</span>
              <span className="text-xs flex items-center gap-2">
                <span className={`inline-block w-1.5 h-1.5 rounded-full ${sfu.ready ? 'bg-green-400' : 'bg-amber-400'}`} aria-hidden="true" />
                <span className={sfu.ready ? 'text-green-400' : 'text-amber-400'}>
                  {sfu.ready ? 'Registered & ready' : (sfu.state || 'starting')}
                </span>
              </span>
            </div>
            <InfoRow label="Media" value={sfu.in_process ? 'In-process (Pion)' : 'External worker'} />
            <InfoRow label="Max participants" value={sfu.max_participants || '—'} />
            <InfoRow label="End-to-end encryption" value={sfu.has_e2ee ? 'Yes' : 'No (operator-owned media)'} />
            {sfu.last_error && (
              <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40 gap-3">
                <span className="text-xs text-red-400">Last error</span>
                <span className="text-xs text-neutral-500 text-right truncate max-w-[55%]" title={sfu.last_error}>{sfu.last_error}</span>
              </div>
            )}
          </div>
        </>
      )}
    </Section>
  )
}
