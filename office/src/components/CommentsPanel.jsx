/**
 * CommentsPanel — OFFICE-26
 *
 * A side-panel showing all comments for a file:
 *   - Anchored to text range / cell / slide
 *   - Threaded replies
 *   - Resolve / reopen
 *   - Author identity (authorId prop or "You")
 *   - Backed by the CRDT CommentStore; persists via REST
 *
 * Props
 * -----
 *   fileId    {string}   the open document's id
 *   anchorCtx {object}   context passed by the editor when adding a comment:
 *                          { type, from, to, snapshot }     (Docs)
 *                          { type, sheet, row, col, snapshot } (Sheets)
 *                          { type, slideId, snapshot }      (Slides)
 *   authorId  {string}   identity of the current user (Vulos account address / session id)
 *   onClose   {function} called when the user clicks the X
 */

import { useEffect, useState, useRef, useCallback } from 'react'
import { X, MessageSquare, CheckCircle, RotateCcw, Trash2, Send, ChevronDown, ChevronUp } from 'lucide-react'
import { api } from '../lib/api'
import { getCommentStore } from '../lib/crdt/comments'
import { IconButton, Tabs } from './ui'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function formatTs(isoStr) {
  if (!isoStr) return ''
  const d = new Date(isoStr)
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) +
    ' ' + d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

function anchorLabel(anchor) {
  if (!anchor) return ''
  if (anchor.orphaned) return '(anchor removed)'
  if (anchor.type === 'text_range') return anchor.snapshot || `chars ${anchor.from}–${anchor.to}`
  if (anchor.type === 'cell') return `${anchor.sheet} ${anchor.row}:${anchor.col}`
  if (anchor.type === 'slide') return `slide ${anchor.slide_id}`
  return ''
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

function ReplyItem({ reply, fileId, commentId, authorId, onDeleted }) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(reply.body)
  const [busy, setBusy] = useState(false)

  const handleSave = async () => {
    if (!draft.trim() || draft === reply.body) { setEditing(false); return }
    setBusy(true)
    try {
      await api.updateReply(fileId, commentId, reply.id, { body: draft.trim() })
      const store = getCommentStore(fileId)
      store.editReply(commentId, reply.id, draft.trim())
      setEditing(false)
    } finally {
      setBusy(false)
    }
  }

  const handleDelete = async () => {
    setBusy(true)
    try {
      await api.deleteReply(fileId, commentId, reply.id)
      const store = getCommentStore(fileId)
      store.deleteReply(commentId, reply.id)
      onDeleted(reply.id)
    } finally {
      setBusy(false)
    }
  }

  if (reply.deleted) {
    return (
      <div className="pl-3 text-2xs text-ink-faint italic py-1 border-l border-line">
        [deleted]
      </div>
    )
  }

  const isOwn = reply.author_id === authorId

  return (
    <div className="pl-3 border-l border-accent-tint-2 py-1.5 space-y-1">
      <div className="flex items-center justify-between">
        <span className="text-2xs font-semibold text-ink-muted tracking-tightish">{reply.author_id || 'Anonymous'}</span>
        <span className="text-2xs text-ink-faint">{formatTs(reply.created_at)}</span>
      </div>
      {editing ? (
        <div className="space-y-1">
          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            rows={2}
            className="w-full text-xs bg-paper border border-line rounded-sm px-2 py-1 resize-none outline-none focus:border-accent focus:shadow-focus transition-colors"
          />
          <div className="flex gap-1">
            <button
              onClick={handleSave}
              disabled={busy}
              className="px-2 py-0.5 text-2xs bg-accent text-white rounded-xs hover:bg-accent-hover disabled:opacity-60 transition-colors"
            >
              Save
            </button>
            <button
              onClick={() => { setEditing(false); setDraft(reply.body) }}
              className="px-2 py-0.5 text-2xs border border-line text-ink-muted rounded-xs hover:bg-bg-elev2 transition-colors"
            >
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <p className="text-xs text-ink whitespace-pre-wrap leading-snug">{reply.body}</p>
      )}
      {isOwn && !editing && (
        <div className="flex gap-2">
          <button onClick={() => setEditing(true)} className="text-2xs text-accent hover:underline">Edit</button>
          <button onClick={handleDelete} disabled={busy} className="text-2xs text-danger/80 hover:text-danger">Delete</button>
        </div>
      )}
    </div>
  )
}

