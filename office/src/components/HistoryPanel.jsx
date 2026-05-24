/**
 * HistoryPanel — version history side-rail.
 *
 * Design treatment:
 *   - Vertical timeline rail with quiet accent dots.
 *   - "Latest" badge uses accent-tint pill.
 *   - Restore confirmation via Modal primitive.
 *   - Named snapshots get a Bookmark accent dot.
 *   - Aligned to CommentsPanel side-rail pattern.
 *
 * Props:
 *   fileId    string  — document ID
 *   onRestore fn      — called with restored File object
 *   onClose   fn      — close the panel
 */

import { useState, useEffect, useCallback } from 'react'
import { History, RotateCcw, Loader2, AlertCircle, Bookmark } from 'lucide-react'
import { api } from '../lib/api'
import { Button, IconButton, Modal } from './ui'

function formatRelative(dateStr) {
  const d = new Date(dateStr)
  const now = new Date()
  const diffMs = now - d
  const diffSec = Math.floor(diffMs / 1000)
  if (diffSec < 60) return 'just now'
  const diffMin = Math.floor(diffSec / 60)
  if (diffMin < 60) return `${diffMin}m ago`
  const diffHr = Math.floor(diffMin / 60)
  if (diffHr < 24) return `${diffHr}h ago`
  const diffDay = Math.floor(diffHr / 24)
  if (diffDay < 30) return `${diffDay}d ago`
  return d.toLocaleDateString()
}

// ─── VersionRow ───────────────────────────────────────────────────────────────
function VersionRow({ v, idx, isLatest, restoring, onRestoreClick }) {
  const isNamed = !!v.label

  return (
    <li className="relative flex gap-3 px-4 py-3 group hover:bg-accent-tint transition-colors duration-fast">
      {/* Timeline rail */}
      <div className="flex flex-col items-center flex-shrink-0" style={{ width: 16 }}>
        {/* Dot */}
        <span
          className={[
            'w-2.5 h-2.5 rounded-full mt-1 flex-shrink-0 border-2',
            'transition-colors duration-fast',
            isNamed
              ? 'bg-warning border-warning'
              : isLatest
                ? 'bg-accent border-accent'
                : 'bg-line-strong border-line group-hover:bg-accent-press group-hover:border-accent-press',
          ].join(' ')}
        />
        {/* Rail line (not on last item — handled by parent) */}
        <span className="w-px flex-1 bg-line mt-1 mb-0" />
      </div>

      {/* Content */}
      <div className="flex-1 min-w-0 pb-1">
        {/* Named label */}
        {isNamed && (
          <div className="flex items-center gap-1 mb-0.5">
            <Bookmark size={10} className="text-warning flex-shrink-0" />
            <span className="text-2xs font-semibold text-warning tracking-tightish truncate">{v.label}</span>
          </div>
        )}

        <p className="text-xs font-medium text-ink truncate tracking-tightish" title={v.name}>
          {v.name || 'Untitled'}
        </p>
        <div className="flex items-center gap-2 mt-0.5">
          <span className="text-2xs text-ink-faint tracking-tightish">{formatRelative(v.created_at)}</span>
          {isLatest && !isNamed && (
            <span className="text-2xs font-semibold text-accent bg-accent-tint px-1.5 py-px rounded-pill tracking-tightish">
              latest
            </span>
          )}
        </div>
      </div>

      {/* Restore button — only shows on hover */}
      <button
        onClick={() => onRestoreClick(v)}
        disabled={restoring === v.id}
        title="Restore this version"
        className={[
          'flex-shrink-0 self-start mt-0.5 flex items-center gap-1',
          'h-6 px-2 text-2xs font-medium rounded-sm',
          'text-accent-press hover:bg-accent-tint-2',
          'transition-[opacity,background] duration-fast ease-out',
          'opacity-0 group-hover:opacity-100',
          'disabled:opacity-40 disabled:cursor-not-allowed',
        ].join(' ')}
      >
        {restoring === v.id
          ? <Loader2 size={11} className="animate-spin" />
          : <RotateCcw size={11} />
        }
        Restore
      </button>
    </li>
  )
}

