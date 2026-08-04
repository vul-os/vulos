import { useState, useEffect, useRef, useCallback } from 'react'
import { usePeering, Channel } from '../../core/usePeering.js'

// MOBILE-ADAPTIVE (WAVE-30): below this width the two-pane layout (conversation
// list + thread) collapses to a single pane that shows the list OR the open
// thread, with a back affordance — the desktop metaphor adapts, it does not
// shrink to an unusable ~80px thread column.
const NARROW_QUERY = '(max-width: 640px)'
function useNarrow() {
  const [narrow, setNarrow] = useState(
    () => typeof window !== 'undefined' && window.matchMedia?.(NARROW_QUERY).matches
  )
  useEffect(() => {
    if (!window.matchMedia) return undefined
    const mq = window.matchMedia(NARROW_QUERY)
    const on = (e) => setNarrow(e.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  return narrow
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function formatTime(iso) {
  if (!iso) return ''
  const d = new Date(iso)
  const now = new Date()
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate()
  if (sameDay) {
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

function formatRelative(iso) {
  if (!iso) return ''
  const diff = Date.now() - new Date(iso).getTime()
  if (diff < 60_000) return 'just now'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`
  return formatTime(iso)
}

function initials(name) {
  if (!name) return '?'
  return name
    .split(' ')
    .slice(0, 2)
    .map(w => w[0]?.toUpperCase() ?? '')
    .join('')
}

function humanFileSize(bytes) {
  if (!bytes) return ''
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0, v = bytes
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i > 0 ? 1 : 0)} ${units[i]}`
}

// ── SVG Icons (inline, no external deps) ─────────────────────────────────────

// Paper-airplane "send" glyph. The artwork's native orientation points
// straight up; rotated 45° here so it points up-and-to-the-right — the
// direction a message actually travels out of a horizontal composer, and the
// same convention Files' SharePeerModal uses for its own peer-send button.
const IconSend = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" width="18" height="18" style={{ transform: 'rotate(45deg)' }}>
    <path d="M10.894 2.553a1 1 0 00-1.788 0l-7 14a1 1 0 001.169 1.409l5-1.429A1 1 0 009 15.571V11a1 1 0 112 0v4.571a1 1 0 00.725.962l5 1.428a1 1 0 001.17-1.408l-7-14z" />
  </svg>
)

const IconPaperclip = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" width="18" height="18">
    <path fillRule="evenodd" d="M8 4a3 3 0 00-3 3v4.5a4.5 4.5 0 009 0V6a1 1 0 112 0v5.5a6.5 6.5 0 11-13 0V7a5 5 0 0110 0v4.5a2.5 2.5 0 01-5 0V8a1 1 0 012 0v3.5a.5.5 0 001 0V7a3 3 0 00-3-3z" clipRule="evenodd" />
  </svg>
)

const IconImage = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" width="14" height="14">
    <path fillRule="evenodd" d="M4 3a2 2 0 00-2 2v10a2 2 0 002 2h12a2 2 0 002-2V5a2 2 0 00-2-2H4zm12 12H4l4-8 3 6 2-4 3 6z" clipRule="evenodd" />
  </svg>
)

const IconFile = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" width="14" height="14">
    <path fillRule="evenodd" d="M4 4a2 2 0 012-2h4.586A2 2 0 0112 2.586L15.414 6A2 2 0 0116 7.414V16a2 2 0 01-2 2H6a2 2 0 01-2-2V4z" clipRule="evenodd" />
  </svg>
)

const IconCheck = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" width="12" height="12">
    <path fillRule="evenodd" d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z" clipRule="evenodd" />
  </svg>
)

const IconX = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" width="14" height="14">
    <path fillRule="evenodd" d="M4.293 4.293a1 1 0 011.414 0L10 8.586l4.293-4.293a1 1 0 111.414 1.414L11.414 10l4.293 4.293a1 1 0 01-1.414 1.414L10 11.414l-4.293 4.293a1 1 0 01-1.414-1.414L8.586 10 4.293 5.707a1 1 0 010-1.414z" clipRule="evenodd" />
  </svg>
)

const IconBack = () => (
  <svg viewBox="0 0 20 20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" width="18" height="18">
    <path d="M12 4l-6 6 6 6" />
  </svg>
)

const IconMessages = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" width="32" height="32">
    <path d="M2 5a2 2 0 012-2h7a2 2 0 012 2v4a2 2 0 01-2 2H9l-3 3v-3H4a2 2 0 01-2-2V5z" />
    <path d="M15 7v2a4 4 0 01-4 4H9.828l-1.766 1.767c.28.149.599.233.938.233h2l3 3v-3h2a2 2 0 002-2V9a2 2 0 00-2-2h-1z" />
  </svg>
)