function CommentItem({ item, fileId, authorId, onUpdated, onDeleted }) {
  const [expanded, setExpanded] = useState(true)
  const [replyDraft, setReplyDraft] = useState('')
  const [busy, setBusy] = useState(false)
  const [editing, setEditing] = useState(false)
  const [editDraft, setEditDraft] = useState(item.body)
  const [replies, setReplies] = useState(item.replies || [])

  // Keep replies in sync with parent updates
  useEffect(() => { setReplies(item.replies || []) }, [item.replies])

  const handleReply = async () => {
    const body = replyDraft.trim()
    if (!body) return
    setBusy(true)
    try {
      const r = await api.createReply(fileId, item.id, authorId, body)
      const store = getCommentStore(fileId)
      store.addReply(item.id, authorId, body)
      setReplies((prev) => [...prev, r])
      setReplyDraft('')
    } finally {
      setBusy(false)
    }
  }

  const handleResolve = async () => {
    setBusy(true)
    try {
      const updated = await api.updateComment(fileId, item.id, { state: 'resolved' })
      const store = getCommentStore(fileId)
      store.resolve(item.id)
      onUpdated({ ...item, ...updated })
    } finally {
      setBusy(false)
    }
  }

  const handleReopen = async () => {
    setBusy(true)
    try {
      const updated = await api.updateComment(fileId, item.id, { state: 'open' })
      const store = getCommentStore(fileId)
      store.reopen(item.id)
      onUpdated({ ...item, ...updated })
    } finally {
      setBusy(false)
    }
  }

  const handleDelete = async () => {
    if (!window.confirm('Delete this comment and all replies?')) return
    setBusy(true)
    try {
      await api.deleteComment(fileId, item.id)
      onDeleted(item.id)
    } finally {
      setBusy(false)
    }
  }

  const handleSaveEdit = async () => {
    const body = editDraft.trim()
    if (!body || body === item.body) { setEditing(false); return }
    setBusy(true)
    try {
      const updated = await api.updateComment(fileId, item.id, { body })
      const store = getCommentStore(fileId)
      store.editComment(item.id, body)
      onUpdated({ ...item, ...updated })
      setEditing(false)
    } finally {
      setBusy(false)
    }
  }

  const isResolved = item.state === 'resolved'
  const isOwn = item.author_id === authorId

  return (
    <div className={`rounded-md border p-3 space-y-2 transition-colors animate-rise-in ${isResolved ? 'bg-bg-elev2 border-line opacity-70' : 'bg-paper border-line'}`}>
      {/* Header */}
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5 flex-wrap">
            <span className="text-xs font-semibold text-ink tracking-tightish">{item.author_id || 'Anonymous'}</span>
            {isResolved && (
              <span className="text-2xs bg-success-bg text-success px-1.5 py-0.5 rounded-pill font-medium">Resolved</span>
            )}
            <span className="text-2xs text-ink-faint">{formatTs(item.created_at)}</span>
          </div>
          {item.anchor && (
            <p className="text-2xs text-accent mt-0.5 truncate font-serif italic" title={anchorLabel(item.anchor)}>
              “{anchorLabel(item.anchor)}”
            </p>
          )}
        </div>
        <button
          onClick={() => setExpanded((v) => !v)}
          className="text-ink-faint hover:text-ink-muted flex-shrink-0 mt-0.5 transition-colors"
        >
          {expanded ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
        </button>
      </div>

      {expanded && (
        <>
          {/* Body */}
          {editing ? (
            <div className="space-y-1.5">
              <textarea
                value={editDraft}
                onChange={(e) => setEditDraft(e.target.value)}
                rows={3}
                className="w-full text-sm bg-paper border border-line rounded-sm px-2 py-1.5 resize-none outline-none focus:border-accent focus:shadow-focus transition-colors"
              />
              <div className="flex gap-1.5">
                <button
                  onClick={handleSaveEdit}
                  disabled={busy}
                  className="px-2.5 py-1 text-xs bg-accent text-white rounded-sm hover:bg-accent-hover disabled:opacity-60 transition-colors"
                >
                  Save
                </button>
                <button
                  onClick={() => { setEditing(false); setEditDraft(item.body) }}
                  className="px-2.5 py-1 text-xs border border-line text-ink-muted rounded-sm hover:bg-bg-elev2 transition-colors"
                >
                  Cancel
                </button>
              </div>
            </div>
          ) : (
            <p className="text-sm text-ink whitespace-pre-wrap leading-snug">{item.body}</p>
          )}

          {/* Replies */}
          {replies.length > 0 && (
            <div className="space-y-2 pt-1">
              {replies.map((r) => (
                <ReplyItem
                  key={r.id}
                  reply={r}
                  fileId={fileId}
                  commentId={item.id}
                  authorId={authorId}
                  onDeleted={(rid) => setReplies((prev) => prev.filter((x) => x.id !== rid))}
                />
              ))}
            </div>
          )}

          {/* Reply input */}
          {!isResolved && (
            <div className="flex items-end gap-1.5 pt-1">
              <textarea
                value={replyDraft}
                onChange={(e) => setReplyDraft(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); handleReply() } }}
                rows={1}
                placeholder="Reply…"
                className="flex-1 text-xs bg-paper border border-line rounded-sm px-2 py-1 resize-none outline-none focus:border-accent focus:shadow-focus transition-colors min-h-[28px]"
              />
              <button
                onClick={handleReply}
                disabled={!replyDraft.trim() || busy}
                className="p-1.5 bg-accent text-white rounded-sm hover:bg-accent-hover disabled:opacity-40 flex-shrink-0 transition-colors"
              >
                <Send size={12} />
              </button>
            </div>
          )}

          {/* Actions */}
          <div className="flex items-center gap-3 pt-0.5 flex-wrap">
            {isResolved ? (
              <button
                onClick={handleReopen}
                disabled={busy}
                className="flex items-center gap-1 text-2xs text-ink-faint hover:text-accent transition-colors"
              >
                <RotateCcw size={10} /> Reopen
              </button>
            ) : (
              <button
                onClick={handleResolve}
                disabled={busy}
                className="flex items-center gap-1 text-2xs text-success hover:text-accent-press transition-colors"
              >
                <CheckCircle size={10} /> Resolve
              </button>
            )}
            {isOwn && !editing && (
              <>
                <button
                  onClick={() => setEditing(true)}
                  className="text-2xs text-accent hover:underline"
                >
                  Edit
                </button>
                <button
                  onClick={handleDelete}
                  disabled={busy}
                  className="flex items-center gap-0.5 text-2xs text-danger/80 hover:text-danger transition-colors"
                >
                  <Trash2 size={10} /> Delete
                </button>
              </>
            )}
          </div>
        </>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// CommentsPanel (main export)
// ---------------------------------------------------------------------------

export default function CommentsPanel({ fileId, anchorCtx, authorId = 'You', onClose }) {
  const [comments, setComments] = useState([])
  const [newBody, setNewBody] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [filter, setFilter] = useState('all') // 'all' | 'open' | 'resolved'
  const textareaRef = useRef(null)

  // Hydrate from server + CRDT store on mount
  useEffect(() => {
    if (!fileId) return
    setLoading(true)
    api.listComments(fileId)
      .then((items) => {
        const store = getCommentStore(fileId)
        store.loadFromServer(items)
        setComments(store.list())
      })
      .catch(console.error)
      .finally(() => setLoading(false))
  }, [fileId])

  const refresh = useCallback(() => {
    const store = getCommentStore(fileId)
    setComments(store.list())
  }, [fileId])

  const handleAdd = async () => {
    const body = newBody.trim()
    if (!body) return
    const anchor = anchorCtx || { type: 'slide', slide_id: '', snapshot: '' }
    setBusy(true)
    try {
      const c = await api.createComment(fileId, anchor, authorId, body)
      const store = getCommentStore(fileId)
      store.addComment(anchor, authorId, body)
      setComments(store.list())
      setNewBody('')
    } catch (err) {
      console.error('createComment failed', err)
    } finally {
      setBusy(false)
    }
  }

  const handleUpdated = useCallback((updated) => {
    const store = getCommentStore(fileId)
    // The store already has the change; just re-read.
    setComments(store.list())
  }, [fileId])

  const handleDeleted = useCallback((commentId) => {
    setComments((prev) => prev.filter((c) => c.id !== commentId))
  }, [])

  const filtered = filter === 'all'
    ? comments
    : comments.filter((c) => c.state === filter)

  /*
   * Side rail (clean, not stacked overlay):
   *   - lives inline as a 288px right column under the topbar
   *   - paper-elev2 background so it reads as "the side", not as a popover
   *   - sticky header / tabs / composer; comments are the only thing that scrolls
   */
  const openCount     = comments.filter((c) => c.state !== 'resolved').length
  const resolvedCount = comments.filter((c) => c.state === 'resolved').length

  return (
    <aside className="w-72 flex-shrink-0 border-l border-line bg-bg-elev2 flex flex-col overflow-hidden animate-slide-in-right">
      {/* Header — discreet, no coloured accent on title */}
      <div className="flex items-center justify-between px-3 h-11 border-b border-line bg-paper flex-shrink-0">
        <div className="flex items-center gap-2">
          <MessageSquare size={14} className="text-ink-muted" />
          <span className="text-sm font-semibold text-ink tracking-tightish">Comments</span>
          {comments.length > 0 && (
            <span className="text-2xs bg-bg-elev2 text-ink-faint rounded-pill px-1.5 py-0.5 font-medium">
              {comments.length}
            </span>
          )}
        </div>
        <IconButton size="sm" title="Close" onClick={onClose}>
          <X size={14} />
        </IconButton>
      </div>

      {/* Filter tabs — using design system Tabs primitive */}
      <div className="bg-paper flex-shrink-0">
        <Tabs
          value={filter}
          onChange={setFilter}
          items={[
            { value: 'all',      label: 'All',      count: comments.length },
            { value: 'open',     label: 'Open',     count: openCount       },
            { value: 'resolved', label: 'Resolved', count: resolvedCount   },
          ]}
        />
      </div>

      {/* Composer — paper bg, accent only on the Post button */}
      <div className="p-3 border-b border-line bg-paper flex-shrink-0 space-y-2">
        {anchorCtx && (
          <p className="text-2xs text-accent font-serif italic truncate" title={anchorLabel(anchorCtx)}>
            On “{anchorLabel(anchorCtx)}”
          </p>
        )}
        <textarea
          ref={textareaRef}
          value={newBody}
          onChange={(e) => setNewBody(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); handleAdd() } }}
          rows={2}
          placeholder="Add a comment…"
          className="w-full text-sm bg-bg-elev2 border border-line rounded-sm px-2 py-1.5 resize-none outline-none focus:border-accent focus:shadow-focus focus:bg-paper transition-colors"
        />
        <button
          onClick={handleAdd}
          disabled={!newBody.trim() || busy}
          className="w-full h-7 text-xs font-medium bg-accent text-white rounded-sm hover:bg-accent-hover disabled:opacity-50 transition-colors tracking-tightish"
        >
          {busy ? 'Posting…' : 'Comment'}
        </button>
      </div>

      {/* Comment list — only the comments scroll */}
      <div className="flex-1 overflow-y-auto p-3 space-y-2.5">
        {loading && (
          <p className="text-xs text-ink-faint text-center py-4">Loading…</p>
        )}
        {!loading && filtered.length === 0 && (
          <p className="text-xs text-ink-faint text-center py-8 font-serif italic">
            {filter === 'all' ? 'No comments yet.' : `No ${filter} comments.`}
          </p>
        )}
        {filtered.map((item) => (
          <CommentItem
            key={item.id}
            item={item}
            fileId={fileId}
            authorId={authorId}
            onUpdated={handleUpdated}
            onDeleted={handleDeleted}
          />
        ))}
      </div>
    </aside>
  )
}
