import { useState, useEffect, useRef, useCallback } from 'react'

// ── WebSocket URL for the peering `message` channel ───────────────────────────
const WS_URL = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/peering/ws`

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

const IconSend = () => (
  <svg viewBox="0 0 20 20" fill="currentColor" width="18" height="18">
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
    background: 'var(--bg, #0d0d0d)',
    color: 'var(--text, #e5e5e5)',
    fontFamily: "'Inter', -apple-system, BlinkMacSystemFont, sans-serif",
    fontSize: 13,
    overflow: 'hidden',
  },
  sidebar: {
    width: 280,
    minWidth: 220,
    flexShrink: 0,
    borderRight: '1px solid var(--border, #1e1e1e)',
    display: 'flex',
    flexDirection: 'column',
    background: 'var(--sidebar-bg, #111)',
    overflow: 'hidden',
  },
  sidebarHeader: {
    padding: '16px 16px 12px',
    borderBottom: '1px solid var(--border, #1e1e1e)',
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
    background: '#22c55e',
    flexShrink: 0,
  },
  offlineDot: {
    width: 8,
    height: 8,
    borderRadius: '50%',
    background: '#555',
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
    padding: '9px 14px',
    cursor: 'pointer',
    background: active ? 'rgba(255,255,255,0.06)' : 'transparent',
    borderLeft: active ? '2px solid #7c3aed' : '2px solid transparent',
    transition: 'background 0.1s',
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
    color: 'var(--muted, #666)',
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    marginTop: 2,
  },
  convTime: {
    fontSize: 11,
    color: 'var(--muted, #555)',
    flexShrink: 0,
    alignSelf: 'flex-start',
    marginTop: 2,
  },
  unreadBadge: {
    background: '#7c3aed',
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
    borderBottom: '1px solid var(--border, #1e1e1e)',
    display: 'flex',
    alignItems: 'center',
    gap: 10,
    background: 'var(--header-bg, #111)',
    flexShrink: 0,
  },
  threadTitle: {
    fontWeight: 600,
    fontSize: 14,
  },
  threadSub: {
    fontSize: 11,
    color: 'var(--muted, #666)',
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
    color: 'var(--muted, #555)',
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
    maxWidth: '65%',
    padding: '8px 12px',
    borderRadius: mine ? '16px 16px 4px 16px' : '16px 16px 16px 4px',
    background: mine ? '#7c3aed' : 'var(--bubble-bg, #1e1e1e)',
    color: mine ? '#fff' : 'var(--text, #e5e5e5)',
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
    color: 'var(--muted, #555)',
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
    background: 'rgba(255,255,255,0.08)',
    borderRadius: 10,
    fontSize: 12,
    marginTop: 4,
    cursor: 'pointer',
    textDecoration: 'none',
    color: 'inherit',
  },
  composer: {
    padding: '12px 18px',
    borderTop: '1px solid var(--border, #1e1e1e)',
    background: 'var(--header-bg, #111)',
    flexShrink: 0,
  },
  dropZone: (dragging) => ({
    border: dragging ? '2px dashed #7c3aed' : '2px dashed transparent',
    borderRadius: 10,
    transition: 'border 0.15s',
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
    borderRadius: 8,
    overflow: 'hidden',
    background: '#1e1e1e',
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
    color: 'var(--muted, #666)',
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
    background: 'var(--input-bg, #1a1a1a)',
    border: '1px solid var(--border, #2a2a2a)',
    borderRadius: 22,
    padding: '9px 16px',
    color: 'inherit',
    fontSize: 13,
    outline: 'none',
    resize: 'none',
    lineHeight: 1.4,
    maxHeight: 120,
    fontFamily: 'inherit',
    overflowY: 'auto',
  },
  iconBtn: (disabled) => ({
    background: 'none',
    border: 'none',
    color: disabled ? 'var(--muted, #444)' : 'var(--muted, #888)',
    cursor: disabled ? 'default' : 'pointer',
    padding: 6,
    borderRadius: 8,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    transition: 'color 0.1s, background 0.1s',
    flexShrink: 0,
  }),
  sendBtn: (canSend) => ({
    background: canSend ? '#7c3aed' : '#2a2a2a',
    border: 'none',
    color: canSend ? '#fff' : '#555',
    cursor: canSend ? 'pointer' : 'default',
    padding: '8px 10px',
    borderRadius: '50%',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    transition: 'background 0.15s',
    flexShrink: 0,
  }),
  uploadProgress: {
    fontSize: 11,
    color: '#7c3aed',
    marginBottom: 4,
  },
  empty: {
    flex: 1,
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 12,
    color: 'var(--muted, #555)',
  },
  emptyTitle: {
    fontSize: 15,
    fontWeight: 600,
    color: 'var(--text, #e5e5e5)',
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
    border: '2px solid #333',
    borderTopColor: '#7c3aed',
    borderRadius: '50%',
    animation: 'spin 0.7s linear infinite',
  },
  errorBanner: {
    background: 'rgba(220,38,38,0.12)',
    border: '1px solid rgba(220,38,38,0.3)',
    borderRadius: 8,
    padding: '8px 12px',
    fontSize: 12,
    color: '#f87171',
    margin: '8px 18px 0',
  },
}

// ── usePeeringWS — live WebSocket for `message` channel ──────────────────────

function usePeeringWS(onMessage) {
  const wsRef = useRef(null)
  const retryRef = useRef(0)
  const [connected, setConnected] = useState(false)
  const cbRef = useRef(onMessage)
  cbRef.current = onMessage

  useEffect(() => {
    let alive = true

    function connect() {
      if (!alive) return
      const ws = new WebSocket(WS_URL)
      wsRef.current = ws

      ws.onopen = () => {
        setConnected(true)
        retryRef.current = 0
        // Subscribe to the `message` channel
        ws.send(JSON.stringify({ channel: 'message', action: 'subscribe' }))
      }

      ws.onmessage = (e) => {
        try {
          const data = JSON.parse(e.data)
          if (data.channel === 'message' || data.type === 'message') {
            cbRef.current(data)
          }
        } catch {}
      }

      ws.onclose = () => {
        setConnected(false)
        wsRef.current = null
        if (alive) {
          const delay = Math.min(1000 * 2 ** retryRef.current, 30_000)
          retryRef.current++
          setTimeout(connect, delay)
        }
      }

      ws.onerror = () => ws.close()
    }

    connect()
    return () => {
      alive = false
      wsRef.current?.close()
    }
  }, [])

  return { connected }
}

// ── ConversationList ──────────────────────────────────────────────────────────

function ConversationList({ conversations, activeId, onSelect, loading, wsConnected }) {
  return (
    <aside style={S.sidebar}>
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
          padding: 24, textAlign: 'center', color: 'var(--muted, #555)', fontSize: 12, lineHeight: 1.6,
        }}>
          No conversations yet.
          <br />Start messaging a contact.
        </div>
      ) : (
        <div style={S.convList}>
          {conversations.map((conv, i) => {
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
  const url = attachment.url || `/api/peering/media/${attachment.id}`

  if (isImage) {
    return (
      <div style={S.mediaAttachment}>
        <img
          src={url}
          alt={attachment.filename || 'image'}
          style={S.mediaImg}
          onClick={() => window.open(url, '_blank')}
        />
      </div>
    )
  }

  return (
    <a href={url} target="_blank" rel="noreferrer" style={S.fileChip}>
      <IconFile />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontSize: 12 }}>
          {attachment.filename || 'file'}
        </div>
        {attachment.size && (
          <div style={{ fontSize: 10, color: 'var(--muted, #666)' }}>{humanFileSize(attachment.size)}</div>
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
        <div style={{ maxWidth: '65%' }}>
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
          <span style={{ color: '#7c3aed' }}><IconCheck /></span>
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
      setAttachments(prev =>
        prev.map(a => a.key === key ? { ...a, uploading: false, id: data.id || data.media_id } : a)
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

    const mediaIds = attachments.filter(a => a.id).map(a => a.id)

    try {
      const body = { body: text.trim() }
      if (mediaIds.length) body.media_ids = mediaIds

      const res = await fetch(`/api/peering/conversations/${convId}/send`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok) throw new Error(`Send failed: ${res.status}`)
      const msg = await res.json()

      // Cleanup objectUrls
      attachments.forEach(a => { if (a.objectUrl) URL.revokeObjectURL(a.objectUrl) })
      setAttachments([])
      setText('')
      textareaRef.current?.focus()
      onSent(msg)
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
                      position: 'absolute', inset: 0, background: 'rgba(220,38,38,0.4)',
                      display: 'flex', alignItems: 'center', justifyContent: 'center',
                      fontSize: 10, color: '#fff', padding: 2, textAlign: 'center',
                    }}>
                      Failed
                    </div>
                  )}
                  <button style={S.attachRemove} onClick={() => removeAttachment(a.key)}>
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

function ThreadView({ conversation, myVulaId }) {
  const [messages, setMessages] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)
  const bottomRef = useRef(null)
  const convId = conversation?.id

  // Load history
  useEffect(() => {
    if (!convId) { setMessages([]); return }
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

  // Expose handleIncoming so parent can call it
  ThreadView._onIncoming = handleIncoming

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
          <IconMessages />
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
            <div style={{ ...S.emptyNote, color: 'var(--muted, #555)' }}>
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
      .then(data => setConversations(Array.isArray(data) ? data : data.conversations || []))
      .catch(() => {})
      .finally(() => setLoadingConvs(false))
  }, [])

  useEffect(() => { fetchConversations() }, [fetchConversations])

  // Handle incoming realtime messages
  const handleWsMessage = useCallback((data) => {
    const msg = data.payload || data

    // Deliver to active thread if it matches
    if (ThreadView._onIncoming) {
      ThreadView._onIncoming(msg)
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
  }, [activeConv, fetchConversations])

  const { connected: wsConnected } = usePeeringWS(handleWsMessage)

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

  return (
    <div style={S.root}>
      <ConversationList
        conversations={conversations}
        activeId={activeConv?.id}
        onSelect={handleSelectConv}
        loading={loadingConvs}
        wsConnected={wsConnected}
      />
      <div style={S.main}>
        <ThreadView
          ref={threadRef}
          conversation={activeConv}
          myVulaId={myVulaId}
        />
      </div>
    </div>
  )
}