// ── Styles (CSS-in-JS inline, no external deps) ───────────────────────────────

const S = {
  root: {
    display: 'flex',
    height: '100%',
    background: 'var(--bg-base, #0d0d0d)',
    color: 'var(--text-primary, #e5e5e5)',
    fontFamily: "'Inter', -apple-system, BlinkMacSystemFont, sans-serif",
    fontSize: 13,
    overflow: 'hidden',
  },
  sidebar: {
    width: 280,
    minWidth: 220,
    flexShrink: 0,
    borderRight: '1px solid var(--border-default, #1e1e1e)',
    display: 'flex',
    flexDirection: 'column',
    background: 'var(--bg-surface, #111)',
    overflow: 'hidden',
  },
  sidebarHeader: {
    padding: '16px 16px 12px',
    borderBottom: '1px solid var(--border-default, #1e1e1e)',
    display: 'flex',
    alignItems: 'center',
    gap: 8,
  },
  sidebarTitle: {
    fontSize: 15,
    fontWeight: 600,
    letterSpacing: '-0.01em',
    flex: 1,
  },
  onlineDot: {
    width: 8,
    height: 8,
    borderRadius: '50%',
    background: 'var(--status-success)',
    flexShrink: 0,
  },
  offlineDot: {
    width: 8,
    height: 8,
    borderRadius: '50%',
    background: 'var(--text-ghost)',
    flexShrink: 0,
  },
  convList: {
    flex: 1,
    overflowY: 'auto',
    padding: '4px 0',
  },
  convItem: (active) => ({
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    padding: '10px 14px',
    minHeight: 44,
    cursor: 'pointer',
    background: active ? 'var(--bg-selected)' : 'transparent',
    borderLeft: active ? '2px solid var(--accent)' : '2px solid transparent',
    transition: 'background var(--motion-fast) var(--ease-standard), border-color var(--motion-fast) var(--ease-standard)',
  }),
  avatar: (seed) => {
    const colors = ['#7c3aed','#2563eb','#059669','#d97706','#dc2626','#db2777','#0891b2']
    const idx = (seed || 0) % colors.length
    return {
      width: 38,
      height: 38,
      borderRadius: '50%',
      background: colors[idx],
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'center',
      fontSize: 13,
      fontWeight: 700,
      color: '#fff',
      flexShrink: 0,
      letterSpacing: '-0.02em',
    }
  },
  convMeta: {
    flex: 1,
    minWidth: 0,
  },
  convName: {
    fontWeight: 500,
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
  },
  convPreview: {
    fontSize: 11,
    color: 'var(--text-faint, #666)',
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    marginTop: 2,
  },
  convTime: {
    fontSize: 11,
    color: 'var(--text-ghost, #555)',
    flexShrink: 0,
    marginTop: 2,
  },
  unreadBadge: {
    background: 'var(--accent)',
    borderRadius: 10,
    fontSize: 10,
    fontWeight: 700,
    padding: '1px 5px',
    color: '#fff',
    flexShrink: 0,
  },
  main: {
    flex: 1,
    display: 'flex',
    flexDirection: 'column',
    overflow: 'hidden',
  },
  threadHeader: {
    padding: '12px 18px',
    borderBottom: '1px solid var(--border-default, #1e1e1e)',
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    background: 'var(--bg-surface, #111)',
    flexShrink: 0,
  },
  threadTitle: {
    fontWeight: 600,
    fontSize: 14,
  },
  threadSub: {
    fontSize: 11,
    color: 'var(--text-faint, #666)',
    marginTop: 1,
  },
  messageArea: {
    flex: 1,
    overflowY: 'auto',
    padding: '16px 18px',
    display: 'flex',
    flexDirection: 'column',
    gap: 2,
  },
  dateSep: {
    textAlign: 'center',
    fontSize: 11,
    color: 'var(--text-ghost, #555)',
    margin: '12px 0 6px',
    position: 'relative',
  },
  msgRow: (mine) => ({
    display: 'flex',
    flexDirection: mine ? 'row-reverse' : 'row',
    alignItems: 'flex-end',
    gap: 8,
    marginBottom: 2,
  }),
  msgAvatar: (seed) => ({
    width: 28,
    height: 28,
    borderRadius: '50%',
    background: S.avatar(seed).background,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    fontSize: 10,
    fontWeight: 700,
    color: '#fff',
    flexShrink: 0,
  }),
  bubble: (mine) => ({
    maxWidth: '100%',
    padding: '8px 12px',
    borderRadius: mine ? '16px 16px 4px 16px' : '16px 16px 16px 4px',
    background: mine ? 'var(--accent)' : 'var(--bg-elevated, #1e1e1e)',
    border: mine ? '1px solid transparent' : '1px solid var(--border-subtle)',
    color: mine ? '#fff' : 'var(--text-primary, #e5e5e5)',
    lineHeight: 1.45,
    wordBreak: 'break-word',
    fontSize: 13,
  }),
  bubbleMeta: (mine) => ({
    display: 'flex',
    gap: 4,
    alignItems: 'center',
    justifyContent: mine ? 'flex-end' : 'flex-start',
    marginTop: 3,
    paddingLeft: mine ? 0 : 36,
  }),
  metaText: {
    fontSize: 10,
    color: 'var(--text-ghost, #555)',
  },
  mediaAttachment: {
    marginTop: 6,
    borderRadius: 10,
    overflow: 'hidden',
    maxWidth: 260,
  },
  mediaImg: {
    width: '100%',
    display: 'block',
    borderRadius: 10,
    cursor: 'pointer',
  },
  fileChip: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    padding: '8px 12px',
    background: 'color-mix(in srgb, var(--text-primary) 9%, transparent)',
    borderRadius: 10,
    fontSize: 12,
    marginTop: 4,
    cursor: 'pointer',
    textDecoration: 'none',
    color: 'inherit',
  },
  composer: {
    padding: '12px 18px',
    borderTop: '1px solid var(--border-default, #1e1e1e)',
    background: 'var(--bg-surface, #111)',
    flexShrink: 0,
  },
  dropZone: (dragging) => ({
    border: dragging ? '2px dashed var(--accent)' : '2px dashed transparent',
    borderRadius: 12,
    background: dragging ? 'var(--accent-soft)' : 'transparent',
    transition: 'border-color var(--motion-base) var(--ease-standard), background var(--motion-base) var(--ease-standard), padding var(--motion-base) var(--ease-standard)',
    padding: dragging ? 8 : 0,
  }),
  attachPreviews: {
    display: 'flex',
    gap: 8,
    flexWrap: 'wrap',
    marginBottom: 8,
  },
  attachPreview: {
    position: 'relative',
    borderRadius: 10,
    overflow: 'hidden',
    background: 'var(--bg-elevated)',
    border: '1px solid var(--border-default)',
  },
  attachImg: {
    width: 60,
    height: 60,
    objectFit: 'cover',
    display: 'block',
  },
  attachFile: {
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    justifyContent: 'center',
    width: 60,
    height: 60,
    fontSize: 10,
    gap: 4,
    color: 'var(--text-faint, #666)',
    padding: 4,
    textAlign: 'center',
    overflow: 'hidden',
  },
  attachRemove: {
    position: 'absolute',
    top: 2,
    right: 2,
    background: 'rgba(0,0,0,0.7)',
    border: 'none',
    borderRadius: '50%',
    width: 16,
    height: 16,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    cursor: 'pointer',
    color: '#fff',
    padding: 0,
  },
  inputRow: {
    display: 'flex',
    gap: 8,
    alignItems: 'flex-end',
  },
  textInput: {
    flex: 1,
    minWidth: 0,
    background: 'var(--bg-elevated, #1a1a1a)',
    border: '1px solid var(--border-strong, #2a2a2a)',
    borderRadius: 22,
    padding: '10px 16px',
    minHeight: 44,
    color: 'inherit',
    fontSize: 13,
    outline: 'none',
    resize: 'none',
    lineHeight: 1.4,
    maxHeight: 120,
    fontFamily: 'inherit',
    overflowY: 'auto',
    transition: 'border-color var(--motion-fast) var(--ease-standard), background var(--motion-fast) var(--ease-standard)',
  },
  iconBtn: (disabled) => ({
    background: 'none',
    border: 'none',
    color: disabled ? 'var(--text-dim, #444)' : 'var(--text-muted, #888)',
    cursor: disabled ? 'default' : 'pointer',
    // ≥44px touch target (WCAG 2.5.8) while the glyph stays 18px.
    width: 44,
    height: 44,
    borderRadius: 8,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    transition: 'color var(--motion-fast) var(--ease-standard), background var(--motion-fast) var(--ease-standard)',
    flexShrink: 0,
  }),
  sendBtn: (canSend) => ({
    background: canSend ? 'var(--accent)' : 'var(--bg-elevated, #2a2a2a)',
    border: canSend ? 'none' : '1px solid var(--border-default)',
    color: canSend ? '#fff' : 'var(--text-ghost)',
    cursor: canSend ? 'pointer' : 'default',
    width: 44,
    height: 44,
    borderRadius: '50%',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    transition: 'background var(--motion-base) var(--ease-standard), color var(--motion-base) var(--ease-standard)',
    flexShrink: 0,
  }),
  backBtn: {
    background: 'none',
    border: 'none',
    color: 'var(--text-muted, #888)',
    cursor: 'pointer',
    width: 36,
    height: 36,
    marginLeft: -6,
    borderRadius: 8,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    flexShrink: 0,
  },
  uploadProgress: {
    fontSize: 11,
    color: 'var(--accent)',
    marginBottom: 4,
  },
  empty: {
    flex: 1,
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 12,
    padding: 24,
    color: 'var(--text-ghost, #555)',
  },
  emptyIconWrap: {
    width: 72,
    height: 72,
    borderRadius: '50%',
    background: 'var(--accent-soft)',
    color: 'var(--accent)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    marginBottom: 2,
  },
  emptyTitle: {
    fontSize: 15,
    fontWeight: 600,
    color: 'var(--text-primary, #e5e5e5)',
  },
  emptyNote: {
    fontSize: 12,
    textAlign: 'center',
    maxWidth: 220,
    lineHeight: 1.5,
  },
  spinner: {
    width: 20,
    height: 20,
    border: '2px solid color-mix(in srgb, var(--accent) 22%, transparent)',
    borderTopColor: 'var(--accent)',
    borderRadius: '50%',
    animation: 'spin 0.7s linear infinite',
  },
  errorBanner: {
    background: 'var(--status-danger-soft)',
    border: '1px solid color-mix(in srgb, var(--status-danger) 34%, transparent)',
    borderRadius: 8,
    padding: '8px 12px',
    fontSize: 12,
    color: 'var(--status-danger)',
    margin: '8px 18px 0',
  },
}

