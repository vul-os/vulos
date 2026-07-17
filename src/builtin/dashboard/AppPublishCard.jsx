/**
 * AppPublishCard — PUBWEB-05
 * Dashboard "Web" section: per-app publish toggle + live resource usage bars.
 * Polls /api/apps/visibility (toggle), /api/cgroups/status (usage), and
 * /api/apps/{id}/deployment (public URL) every 5 s.
 */
import { useState, useEffect, useCallback, useRef } from 'react'

// ── constants ─────────────────────────────────────────────────────────────────

const APC_POLL_MS = 5_000
// memory.high default for published apps per PUBWEB-04 spec (128 MiB)
const APC_MEM_HIGH_DEFAULT = 128 * 1024 * 1024
const APC_WARN_THRESHOLD = 0.8 // 80 % of memory.high

// ── API helpers ───────────────────────────────────────────────────────────────

async function apcFetchVisibility() {
  const r = await fetch('/api/apps/visibility')
  if (!r.ok) throw new Error('vis fetch failed')
  return r.json() // [{app_id, visibility}]
}

async function apcPatchVisibility(appId, visibility) {
  const r = await fetch(`/api/apps/${appId}/visibility`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ visibility }),
  })
  if (!r.ok) throw new Error('patch failed')
  return r.json()
}

async function apcFetchCgroups() {
  const r = await fetch('/api/cgroups/status')
  if (!r.ok) return []
  return r.json() // [{app_id, cpu_pct, mem_current, mem_high, mem_max}]
}

async function apcFetchDeployment(appId) {
  const r = await fetch(`/api/apps/${appId}/deployment`)
  if (!r.ok) return null
  return r.json() // {fqdn, ...} or null
}

async function apcPurgeCache(appId) {
  await fetch(`/api/apps/${appId}/cache/purge`, { method: 'POST' })
}

// ── ResourceBar ──────────────────────────────────────────────────────────────

function ResourceBar({ label, pct, warn }) {
  const clamp = Math.min(Math.max(pct || 0, 0), 100)
  const barColor = warn
    ? 'bg-warning'
    : clamp > 60
    ? 'accent-bg'
    : 'bg-neutral-600'

  return (
    <div className="flex items-center gap-2 min-w-0">
      <span className="text-[10px] text-neutral-500 w-8 shrink-0 font-mono">{label}</span>
      <div className="flex-1 h-1.5 rounded-full bg-neutral-800 overflow-hidden">
        <div
          className={`h-full rounded-full transition-all duration-500 ${barColor}`}
          style={{ width: `${clamp}%` }}
        />
      </div>
      <span className={`text-[10px] w-8 text-right shrink-0 font-mono ${warn ? 'text-warning' : 'text-neutral-500'}`}>
        {Math.round(clamp)}%
      </span>
    </div>
  )
}

// ── MemWarningBanner ─────────────────────────────────────────────────────────

function MemWarningBanner({ appId, memPct }) {
  if (memPct < APC_WARN_THRESHOLD * 100) return null
  return (
    <div className="mt-1.5 flex items-center gap-1.5 px-2 py-1 rounded-lg bg-warning-soft border border-warning-soft">
      <svg viewBox="0 0 16 16" className="w-3 h-3 text-warning shrink-0" fill="currentColor">
        <path d="M8 1L1 14h14L8 1zm0 3.5l4.5 8H3.5L8 4.5zm-.75 3v3h1.5V7.5h-1.5zm0 3.75v1.5h1.5v-1.5h-1.5z"/>
      </svg>
      <span className="text-[10px] text-warning leading-tight">
        {appId} is approaching its memory limit ({Math.round(memPct)}%)
      </span>
    </div>
  )
}

// ── AppCard ──────────────────────────────────────────────────────────────────

