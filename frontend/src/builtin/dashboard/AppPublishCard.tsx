/**
 * AppPublishCard — PUBWEB-05
 * Dashboard "Web" section: per-app publish toggle + live resource usage bars.
 * Polls /api/apps/visibility (toggle), /api/cgroups/status (usage), and
 * /api/apps/{id}/deployment (public URL) every 5 s.
 */
import { useState, useEffect, useCallback, useRef } from 'react'
import { Pill, EmptyState, Banner } from '../../core/settings/ui'

// ── constants ─────────────────────────────────────────────────────────────────

const APC_POLL_MS = 5_000
// memory.high default for published apps per PUBWEB-04 spec (128 MiB)
const APC_MEM_HIGH_DEFAULT = 128 * 1024 * 1024
const APC_WARN_THRESHOLD = 0.8 // 80 % of memory.high

// Mirrors backend/services/appnet/visibility_api.go's Visibility field
// ("private" | "local" | "public"; default "private").
type ApcVisibility = 'private' | 'local' | 'public'

interface ApcVisibilityEntry {
  app_id: string
  visibility: ApcVisibility
}

interface ApcCgroupEntry {
  app_id: string
  cpu_pct?: number
  mem_current?: number
  mem_high?: number
  mem_max?: number
}

interface ApcDeployment {
  fqdn?: string
}

// ── wire narrowing ───────────────────────────────────────────────────────────
// Wire replies are untrusted network JSON — narrow field-by-field rather than
// trusting the array/object shape wholesale.

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function isApcVisibility(v: unknown): v is ApcVisibility {
  return v === 'private' || v === 'local' || v === 'public'
}

function toApcVisibilityEntry(x: unknown): ApcVisibilityEntry | null {
  if (!isRecord(x)) return null
  const appId = typeof x.app_id === 'string' ? x.app_id : null
  const visibility = isApcVisibility(x.visibility) ? x.visibility : null
  if (!appId || !visibility) return null
  return { app_id: appId, visibility }
}
function toApcVisibilityEntries(x: unknown): ApcVisibilityEntry[] {
  if (!Array.isArray(x)) return []
  return x.map(toApcVisibilityEntry).filter((e): e is ApcVisibilityEntry => e !== null)
}

function toApcCgroupEntry(x: unknown): ApcCgroupEntry | null {
  if (!isRecord(x)) return null
  const appId = typeof x.app_id === 'string' ? x.app_id : null
  if (!appId) return null
  return {
    app_id: appId,
    cpu_pct: typeof x.cpu_pct === 'number' ? x.cpu_pct : undefined,
    mem_current: typeof x.mem_current === 'number' ? x.mem_current : undefined,
    mem_high: typeof x.mem_high === 'number' ? x.mem_high : undefined,
    mem_max: typeof x.mem_max === 'number' ? x.mem_max : undefined,
  }
}
function toApcCgroupEntries(x: unknown): ApcCgroupEntry[] {
  if (!Array.isArray(x)) return []
  return x.map(toApcCgroupEntry).filter((e): e is ApcCgroupEntry => e !== null)
}

function toApcDeployment(x: unknown): ApcDeployment | null {
  if (!isRecord(x)) return null
  return { fqdn: typeof x.fqdn === 'string' ? x.fqdn : undefined }
}

// ── API helpers ───────────────────────────────────────────────────────────────

async function apcFetchVisibility(): Promise<ApcVisibilityEntry[]> {
  const r = await fetch('/api/apps/visibility')
  if (!r.ok) throw new Error('vis fetch failed')
  return toApcVisibilityEntries(await r.json())
}

async function apcPatchVisibility(appId: string, visibility: ApcVisibility): Promise<unknown> {
  const r = await fetch(`/api/apps/${appId}/visibility`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ visibility }),
  })
  if (!r.ok) throw new Error('patch failed')
  return r.json()
}

async function apcFetchCgroups(): Promise<ApcCgroupEntry[]> {
  const r = await fetch('/api/cgroups/status')
  if (!r.ok) return []
  return toApcCgroupEntries(await r.json())
}

async function apcFetchDeployment(appId: string): Promise<ApcDeployment | null> {
  const r = await fetch(`/api/apps/${appId}/deployment`)
  if (!r.ok) return null
  return toApcDeployment(await r.json())
}

async function apcPurgeCache(appId: string): Promise<void> {
  await fetch(`/api/apps/${appId}/cache/purge`, { method: 'POST' })
}

// ── ResourceBar ──────────────────────────────────────────────────────────────