// ── normalizeConversation — map backend ConversationSummary to UI shape ───────

function normalizeConversation(raw) {
  // Backend returns: { conv_id, last_activity, message_count }
  // UI expects: { id, peer_id, peer_name, last_message, unread_count }
  if (!raw) return raw
  const id = raw.id || raw.conv_id
  // Extract peer vula_id from conv_id: "<lower>_<higher>" where local node is one half.
  // We surface the full conv_id as peer_id for display; contacts can provide display names.
  const peerName = raw.peer_name || raw.peer_id || null
  return {
    ...raw,
    id,
    peer_id: raw.peer_id || id,
    peer_name: peerName,
    last_message: raw.last_message || (raw.last_activity ? { timestamp: raw.last_activity } : null),
    unread_count: raw.unread_count || 0,
  }
}

// ── ConversationList ──────────────────────────────────────────────────────────

function ConversationList({ conversations, activeId, onSelect, loading, wsConnected, narrow }) {
  const sidebarStyle = narrow
    ? { ...S.sidebar, width: '100%', minWidth: 0, borderRight: 'none' }
    : S.sidebar
  return (
    <aside style={sidebarStyle}>
      <div style={S.sidebarHeader}>
        <div style={S.sidebarTitle}>Messages</div>
        <div
          title={wsConnected ? 'Connected' : 'Disconnected'}
          style={wsConnected ? S.onlineDot : S.offlineDot}
        />
      </div>

      {loading && conversations.length === 0 ? (
        <div style={{ display: 'flex', justifyContent: 'center', padding: 24 }}>
          <div style={S.spinner} />
        </div>
      ) : conversations.length === 0 ? (
        <div style={{
          padding: 24, textAlign: 'center', color: 'var(--text-ghost, #555)', fontSize: 12, lineHeight: 1.6,
        }}>
          No conversations yet.
          <br />Start messaging a contact.
        </div>
      ) : (
        <div style={S.convList}>
          {conversations.map((conv) => {
            const active = conv.id === activeId
            const name = conv.peer_name || conv.peer_id || 'Unknown'
            const seed = name.charCodeAt(0)
            return (
              <div
                key={conv.id}
                style={S.convItem(active)}
                onClick={() => onSelect(conv)}
              >
                <div style={S.avatar(seed % 7)}>
                  {initials(name)}
                </div>
                <div style={S.convMeta}>
                  <div style={S.convName}>{name}</div>
                  <div style={S.convPreview}>
                    {conv.last_message?.body
                      ? conv.last_message.body.slice(0, 50)
                      : conv.last_message?.type === 'image'
                        ? 'Image'
                        : conv.last_message?.type === 'file'
                          ? 'File'
                          : 'No messages'}
                  </div>
                </div>
                <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 4 }}>
                  {conv.last_message?.timestamp && (
                    <div style={S.convTime}>{formatRelative(conv.last_message.timestamp)}</div>
                  )}
                  {conv.unread_count > 0 && (
                    <div style={S.unreadBadge}>{conv.unread_count}</div>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </aside>
  )
}

// ── MessageBubble ─────────────────────────────────────────────────────────────

function MediaThumb({ attachment }) {
  const isImage = attachment.mime_type?.startsWith('image/') || attachment.type === 'image'
  // PEER-16: hash is "sha256:<hex>"; use thumb endpoint for images, fetch (signed) for others
  const hash = attachment.hash || attachment.id
  const thumbUrl = hash ? `/api/peering/media/thumb/${hash}` : null
  const fetchUrl = attachment.signed_url || (hash ? `/api/peering/media/fetch/${hash}` : null)
  const url = isImage ? (thumbUrl || attachment.url) : (fetchUrl || attachment.url)

  if (isImage) {
    return (
      <div style={S.mediaAttachment}>
        <img
          src={url}
          alt={attachment.filename || 'image'}
          style={S.mediaImg}
          onClick={() => window.open(fetchUrl || url, '_blank')}
        />
      </div>
    )
  }

  return (
    <a href={fetchUrl || url} target="_blank" rel="noreferrer" style={S.fileChip}>
      <IconFile />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontSize: 12 }}>
          {attachment.filename || 'file'}
        </div>
        {attachment.size && (
          <div style={{ fontSize: 10, color: 'var(--text-faint, #666)' }}>{humanFileSize(attachment.size)}</div>
        )}
      </div>
    </a>
  )
}

function MessageBubble({ msg, isMine, peerName }) {
  const seed = (peerName || '').charCodeAt(0) % 7
  return (
    <div>
      <div style={S.msgRow(isMine)}>
        {!isMine && (
          <div style={S.msgAvatar(seed)}>
            {initials(peerName)}
          </div>
        )}
        <div style={{ maxWidth: '74%', minWidth: 0 }}>
          <div style={S.bubble(isMine)}>
            {msg.body && <div>{msg.body}</div>}
            {msg.attachments?.map((att, i) => (
              <MediaThumb key={att.id || i} attachment={att} />
            ))}
          </div>
        </div>
      </div>
      <div style={S.bubbleMeta(isMine)}>
        <span style={S.metaText}>{formatTime(msg.timestamp)}</span>
        {isMine && msg.delivered && (
          <span style={{ color: 'var(--accent)' }}><IconCheck /></span>
        )}
      </div>
    </div>
  )
}

// ── Composer ──────────────────────────────────────────────────────────────────

function Composer({ convId, onSent, disabled }) {
  const [text, setText] = useState('')
  const [attachments, setAttachments] = useState([]) // [{file, objectUrl, uploading, id, error}]
  const [dragging, setDragging] = useState(false)
  const [sending, setSending] = useState(false)
  const [error, setError] = useState(null)
  const textareaRef = useRef(null)
  const fileInputRef = useRef(null)
  const dragCounterRef = useRef(0)

  const uploadFile = useCallback(async (file) => {
    const key = Date.now() + Math.random()
    const objectUrl = URL.createObjectURL(file)
    setAttachments(prev => [...prev, { key, file, objectUrl, uploading: true, id: null, error: null }])

    const fd = new FormData()
    fd.append('file', file)

    try {
      const res = await fetch('/api/peering/media/upload', { method: 'POST', body: fd })
      if (!res.ok) throw new Error(`Upload failed: ${res.status}`)
      const data = await res.json()
      // Backend (PEER-16) returns { hash: "sha256:<hex>", signed_url, size, mime_type }
      setAttachments(prev =>
        prev.map(a => a.key === key ? { ...a, uploading: false, id: data.hash, mimeType: data.mime_type, size: data.size } : a)
      )
    } catch (err) {
      setAttachments(prev =>
        prev.map(a => a.key === key ? { ...a, uploading: false, error: err.message } : a)
      )
    }
  }, [])

  const handleFiles = useCallback((files) => {
    for (const f of files) {
      uploadFile(f)
    }
  }, [uploadFile])

  const handleDragEnter = (e) => {
    e.preventDefault()
    dragCounterRef.current++
    setDragging(true)
  }
  const handleDragLeave = (e) => {
    e.preventDefault()
    dragCounterRef.current--
    if (dragCounterRef.current <= 0) { dragCounterRef.current = 0; setDragging(false) }
  }
  const handleDragOver = (e) => e.preventDefault()
  const handleDrop = (e) => {
    e.preventDefault()
    dragCounterRef.current = 0
    setDragging(false)
    const files = Array.from(e.dataTransfer.files)
    if (files.length) handleFiles(files)
  }

  const removeAttachment = (key) => {
    setAttachments(prev => {
      const a = prev.find(x => x.key === key)
      if (a?.objectUrl) URL.revokeObjectURL(a.objectUrl)
      return prev.filter(x => x.key !== key)
    })
  }

  const canSend = !disabled && !sending && (text.trim().length > 0 || attachments.some(a => a.id && !a.uploading && !a.error))

  const send = async () => {
    if (!canSend) return
    setSending(true)
    setError(null)

    // PEER-14 backend only accepts { body } — no media_ids field.
    // Send each uploaded media hash as a separate message body referencing the hash,
    // then send the text body (if any).
    try {
      const readyAttachments = attachments.filter(a => a.id && !a.uploading && !a.error)

      const sendOne = async (bodyText) => {
        const res = await fetch(`/api/peering/conversations/${convId}/send`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ body: bodyText }),
        })
        if (!res.ok) throw new Error(`Send failed: ${res.status}`)
        return res.json()
      }

      let lastMsg = null
      // Send each media attachment as its own message with a hash reference
      for (const att of readyAttachments) {
        const label = att.file?.name ? `[media: ${att.file.name}] ${att.id}` : `[media] ${att.id}`
        lastMsg = await sendOne(label)
      }

      // Send text body if present (or if there were no attachments)
      if (text.trim() || readyAttachments.length === 0) {
        lastMsg = await sendOne(text.trim() || ' ')
      }

      // Cleanup objectUrls
      attachments.forEach(a => { if (a.objectUrl) URL.revokeObjectURL(a.objectUrl) })
      setAttachments([])
      setText('')
      textareaRef.current?.focus()
      if (lastMsg) onSent(lastMsg)
    } catch (err) {
      setError(err.message)
    } finally {
      setSending(false)
    }
  }

  const handleKeyDown = (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      send()
    }
  }

  const uploading = attachments.some(a => a.uploading)

  return (
    <div style={S.composer}>
      {error && <div style={S.errorBanner}>{error}</div>}
      <div
        style={S.dropZone(dragging)}
        onDragEnter={handleDragEnter}
        onDragLeave={handleDragLeave}
        onDragOver={handleDragOver}
        onDrop={handleDrop}
      >
        {attachments.length > 0 && (
          <div style={S.attachPreviews}>
            {attachments.map(a => {
              const isImg = a.file?.type?.startsWith('image/')
              return (
                <div key={a.key} style={S.attachPreview}>
                  {isImg
                    ? <img src={a.objectUrl} alt="preview" style={S.attachImg} />
                    : (
                      <div style={S.attachFile}>
                        <IconFile />
                        <span style={{ textOverflow: 'ellipsis', overflow: 'hidden', width: 52, whiteSpace: 'nowrap' }}>
                          {a.file?.name}
                        </span>
                      </div>
                    )
                  }
                  {a.uploading && (
                    <div style={{
                      position: 'absolute', inset: 0, background: 'rgba(0,0,0,0.5)',
                      display: 'flex', alignItems: 'center', justifyContent: 'center',
                    }}>
                      <div style={{ ...S.spinner, width: 16, height: 16 }} />
                    </div>
                  )}
                  {a.error && (
                    <div style={{
                      position: 'absolute', inset: 0, background: 'color-mix(in srgb, var(--status-danger) 55%, transparent)',
                      display: 'flex', alignItems: 'center', justifyContent: 'center',
                      fontSize: 10, color: '#fff', padding: 2, textAlign: 'center',
                    }}>
                      Failed
                    </div>
                  )}
                  <button style={S.attachRemove} aria-label="Remove attachment" onClick={() => removeAttachment(a.key)}>
                    <IconX />
                  </button>
                </div>
              )
            })}
          </div>
        )}

        {uploading && (
          <div style={S.uploadProgress}>Uploading media…</div>
        )}

        <div style={S.inputRow}>
          <input
            ref={fileInputRef}
            type="file"
            multiple
            style={{ display: 'none' }}
            onChange={e => { handleFiles(Array.from(e.target.files)); e.target.value = '' }}
          />
          <button
            style={S.iconBtn(disabled)}
            title="Attach file"
            aria-label="Attach file"
            onClick={() => fileInputRef.current?.click()}
            disabled={disabled}
          >
            <IconPaperclip />
          </button>
          <textarea
            ref={textareaRef}
            style={S.textInput}
            placeholder={disabled ? 'Select a conversation' : 'Message… (Enter to send, Shift+Enter for newline)'}
            value={text}
            onChange={e => setText(e.target.value)}
            onKeyDown={handleKeyDown}
            disabled={disabled}
            rows={1}
          />
          <button
            style={S.sendBtn(canSend)}
            title="Send"
            aria-label="Send message"
            onClick={send}
            disabled={!canSend}
          >
            <IconSend />
          </button>
        </div>
      </div>
    </div>
  )
}

