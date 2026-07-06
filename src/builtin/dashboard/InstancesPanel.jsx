/**
 * InstancesPanel — MINST-07
 * Unified view of all account instances (device + cloud).
 * Polls /api/instances every 10 s; fetches /api/routing/apps per instance on demand.
 */
import { useState, useEffect, useCallback, useRef } from 'react'

// ── constants ─────────────────────────────────────────────────────────────────

const IP_POLL_MS = 10_000
const IP_RESOURCE_POLL_MS = 15_000

// ── API helpers ───────────────────────────────────────────────────────────────

async function ipFetchInstances() {
  const r = await fetch('/api/instances')
  if (!r.ok) throw new Error('instances fetch failed')
  return r.json() // [{id, display_name, type, online, last_seen, cpu_pct, ram_pct}]
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
  if (!r.ok) throw new Error('rename failed')
  return r.json()
}

async function ipRemoveInstance(id) {
  const r = await fetch(`/api/instances/${id}`, { method: 'DELETE' })
  if (!r.ok) throw new Error('remove failed')
}

async function ipGenerateInvite() {
  const r = await fetch('/api/instances/invite', { method: 'POST' })
  if (!r.ok) throw new Error('invite failed')
  return r.json() // {token, url}
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
  return (
    <span
      className={`inline-flex items-center gap-1 text-[10px] px-1.5 py-0.5 rounded-full font-medium border ${
        online
          ? 'bg-emerald-900/40 text-emerald-400 border-emerald-800/40'
          : 'bg-neutral-800/60 text-neutral-500 border-neutral-700/40'
      }`}
    >
      <span
        className={`w-1.5 h-1.5 rounded-full ${online ? 'bg-emerald-400 animate-pulse' : 'bg-neutral-600'}`}
      />
      {online ? 'Online' : 'Offline'}
    </span>
  )
}

// ── ResourceMiniBar ──────────────────────────────────────────────────────────

function ResourceMiniBar({ label, pct }) {
  const clamp = Math.min(Math.max(pct || 0, 0), 100)
  const color = clamp > 80 ? 'bg-amber-500' : clamp > 60 ? 'bg-blue-500' : 'bg-neutral-600'
  return (
    <div className="flex items-center gap-1.5 min-w-0">
      <span className="text-[10px] text-neutral-500 w-7 shrink-0">{label}</span>
      <div className="flex-1 h-1 rounded-full bg-neutral-800 overflow-hidden">
        <div className={`h-full rounded-full transition-all duration-500 ${color}`} style={{ width: `${clamp}%` }} />
      </div>
      <span className="text-[10px] text-neutral-500 w-7 text-right shrink-0">{Math.round(clamp)}%</span>
    </div>
  )
}

// ── RenameModal ───────────────────────────────────────────────────────────────

