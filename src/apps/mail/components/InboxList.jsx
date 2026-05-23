import { useState } from 'react'

const S = {
  container: {
    display: 'flex',
    flexDirection: 'column',
    height: '100%',
    overflow: 'hidden',
  },
  header: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: '10px 14px',
    borderBottom: '1px solid rgba(255,255,255,0.06)',
    flexShrink: 0,
  },
  headerTitle: {
    fontSize: 11,
    fontWeight: 600,
    color: 'rgba(255,255,255,0.4)',
    textTransform: 'uppercase',
    letterSpacing: '0.08em',
  },
  list: {
    flex: 1,
    overflowY: 'auto',
  },
  row: (selected, unread) => ({
    display: 'flex',
    flexDirection: 'column',
    gap: 3,
    padding: '10px 14px',
    cursor: 'pointer',
    borderBottom: '1px solid rgba(255,255,255,0.04)',
    background: selected
      ? 'rgba(99,102,241,0.15)'
      : unread
        ? 'rgba(255,255,255,0.03)'
        : 'transparent',
    borderLeft: `3px solid ${selected ? '#818cf8' : unread ? '#6366f1' : 'transparent'}`,
    transition: 'background 0.12s',
  }),
  rowTop: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    gap: 6,
  },
  fromText: (unread) => ({
    fontSize: 12,
    fontWeight: unread ? 600 : 400,
    color: unread ? '#f5f5f5' : 'rgba(255,255,255,0.65)',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
    maxWidth: 140,
  }),
  dateText: {
    fontSize: 10,
    color: 'rgba(255,255,255,0.3)',
    flexShrink: 0,
  },
  subject: (unread) => ({
    fontSize: 11,
    color: unread ? 'rgba(255,255,255,0.8)' : 'rgba(255,255,255,0.45)',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
    fontWeight: unread ? 500 : 400,
  }),
  unreadDot: {
    width: 6,
    height: 6,
    borderRadius: '50%',
    background: '#818cf8',
    flexShrink: 0,
    marginLeft: 'auto',
    alignSelf: 'center',
  },
  emptyState: {
    display: 'flex',
    flexDirection: 'column',
    alignItems: 'center',
    justifyContent: 'center',
    height: '100%',
    gap: 10,
    color: 'rgba(255,255,255,0.2)',
  },
}

function formatDate(dateStr) {
  if (!dateStr) return ''
  const d = new Date(dateStr)
  const now = new Date()
  const isToday = d.toDateString() === now.toDateString()
  if (isToday) return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

export default function InboxList({ messages, selectedId, onSelect, folder }) {
  const [hovered, setHovered] = useState(null)

  const label = folder === 'sent' ? 'Sent' : folder === 'archive' ? 'Archive' : 'Inbox'

  return (
    <div style={S.container}>
      <div style={S.header}>
        <span style={S.headerTitle}>{label}</span>
        <span style={{ fontSize: 10, color: 'rgba(255,255,255,0.2)' }}>
          {messages.length} message{messages.length !== 1 ? 's' : ''}
        </span>
      </div>
      <div style={S.list}>
        {messages.length === 0 ? (
          <div style={S.emptyState}>
            <span style={{ fontSize: 28 }}>✉</span>
            <span style={{ fontSize: 12 }}>No mail here</span>
          </div>
        ) : (
          messages.map((msg) => {
            const isUnread = !msg.read && folder !== 'sent'
            const isSelected = msg.id === selectedId
            return (
              <div
                key={msg.id}
                style={{
                  ...S.row(isSelected, isUnread),
                  ...(hovered === msg.id && !isSelected ? { background: 'rgba(255,255,255,0.04)' } : {}),
                }}
                onClick={() => onSelect(msg)}
                onMouseEnter={() => setHovered(msg.id)}
                onMouseLeave={() => setHovered(null)}
              >
                <div style={S.rowTop}>
                  <span style={S.fromText(isUnread)}>
                    {folder === 'sent' ? msg.to : (msg.from_address || msg.from)}
                  </span>
                  <span style={S.dateText}>{formatDate(msg.received_at || msg.date)}</span>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <span style={S.subject(isUnread)}>{msg.subject || '(no subject)'}</span>
                  {isUnread && <span style={S.unreadDot} />}
                </div>
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}
