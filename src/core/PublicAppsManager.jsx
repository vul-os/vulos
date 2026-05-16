/**
 * PublicAppsManager — AI-04
 * Self-contained popover for managing per-app network visibility.
 * Opens via window CustomEvent `vulos:open-public-apps`.
 * All identifiers are PAM- or PublicAppsManager- prefixed to avoid collisions.
 */
import { useState, useEffect, useCallback, useRef } from 'react'

// ── Constants ─────────────────────────────────────────────────────────────────

const PAM_EVENT = 'vulos:open-public-apps'
const PAM_VISIBILITY_OPTIONS = [
  { value: 'private', label: 'Private', desc: 'Only you, on this device' },
  { value: 'local',   label: 'Local',   desc: 'Anyone on your network' },
  { value: 'public',  label: 'Public',  desc: 'Anyone with the link' },
]
const PAM_BADGE_MAP = {
  private: { text: 'Private', cls: 'bg-neutral-800 text-neutral-500' },
  local:   { text: 'Local',   cls: 'bg-amber-900/40 text-amber-400' },
  public:  { text: 'Public',  cls: 'bg-green-900/40 text-green-400' },
}

// ── API helpers ───────────────────────────────────────────────────────────────

async function pamFetchVisibility() {
  const res = await fetch('/api/apps/visibility')
  if (!res.ok) throw new Error('fetch failed')
  return res.json() // [{app_id, visibility}]
}

async function pamSetVisibility(appId, visibility) {
  const res = await fetch(`/api/apps/${appId}/visibility`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ visibility }),
  })
  if (!res.ok) throw new Error('set failed')
  return res.json()
}

// ── First-time public confirmation dialog ─────────────────────────────────────

function PamConfirmPublicDialog({ appId, onConfirm, onCancel }) {
  return (
    <div className="fixed inset-0 z-[200] flex items-center justify-center">
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-black/60 backdrop-blur-sm"
        onClick={onCancel}
      />
      {/* Dialog */}
      <div className="relative z-10 w-[360px] rounded-2xl bg-neutral-900 border border-neutral-700/60 shadow-2xl shadow-black/60 p-6 animate-[fadeIn_0.15s_ease-out]">
        <div className="flex items-start gap-3 mb-4">
          <div className="w-9 h-9 rounded-xl bg-green-900/30 border border-green-700/30 flex items-center justify-center shrink-0 mt-0.5">
            <svg viewBox="0 0 16 16" className="w-4 h-4 text-green-400" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round">
              <circle cx="8" cy="8" r="6.5" />
              <path d="M5 8.5l2 2 4-4" />
            </svg>
          </div>
          <div>
            <h3 className="text-sm font-semibold text-neutral-100 mb-1">Make app public?</h3>
            <p className="text-xs text-neutral-400 leading-relaxed">
              Setting <span className="font-medium text-neutral-200">{appId}</span> to{' '}
              <span className="font-medium text-green-400">Public</span> means anyone with your
              device URL can open it. Only do this if you trust your network or have a public URL
              you want to share.
            </p>
          </div>
        </div>

        <div className="flex gap-2 justify-end">
          <button
            onClick={onCancel}
            className="px-4 py-1.5 rounded-lg text-sm text-neutral-400 hover:text-neutral-200 hover:bg-neutral-800 transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={onConfirm}
            className="px-4 py-1.5 rounded-lg text-sm font-medium bg-green-700/80 hover:bg-green-600/80 text-white transition-colors"
          >
            Make Public
          </button>
        </div>
      </div>
    </div>
  )
}

// ── Per-app visibility row ────────────────────────────────────────────────────

