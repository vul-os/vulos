/**
 * SuggestionPanel — OFFICE-27
 *
 * Design treatment:
 *   - Uses Tabs primitive for Pending / Accepted / Rejected / All filter.
 *   - Signal-color pills: success = accepted, danger = rejected, warning = pending.
 *   - Warm neutrals throughout — no raw green-500 / red-500.
 *
 * Props
 * -----
 *   fileId      {string}   open document id
 *   authorId    {string}   current user id (for display)
 *   onClose     {function} close the panel
 *   suggestions {Array}    current suggestions (from parent state)
 *   onAccept    {function(suggestion)} called after accept
 *   onReject    {function(suggestion)} called after reject
 */

import { useState } from 'react'
import { Check, XCircle, Type, Trash2, ChevronDown, ChevronUp } from 'lucide-react'
import { Tabs, Button, IconButton } from './ui'

// ─── Helpers ──────────────────────────────────────────────────────────────────

function formatTs(isoStr) {
  if (!isoStr) return ''
  const d = new Date(isoStr)
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) +
    ' ' + d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

function kindColor(kind) {
  return kind === 'insert'
    ? 'text-success bg-success-bg border-success'
    : 'text-danger bg-danger-bg border-danger'
}

// Signal-color pill for suggestion state
function StatePill({ state }) {
  const map = {
    accepted: 'bg-success-bg text-success border border-success',
    rejected:  'bg-danger-bg  text-danger  border border-danger',
    pending:   'bg-warning-bg text-warning border border-warning',
  }
  const labels = { accepted: 'Accepted', rejected: 'Rejected', pending: 'Pending' }
  return (
    <span className={`text-2xs font-semibold px-1.5 py-px rounded-pill tracking-tightish ${map[state] || map.pending}`}>
      {labels[state] || state}
    </span>
  )
}

// ─── SuggestionItem ───────────────────────────────────────────────────────────
function SuggestionItem({ item, onAccept, onReject, busy }) {
  const [expanded, setExpanded] = useState(true)
  const isPending = item.state === 'pending'
  const isInsert  = item.kind === 'insert'

  return (
    <div
      className={[
        'rounded-lg border overflow-hidden',
        'transition-opacity duration-fast',
        isPending
          ? 'bg-paper border-line'
          : 'bg-bg-elev2 border-line opacity-70',
      ].join(' ')}
    >
      {/* Header */}
      <div className="flex items-start justify-between gap-2 px-3 py-2.5">
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex items-center gap-1.5 flex-wrap">
            {isInsert
              ? <Type size={11} className="text-success flex-shrink-0" />
              : <Trash2 size={11} className="text-danger flex-shrink-0" />
            }
            <span className="text-xs font-semibold text-ink tracking-tightish">
              {item.author_id || 'Anonymous'}
            </span>
            <StatePill state={item.state} />
          </div>
          <p className="text-2xs text-ink-faint tracking-tightish">{formatTs(item.created_at)}</p>
        </div>
        <button
          onClick={() => setExpanded(v => !v)}
          className="text-ink-faint hover:text-ink-muted flex-shrink-0 mt-0.5 transition-colors"
        >
          {expanded ? <ChevronUp size={13} /> : <ChevronDown size={13} />}
        </button>
      </div>

      {expanded && (
        <div className="px-3 pb-3 space-y-2.5">
          {/* Change preview */}
          <div className={`rounded-sm px-2.5 py-1.5 border text-xs font-mono break-all ${kindColor(item.kind)}`}>
            {isInsert ? (
              <span>
                <span className="opacity-50 text-2xs mr-1">+</span>
                {item.text || <em className="opacity-50">empty</em>}
              </span>
            ) : (
              <span>
                <span className="opacity-50 text-2xs mr-1">−</span>
                chars {item.from}–{item.to}
              </span>
            )}
          </div>

          {/* Accept / Reject (pending only) */}
          {isPending && (
            <div className="flex items-center gap-1.5">
              <Button
                variant="secondary"
                size="sm"
                onClick={() => onAccept(item)}
                disabled={busy}
                className="flex-1 border-success text-success hover:bg-success-bg"
              >
                <Check size={11} /> Accept
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => onReject(item)}
                disabled={busy}
                className="flex-1 border-danger text-danger hover:bg-danger-bg"
              >
                <XCircle size={11} /> Reject
              </Button>
            </div>
          )}

          {/* Reviewer credit */}
          {!isPending && item.reviewer_id && (
            <p className="text-2xs text-ink-faint tracking-tightish">
              {item.state === 'accepted' ? 'Accepted' : 'Rejected'} by {item.reviewer_id}
            </p>
          )}
        </div>
      )}
    </div>
  )
}

// ─── SuggestionPanel ─────────────────────────────────────────────────────────
const TAB_ITEMS = [
  { value: 'pending',  label: 'Pending'  },
  { value: 'all',      label: 'All'      },
  { value: 'accepted', label: 'Accepted' },
  { value: 'rejected', label: 'Rejected' },
]

export default function SuggestionPanel({
  fileId, authorId = 'You', suggestions = [], onAccept, onReject, onClose,
}) {
  const [busy, setBusy]     = useState(false)
  const [filter, setFilter] = useState('pending')

  const handleAccept = async (item) => {
    setBusy(true)
    try { await onAccept(item) } finally { setBusy(false) }
  }

  const handleReject = async (item) => {
    setBusy(true)
    try { await onReject(item) } finally { setBusy(false) }
  }

  const filtered = filter === 'all'
    ? suggestions
    : suggestions.filter(s => s.state === filter)

  const pendingCount = suggestions.filter(s => s.state === 'pending').length

  const tabItems = TAB_ITEMS.map(t =>
    t.value === 'pending' && pendingCount > 0
      ? { ...t, count: pendingCount }
      : t
  )

  return (
    <div className="w-72 flex-shrink-0 border-l border-line bg-paper flex flex-col overflow-hidden animate-slide-in-right">
      {/* ── Header ── */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-line bg-bg-elev2 flex-shrink-0">
        <div className="flex items-center gap-2">
          <Type size={13} className="text-success" />
          <span className="text-sm font-semibold text-ink tracking-tightish">Suggestions</span>
        </div>
        <IconButton size="sm" onClick={onClose} title="Close">
          <svg width="13" height="13" viewBox="0 0 14 14" fill="none">
            <path d="M1 1l12 12M13 1L1 13" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/>
          </svg>
        </IconButton>
      </div>

      {/* ── Tabs — Tabs primitive ── */}
      <Tabs
        value={filter}
        onChange={setFilter}
        items={tabItems}
        className="flex-shrink-0 bg-paper"
      />

      {/* ── List ── */}
      <div className="flex-1 overflow-y-auto p-3 space-y-2.5">
        {filtered.length === 0 && (
          <p className="text-sm text-ink-faint font-serif italic text-center py-8">
            {filter === 'pending' ? 'No pending suggestions.' : `No ${filter} suggestions.`}
          </p>
        )}
        {filtered.map(item => (
          <SuggestionItem
            key={item.id}
            item={item}
            authorId={authorId}
            onAccept={handleAccept}
            onReject={handleReject}
            busy={busy}
          />
        ))}
      </div>

      {/* ── Footer hint ── */}
      <div className="px-4 py-2.5 border-t border-line bg-bg-elev2 flex-shrink-0">
        <p className="text-2xs text-ink-faint leading-relaxed">
          Accepting folds the change into the document. Rejecting discards it.
        </p>
      </div>
    </div>
  )
}