// ── ThreadView ────────────────────────────────────────────────────────────────

// incomingCallbackRef is a stable ref the root Messages component writes into,
// so the WS subscriber can deliver inbound frames without mutating a component.
const incomingCallbackRef = { current: null }

function ThreadView({ conversation, myVulaId, onBack }) {
  const [messages, setMessages] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const bottomRef = useRef(null)
  const convId = conversation?.id

  // Load history — clearing messages when convId is absent is intentional.
  useEffect(() => {
    if (!convId) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setMessages([])
      return
    }
    setLoading(true)
    setError(null)
    fetch(`/api/peering/conversations/${convId}/messages`)
      .then(r => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then(data => {
        setMessages(Array.isArray(data) ? data : data.messages || [])
      })
      .catch(err => setError(err.message))
      .finally(() => setLoading(false))
  }, [convId])

  // Scroll to bottom on new messages
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  const handleSent = useCallback((msg) => {
    setMessages(prev => {
      // Avoid duplicates if WS already delivered it
      if (prev.some(m => m.id === msg.id)) return prev
      return [...prev, msg]
    })
  }, [])

  const handleIncoming = useCallback((msg) => {
    if (msg.conversation_id !== convId && msg.conv_id !== convId) return
    setMessages(prev => {
      if (prev.some(m => m.id === msg.id)) return prev
      return [...prev, msg]
    })
  }, [convId])

  // Register our callback so the parent WS subscriber can call it
  useEffect(() => {
    incomingCallbackRef.current = handleIncoming
    return () => { incomingCallbackRef.current = null }
  }, [handleIncoming])

  const peerName = conversation?.peer_name || conversation?.peer_id || 'Peer'
  const seed = peerName.charCodeAt(0) % 7

  // Group messages by date
  function groupByDate(msgs) {
    const groups = []
    let lastDate = null
    for (const m of msgs) {
      const d = m.timestamp ? new Date(m.timestamp).toDateString() : null
      if (d !== lastDate) {
        groups.push({ type: 'date', date: d || 'Unknown date' })
        lastDate = d
      }
      groups.push({ type: 'msg', msg: m })
    }
    return groups
  }

  if (!conversation) {
    return (
      <div style={{ ...S.main, justifyContent: 'center' }}>
        <div style={S.empty}>
          <div style={S.emptyIconWrap}>
            <IconMessages />
          </div>
          <div style={S.emptyTitle}>Your Messages</div>
          <div style={S.emptyNote}>
            Select a conversation to read messages or send to a contact.
          </div>
        </div>
      </div>
    )
  }

  return (
    <>
      {/* Thread header */}
      <div style={S.threadHeader}>
        {onBack && (
          <button style={S.backBtn} aria-label="Back to conversations" onClick={onBack}>
            <IconBack />
          </button>
        )}
        <div style={S.avatar(seed)}>
          {initials(peerName)}
        </div>
        <div>
          <div style={S.threadTitle}>{peerName}</div>
          {conversation.peer_id && (
            <div style={S.threadSub} title={conversation.peer_id}>
              {conversation.peer_id.length > 40
                ? conversation.peer_id.slice(0, 20) + '…' + conversation.peer_id.slice(-10)
                : conversation.peer_id}
            </div>
          )}
        </div>
      </div>

      {/* Message list */}
      <div style={S.messageArea}>
        {loading && (
          <div style={{ display: 'flex', justifyContent: 'center', padding: 24 }}>
            <div style={S.spinner} />
          </div>
        )}
        {error && (
          <div style={{ ...S.errorBanner, margin: '0 0 12px 0' }}>
            Failed to load messages: {error}
          </div>
        )}

        {!loading && messages.length === 0 && !error && (
          <div style={{ ...S.empty, flex: 'none', padding: 32 }}>
            <div style={{ ...S.emptyNote, color: 'var(--text-ghost, #555)' }}>
              No messages yet. Say hello!
            </div>
          </div>
        )}

        {groupByDate(messages).map((item, i) => {
          if (item.type === 'date') {
            return (
              <div key={`date-${i}`} style={S.dateSep}>{item.date}</div>
            )
          }
          const msg = item.msg
          const isMine = msg.from === myVulaId || msg.direction === 'out' || msg.is_mine
          return (
            <MessageBubble
              key={msg.id || i}
              msg={msg}
              isMine={isMine}
              peerName={peerName}
            />
          )
        })}
        <div ref={bottomRef} />
      </div>

      {/* Composer */}
      <Composer convId={convId} onSent={handleSent} disabled={false} />
    </>
  )
}

