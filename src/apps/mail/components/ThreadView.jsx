import { useState, useEffect } from 'react'

const S = {
  container: {
    display: 'flex',
    flexDirection: 'column',
    height: '100%',
    overflow: 'hidden',
  },
  toolbar: {
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    padding: '9px 14px',
    borderBottom: '1px solid rgba(255,255,255,0.06)',
    flexShrink: 0,
  },
  iconBtn: {
    background: 'transparent',
    border: 'none',
    color: 'rgba(255,255,255,0.4)',
    cursor: 'pointer',
    padding: '4px 6px',
    borderRadius: 6,
    fontSize: 12,
    lineHeight: 1,
    transition: 'color 0.12s, background 0.12s',
  },
  threadScroll: {
    flex: 1,
    overflowY: 'auto',
    padding: '12px 14px',
    display: 'flex',
    flexDirection: 'column',
    gap: 12,
  },
  subject: {
    fontSize: 14,
    fontWeight: 600,
    color: '#f5f5f5',
    marginBottom: 12,
    lineHeight: 1.4,
  },
  messageCard: (expanded) => ({
    borderRadius: 10,
    border: '1px solid rgba(255,255,255,0.08)',
    background: expanded ? 'rgba(255,255,255,0.04)' : 'rgba(255,255,255,0.02)',
    overflow: 'hidden',
  }),
  cardHeader: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
    padding: '9px 12px',
    cursor: 'pointer',
    userSelect: 'none',
  },
  cardFrom: {
    fontSize: 12,
    fontWeight: 500,
    color: 'rgba(255,255,255,0.75)',
  },
  cardDate: {
    fontSize: 10,
    color: 'rgba(255,255,255,0.3)',
  },
  cardBody: {
    padding: '8px 12px 12px',
    fontSize: 12,
    color: 'rgba(255,255,255,0.7)',
    lineHeight: 1.65,
    borderTop: '1px solid rgba(255,255,255,0.05)',
    whiteSpace: 'pre-wrap',
    wordBreak: 'break-word',
  },
  loading: {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    padding: 24,
    gap: 8,
    color: 'rgba(255,255,255,0.3)',
    fontSize: 12,
  },
  spinner: {
    width: 14,
    height: 14,
    border: '2px solid rgba(255,255,255,0.1)',
    borderTopColor: '#818cf8',
    borderRadius: '50%',
    display: 'inline-block',
    animation: 'spin 0.8s linear infinite',
  },
}