function PamAppRow({ entry, onChanged }) {
  const [saving, setSaving] = useState(false)
  const [confirm, setConfirm] = useState(null) // {appId, nextVis} when awaiting confirmation

  const handleChange = useCallback(async (nextVis) => {
    if (nextVis === entry.visibility) return

    // First-time public guard
    if (nextVis === 'public') {
      setConfirm({ appId: entry.app_id, nextVis })
      return
    }

    setSaving(true)
    try {
      await pamSetVisibility(entry.app_id, nextVis)
      onChanged()
    } catch { /* silently fail – list will refresh */ }
    finally { setSaving(false) }
  }, [entry, onChanged])

  const handleConfirm = useCallback(async () => {
    if (!confirm) return
    setConfirm(null)
    setSaving(true)
    try {
      await pamSetVisibility(confirm.appId, confirm.nextVis)
      onChanged()
    } catch {}
    finally { setSaving(false) }
  }, [confirm, onChanged])

  const badge = PAM_BADGE_MAP[entry.visibility] || PAM_BADGE_MAP.private

  return (
    <>
      <div className="flex items-center justify-between py-2.5 border-b border-neutral-800/40 last:border-0 gap-3">
        <div className="flex items-center gap-2 min-w-0">
          <span className={`shrink-0 text-[10px] px-1.5 py-0.5 rounded-full font-medium ${badge.cls}`}>
            {badge.text}
          </span>
          <span className="text-sm text-neutral-200 truncate">{entry.app_id}</span>
        </div>
        <div className="flex items-center gap-1.5 shrink-0">
          {entry.visibility !== 'private' && (
            <button
              onClick={() => handleChange('private')}
              disabled={saving}
              className="text-[11px] px-2 py-1 rounded-lg bg-neutral-800 text-neutral-400 hover:bg-neutral-700 hover:text-neutral-200 transition-colors disabled:opacity-40"
            >
              Make private
            </button>
          )}
          <select
            value={entry.visibility}
            onChange={e => handleChange(e.target.value)}
            disabled={saving}
            className="text-[11px] bg-neutral-800 border border-neutral-700/50 rounded-lg px-2 py-1 text-neutral-300 focus:outline-none focus:border-blue-500/50 disabled:opacity-40 cursor-pointer"
          >
            {PAM_VISIBILITY_OPTIONS.map(o => (
              <option key={o.value} value={o.value}>{o.label}</option>
            ))}
          </select>
        </div>
      </div>

      {confirm && (
        <PamConfirmPublicDialog
          appId={confirm.appId}
          onConfirm={handleConfirm}
          onCancel={() => setConfirm(null)}
        />
      )}
    </>
  )
}

// ── Main popover panel ────────────────────────────────────────────────────────