interface ResourceBarProps {
  label: string
  pct: number
  warn: boolean
}

function ResourceBar({ label, pct, warn }: ResourceBarProps) {
  const clamp = Math.min(Math.max(pct || 0, 0), 100)
  const barColor = warn ? 'bg-[var(--status-warning)]' : clamp > 60 ? 'accent-bg' : 'bg-[var(--text-ghost)]'

  return (
    <div className="flex items-center gap-2.5 min-w-0">
      <span className="text-[12px] uppercase tracking-wide text-[var(--text-muted)] w-8 shrink-0 font-medium">{label}</span>
      <div className="flex-1 h-1.5 rounded-full bg-[var(--bg-elevated)] overflow-hidden">
        <div
          className={`h-full rounded-full transition-[width] duration-500 ${barColor}`}
          style={{ width: `${clamp}%` }}
        />
      </div>
      <span className={`text-[12px] w-9 text-right shrink-0 mono tabular-nums ${warn ? 'text-warning' : 'text-[var(--text-tertiary)]'}`}>
        {Math.round(clamp)}%
      </span>
    </div>
  )
}

// ── MemWarningBanner ─────────────────────────────────────────────────────────

interface MemWarningBannerProps {
  appId: string
  memPct: number
}

function MemWarningBanner({ appId, memPct }: MemWarningBannerProps) {
  if (memPct < APC_WARN_THRESHOLD * 100) return null
  return (
    <div className="mt-2">
      <Banner tone="warning">{appId} is approaching its memory limit ({Math.round(memPct)}%)</Banner>
    </div>
  )
}

// ── AppCard ──────────────────────────────────────────────────────────────────

interface AppCardProps {
  app: ApcVisibilityEntry
  cgroupInfo: ApcCgroupEntry | undefined
  onToggle: () => void
}