function AppCard({ app, cgroupInfo, onToggle }) {
  const [deployment, setDeployment] = useState(null)
  const [loadingDeploy, setLoadingDeploy] = useState(false)
  const [toggling, setToggling] = useState(false)
  const [purging, setPurging] = useState(false)
  const [copied, setCopied] = useState(false)
  const isPublic = app.visibility === 'public'

  // Fetch deployment URL when app is public
  useEffect(() => {
    if (!isPublic) { setDeployment(null); return }
    setLoadingDeploy(true)
    apcFetchDeployment(app.app_id)
      .then(d => setDeployment(d))
      .catch(() => setDeployment(null))
      .finally(() => setLoadingDeploy(false))
  }, [app.app_id, isPublic])

  const handleToggle = useCallback(async () => {
    if (toggling) return
    const next = isPublic ? 'private' : 'public'
    setToggling(true)
    try {
      await apcPatchVisibility(app.app_id, next)
      onToggle()
    } catch { /* noop */ }
    finally { setToggling(false) }
  }, [app.app_id, isPublic, toggling, onToggle])

  const handleCopy = useCallback(() => {
    if (!deployment?.fqdn) return
    navigator.clipboard.writeText(`https://${deployment.fqdn}`).catch(() => {})
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }, [deployment])

  const handlePurge = useCallback(async () => {
    if (purging) return
    setPurging(true)
    try { await apcPurgeCache(app.app_id) } catch { /* noop */ }
    finally { setPurging(false) }
  }, [app.app_id, purging])

  // cgroup data
  const cg = cgroupInfo || {}
  const memHigh = cg.mem_high || APC_MEM_HIGH_DEFAULT
  const memCurrent = cg.mem_current || 0
  const cpuPct = cg.cpu_pct || 0
  const memPct = memHigh > 0 ? (memCurrent / memHigh) * 100 : 0
  const memWarn = memPct >= APC_WARN_THRESHOLD * 100

  return (
    <div className={`rounded-xl border p-3 transition-all hover:border-neutral-600/50 ${
      isPublic
        ? 'bg-neutral-900/70 border-success-soft'
        : 'bg-neutral-900/40 border-neutral-800/40'
    }`}>
      {/* Header row */}
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 min-w-0">
          {/* Published badge */}
          {isPublic && (
            <span className="shrink-0 text-[9px] px-1.5 py-0.5 rounded-full font-semibold bg-success-soft text-success border border-success-soft">
              Public
            </span>
          )}
          <span className="text-sm font-medium text-neutral-200 truncate">{app.app_id}</span>
        </div>

        {/* Publish toggle */}
        <button
          onClick={handleToggle}
          disabled={toggling}
          title={isPublic ? 'Make private' : 'Publish to web'}
          aria-pressed={isPublic}
          className={`shrink-0 relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus-primary disabled:opacity-50 ${
            isPublic ? 'bg-success' : 'bg-neutral-700'
          }`}
        >
          <span className={`inline-block h-3.5 w-3.5 transform rounded-full bg-white shadow transition-transform ${
            isPublic ? 'translate-x-4' : 'translate-x-0.5'
          }`} />
        </button>
      </div>

      {/* Resource bars */}
      <div className="mt-2 space-y-1">
        <ResourceBar label="CPU" pct={cpuPct} warn={false} />
        <ResourceBar label="RAM" pct={memPct} warn={memWarn} />
      </div>

      {/* Memory warning */}
      <MemWarningBanner appId={app.app_id} memPct={memPct} />

      {/* Public URL + actions */}
      {isPublic && (
        <div className="mt-2 pt-2 border-t border-neutral-800/40 space-y-1.5">
          {loadingDeploy && (
            <span className="text-[10px] text-neutral-600">Loading URL...</span>
          )}
          {!loadingDeploy && deployment?.fqdn && (
            <div className="flex items-center gap-1.5">
              <span className="text-[10px] text-neutral-400 truncate flex-1 font-mono">
                {deployment.fqdn}
              </span>
              <button
                onClick={handleCopy}
                title="Copy link"
                className="shrink-0 text-[10px] px-2 py-0.5 rounded-md bg-neutral-800 hover:bg-neutral-700 text-neutral-400 hover:text-neutral-200 transition-colors"
              >
                {copied ? 'Copied' : 'Copy'}
              </button>
              <button
                onClick={handlePurge}
                disabled={purging}
                title="Purge cache"
                className="shrink-0 text-[10px] px-2 py-0.5 rounded-md bg-neutral-800 hover:bg-neutral-700 text-neutral-400 hover:text-neutral-200 transition-colors disabled:opacity-40"
              >
                {purging ? '...' : 'Purge'}
              </button>
            </div>
          )}
          {!loadingDeploy && !deployment?.fqdn && (
            <span className="text-[10px] text-neutral-600 italic">Subdomain provisioning...</span>
          )}
        </div>
      )}
    </div>
  )
}