function PamPopover({ onClose }) {
  const [entries, setEntries] = useState(null)  // null = loading
  const [error, setError] = useState(null)
  const panelRef = useRef(null)

  const loadEntries = useCallback(() => {
    setError(null)
    pamFetchVisibility()
      .then(data => {
        // Only show local/public apps (non-private) — those are the ones that need attention
        const nonPrivate = (data || []).filter(e => e.visibility !== 'private')
        setEntries(nonPrivate)
      })
      .catch(() => {
        setError('Could not load app visibility data.')
        setEntries([])
      })
  }, [])

  useEffect(() => {
    loadEntries()
  }, [loadEntries])

  // Close on Escape
  useEffect(() => {
    const handler = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  // Close on click outside
  useEffect(() => {
    const handler = (e) => {
      if (panelRef.current && !panelRef.current.contains(e.target)) onClose()
    }
    // slight delay so the opening click doesn't immediately close
    const tid = setTimeout(() => document.addEventListener('mousedown', handler), 80)
    return () => { clearTimeout(tid); document.removeEventListener('mousedown', handler) }
  }, [onClose])

  return (
    <div className="fixed inset-0 z-[150] flex items-start justify-end pt-10 pr-4">
      <div
        ref={panelRef}
        className="w-[420px] max-h-[70vh] flex flex-col rounded-2xl bg-neutral-900/97 backdrop-blur-2xl border border-neutral-700/50 shadow-2xl shadow-black/60 animate-[fadeIn_0.15s_ease-out] overflow-hidden"
      >
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-neutral-800/60 shrink-0">
          <div>
            <h2 className="text-sm font-semibold text-neutral-100">Public App Manager</h2>
            <p className="text-[11px] text-neutral-500 mt-0.5">Apps with network visibility beyond Private</p>
          </div>
          <button
            onClick={onClose}
            className="w-6 h-6 flex items-center justify-center rounded-lg text-neutral-500 hover:text-neutral-200 hover:bg-neutral-800 transition-colors text-sm"
          >
            {'×'}
          </button>
        </div>

        {/* Content */}
        <div className="overflow-y-auto flex-1 px-4 py-2">
          {entries === null && (
            <div className="flex items-center gap-2 py-6 justify-center text-neutral-600 text-xs">
              <span className="w-3 h-3 rounded-full border-2 border-neutral-700 border-t-blue-500 animate-spin" />
              Loading...
            </div>
          )}
          {error && (
            <p className="text-xs text-red-400 py-4 text-center">{error}</p>
          )}
          {entries !== null && entries.length === 0 && !error && (
            <div className="py-8 text-center">
              <div className="text-2xl mb-2 opacity-40">&#128274;</div>
              <p className="text-xs text-neutral-500">All apps are private — nothing is shared externally.</p>
            </div>
          )}
          {entries && entries.map(entry => (
            <PamAppRow key={entry.app_id} entry={entry} onChanged={loadEntries} />
          ))}
        </div>

        {/* Footer */}
        <div className="px-4 py-2.5 border-t border-neutral-800/60 shrink-0">
          <div className="flex items-center gap-3">
            {PAM_VISIBILITY_OPTIONS.map(o => (
              <span key={o.value} className="text-[10px] text-neutral-600">
                <span className={`font-medium ${o.value === 'public' ? 'text-green-500' : o.value === 'local' ? 'text-amber-500' : 'text-neutral-500'}`}>{o.label}</span>
                {' — '}{o.desc}
              </span>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}

// ── Exported component ────────────────────────────────────────────────────────
// Mount once in the app root; it listens for `vulos:open-public-apps` and
// renders the popover. Additive — does not interfere with anything else.

export default function PublicAppsManager() {
  const [pamOpen, setPamOpen] = useState(false)

  useEffect(() => {
    const handler = () => setPamOpen(true)
    window.addEventListener(PAM_EVENT, handler)
    return () => window.removeEventListener(PAM_EVENT, handler)
  }, [])

  if (!pamOpen) return null
  return <PamPopover onClose={() => setPamOpen(false)} />
}

// ── Inline visibility control for Settings → AI Apps tab ─────────────────────
// Exported separately so Settings.jsx can import and embed it per-app row.

export function PamVisibilityControl({ appId, onChanged }) {
  const [visibility, setVisibility] = useState(null) // null = not yet loaded
  const [saving, setSaving] = useState(false)
  const [confirm, setConfirm] = useState(null)

  useEffect(() => {
    pamFetchVisibility()
      .then(data => {
        const found = (data || []).find(e => e.app_id === appId)
        setVisibility(found?.visibility ?? 'private')
      })
      .catch(() => setVisibility('private'))
  }, [appId])

  const handleChange = useCallback(async (nextVis) => {
    if (nextVis === visibility) return

    if (nextVis === 'public') {
      setConfirm({ appId, nextVis })
      return
    }

    setSaving(true)
    try {
      await pamSetVisibility(appId, nextVis)
      setVisibility(nextVis)
      onChanged?.()
    } catch {}
    finally { setSaving(false) }
  }, [appId, visibility, onChanged])

  const handleConfirm = useCallback(async () => {
    if (!confirm) return
    setConfirm(null)
    setSaving(true)
    try {
      await pamSetVisibility(confirm.appId, confirm.nextVis)
      setVisibility(confirm.nextVis)
      onChanged?.()
    } catch {}
    finally { setSaving(false) }
  }, [confirm, onChanged])

  if (visibility === null) return <span className="text-[10px] text-neutral-600">...</span>

  return (
    <>
      <select
        value={visibility}
        onChange={e => handleChange(e.target.value)}
        disabled={saving}
        className="text-[11px] bg-neutral-800 border border-neutral-700/50 rounded-lg px-2 py-1 text-neutral-300 focus:outline-none focus:border-blue-500/50 disabled:opacity-40 cursor-pointer"
        title="App visibility"
      >
        {PAM_VISIBILITY_OPTIONS.map(o => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </select>

      {confirm && (
        <PamConfirmPublicDialog
          appId={confirm.appId}
          onConfirm={handleConfirm}
          onCancel={() => setConfirm(null)}
        />
      )}
    </>
  )
}