function formatDate(dateStr) {
  if (!dateStr) return ''
  return new Date(dateStr).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

function MessageCard({ msg, initialExpanded, onMarkRead, onAction }) {
  const [expanded, setExpanded] = useState(initialExpanded)
  const [body, setBody] = useState(msg.body || null)
  const [loadingBody, setLoadingBody] = useState(false)

  const toggle = async () => {
    if (!expanded && !body) {
      setLoadingBody(true)
      try {
        const res = await fetch(`/api/identity/mailbox/${msg.id}`)
        if (res.ok) {
          const data = await res.json()
          setBody(data.body || data.body_text || '(empty)')
          if (!msg.read) onMarkRead(msg.id)
        } else {
          setBody('(could not load message body)')
        }
      } catch {
        setBody('(network error)')
      }
      setLoadingBody(false)
    } else if (!expanded && body && !msg.read) {
      onMarkRead(msg.id)
    }
    setExpanded(e => !e)
  }

  return (
    <div style={S.messageCard(expanded)}>
      <div style={S.cardHeader} onClick={toggle}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span style={S.cardFrom}>{msg.from_address || msg.from || 'Unknown'}</span>
          <span style={S.cardDate}>{formatDate(msg.received_at || msg.date)}</span>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          {!msg.read && (
            <span style={{
              fontSize: 9,
              fontWeight: 600,
              color: '#818cf8',
              background: 'rgba(99,102,241,0.2)',
              padding: '2px 6px',
              borderRadius: 4,
            }}>
              NEW
            </span>
          )}
          <span style={{ color: 'rgba(255,255,255,0.3)', fontSize: 10 }}>
            {expanded ? '▲' : '▼'}
          </span>
        </div>
      </div>
      {expanded && (
        <div style={S.cardBody}>
          {loadingBody ? (
            <div style={S.loading}>
              <span style={S.spinner} />
              Loading...
            </div>
          ) : (
            <>
              <div>{body || '(empty)'}</div>
              <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
                <button
                  style={{
                    ...S.iconBtn,
                    background: 'rgba(255,255,255,0.06)',
                    color: 'rgba(255,255,255,0.55)',
                    fontSize: 11,
                    padding: '4px 10px',
                  }}
                  onClick={() => onAction('reply', msg)}
                  onMouseEnter={e => { e.currentTarget.style.background = 'rgba(255,255,255,0.1)'; e.currentTarget.style.color = '#f5f5f5' }}
                  onMouseLeave={e => { e.currentTarget.style.background = 'rgba(255,255,255,0.06)'; e.currentTarget.style.color = 'rgba(255,255,255,0.55)' }}
                >
                  ↩ Reply
                </button>
                <button
                  style={{
                    ...S.iconBtn,
                    background: 'rgba(255,255,255,0.06)',
                    color: 'rgba(255,255,255,0.55)',
                    fontSize: 11,
                    padding: '4px 10px',
                  }}
                  onClick={() => onAction('archive', msg)}
                  onMouseEnter={e => { e.currentTarget.style.background = 'rgba(255,255,255,0.1)'; e.currentTarget.style.color = '#f5f5f5' }}
                  onMouseLeave={e => { e.currentTarget.style.background = 'rgba(255,255,255,0.06)'; e.currentTarget.style.color = 'rgba(255,255,255,0.55)' }}
                >
                  Archive
                </button>
                <button
                  style={{
                    ...S.iconBtn,
                    background: 'rgba(239,68,68,0.08)',
                    color: 'rgba(248,113,113,0.7)',
                    fontSize: 11,
                    padding: '4px 10px',
                  }}
                  onClick={() => onAction('delete', msg)}
                  onMouseEnter={e => { e.currentTarget.style.background = 'rgba(239,68,68,0.15)'; e.currentTarget.style.color = '#f87171' }}
                  onMouseLeave={e => { e.currentTarget.style.background = 'rgba(239,68,68,0.08)'; e.currentTarget.style.color = 'rgba(248,113,113,0.7)' }}
                >
                  Delete
                </button>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  )
}

export default function ThreadView({ thread, onBack, onMarkRead, onAction, onReply }) {
  if (!thread || thread.length === 0) {
    return (
      <div style={{
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        height: '100%', color: 'rgba(255,255,255,0.2)', fontSize: 13,
        flexDirection: 'column', gap: 10,
      }}>
        <span style={{ fontSize: 28 }}>✉</span>
        <span>Select a message to read</span>
      </div>
    )
  }

  const subject = thread[0]?.subject || '(no subject)'

  const handleAction = async (action, msg) => {
    if (action === 'reply') {
      onReply(msg)
      return
    }
    const status = action === 'archive' ? 'archived' : 'deleted'
    try {
      await fetch(`/api/identity/mailbox/${msg.id}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status }),
      })
    } catch { /* degrade gracefully */ }
    onAction(action, msg)
  }

  return (
    <div style={S.container}>
      <div style={S.toolbar}>
        <button
          style={S.iconBtn}
          onClick={onBack}
          title="Back to inbox"
          onMouseEnter={e => { e.currentTarget.style.color = '#f5f5f5'; e.currentTarget.style.background = 'rgba(255,255,255,0.07)' }}
          onMouseLeave={e => { e.currentTarget.style.color = 'rgba(255,255,255,0.4)'; e.currentTarget.style.background = 'transparent' }}
        >
          ← Back
        </button>
        <span style={{ flex: 1 }} />
        <span style={{ fontSize: 10, color: 'rgba(255,255,255,0.2)' }}>
          {thread.length} message{thread.length !== 1 ? 's' : ''}
        </span>
      </div>
      <div style={S.threadScroll}>
        <div style={S.subject}>{subject}</div>
        {thread.map((msg, idx) => (
          <MessageCard
            key={msg.id}
            msg={msg}
            initialExpanded={idx === thread.length - 1}
            onMarkRead={onMarkRead}
            onAction={handleAction}
          />
        ))}
      </div>
      <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
    </div>
  )
}
