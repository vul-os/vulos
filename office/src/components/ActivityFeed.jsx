/**
 * ActivityFeed — OFFICE-28
 *
 * Design treatment:
 *   - Tabs primitive for Activity / Snapshots.
 *   - Date separators in serif italic small-caps.
 *   - Event kind icons in accent-tint backgrounds (warm signal colours).
 *   - Named-snapshot creation form using Input + Button primitives.
 *   - Restore confirmation via Modal.
 *
 * Props:
 *   fileId    string  — document ID
 *   onRestore fn      — called with restored File object
 *   onClose   fn      — close the panel
 */

import { useState, useEffect, useCallback } from 'react'
import {
  Activity, Bookmark, Loader2, AlertCircle, RotateCcw,
  Plus, CheckCircle, Edit3, MessageSquare, Shield,
} from 'lucide-react'
import { api } from '../lib/api'
import { Tabs, Button, IconButton, Input, Modal } from './ui'

// ─── Helpers ──────────────────────────────────────────────────────────────────

function formatRelative(dateStr) {
  const d = new Date(dateStr)
  const now = new Date()
  const diffMs = now - d
  const diffSec = Math.floor(diffMs / 1000)
  if (diffSec < 60)  return 'just now'
  const diffMin = Math.floor(diffSec / 60)
  if (diffMin < 60)  return `${diffMin}m ago`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24)   return `${diffHr}h ago`
  const diffDay = Math.floor(diffHr / 24)
  if (diffDay < 30)  return `${diffDay}d ago`
  return d.toLocaleDateString()
}

function formatDay(dateStr) {
  const d = new Date(dateStr)
  const now = new Date()
  if (d.toDateString() === now.toDateString()) return 'Today'
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  if (d.toDateString() === yesterday.toDateString()) return 'Yesterday'
  return d.toLocaleDateString(undefined, { weekday: 'long', month: 'short', day: 'numeric' })
}

// Event kind icons + warm-token colour
const KIND_CONFIG = {
  edit:     { icon: Edit3,         iconCn: 'text-accent',   bgCn: 'bg-accent-tint'   },
  comment:  { icon: MessageSquare, iconCn: 'text-info',     bgCn: 'bg-info-bg'       },
  sign:     { icon: Shield,        iconCn: 'text-success',  bgCn: 'bg-success-bg'    },
  snapshot: { icon: Bookmark,      iconCn: 'text-warning',  bgCn: 'bg-warning-bg'    },
}

function KindBadge({ kind }) {
  const k = KIND_CONFIG[kind] || KIND_CONFIG.edit
  return (
    <span className={`text-2xs font-semibold px-1.5 py-px rounded-xs capitalize tracking-tightish ${k.bgCn} ${k.iconCn}`}>
      {kind}
    </span>
  )
}

function KindDot({ kind }) {
  const k = KIND_CONFIG[kind] || KIND_CONFIG.edit
  const KIcon = k.icon
  return (
    <span className={`w-6 h-6 rounded-md flex items-center justify-center flex-shrink-0 ${k.bgCn}`}>
      <KIcon size={12} className={k.iconCn} />
    </span>
  )
}

// ─── ActivityList ─────────────────────────────────────────────────────────────
function ActivityList({ fileId }) {
  const [events, setEvents] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await api.getActivity(fileId)
      setEvents([...data].reverse())
    } catch (e) {
      setError(e.message || 'Failed to load activity')
    } finally {
      setLoading(false)
    }
  }, [fileId])

  useEffect(() => { load() }, [load])

  if (loading) return (
    <div className="flex items-center justify-center py-12">
      <Loader2 size={18} className="animate-spin text-accent" />
    </div>
  )

  if (error) return (
    <div className="flex flex-col items-center gap-2 py-10 px-4 text-center">
      <AlertCircle size={18} className="text-danger" />
      <p className="text-xs text-danger">{error}</p>
      <Button variant="link" size="sm" onClick={load}>Retry</Button>
    </div>
  )

  if (events.length === 0) return (
    <div className="py-12 px-4 text-center">
      <p className="font-serif text-sm text-ink-muted italic">No activity yet.</p>
      <p className="text-2xs text-ink-faint mt-1.5 leading-snug">
        Edits, comments, and signings appear here.
      </p>
    </div>
  )

  // Group events by day for date separators
  let lastDay = null
  const rows = []
  for (const ev of events) {
    const day = formatDay(ev.timestamp)
    if (day !== lastDay) {
      lastDay = day
      rows.push({ type: 'separator', day })
    }
    rows.push({ type: 'event', ev })
  }

  return (
    <div className="py-2">
      {rows.map((row, i) => {
        if (row.type === 'separator') {
          return (
            <div
              key={`sep-${i}`}
              className="flex items-center gap-3 px-4 py-2"
            >
              {/* Serif italic small-caps date separator */}
              <span
                className="text-2xs text-ink-faint tracking-wide font-serif italic"
                style={{ fontVariant: 'small-caps' }}
              >
                {row.day}
              </span>
              <span className="flex-1 h-px bg-line" />
            </div>
          )
        }
        const { ev } = row
        return (
          <div
            key={ev.id}
            className="flex items-start gap-2.5 px-4 py-2.5 hover:bg-accent-tint transition-colors duration-fast group"
          >
            <KindDot kind={ev.kind} />
            <div className="min-w-0 flex-1">
              <p className="text-xs text-ink leading-snug tracking-tightish">{ev.summary}</p>
              <div className="flex items-center gap-2 mt-0.5">
                {ev.author && (
                  <span className="text-2xs text-ink-faint">by {ev.author}</span>
                )}
                <span className="text-2xs text-ink-faint">{formatRelative(ev.timestamp)}</span>
              </div>
            </div>
            <KindBadge kind={ev.kind} />
          </div>
        )
      })}
    </div>
  )
}