function AppCard({ app, cgroupInfo, onToggle }: AppCardProps) {
  const [deployment, setDeployment] = useState<ApcDeployment | null>(null)
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
    const next: ApcVisibility = isPublic ? 'private' : 'public'
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
  const memHigh = cgroupInfo?.mem_high || APC_MEM_HIGH_DEFAULT
  const memCurrent = cgroupInfo?.mem_current || 0
  const cpuPct = cgroupInfo?.cpu_pct || 0
  const memPct = memHigh > 0 ? (memCurrent / memHigh) * 100 : 0
  const memWarn = memPct >= APC_WARN_THRESHOLD * 100

  return (
    <div className={`rounded-xl border p-3.5 transition-all hover:shadow-md ${
      isPublic
        ? 'bg-[var(--bg-surface)] border-success-soft'
        : 'bg-[var(--bg-surface)]/60 border-[var(--border-default)] hover:border-[var(--border-strong)]'
    }`}>
      {/* Header row */}
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 min-w-0">
          <span className="text-sm font-semibold text-[var(--text-primary)] truncate">{app.app_id}</span>
          {isPublic ? (
            <Pill tone="success">Public</Pill>
          ) : (
            <Pill tone="neutral" dot={false}>Private</Pill>
          )}
        </div>

        {/* Publish toggle */}
        <button
          onClick={handleToggle}
          disabled={toggling}
          title={isPublic ? 'Make private' : 'Publish to web'}
          aria-pressed={isPublic}
          className={`shrink-0 relative inline-flex h-[22px] w-[40px] items-center rounded-full transition-colors focus-primary disabled:opacity-50 ${
            isPublic ? 'bg-[var(--status-success)]' : 'bg-[var(--border-emphasis)]'
          }`}
        >
          <span className={`inline-block h-[16px] w-[16px] transform rounded-full bg-white shadow transition-transform ${
            isPublic ? 'translate-x-[21px]' : 'translate-x-[3px]'
          }`} />
        </button>
      </div>

      {/* Resource bars */}
      <div className="mt-3 space-y-1.5">
        <ResourceBar label="CPU" pct={cpuPct} warn={false} />
        <ResourceBar label="RAM" pct={memPct} warn={memWarn} />
      </div>

      {/* Memory warning */}
      <MemWarningBanner appId={app.app_id} memPct={memPct} />

      {/* Public URL + actions */}
      {isPublic && (
        <div className="mt-3 pt-3 border-t border-[var(--border-subtle)] space-y-1.5">
          {loadingDeploy && (
            <span className="text-[12px] text-[var(--text-muted)]">Loading URL…</span>
          )}
          {!loadingDeploy && deployment?.fqdn && (
            <div className="flex items-center gap-1.5">
              <span className="text-[12px] text-[var(--text-secondary)] truncate flex-1 mono">
                {deployment.fqdn}
              </span>
              <button
                onClick={handleCopy}
                title="Copy link"
                className="shrink-0 text-[12px] px-2 py-1 rounded-md bg-[var(--bg-elevated)] hover:bg-[var(--bg-hover)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)] transition-colors"
              >
                {copied ? 'Copied' : 'Copy'}
              </button>
              <button
                onClick={handlePurge}
                disabled={purging}
                title="Purge cache"
                className="shrink-0 text-[12px] px-2 py-1 rounded-md bg-[var(--bg-elevated)] hover:bg-[var(--bg-hover)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)] transition-colors disabled:opacity-40"
              >
                {purging ? '…' : 'Purge'}
              </button>
            </div>
          )}
          {!loadingDeploy && !deployment?.fqdn && (
            <span className="text-[12px] text-[var(--text-muted)] italic">Subdomain provisioning…</span>
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
  const [apps, setApps] = useState<ApcVisibilityEntry[] | null>(null)  // null = loading
  const [cgroups, setCgroups] = useState<Record<string, ApcCgroupEntry>>({}) // keyed by app_id
  const [error, setError] = useState<string | null>(null)
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)

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
      const cgMap: Record<string, ApcCgroupEntry> = {}
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
    return () => { if (timerRef.current) clearInterval(timerRef.current) }
  }, [loadData])

  const publicApps = (apps || []).filter(a => a.visibility === 'public')
  const privateApps = (apps || []).filter(a => a.visibility !== 'public')

  return (
    <div className="flex flex-col h-full bg-[var(--bg-base)] text-[var(--text-primary)] overflow-y-auto">
      {/* Section header */}
      <div className="px-5 pt-5 pb-4 border-b border-[var(--border-default)]">
        <div className="flex items-center gap-2.5">
          <span aria-hidden="true" className="grid place-items-center w-8 h-8 rounded-xl accent-bg-soft accent-text">
            <svg viewBox="0 0 24 24" className="w-[18px] h-[18px]" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="9"/><path d="M2.5 12h19M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18"/></svg>
          </span>
          <div>
            <h2 className="text-[15px] font-semibold text-[var(--text-primary)] tracking-tight">Web Publishing</h2>
            <p className="text-xs text-[var(--text-tertiary)] mt-0.5">
              Toggle per-app visibility and monitor resource usage. Live, every 5 s.
            </p>
          </div>
        </div>
      </div>

      {/* Body */}
      <div className="flex-1 px-5 py-5 space-y-6 overflow-y-auto">
        {/* Error */}
        {error && <Banner tone="danger">{error}</Banner>}

        {/* Loading */}
        {apps === null && !error && (
          <div className="flex items-center gap-2 py-10 justify-center text-[var(--text-muted)] text-xs">
            <span className="w-3.5 h-3.5 spinner" />
            Loading apps…
          </div>
        )}

        {/* Published apps */}
        {apps !== null && publicApps.length > 0 && (
          <section>
            <h3 className="flex items-center gap-2 text-[12px] font-semibold text-[var(--text-tertiary)] uppercase tracking-[0.1em] mb-2.5">
              Published
              <span className="accent-bg-soft accent-text rounded-full px-1.5 py-0.5 text-[12px] tabular-nums">{publicApps.length}</span>
            </h3>
            <div className="space-y-2.5">
              {publicApps.map(app => (
                <AppCard key={app.app_id} app={app} cgroupInfo={cgroups[app.app_id]} onToggle={loadData} />
              ))}
            </div>
          </section>
        )}

        {/* Private apps */}
        {apps !== null && privateApps.length > 0 && (
          <section>
            <h3 className="flex items-center gap-2 text-[12px] font-semibold text-[var(--text-tertiary)] uppercase tracking-[0.1em] mb-2.5">
              Private
              <span className="bg-[var(--bg-elevated)] text-[var(--text-muted)] rounded-full px-1.5 py-0.5 text-[12px] tabular-nums">{privateApps.length}</span>
            </h3>
            <div className="space-y-2.5">
              {privateApps.map(app => (
                <AppCard key={app.app_id} app={app} cgroupInfo={cgroups[app.app_id]} onToggle={loadData} />
              ))}
            </div>
          </section>
        )}

        {/* Empty state */}
        {apps !== null && apps.length === 0 && !error && (
          <EmptyState
            icon="network"
            title="No apps found"
            hint="Install apps from the App Hub to manage their web visibility and resources here."
          />
        )}
      </div>
    </div>
  )
}
