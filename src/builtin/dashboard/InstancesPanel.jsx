/**
 * InstancesPanel — MINST-07
 * Unified view of all account instances (device + cloud).
 * Polls /api/instances every 10 s; fetches /api/routing/apps per instance on demand.
 */
import { useState, useEffect, useCallback, useRef } from 'react'
import { Pill, EmptyState, Banner } from '../../core/settings/ui.jsx'

// ── constants ─────────────────────────────────────────────────────────────────

const IP_POLL_MS = 10_000
const IP_RESOURCE_POLL_MS = 15_000

// ── API helpers ───────────────────────────────────────────────────────────────

// ipError turns a failed box response into the message the box actually sent,
// so a refusal (403 admin-only, 409 owner, 404 gone) reaches the user instead of
// a generic shrug.
async function ipError(r, fallback) {
  try {
    const body = await r.json()
    if (body && body.error) return new Error(body.error)
  } catch { /* not JSON */ }
  return new Error(fallback)
}

// ipFetchInstances reads the registry roster. The box serves the registry's own
// shape — { instances: [{ ulid, kind, status, last_seen_at, role, … }] } — which
// this maps onto the panel's view model.
async function ipFetchInstances() {
  const r = await fetch('/api/instances')
  if (!r.ok) throw await ipError(r, 'instances fetch failed')
  const body = await r.json()
  const list = Array.isArray(body) ? body : (body?.instances || [])
  return list.map(inst => ({
    id: inst.ulid,
    display_name: inst.display_name,
    type: inst.kind === 'cloud' ? 'cloud' : 'device',
    online: inst.status === 'online',
    last_seen: inst.last_seen_at,
    is_owner: inst.role === 'owner',
    // NODE-CAP-01: a store-only member syncs but is never a route/ingress
    // target. Absent/false = serving (the safe default from a box or CP that
    // predates the field).
    store_only: !!inst.store_only,
    cpu_pct: inst.cpu_pct,
    ram_pct: inst.ram_pct,
  }))
}

async function ipFetchRoutingApps() {
  const r = await fetch('/api/routing/apps')
  if (!r.ok) return []
  return r.json() // [{app_id, instance_id, fqdn}]
}

async function ipRenameInstance(id, name) {
  const r = await fetch(`/api/instances/${id}/rename`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ display_name: name }),
  })
  if (!r.ok) throw await ipError(r, 'rename failed')
  return r.json()
}

async function ipRemoveInstance(id) {
  const r = await fetch(`/api/instances/${id}`, { method: 'DELETE' })
  if (!r.ok) throw await ipError(r, 'remove failed')
  return r.json()
}

// ipSetStoreOnly flips whether an instance serves the cluster (NODE-CAP-01).
// store_only=true → syncs but is never a route target. For this box (owner) the
// change is effective immediately; for a remote peer it is a local-view change
// until the control plane round-trips the flag.
async function ipSetStoreOnly(id, storeOnly) {
  const r = await fetch(`/api/instances/${id}/store-only`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ store_only: storeOnly }),
  })
  if (!r.ok) throw await ipError(r, 'update failed')
  return r.json()
}

// ── helpers ───────────────────────────────────────────────────────────────────

function formatLastSeen(ts) {
  if (!ts) return 'Never'
  const now = Date.now()
  const ms = now - new Date(ts).getTime()
  if (ms < 60_000) return 'Just now'
  if (ms < 3_600_000) return `${Math.floor(ms / 60_000)}m ago`
  if (ms < 86_400_000) return `${Math.floor(ms / 3_600_000)}h ago`
  return `${Math.floor(ms / 86_400_000)}d ago`
}

function truncateId(id) {
  if (!id) return ''
  return id.length > 16 ? `${id.slice(0, 8)}…${id.slice(-6)}` : id
}

// ── OnlineBadge ───────────────────────────────────────────────────────────────

function OnlineBadge({ online }) {
  return online
    ? <Pill tone="success" pulse>Online</Pill>
    : <Pill tone="neutral">Offline</Pill>
}

// ── ResourceMiniBar ──────────────────────────────────────────────────────────

