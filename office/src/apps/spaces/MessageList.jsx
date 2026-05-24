/**
 * MessageList — renders a flat list of top-level messages for a channel.
 * Thread replies are surfaced via a "N replies" affordance that the parent
 * ChannelView opens in a right-rail thread panel (CommentsPanel-style).
 *
 * Design pass:
 *   - Date separators in serif italic small-caps with a hairline rule.
 *   - Comfortable message rows: 8px vertical padding, 32px avatar, no hover-flash.
 *   - Own messages get a quiet accent-tint left-rail (subtle).
 *   - Status indicators (edited, deleted) use sage/honey/ink-faint, never green/red.
 *   - Reactions row below message body.
 *   - Right-click context menu: Edit / Delete / Pin / React.
 */
import { useState, useRef, useCallback } from 'react'
import {
  MoreHorizontal, MessageSquare, Pencil, Trash2, X, Check, Smile, Pin,
} from 'lucide-react'
import { STATE_DELETED, STATE_EDITED } from '../../lib/crdt/messages.js'
import { PresenceDot } from '../../components/PresenceBar.jsx'
import RichMessage from './RichMessage.jsx'
import EmojiPicker from './EmojiPicker.jsx'
import { PinBadge } from './PinnedPanel.jsx'
import { api } from '../../lib/api.js'