// ─── SnapshotsTab ─────────────────────────────────────────────────────────────
function SnapshotsTab({ fileId, onRestore }) {
  const [versions, setVersions] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [restoring, setRestoring] = useState(null)
  const [confirmVersion, setConfirmVersion] = useState(null)
  const [labelInput, setLabelInput] = useState('')
  const [creating, setCreating] = useState(false)
  const [toast, setToast] = useState(null)

  const showToast = (msg, ok = true) => {
    setToast({ msg, ok })
    setTimeout(() => setToast(null), 3000)
  }

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await api.listVersions(fileId)
      setVersions(data)
    } catch (e) {
      setError(e.message || 'Failed to load snapshots')
    } finally {
      setLoading(false)
    }
  }, [fileId])

  useEffect(() => { load() }, [load])

  const handleCreate = async () => {
    const label = labelInput.trim()
    if (!label) return
    setCreating(true)
    try {
      await api.createNamedSnapshot(fileId, label)
      setLabelInput('')
      showToast('Snapshot created')
      await load()
    } catch (e) {
      showToast(e.message || 'Failed to create snapshot', false)
    } finally {
      setCreating(false)
    }
  }

  const doRestore = async (v) => {
    setConfirmVersion(null)
    setRestoring(v.id)
    try {
      const updated = await api.restoreVersion(fileId, v.id)
      showToast('Version restored')
      onRestore?.(updated)
      await load()
    } catch (e) {
      showToast(e.message || 'Restore failed', false)
    } finally {
      setRestoring(null)
    }
  }

  const named = versions.filter(v => v.label)
  const auto  = versions.filter(v => !v.label)

  return (
    <>
      <div className="flex flex-col h-full">
        {/* Create named snapshot — Input + Button primitives */}
        <div className="px-4 py-3 border-b border-line bg-bg-elev2 flex-shrink-0">
          <p className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase mb-2">
            Pin this state
          </p>
          <div className="flex gap-2">
            <Input
              value={labelInput}
              onChange={e => setLabelInput(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') handleCreate() }}
              placeholder="e.g. v1 final draft"
              size="sm"
              className="flex-1"
            />
            <Button
              variant="primary"
              size="sm"
              onClick={handleCreate}
              disabled={creating || !labelInput.trim()}
            >
              {creating ? <Loader2 size={11} className="animate-spin" /> : <Plus size={11} />}
              Save
            </Button>
          </div>
        </div>

        <div className="flex-1 overflow-y-auto">
          {loading && (
            <div className="flex items-center justify-center py-8">
              <Loader2 size={18} className="animate-spin text-accent" />
            </div>
          )}

          {error && !loading && (
            <div className="flex flex-col items-center gap-2 py-8 px-4 text-center">
              <AlertCircle size={18} className="text-danger" />
              <p className="text-xs text-danger">{error}</p>
              <Button variant="link" size="sm" onClick={load}>Retry</Button>
            </div>
          )}

          {!loading && !error && (
            <>
              {/* Named snapshots */}
              {named.length > 0 && (
                <div>
                  <p className="px-4 pt-3 pb-1 text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
                    Named
                  </p>
                  <ul className="divide-y divide-line">
                    {named.map((v, idx) => (
                      <VersionRow
                        key={v.id}
                        v={v}
                        idx={idx}
                        restoring={restoring}
                        onRestoreClick={setConfirmVersion}
                        isNamed
                      />
                    ))}
                  </ul>
                </div>
              )}

              {named.length === 0 && (
                <div className="px-4 py-8 text-center">
                  <Bookmark size={18} className="mx-auto text-ink-faint mb-2" />
                  <p className="font-serif text-sm text-ink-muted italic">No named snapshots yet.</p>
                  <p className="text-2xs text-ink-faint mt-1 leading-snug">
                    Give a name above to pin this state.
                  </p>
                </div>
              )}

              {/* Auto-saves */}
              {auto.length > 0 && (
                <div>
                  <p className="px-4 pt-3 pb-1 text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
                    Auto-saves
                  </p>
                  <ul className="divide-y divide-line">
                    {auto.map((v, idx) => (
                      <VersionRow
                        key={v.id}
                        v={v}
                        idx={idx}
                        restoring={restoring}
                        onRestoreClick={setConfirmVersion}
                      />
                    ))}
                  </ul>
                </div>
              )}
            </>
          )}
        </div>

        {/* Toast */}
        {toast && (
          <div
            className={[
              'mx-3 mb-3 flex items-center gap-2 px-3 py-2 rounded-md text-xs font-medium text-paper animate-rise-in',
              toast.ok ? 'bg-ink' : 'bg-danger',
            ].join(' ')}
          >
            <CheckCircle size={12} />
            {toast.msg}
          </div>
        )}
      </div>

      {/* Restore confirmation */}
      <Modal
        open={!!confirmVersion}
        onClose={() => setConfirmVersion(null)}
        title="Restore this version?"
        size="sm"
      >
        <Modal.Body>
          <p className="text-sm text-ink-muted leading-relaxed">
            Replace the current document with{' '}
            <span className="text-ink font-medium">
              {confirmVersion?.label || confirmVersion?.name || 'this version'}
            </span>
            {' '}from{' '}
            <span className="text-ink font-medium">
              {confirmVersion ? formatRelative(confirmVersion.created_at) : ''}
            </span>?
          </p>
        </Modal.Body>
        <Modal.Footer>
          <Button variant="secondary" size="md" onClick={() => setConfirmVersion(null)}>
            Cancel
          </Button>
          <Button variant="primary" size="md" onClick={() => doRestore(confirmVersion)}>
            <RotateCcw size={13} /> Restore
          </Button>
        </Modal.Footer>
      </Modal>
    </>
  )
}

// ─── VersionRow (shared between HistoryPanel & SnapshotsTab) ─────────────────
function VersionRow({ v, idx, restoring, onRestoreClick, isNamed = false }) {
  return (
    <li className="flex items-start gap-3 px-4 py-2.5 hover:bg-accent-tint group transition-colors duration-fast">
      {isNamed ? (
        <Bookmark size={13} className="text-warning flex-shrink-0 mt-0.5" />
      ) : (
        <span className="w-2 h-2 rounded-full bg-line-strong flex-shrink-0 mt-1.5 group-hover:bg-accent transition-colors" />
      )}
      <div className="flex-1 min-w-0">
        {isNamed && (
          <p className="text-xs font-semibold text-warning truncate tracking-tightish">{v.label}</p>
        )}
        <p className="text-xs text-ink truncate tracking-tightish" title={v.name}>{v.name}</p>
        <div className="flex items-center gap-2 mt-0.5">
          <span className="text-2xs text-ink-faint tracking-tightish">
            {formatRelative(v.created_at)}
          </span>
          {idx === 0 && !isNamed && (
            <span className="text-2xs font-semibold text-accent bg-accent-tint px-1.5 py-px rounded-pill tracking-tightish">
              latest
            </span>
          )}
        </div>
      </div>
      <button
        onClick={() => onRestoreClick(v)}
        disabled={restoring === v.id}
        className={[
          'flex items-center gap-1 h-6 px-2 text-2xs font-medium rounded-sm',
          'text-accent-press hover:bg-accent-tint-2',
          'opacity-0 group-hover:opacity-100 transition-[opacity,background] duration-fast',
          'disabled:opacity-40 disabled:cursor-not-allowed',
        ].join(' ')}
      >
        {restoring === v.id ? <Loader2 size={10} className="animate-spin" /> : <RotateCcw size={10} />}
        Restore
      </button>
    </li>
  )
}

// ─── ActivityFeed (main export) ───────────────────────────────────────────────
const PANEL_TABS = [
  { value: 'activity',  label: 'Activity'  },
  { value: 'snapshots', label: 'Snapshots' },
]

export default function ActivityFeed({ fileId, onRestore, onClose }) {
  const [tab, setTab] = useState('activity')

  return (
    <div className="w-72 flex flex-col border-l border-line bg-paper h-full overflow-hidden animate-slide-in-right">
      {/* ── Header ── */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-line bg-bg-elev2 flex-shrink-0">
        <div className="flex items-center gap-2">
          <Activity size={13} className="text-ink-faint" />
          <span className="text-sm font-semibold text-ink tracking-tightish">Activity</span>
        </div>
        {onClose && (
          <IconButton size="sm" onClick={onClose} title="Close">
            <svg width="13" height="13" viewBox="0 0 14 14" fill="none">
              <path d="M1 1l12 12M13 1L1 13" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/>
            </svg>
          </IconButton>
        )}
      </div>

      {/* ── Tabs — Tabs primitive ── */}
      <Tabs
        value={tab}
        onChange={setTab}
        items={PANEL_TABS}
        className="flex-shrink-0 bg-paper"
      />

      {/* ── Body ── */}
      <div className="flex-1 overflow-y-auto">
        {tab === 'activity'  && <ActivityList fileId={fileId} />}
        {tab === 'snapshots' && <SnapshotsTab fileId={fileId} onRestore={onRestore} />}
      </div>
    </div>
  )
}