// ── Root: Messages ────────────────────────────────────────────────────────────

export default function Messages() {
  const [conversations, setConversations] = useState([])
  const [activeConv, setActiveConv] = useState(null)
  const [loadingConvs, setLoadingConvs] = useState(false)
  const [myVulaId, setMyVulaId] = useState(null)
  const threadRef = useRef(null)

  // Fetch own identity
  useEffect(() => {
    fetch('/api/peering/identity')
      .then(r => r.ok ? r.json() : null)
      .then(data => { if (data?.vula_id) setMyVulaId(data.vula_id) })
      .catch(() => {})
  }, [])

  // Fetch conversation list
  const fetchConversations = useCallback(() => {
    setLoadingConvs(true)
    fetch('/api/peering/conversations')
      .then(r => r.ok ? r.json() : [])
      .then(data => {
        const raw = Array.isArray(data) ? data : data.conversations || []
        setConversations(raw.map(normalizeConversation))
      })
      .catch(() => {})
      .finally(() => setLoadingConvs(false))
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { fetchConversations() }, [fetchConversations])

  // Realtime: subscribe to the WS `message` channel via usePeering (PEER-05)
  const { connected: wsConnected, subscribe } = usePeering()

  useEffect(() => {
    const unsub = subscribe(Channel.MESSAGE, (frame) => {
      const msg = frame.payload || frame

      // Deliver to active thread if it matches
      if (incomingCallbackRef.current) {
        incomingCallbackRef.current(msg)
      }

      // Update conversation list: bump preview + unread
      setConversations(prev => {
        const convId = msg.conversation_id || msg.conv_id
        if (!convId) return prev

        const exists = prev.some(c => c.id === convId)
        if (exists) {
          return prev.map(c => {
            if (c.id !== convId) return c
            return {
              ...c,
              last_message: msg,
              unread_count: activeConv?.id === convId ? 0 : (c.unread_count || 0) + 1,
            }
          })
        }
        // Unknown conversation — refresh the list
        fetchConversations()
        return prev
      })
    })
    return unsub
  }, [subscribe, activeConv, fetchConversations])

  const handleSelectConv = (conv) => {
    setActiveConv(conv)
    // Mark as read
    setConversations(prev =>
      prev.map(c => c.id === conv.id ? { ...c, unread_count: 0 } : c)
    )
  }

  // Inject global keyframe for spinner (once)
  useEffect(() => {
    if (document.getElementById('peer-msg-keyframes')) return
    const style = document.createElement('style')
    style.id = 'peer-msg-keyframes'
    style.textContent = `@keyframes spin { to { transform: rotate(360deg); } }`
    document.head.appendChild(style)
    return () => style.remove()
  }, [])

  // MOBILE-ADAPTIVE: on a phone show ONE pane — the list, or (once a
  // conversation is picked) the thread with a back button. On wider screens
  // keep the classic two-pane messenger layout.
  const narrow = useNarrow()
  const showList = !narrow || !activeConv
  const showThread = !narrow || !!activeConv

  return (
    <div style={S.root}>
      {showList && (
        <ConversationList
          conversations={conversations}
          activeId={activeConv?.id}
          onSelect={handleSelectConv}
          loading={loadingConvs}
          wsConnected={wsConnected}
          narrow={narrow}
        />
      )}
      {showThread && (
        <div style={S.main}>
          <ThreadView
            ref={threadRef}
            conversation={activeConv}
            myVulaId={myVulaId}
            onBack={narrow ? () => setActiveConv(null) : undefined}
          />
        </div>
      )}
    </div>
  )
}