function ResourceMiniBar({ label, pct }) {
  const clamp = Math.min(Math.max(pct || 0, 0), 100)
  const color = clamp > 80 ? 'bg-[var(--status-warning)]' : clamp > 60 ? 'accent-bg' : 'bg-[var(--text-ghost)]'
  return (
    <div className="flex items-center gap-2 min-w-0">
      <span className="text-[12px] uppercase tracking-wide text-[var(--text-muted)] w-7 shrink-0 font-medium">{label}</span>
      <div className="flex-1 h-1.5 rounded-full bg-[var(--bg-elevated)] overflow-hidden">
        <div className={`h-full rounded-full transition-[width] duration-500 ${color}`} style={{ width: `${clamp}%` }} />
      </div>
      <span className="text-[12px] text-[var(--text-tertiary)] w-8 text-right shrink-0 mono tabular-nums">{Math.round(clamp)}%</span>
    </div>
  )
}

// ── RenameModal ───────────────────────────────────────────────────────────────

function RenameModal({ instance, onSave, onCancel }) {
  const [name, setName] = useState(instance.display_name || '')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState(null)
  const inputRef = useRef(null)

  useEffect(() => { setTimeout(() => inputRef.current?.focus(), 50) }, [])

  const handleSave = useCallback(async () => {
    if (!name.trim() || saving) return
    setSaving(true)
    setError(null)
    try {
      await ipRenameInstance(instance.id, name.trim())
      onSave(name.trim())
    } catch (e) {
      setError(e.message || 'Rename failed.')
    }
    finally { setSaving(false) }
  }, [instance.id, name, saving, onSave])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-[var(--overlay)] backdrop-blur-sm p-4 animate-[fadeIn_0.15s_ease-out]">
      <div className="bg-[var(--bg-surface)] border border-[var(--border-strong)] rounded-2xl p-5 w-80 max-w-full shadow-2xl anim-sheet-up">
        <h3 className="flex items-center gap-2.5 text-sm font-semibold text-[var(--text-primary)] mb-4">
          <span aria-hidden="true" className="w-1 h-4 rounded-full accent-bg" /> Rename instance
        </h3>
        <input
          ref={inputRef}
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') handleSave(); if (e.key === 'Escape') onCancel() }}
          className="input"
          placeholder="Instance name"
        />
        {error && <p className="mt-2 text-[12px] text-danger">{error}</p>}
        <div className="flex gap-2 mt-4 justify-end">
          <button onClick={onCancel} className="btn-secondary text-xs px-3 py-1.5">Cancel</button>
          <button onClick={handleSave} disabled={!name.trim() || saving} className="btn-primary text-xs px-3 py-1.5 active:scale-[0.97]">
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ── RemoveConfirmModal ────────────────────────────────────────────────────────

function RemoveConfirmModal({ instance, onConfirm, onCancel }) {
  const [removing, setRemoving] = useState(false)
  const [error, setError] = useState(null)

  const handleConfirm = useCallback(async () => {
    if (removing) return
    setRemoving(true)
    setError(null)
    try {
      await ipRemoveInstance(instance.id)
      onConfirm()
    } catch (e) {
      // The instance stays in the list: the box did not remove it.
      setError(e.message || 'Remove failed.')
    }
    finally { setRemoving(false) }
  }, [instance.id, removing, onConfirm])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-[var(--overlay)] backdrop-blur-sm p-4 animate-[fadeIn_0.15s_ease-out]">
      <div className="bg-[var(--bg-surface)] border border-[var(--border-strong)] rounded-2xl p-5 w-80 max-w-full shadow-2xl anim-sheet-up">
        <h3 className="flex items-center gap-2.5 text-sm font-semibold text-[var(--text-primary)] mb-2">
          <span aria-hidden="true" className="w-1 h-4 rounded-full bg-danger" /> Remove instance?
        </h3>
        <p className="text-xs text-[var(--text-tertiary)] mb-4 leading-relaxed">
          Remove <span className="text-[var(--text-primary)] font-medium">{instance.display_name || truncateId(instance.id)}</span> from your account?
          This cannot be undone.
        </p>
        {error && <p className="text-[12px] text-danger mb-3">{error}</p>}
        <div className="flex gap-2 justify-end">
          <button onClick={onCancel} className="btn-secondary text-xs px-3 py-1.5">Cancel</button>
          <button
            onClick={handleConfirm}
            disabled={removing}
            className="px-3 py-1.5 text-xs rounded-lg bg-danger text-white font-medium hover:brightness-110 active:scale-[0.97] disabled:opacity-40 transition-all focus-primary"
          >
            {removing ? 'Removing…' : 'Remove'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ── InstanceCard ──────────────────────────────────────────────────────────────

function InstanceCard({ instance, apps, onRename, onRemove, onStoreOnlyChanged }) {
  const [expanded, setExpanded] = useState(false)
  const [toggling, setToggling] = useState(false)
  const [toggleErr, setToggleErr] = useState(null)
  const instanceApps = (apps || []).filter(a => a.instance_id === instance.id)

  const handleToggleStoreOnly = useCallback(async () => {
    if (toggling) return
    setToggling(true)
    setToggleErr(null)
    const next = !instance.store_only
    try {
      await ipSetStoreOnly(instance.id, next)
      onStoreOnlyChanged(instance.id, next)
    } catch (e) {
      // Leave the state as-is: the box did not change it.
      setToggleErr(e.message || 'Update failed.')
    } finally {
      setToggling(false)
    }
  }, [instance.id, instance.store_only, toggling, onStoreOnlyChanged])

  const typeLabel = instance.type === 'cloud' ? 'Cloud' : 'Device'

  return (
    <div className={`rounded-xl border transition-all hover:shadow-md ${
      instance.online
        ? 'bg-[var(--bg-surface)] border-[var(--border-default)] hover:border-[var(--border-strong)]'
        : 'bg-[var(--bg-surface)]/50 border-[var(--border-subtle)]'
    }`}>
      {/* Header */}
      <div className="p-3.5">
        <div className="flex items-start justify-between gap-2">
          <div className="flex items-center gap-2 min-w-0 flex-1">
            <span aria-hidden="true" className={`shrink-0 grid place-items-center w-8 h-8 rounded-xl ${instance.type === 'cloud' ? 'accent-bg-soft accent-text' : 'bg-[var(--bg-elevated)] text-[var(--text-tertiary)]'}`}>
              {instance.type === 'cloud'
                ? <svg viewBox="0 0 24 24" className="w-[18px] h-[18px]" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><path d="M17.5 19a4.5 4.5 0 0 0 .5-8.97A6 6 0 0 0 6.34 9.4 4 4 0 0 0 7 17h10.5Z"/></svg>
                : <svg viewBox="0 0 24 24" className="w-[18px] h-[18px]" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/></svg>}
            </span>
            <div className="min-w-0">
              <div className="flex items-center gap-1.5">
                <span className="text-sm font-semibold text-[var(--text-primary)] truncate">
                  {instance.display_name || truncateId(instance.id)}
                </span>
              </div>
              <div className="flex items-center gap-2 mt-0.5">
                <span className="text-[12px] text-[var(--text-muted)]">{typeLabel}</span>
                <span aria-hidden="true" className="w-0.5 h-0.5 rounded-full bg-[var(--text-ghost)]" />
                <span className="text-[12px] text-[var(--text-muted)]">
                  {instance.online ? 'Active now' : `Last seen ${formatLastSeen(instance.last_seen)}`}
                </span>
              </div>
            </div>
          </div>
          <div className="flex items-center gap-1.5 shrink-0">
            {/* NODE-CAP-01: store-only members sync but never serve routed traffic. */}
            {instance.store_only && (
              <span title="Syncs data but does not serve app traffic — never a route target">
                <Pill tone="warning" dot={false}>Sync-only</Pill>
              </span>
            )}
            <OnlineBadge online={instance.online} />
          </div>
        </div>

        {/* ULID */}
        <div className="mt-2 text-[12px] mono text-[var(--text-ghost)]">{truncateId(instance.id)}</div>

        {/* Resource bars — only when online */}
        {instance.online && (instance.cpu_pct != null || instance.ram_pct != null) && (
          <div className="mt-2.5 space-y-1.5">
            {instance.cpu_pct != null && <ResourceMiniBar label="CPU" pct={instance.cpu_pct} />}
            {instance.ram_pct != null && <ResourceMiniBar label="RAM" pct={instance.ram_pct} />}
          </div>
        )}

        {/* Apps section toggle */}
        {instanceApps.length > 0 && (
          <button
            onClick={() => setExpanded(v => !v)}
            className="mt-2.5 text-[12px] text-[var(--text-tertiary)] hover:text-[var(--text-secondary)] flex items-center gap-1.5 transition-colors"
          >
            <span aria-hidden="true" className={`transition-transform text-[8px] ${expanded ? 'rotate-90' : ''}`}>▶</span>
            {instanceApps.length} app{instanceApps.length !== 1 ? 's' : ''}
          </button>
        )}
      </div>

      {/* Apps list (expanded) */}
      {expanded && instanceApps.length > 0 && (
        <div className="px-3.5 pb-3.5 pt-0 space-y-1.5 border-t border-[var(--border-subtle)] mt-1">
          {instanceApps.map(app => (
            <div key={app.app_id} className="flex items-center justify-between gap-2 pt-1.5">
              <span className="text-xs text-[var(--text-secondary)] truncate">{app.app_id}</span>
              {app.fqdn && (
                <a
                  href={`https://${app.fqdn}`}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="shrink-0 text-[12px] px-2 py-0.5 rounded-md bg-[var(--bg-elevated)] hover:bg-[var(--bg-hover)] accent-text transition-colors focus-primary"
                  title={`Open ${app.app_id} on this instance`}
                >
                  Open ↗
                </a>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Actions — the owner instance is this box: it cannot remove itself from
          its own fleet (the box refuses with a 409), so it is not offered. */}
      <div className="px-3.5 pb-3.5 flex flex-wrap items-center gap-2">
        <button
          onClick={onRename}
          className="text-[12px] px-2.5 py-1.5 rounded-lg bg-[var(--bg-elevated)] hover:bg-[var(--bg-hover)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)] transition-colors focus-primary"
        >
          Rename
        </button>
        {/* NODE-CAP-01: toggle whether this device serves the cluster. */}
        {/* aria-pressed conveys current serving state; the visible text is the
            accessible name (WCAG 2.5.3 — no divergent aria-label). */}
        <button
          onClick={handleToggleStoreOnly}
          disabled={toggling}
          aria-pressed={!!instance.store_only}
          title={instance.store_only
            ? 'Currently sync-only. Make this device serve app traffic to the cluster.'
            : 'Currently serving. Make this device sync data only — never a route target.'}
          className="text-[12px] px-2.5 py-1.5 rounded-lg bg-[var(--bg-elevated)] hover:bg-[var(--bg-hover)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)] transition-colors focus-primary disabled:opacity-40"
        >
          {toggling ? 'Updating…' : instance.store_only ? 'Make serving' : 'Make sync-only'}
        </button>
        {!instance.is_owner && (
          <button
            onClick={onRemove}
            className="text-[12px] px-2.5 py-1.5 rounded-lg bg-[var(--bg-elevated)] hover:bg-danger-soft text-[var(--text-muted)] hover:text-danger transition-colors focus-primary"
          >
            Remove
          </button>
        )}
        {toggleErr && <span role="alert" className="text-[12px] text-danger w-full">{toggleErr}</span>}
      </div>
    </div>
  )
}

// ── InstancesPanel (main export) ─────────────────────────────────────────────

export default function InstancesPanel() {
  const [instances, setInstances] = useState(null) // null = loading
  const [apps, setApps] = useState([])
  const [error, setError] = useState(null)
  const [renaming, setRenaming] = useState(null)   // instance object
  const [removing, setRemoving] = useState(null)   // instance object
  const pollRef = useRef(null)
  // NODE-CAP-01: optimistic-override map (id → store_only just set by the user).
  // The 10s poll does a wholesale setInstances; a GET that was already in flight
  // when the user toggled would otherwise resolve with pre-toggle data and
  // revert the switch for one cycle. We re-apply pending values over every poll
  // result until the server reflects them, then drop the override.
  const pendingStoreOnlyRef = useRef({})

  const loadData = useCallback(async () => {
    try {
      const [instData, appsData] = await Promise.all([
        ipFetchInstances(),
        ipFetchRoutingApps(),
      ])
      const pending = pendingStoreOnlyRef.current
      const liveIds = new Set()
      const merged = (instData || []).map(i => {
        liveIds.add(i.id)
        const p = pending[i.id]
        if (!p) return i
        if (i.store_only === p.value) {
          delete pending[i.id] // server caught up — stop overriding
          return i
        }
        p.ttl -= 1
        if (p.ttl <= 0) {
          delete pending[i.id] // server never converged — trust it, don't override forever
          return i
        }
        return { ...i, store_only: p.value } // stale poll — hold the optimistic value
      })
      // Prune overrides whose instance left the roster (removed/vanished), so the
      // pending map can't grow unbounded.
      for (const id of Object.keys(pending)) {
        if (!liveIds.has(id)) delete pending[id]
      }
      setInstances(merged)
      setApps(appsData || [])
      setError(null)
    } catch {
      setError('Could not load instances. Retrying...')
    }
  }, [])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadData()
    pollRef.current = setInterval(loadData, IP_POLL_MS)
    return () => clearInterval(pollRef.current)
  }, [loadData])

  const handleRenameSuccess = useCallback((instanceId, newName) => {
    setInstances(prev => (prev || []).map(i => i.id === instanceId ? { ...i, display_name: newName } : i))
    setRenaming(null)
  }, [])

  const handleRemoveSuccess = useCallback((instanceId) => {
    setInstances(prev => (prev || []).filter(i => i.id !== instanceId))
    setRemoving(null)
  }, [])

  const handleStoreOnlyChanged = useCallback((instanceId, storeOnly) => {
    // Record the override (with a small poll budget) so an in-flight poll can't
    // revert it, but it can't override server truth forever either (see loadData).
    pendingStoreOnlyRef.current[instanceId] = { value: storeOnly, ttl: 3 }
    setInstances(prev => (prev || []).map(i => i.id === instanceId ? { ...i, store_only: storeOnly } : i))
  }, [])

  const online = (instances || []).filter(i => i.online)
  const offline = (instances || []).filter(i => !i.online)

  const total = (instances || []).length

  return (
    <div className="flex flex-col h-full bg-[var(--bg-base)] text-[var(--text-primary)] overflow-y-auto">
      {/* Modals */}
      {renaming && (
        <RenameModal
          instance={renaming}
          onSave={(name) => handleRenameSuccess(renaming.id, name)}
          onCancel={() => setRenaming(null)}
        />
      )}
      {removing && (
        <RemoveConfirmModal
          instance={removing}
          onConfirm={() => handleRemoveSuccess(removing.id)}
          onCancel={() => setRemoving(null)}
        />
      )}

      {/* Section header */}
      <div className="px-5 pt-5 pb-4 border-b border-[var(--border-default)]">
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-2.5">
            <span aria-hidden="true" className="grid place-items-center w-8 h-8 rounded-xl accent-bg-soft accent-text">
              <svg viewBox="0 0 24 24" className="w-[18px] h-[18px]" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/></svg>
            </span>
            <div>
              <h2 className="text-[15px] font-semibold text-[var(--text-primary)] tracking-tight">Instances</h2>
              <p className="text-xs text-[var(--text-tertiary)] mt-0.5">
                All devices and cloud nodes in your account. Live, every 10 s.
              </p>
            </div>
          </div>
          {total > 0 && (
            <div className="hidden sm:flex items-center gap-2">
              <Pill tone="success" pulse>{online.length} online</Pill>
              {offline.length > 0 && <Pill tone="neutral">{offline.length} offline</Pill>}
            </div>
          )}
        </div>
      </div>

      {/* Body */}
      <div className="flex-1 px-5 py-5 space-y-6 overflow-y-auto">
        {/* Error */}
        {error && <Banner tone="danger">{error}</Banner>}

        {/* Loading */}
        {instances === null && !error && (
          <div className="flex items-center gap-2 py-10 justify-center text-[var(--text-muted)] text-xs">
            <span className="w-3.5 h-3.5 spinner" />
            Loading instances…
          </div>
        )}

        {/* Online instances */}
        {instances !== null && online.length > 0 && (
          <section>
            <h3 className="flex items-center gap-2 text-[12px] font-semibold text-[var(--text-tertiary)] uppercase tracking-[0.1em] mb-2.5">
              Online
              <span className="accent-bg-soft accent-text rounded-full px-1.5 py-0.5 text-[12px] tabular-nums">{online.length}</span>
            </h3>
            <div className="space-y-2.5">
              {online.map(inst => (
                <InstanceCard
                  key={inst.id}
                  instance={inst}
                  apps={apps}
                  onRename={() => setRenaming(inst)}
                  onRemove={() => setRemoving(inst)}
                  onStoreOnlyChanged={handleStoreOnlyChanged}
                />
              ))}
            </div>
          </section>
        )}

        {/* Offline instances */}
        {instances !== null && offline.length > 0 && (
          <section>
            <h3 className="flex items-center gap-2 text-[12px] font-semibold text-[var(--text-tertiary)] uppercase tracking-[0.1em] mb-2.5">
              Offline
              <span className="bg-[var(--bg-elevated)] text-[var(--text-muted)] rounded-full px-1.5 py-0.5 text-[12px] tabular-nums">{offline.length}</span>
            </h3>
            <div className="space-y-2.5">
              {offline.map(inst => (
                <InstanceCard
                  key={inst.id}
                  instance={inst}
                  apps={apps}
                  onRename={() => setRenaming(inst)}
                  onRemove={() => setRemoving(inst)}
                  onStoreOnlyChanged={handleStoreOnlyChanged}
                />
              ))}
            </div>
          </section>
        )}

        {/* Empty state */}
        {instances !== null && instances.length === 0 && !error && (
          <EmptyState
            icon="storagemode"
            title="No instances found"
            hint="A new device joins this account from its own setup wizard — choose “Join an existing system” there, and it appears here."
          />
        )}
      </div>
    </div>
  )
}