function RenameModal({ instance, onSave, onCancel }) {
  const [name, setName] = useState(instance.display_name || '')
  const [saving, setSaving] = useState(false)
  const inputRef = useRef(null)

  useEffect(() => { setTimeout(() => inputRef.current?.focus(), 50) }, [])

  const handleSave = useCallback(async () => {
    if (!name.trim() || saving) return
    setSaving(true)
    try {
      await ipRenameInstance(instance.id, name.trim())
      onSave(name.trim())
    } catch { /* noop */ }
    finally { setSaving(false) }
  }, [instance.id, name, saving, onSave])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm">
      <div className="bg-neutral-900 border border-neutral-700/50 rounded-2xl p-5 w-80 shadow-2xl">
        <h3 className="text-sm font-semibold text-neutral-100 mb-3">Rename Instance</h3>
        <input
          ref={inputRef}
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') handleSave(); if (e.key === 'Escape') onCancel() }}
          className="w-full bg-neutral-800 border border-neutral-700/60 rounded-lg px-3 py-2 text-sm text-neutral-100 outline-none focus:border-neutral-500/70 transition-colors"
          placeholder="Instance name"
        />
        <div className="flex gap-2 mt-4 justify-end">
          <button
            onClick={onCancel}
            className="px-3 py-1.5 text-xs rounded-lg bg-neutral-800 text-neutral-400 hover:bg-neutral-700 transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleSave}
            disabled={!name.trim() || saving}
            className="px-3 py-1.5 text-xs rounded-lg bg-blue-600 text-white hover:bg-blue-500 disabled:opacity-40 transition-colors"
          >
            {saving ? 'Saving...' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ── RemoveConfirmModal ────────────────────────────────────────────────────────

function RemoveConfirmModal({ instance, onConfirm, onCancel }) {
  const [removing, setRemoving] = useState(false)

  const handleConfirm = useCallback(async () => {
    if (removing) return
    setRemoving(true)
    try {
      await ipRemoveInstance(instance.id)
      onConfirm()
    } catch { /* noop */ }
    finally { setRemoving(false) }
  }, [instance.id, removing, onConfirm])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm">
      <div className="bg-neutral-900 border border-neutral-700/50 rounded-2xl p-5 w-80 shadow-2xl">
        <h3 className="text-sm font-semibold text-neutral-100 mb-2">Remove Instance?</h3>
        <p className="text-xs text-neutral-400 mb-4">
          Remove <span className="text-neutral-200 font-medium">{instance.display_name || truncateId(instance.id)}</span> from your account?
          This cannot be undone.
        </p>
        <div className="flex gap-2 justify-end">
          <button
            onClick={onCancel}
            className="px-3 py-1.5 text-xs rounded-lg bg-neutral-800 text-neutral-400 hover:bg-neutral-700 transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleConfirm}
            disabled={removing}
            className="px-3 py-1.5 text-xs rounded-lg bg-red-700 text-white hover:bg-red-600 disabled:opacity-40 transition-colors"
          >
            {removing ? 'Removing...' : 'Remove'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ── InviteModal ───────────────────────────────────────────────────────────────

function InviteModal({ onClose }) {
  const [invite, setInvite] = useState(null)
  const [loading, setLoading] = useState(true)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    ipGenerateInvite()
      .then(setInvite)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  const handleCopy = useCallback(() => {
    if (!invite?.url) return
    navigator.clipboard.writeText(invite.url).catch(() => {})
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }, [invite])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm">
      <div className="bg-neutral-900 border border-neutral-700/50 rounded-2xl p-5 w-80 shadow-2xl">
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-sm font-semibold text-neutral-100">Add Existing Device</h3>
          <button onClick={onClose} className="text-neutral-500 hover:text-neutral-300 text-lg leading-none">×</button>
        </div>

        {loading && (
          <div className="flex items-center justify-center py-8 text-neutral-600 text-xs gap-2">
            <span className="w-3.5 h-3.5 spinner" />
            Generating invite...
          </div>
        )}

        {!loading && invite && (
          <>
            {/* QR code placeholder — real QR would be rendered by backend */}
            <div className="flex items-center justify-center mb-3">
              <div className="w-32 h-32 bg-neutral-800 rounded-xl flex items-center justify-center text-neutral-600 text-xs border border-neutral-700/40">
                {invite.qr_svg
                  ? <div dangerouslySetInnerHTML={{ __html: invite.qr_svg }} />
                  : <span className="text-center px-2">QR code<br />provisioning...</span>
                }
              </div>
            </div>
            <p className="text-[10px] text-neutral-500 text-center mb-3">
              Scan with another Vulos device, or copy the link below.
            </p>
            <div className="flex items-center gap-1.5">
              <span className="text-[10px] font-mono text-neutral-400 flex-1 truncate bg-neutral-800 rounded-lg px-2 py-1.5">
                {invite.url || invite.token}
              </span>
              <button
                onClick={handleCopy}
                className="shrink-0 text-[10px] px-2 py-1.5 rounded-lg bg-neutral-700 hover:bg-neutral-600 text-neutral-300 transition-colors"
              >
                {copied ? 'Copied' : 'Copy'}
              </button>
            </div>
            <p className="text-[10px] text-neutral-600 mt-2 text-center">Link expires in 10 minutes.</p>
          </>
        )}

        {!loading && !invite && (
          <p className="text-xs text-red-400 text-center py-4">Could not generate invite. Try again.</p>
        )}
      </div>
    </div>
  )
}

// ── InstanceCard ──────────────────────────────────────────────────────────────

function InstanceCard({ instance, apps, onRename, onRemove }) {
  const [expanded, setExpanded] = useState(false)
  const instanceApps = (apps || []).filter(a => a.instance_id === instance.id)

  const typeLabel = instance.type === 'cloud' ? 'Cloud' : 'Device'
  const typeColor = instance.type === 'cloud'
    ? 'bg-indigo-900/40 text-indigo-400 border-indigo-800/40'
    : 'bg-neutral-800/60 text-neutral-400 border-neutral-700/40'

  return (
    <div className={`rounded-xl border transition-all ${
      instance.online
        ? 'bg-neutral-900/70 border-neutral-700/50'
        : 'bg-neutral-900/30 border-neutral-800/30'
    }`}>
      {/* Header */}
      <div className="p-3">
        <div className="flex items-start justify-between gap-2">
          <div className="flex items-center gap-2 min-w-0 flex-1">
            {/* Type badge */}
            <span className={`shrink-0 text-[9px] px-1.5 py-0.5 rounded-full font-semibold border ${typeColor}`}>
              {typeLabel}
            </span>
            {/* Name */}
            <span className="text-sm font-medium text-neutral-200 truncate">
              {instance.display_name || truncateId(instance.id)}
            </span>
          </div>
          <OnlineBadge online={instance.online} />
        </div>

        {/* ULID + last seen */}
        <div className="mt-1.5 flex items-center gap-3">
          <span className="text-[10px] font-mono text-neutral-600">{truncateId(instance.id)}</span>
          <span className="text-[10px] text-neutral-600">
            {instance.online ? 'Active now' : `Last seen ${formatLastSeen(instance.last_seen)}`}
          </span>
        </div>

        {/* Resource bars — only when online */}
        {instance.online && (instance.cpu_pct != null || instance.ram_pct != null) && (
          <div className="mt-2 space-y-1">
            {instance.cpu_pct != null && <ResourceMiniBar label="CPU" pct={instance.cpu_pct} />}
            {instance.ram_pct != null && <ResourceMiniBar label="RAM" pct={instance.ram_pct} />}
          </div>
        )}

        {/* Apps section toggle */}
        {instanceApps.length > 0 && (
          <button
            onClick={() => setExpanded(v => !v)}
            className="mt-2 text-[10px] text-neutral-500 hover:text-neutral-300 flex items-center gap-1 transition-colors"
          >
            <span className={`transition-transform ${expanded ? 'rotate-90' : ''}`}>▶</span>
            {instanceApps.length} app{instanceApps.length !== 1 ? 's' : ''}
          </button>
        )}
      </div>

      {/* Apps list (expanded) */}
      {expanded && instanceApps.length > 0 && (
        <div className="px-3 pb-3 pt-0 space-y-1.5 border-t border-neutral-800/40">
          {instanceApps.map(app => (
            <div key={app.app_id} className="flex items-center justify-between gap-2">
              <span className="text-xs text-neutral-400 truncate">{app.app_id}</span>
              {app.fqdn && (
                <a
                  href={`https://${app.fqdn}`}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="shrink-0 text-[10px] px-2 py-0.5 rounded-md bg-neutral-800 hover:bg-neutral-700 text-blue-400 hover:text-blue-300 transition-colors"
                  title={`Open ${app.app_id} on this instance`}
                >
                  Open ↗
                </a>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Actions */}
      <div className="px-3 pb-3 flex gap-2">
        <button
          onClick={onRename}
          className="text-[10px] px-2.5 py-1 rounded-lg bg-neutral-800 hover:bg-neutral-700 text-neutral-400 hover:text-neutral-200 transition-colors"
        >
          Rename
        </button>
        <button
          onClick={onRemove}
          className="text-[10px] px-2.5 py-1 rounded-lg bg-neutral-800 hover:bg-red-900/40 text-neutral-500 hover:text-red-400 transition-colors"
        >
          Remove
        </button>
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
  const [showInvite, setShowInvite] = useState(false)
  const pollRef = useRef(null)

  const loadData = useCallback(async () => {
    try {
      const [instData, appsData] = await Promise.all([
        ipFetchInstances(),
        ipFetchRoutingApps(),
      ])
      setInstances(instData || [])
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

  const online = (instances || []).filter(i => i.online)
  const offline = (instances || []).filter(i => !i.online)

  return (
    <div className="flex flex-col h-full bg-neutral-950 text-neutral-200 overflow-y-auto">
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
      {showInvite && <InviteModal onClose={() => setShowInvite(false)} />}

      {/* Section header */}
      <div className="px-5 pt-5 pb-3 border-b border-neutral-800/50 flex items-center justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-neutral-100">Instances</h2>
          <p className="text-xs text-neutral-500 mt-0.5">
            All devices and cloud nodes in your account. Updates every 10 s.
          </p>
        </div>
        <button
          onClick={() => setShowInvite(true)}
          className="shrink-0 text-xs px-3 py-1.5 rounded-lg bg-neutral-800 hover:bg-neutral-700 text-neutral-300 border border-neutral-700/50 transition-colors"
        >
          + Add device
        </button>
      </div>

      {/* Body */}
      <div className="flex-1 px-5 py-4 space-y-5 overflow-y-auto">
        {/* Error */}
        {error && (
          <div className="flex items-center gap-2 px-3 py-2 rounded-lg bg-red-900/20 border border-red-800/40">
            <span className="text-xs text-red-400">{error}</span>
          </div>
        )}

        {/* Loading */}
        {instances === null && !error && (
          <div className="flex items-center gap-2 py-10 justify-center text-neutral-600 text-xs">
            <span className="w-3.5 h-3.5 spinner" />
            Loading instances...
          </div>
        )}

        {/* Online instances */}
        {instances !== null && online.length > 0 && (
          <section>
            <h3 className="text-xs font-semibold text-neutral-400 uppercase tracking-wider mb-2">
              Online ({online.length})
            </h3>
            <div className="space-y-2">
              {online.map(inst => (
                <InstanceCard
                  key={inst.id}
                  instance={inst}
                  apps={apps}
                  onRename={() => setRenaming(inst)}
                  onRemove={() => setRemoving(inst)}
                />
              ))}
            </div>
          </section>
        )}

        {/* Offline instances */}
        {instances !== null && offline.length > 0 && (
          <section>
            <h3 className="text-xs font-semibold text-neutral-400 uppercase tracking-wider mb-2">
              Offline ({offline.length})
            </h3>
            <div className="space-y-2">
              {offline.map(inst => (
                <InstanceCard
                  key={inst.id}
                  instance={inst}
                  apps={apps}
                  onRename={() => setRenaming(inst)}
                  onRemove={() => setRemoving(inst)}
                />
              ))}
            </div>
          </section>
        )}

        {/* Empty state */}
        {instances !== null && instances.length === 0 && !error && (
          <div className="py-16 text-center">
            <div className="text-3xl mb-3 opacity-30">◎</div>
            <p className="text-sm text-neutral-500">No instances found.</p>
            <p className="text-xs text-neutral-600 mt-1">
              Add a device using the button above to link it to your account.
            </p>
          </div>
        )}
      </div>
    </div>
  )
}