// ── AppPublishCard (main export) ─────────────────────────────────────────────
// Self-contained "Web" section for the OS Dashboard.
// Lists all installed apps with publish toggle + resource usage.

export default function AppPublishCard() {
  const [apps, setApps] = useState(null)  // null = loading
  const [cgroups, setCgroups] = useState({}) // keyed by app_id
  const [error, setError] = useState(null)
  const timerRef = useRef(null)

  const loadData = useCallback(async () => {
    try {
      const [visData, cgData] = await Promise.all([
        apcFetchVisibility(),
        apcFetchCgroups(),
      ])

      // All apps (not just non-private) so the full list shows
      setApps(visData || [])
      setError(null)

      // Index cgroup data by app_id
      const cgMap = {}
      for (const entry of (cgData || [])) {
        cgMap[entry.app_id] = entry
      }
      setCgroups(cgMap)
    } catch {
      setError('Could not load app data. Retrying...')
    }
  }, [])

  // Initial load + poll every 5 s
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadData()
    timerRef.current = setInterval(loadData, APC_POLL_MS)
    return () => clearInterval(timerRef.current)
  }, [loadData])

  const publicApps = (apps || []).filter(a => a.visibility === 'public')
  const privateApps = (apps || []).filter(a => a.visibility !== 'public')

  return (
    <div className="flex flex-col h-full bg-neutral-950 text-neutral-200 overflow-y-auto">
      {/* Section header */}
      <div className="px-5 pt-5 pb-3 border-b border-neutral-800/50">
        <h2 className="text-base font-semibold text-neutral-100">Web Publishing</h2>
        <p className="text-xs text-neutral-500 mt-0.5">
          Toggle per-app visibility and monitor resource usage. Updates every 5 s.
        </p>
      </div>

      {/* Body */}
      <div className="flex-1 px-5 py-4 space-y-5 overflow-y-auto">
        {/* Error */}
        {error && (
          <div className="flex items-center gap-2 px-3 py-2 rounded-lg bg-danger-soft border border-danger-soft">
            <span className="text-xs text-danger">{error}</span>
          </div>
        )}

        {/* Loading */}
        {apps === null && !error && (
          <div className="flex items-center gap-2 py-10 justify-center text-neutral-600 text-xs">
            <span className="w-3.5 h-3.5 spinner" />
            Loading apps...
          </div>
        )}

        {/* Published apps */}
        {apps !== null && publicApps.length > 0 && (
          <section>
            <h3 className="text-xs font-semibold text-neutral-400 uppercase tracking-wider mb-2">
              Published ({publicApps.length})
            </h3>
            <div className="space-y-2">
              {publicApps.map(app => (
                <AppCard
                  key={app.app_id}
                  app={app}
                  cgroupInfo={cgroups[app.app_id]}
                  onToggle={loadData}
                />
              ))}
            </div>
          </section>
        )}

        {/* Private apps */}
        {apps !== null && privateApps.length > 0 && (
          <section>
            <h3 className="text-xs font-semibold text-neutral-400 uppercase tracking-wider mb-2">
              Private ({privateApps.length})
            </h3>
            <div className="space-y-2">
              {privateApps.map(app => (
                <AppCard
                  key={app.app_id}
                  app={app}
                  cgroupInfo={cgroups[app.app_id]}
                  onToggle={loadData}
                />
              ))}
            </div>
          </section>
        )}

        {/* Empty state */}
        {apps !== null && apps.length === 0 && !error && (
          <div className="py-16 text-center animate-[fadeIn_0.2s_ease-out]">
            <div className="w-14 h-14 mx-auto mb-4 grid place-items-center rounded-2xl border border-neutral-800 text-neutral-600">
              <svg viewBox="0 0 24 24" className="w-7 h-7" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="9"/><path d="M2 12h20M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18"/></svg>
            </div>
            <p className="text-sm text-neutral-400">No apps found.</p>
            <p className="text-xs text-neutral-600 mt-1.5 max-w-xs mx-auto leading-relaxed">Install apps from the App Hub to manage their visibility here.</p>
          </div>
        )}
      </div>
    </div>
  )
}