// ─── HistoryPanel ─────────────────────────────────────────────────────────────
export default function HistoryPanel({ fileId, onRestore, onClose }) {
  const [versions, setVersions] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [restoring, setRestoring] = useState(null)
  const [confirmVersion, setConfirmVersion] = useState(null)  // version to confirm restore
  const [toast, setToast] = useState(null)

  const showToast = (msg, ok = true) => {
    setToast({ msg, ok })
    setTimeout(() => setToast(null), 3000)
  }

  const load = useCallback(async () => {
    if (!fileId) return
    setLoading(true)
    setError(null)
    try {
      const data = await api.listVersions(fileId)
      setVersions(data)
    } catch (e) {
      setError(e.message || 'Failed to load history')
    } finally {
      setLoading(false)
    }
  }, [fileId])

  useEffect(() => { load() }, [load])

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

  return (
    <>
      <div className="w-72 flex flex-col border-l border-line bg-paper h-full overflow-hidden animate-slide-in-right">
        {/* ── Header ── */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-line bg-bg-elev2 flex-shrink-0">
          <div className="flex items-center gap-2">
            <History size={13} className="text-ink-faint" />
            <span className="text-sm font-semibold text-ink tracking-tightish">Version History</span>
          </div>
          {onClose && (
            <IconButton size="sm" onClick={onClose} title="Close">
              <svg width="13" height="13" viewBox="0 0 14 14" fill="none">
                <path d="M1 1l12 12M13 1L1 13" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/>
              </svg>
            </IconButton>
          )}
        </div>

        {/* ── Body ── */}
        <div className="flex-1 overflow-y-auto">
          {loading && (
            <div className="flex items-center justify-center py-12">
              <Loader2 size={18} className="animate-spin text-accent" />
            </div>
          )}

          {error && !loading && (
            <div className="flex flex-col items-center gap-2 py-10 px-4 text-center">
              <AlertCircle size={18} className="text-danger" />
              <p className="text-xs text-danger">{error}</p>
              <Button variant="link" size="sm" onClick={load}>Retry</Button>
            </div>
          )}

          {!loading && !error && versions.length === 0 && (
            <div className="py-12 px-4 text-center">
              <p className="font-serif text-sm text-ink-muted italic">No saved versions yet.</p>
              <p className="text-2xs text-ink-faint mt-1.5 leading-snug">
                Versions are created automatically on each save.
              </p>
            </div>
          )}

          {!loading && !error && versions.length > 0 && (
            <ul className="divide-y divide-line">
              {versions.map((v, idx) => (
                <VersionRow
                  key={v.id}
                  v={v}
                  idx={idx}
                  isLatest={idx === 0}
                  restoring={restoring}
                  onRestoreClick={setConfirmVersion}
                />
              ))}
              {/* Rail bottom cap */}
              <li className="h-4" />
            </ul>
          )}
        </div>

        {/* ── Toast ── */}
        {toast && (
          <div
            className={[
              'mx-3 mb-3 flex items-center gap-2 px-3 py-2 rounded-md text-xs font-medium text-paper',
              'animate-rise-in',
              toast.ok ? 'bg-ink' : 'bg-danger',
            ].join(' ')}
          >
            <RotateCcw size={11} />
            {toast.msg}
          </div>
        )}
      </div>

      {/* ── Restore confirmation modal ── */}
      <Modal
        open={!!confirmVersion}
        onClose={() => setConfirmVersion(null)}
        title="Restore this version?"
        size="sm"
      >
        <Modal.Body>
          <p className="text-sm text-ink-muted leading-relaxed">
            The current document will be replaced with{' '}
            <span className="text-ink font-medium">
              {confirmVersion?.name || 'this version'}
            </span>
            {' '}from <span className="text-ink font-medium">
              {confirmVersion ? formatRelative(confirmVersion.created_at) : ''}
            </span>.
            {' '}A snapshot of the current state will be saved first.
          </p>
        </Modal.Body>
        <Modal.Footer>
          <Button variant="secondary" size="md" onClick={() => setConfirmVersion(null)}>
            Cancel
          </Button>
          <Button
            variant="primary"
            size="md"
            onClick={() => doRestore(confirmVersion)}
          >
            <RotateCcw size={13} /> Restore
          </Button>
        </Modal.Footer>
      </Modal>
    </>
  )
}