function formatTime(ts) {
  if (!ts) return ''
  return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function formatDate(ts) {
  if (!ts) return ''
  return new Date(ts).toLocaleDateString([], {
    weekday: 'long',
    month: 'long',
    day: 'numeric',
  })
}

function Avatar({ name, presencePeer, size = 32 }) {
  const initials = (name || '?')[0].toUpperCase()
  const tints = ['#0f6a6c', '#4f7a4d', '#c08436', '#b8453a', '#4a6b8a', '#6e5b8a']
  const idx = (name?.charCodeAt(0) || 0) % tints.length
  const bg = presencePeer?.color || tints[idx]
  return (
    <div className="relative flex-shrink-0" style={{ width: size, height: size }}>
      <div
        className="w-full h-full rounded-full flex items-center justify-center text-white text-sm font-semibold tracking-tightish select-none"
        style={{ backgroundColor: bg }}
        title={presencePeer?.statusText
          ? `${presencePeer.displayName} — ${presencePeer.statusText}`
          : presencePeer?.displayName || name}
      >
        {initials}
      </div>
      {presencePeer && (
        <span className="absolute bottom-0 right-0">
          <PresenceDot status={presencePeer.status} size={7} />
        </span>
      )}
    </div>
  )
}

// ---- ReactionBar ---------------------------------------------------------------

/**
 * ReactionBar — shows existing reactions and allows adding/toggling.
 *
 * Props:
 *   reactions    — { [emoji]: { count, userIds: string[] } }
 *   currentUser  — string
 *   onToggle     — (emoji) => void
 *   onAdd        — called when Add Reaction button clicked
 */
function ReactionBar({ reactions = {}, currentUser, onToggle, onAdd }) {
  const [whoModal, setWhoModal] = useState(null) // emoji → show who reacted
  const entries = Object.entries(reactions)
  if (entries.length === 0 && !onAdd) return null

  return (
    <div className="flex flex-wrap items-center gap-1 mt-1.5">
      {entries.map(([emoji, { count, userIds }]) => {
        const mine = userIds.includes(currentUser)
        return (
          <div key={emoji} className="relative">
            <button
              type="button"
              onClick={() => onToggle(emoji)}
              onMouseEnter={() => setWhoModal(emoji)}
              onMouseLeave={() => setWhoModal(null)}
              className={[
                'flex items-center gap-1 text-xs px-2 py-0.5 rounded-pill border transition-colors duration-fast',
                mine
                  ? 'bg-accent-tint border-accent text-ink'
                  : 'bg-bg-elev2 border-line text-ink-muted hover:border-accent hover:bg-accent-tint',
              ].join(' ')}
              aria-label={`${emoji} reaction, ${count} people`}
            >
              <span>{emoji}</span>
              <span className="tabular-nums font-medium">{count}</span>
            </button>
            {whoModal === emoji && (
              <div className="absolute bottom-full mb-1 left-0 z-50 bg-ink text-paper rounded-sm px-2 py-1 text-2xs shadow-e2 whitespace-nowrap pointer-events-none animate-fade-in">
                {userIds.slice(0, 8).join(', ')}
                {userIds.length > 8 ? ` +${userIds.length - 8} more` : ''}
              </div>
            )}
          </div>
        )
      })}

      {/* Add reaction button — shows only on hover via parent group */}
      <button
        type="button"
        onClick={onAdd}
        className="flex items-center gap-0.5 text-xs px-1.5 py-0.5 rounded-pill border border-transparent text-ink-faint hover:border-line hover:bg-bg-elev2 transition-colors opacity-0 group-hover:opacity-100"
        title="Add reaction"
        aria-label="Add reaction"
      >
        <Smile size={11} />
      </button>
    </div>
  )
}

// ---- MessageItem ---------------------------------------------------------------

function MessageItem({
  msg, replies, onReply, onEdit, onDelete, onPin, onUnpin,
  currentUser, roster = [], isPinned = false,
  reactions = {}, onReact,
}) {
  const [showMenu, setShowMenu] = useState(false)
  const [showEmojiPicker, setShowEmojiPicker] = useState(false)
  const [editing, setEditing] = useState(false)
  const [editBody, setEditBody] = useState(msg.body)
  const emojiAnchorRef = useRef(null)
  const msgRef = useRef(null)

  const isOwn = msg.author_id === currentUser
  const isDeleted = msg.state === STATE_DELETED

  function submitEdit() {
    if (editBody.trim() && editBody.trim() !== msg.body) {
      onEdit(msg.id, editBody.trim())
    }
    setEditing(false)
  }

  const presencePeer = roster.find(
    (p) => p.accountId === msg.author_id || p.displayName === msg.author_id,
  )

  // Context menu (right-click)
  function onContextMenu(e) {
    e.preventDefault()
    setShowMenu(true)
  }

  // Close menu on outside click
  const closeMenu = useCallback(() => setShowMenu(false), [])

  return (
    <div
      ref={msgRef}
      data-msg-id={msg.id}
      onContextMenu={onContextMenu}
      className={[
        'group relative flex gap-3 px-4 py-2 transition-colors duration-fast ease-out',
        isOwn ? 'hover:bg-accent-tint/60' : 'hover:bg-bg-elev2',
      ].join(' ')}
    >
      {/* Own-message accent left rail */}
      {isOwn && (
        <span
          aria-hidden
          className="absolute left-0 top-2 bottom-2 w-[2px] rounded-r-full bg-accent/40"
        />
      )}

      <Avatar name={msg.author_id} presencePeer={presencePeer} />

      <div className="flex-1 min-w-0">
        <div className="flex items-baseline gap-2">
          <span className="font-semibold text-sm text-ink tracking-tightish">
            {msg.author_id}
          </span>
          <span className="text-2xs text-ink-faint">{formatTime(msg.created_at)}</span>
          {msg.state === STATE_EDITED && (
            <span className="text-2xs text-ink-faint italic">edited</span>
          )}
          {isPinned && <PinBadge />}
        </div>

        {isDeleted ? (
          <p className="text-sm text-ink-faint italic font-serif">
            This message was deleted.
          </p>
        ) : editing ? (
          <div className="mt-1 flex gap-2 items-end">
            <textarea
              className="flex-1 bg-paper border border-accent rounded-sm px-2 py-1.5 text-sm resize-none outline-none focus:shadow-focus text-ink"
              rows={2}
              value={editBody}
              onChange={(e) => setEditBody(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); submitEdit() }
                if (e.key === 'Escape') setEditing(false)
              }}
              autoFocus
            />
            <button
              type="button"
              onClick={submitEdit}
              className="p-1.5 rounded-sm bg-accent text-white hover:bg-accent-hover transition-colors"
              title="Save"
            >
              <Check size={14} />
            </button>
            <button
              type="button"
              onClick={() => setEditing(false)}
              className="p-1.5 rounded-sm bg-bg-elev2 text-ink-muted border border-line hover:bg-paper transition-colors"
              title="Cancel"
            >
              <X size={14} />
            </button>
          </div>
        ) : (
          <RichMessage body={msg.body} members={roster} />
        )}

        {/* Attachment */}
        {msg.attachment && !isDeleted && (
          <div className="mt-1">
            {/* Rendered by parent via AttachmentPreview if needed */}
          </div>
        )}

        {/* Reactions */}
        {!isDeleted && (
          <ReactionBar
            reactions={reactions}
            currentUser={currentUser}
            onToggle={(emoji) => onReact(msg.id, emoji)}
            onAdd={() => setShowEmojiPicker(true)}
          />
        )}

        {/* Thread affordance */}
        {replies.length > 0 && !isDeleted && (
          <button
            type="button"
            onClick={() => onReply(msg)}
            className="mt-1.5 inline-flex items-center gap-1.5 text-2xs text-accent hover:text-accent-press font-medium tracking-tightish transition-colors"
          >
            <MessageSquare size={11} />
            {replies.length} {replies.length === 1 ? 'reply' : 'replies'}
          </button>
        )}

        {!isDeleted && replies.length === 0 && (
          <button
            type="button"
            onClick={() => onReply(msg)}
            className="mt-1 inline-flex items-center gap-1 text-2xs text-ink-faint hover:text-accent opacity-0 group-hover:opacity-100 transition-[opacity,color] duration-fast ease-out"
          >
            <MessageSquare size={11} /> Reply in thread
          </button>
        )}
      </div>

      {/* Hover action bar */}
      {!isDeleted && (
        <div className="relative flex-shrink-0 flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity duration-fast">
          {/* React */}
          <div className="relative" ref={emojiAnchorRef}>
            <button
              type="button"
              onClick={() => setShowEmojiPicker((v) => !v)}
              className="p-1 rounded-sm text-ink-faint hover:text-ink hover:bg-accent-tint transition-colors"
              title="React"
              aria-label="Add reaction"
            >
              <Smile size={14} />
            </button>
            {showEmojiPicker && (
              <div className="absolute right-0 bottom-full mb-1 z-50">
                <EmojiPicker
                  onPick={(emoji) => { onReact(msg.id, emoji); setShowEmojiPicker(false) }}
                  onClose={() => setShowEmojiPicker(false)}
                />
              </div>
            )}
          </div>

          {/* Message menu */}
          <button
            type="button"
            onClick={() => setShowMenu(!showMenu)}
            className="p-1 rounded-sm text-ink-faint hover:text-ink hover:bg-accent-tint transition-colors"
            title="More"
            aria-label="Message actions"
          >
            <MoreHorizontal size={14} />
          </button>
          {showMenu && (
            <div
              className="absolute right-0 top-7 z-10 bg-paper border border-line rounded-md shadow-e2 py-1 min-w-[160px] animate-scale-in"
              onMouseLeave={closeMenu}
            >
              {!isDeleted && (
                <>
                  {isOwn && (
                    <button
                      type="button"
                      onClick={() => { setEditing(true); setEditBody(msg.body); setShowMenu(false) }}
                      className="w-full flex items-center gap-2 px-3 py-1.5 text-xs text-ink-muted hover:bg-accent-tint hover:text-ink transition-colors"
                    >
                      <Pencil size={11} /> Edit
                    </button>
                  )}
                  {!isPinned ? (
                    <button
                      type="button"
                      onClick={() => { onPin(msg); setShowMenu(false) }}
                      className="w-full flex items-center gap-2 px-3 py-1.5 text-xs text-ink-muted hover:bg-accent-tint hover:text-ink transition-colors"
                    >
                      <Pin size={11} /> Pin to channel
                    </button>
                  ) : (
                    <button
                      type="button"
                      onClick={() => { onUnpin(msg.id); setShowMenu(false) }}
                      className="w-full flex items-center gap-2 px-3 py-1.5 text-xs text-ink-muted hover:bg-accent-tint hover:text-ink transition-colors"
                    >
                      <Pin size={11} /> Unpin
                    </button>
                  )}
                  {isOwn && (
                    <button
                      type="button"
                      onClick={() => { onDelete(msg.id); setShowMenu(false) }}
                      className="w-full flex items-center gap-2 px-3 py-1.5 text-xs text-danger hover:bg-danger-bg transition-colors"
                    >
                      <Trash2 size={11} /> Delete
                    </button>
                  )}
                </>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ---- MessageList (exported) ---------------------------------------------------

export default function MessageList({
  messages,
  onReply,
  onEdit,
  onDelete,
  onPin,
  onUnpin,
  onReact,
  currentUser,
  roster = [],
  pinnedIds = new Set(),
  reactions = {},
  highlightId = null,
}) {
  if (!messages || messages.length === 0) {
    return (
      <div className="flex-1 flex items-center justify-center text-ink-faint text-sm bg-bg">
        <p className="font-serif italic">
          No messages yet. Be the first to say something.
        </p>
      </div>
    )
  }

  const topLevel = messages.filter((m) => !m.thread_parent)
  const replyMap = {}
  messages.filter((m) => m.thread_parent).forEach((r) => {
    if (!replyMap[r.thread_parent]) replyMap[r.thread_parent] = []
    replyMap[r.thread_parent].push(r)
  })

  let lastDate = null

  return (
    <div className="flex-1 overflow-y-auto py-2 bg-bg">
      {topLevel.map((msg) => {
        const date = formatDate(msg.created_at)
        const showDate = date !== lastDate
        lastDate = date
        return (
          <div
            key={msg.id}
            className={highlightId === msg.id ? 'ring-2 ring-accent ring-inset rounded-sm' : ''}
          >
            {showDate && (
              <div className="flex items-center gap-3 px-4 py-3">
                <div className="flex-1 h-px bg-line" />
                <span
                  className="font-serif italic text-2xs text-ink-faint uppercase tracking-eyebrow"
                  style={{ fontVariant: 'small-caps' }}
                >
                  {date}
                </span>
                <div className="flex-1 h-px bg-line" />
              </div>
            )}
            <MessageItem
              msg={msg}
              replies={replyMap[msg.id] || []}
              onReply={onReply}
              onEdit={onEdit}
              onDelete={onDelete}
              onPin={onPin}
              onUnpin={onUnpin}
              onReact={onReact || (() => {})}
              currentUser={currentUser}
              roster={roster}
              isPinned={pinnedIds.has(msg.id)}
              reactions={reactions[msg.id] || {}}
            />
          </div>
        )
      })}
    </div>
  )
}
