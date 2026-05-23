import { useState, useEffect, useCallback } from 'react'
import InboxList from './components/InboxList'
import ThreadView from './components/ThreadView'
import ComposeView from './components/ComposeView'

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

function groupIntoThreads(messages) {
  // Group by subject + participants (normalised)
  const map = new Map()
  for (const msg of messages) {
    const key = (msg.subject || '').toLowerCase().replace(/^(re:|fwd?:)\s*/gi, '').trim()
    if (!map.has(key)) map.set(key, [])
    map.get(key).push(msg)
  }
  // Sort each thread oldest-first; sort threads by most-recent message desc
  const threads = []
  for (const [, msgs] of map.entries()) {
    const sorted = [...msgs].sort((a, b) =>
      new Date(a.received_at || a.date || 0) - new Date(b.received_at || b.date || 0)
    )
    threads.push(sorted)
  }
  threads.sort((a, b) => {
    const aLatest = new Date(a[a.length - 1].received_at || a[a.length - 1].date || 0)
    const bLatest = new Date(b[b.length - 1].received_at || b[b.length - 1].date || 0)
    return bLatest - aLatest
  })
  return threads
}

// Representative message for each thread (the latest one)
function threadRep(thread) {
  return thread[thread.length - 1]
}

// ──────────────────────────────────────────────────────────────────────────────
// Sidebar nav
// ──────────────────────────────────────────────────────────────────────────────

const FOLDERS = [
  { id: 'inbox', label: 'Inbox', icon: '✉' },
  { id: 'sent', label: 'Sent', icon: '↗' },
  { id: 'archive', label: 'Archive', icon: '⊡' },
]

function Sidebar({ folder, onFolder, onCompose, unreadCount, identity }) {
  return (
    <div style={{
      width: 160,
      flexShrink: 0,
      borderRight: '1px solid rgba(255,255,255,0.06)',
      display: 'flex',
      flexDirection: 'column',
      background: 'rgba(0,0,0,0.15)',
      overflow: 'hidden',
    }}>
      {/* Identity header */}
      <div style={{
        padding: '14px 12px 10px',
        borderBottom: '1px solid rgba(255,255,255,0.05)',
        flexShrink: 0,
      }}>
        <div style={{ fontSize: 11, fontWeight: 700, color: '#f5f5f5', letterSpacing: '-0.01em' }}>
          Vulos Mail
        </div>
        {identity?.address ? (
          <div style={{
            fontSize: 9.5,
            color: 'rgba(255,255,255,0.3)',
            marginTop: 2,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}>
            {identity.address}
          </div>
        ) : (
          <div style={{ fontSize: 9.5, color: 'rgba(255,255,255,0.2)', marginTop: 2 }}>
            No identity
          </div>
        )}
      </div>

      {/* Compose button */}
      <div style={{ padding: '8px 10px', flexShrink: 0 }}>
        <button
          onClick={onCompose}
          style={{
            width: '100%',
            padding: '7px 0',
            borderRadius: 8,
            background: 'rgba(99,102,241,0.35)',
            border: '1px solid rgba(99,102,241,0.45)',
            color: '#a5b4fc',
            fontSize: 11,
            fontWeight: 600,
            cursor: 'pointer',
            transition: 'background 0.15s',
            letterSpacing: '0.02em',
          }}
          onMouseEnter={e => e.currentTarget.style.background = 'rgba(99,102,241,0.5)'}
          onMouseLeave={e => e.currentTarget.style.background = 'rgba(99,102,241,0.35)'}
        >
          + Compose
        </button>
      </div>

      {/* Folder nav */}
      <nav style={{ flex: 1, padding: '4px 6px', overflowY: 'auto' }}>
        {FOLDERS.map(f => (
          <button
            key={f.id}
            onClick={() => onFolder(f.id)}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 7,
              width: '100%',
              padding: '7px 8px',
              borderRadius: 7,
              background: folder === f.id ? 'rgba(99,102,241,0.18)' : 'transparent',
              border: 'none',
              color: folder === f.id ? '#a5b4fc' : 'rgba(255,255,255,0.45)',
              fontSize: 11,
              fontWeight: folder === f.id ? 600 : 400,
              cursor: 'pointer',
              textAlign: 'left',
              transition: 'background 0.12s, color 0.12s',
              marginBottom: 1,
            }}
            onMouseEnter={e => {
              if (folder !== f.id) {
                e.currentTarget.style.background = 'rgba(255,255,255,0.05)'
                e.currentTarget.style.color = 'rgba(255,255,255,0.7)'
              }
            }}
            onMouseLeave={e => {
              if (folder !== f.id) {
                e.currentTarget.style.background = 'transparent'
                e.currentTarget.style.color = 'rgba(255,255,255,0.45)'
              }
            }}
          >
            <span style={{ fontSize: 12 }}>{f.icon}</span>
            <span style={{ flex: 1 }}>{f.label}</span>
            {f.id === 'inbox' && unreadCount > 0 && (
              <span style={{
                fontSize: 9,
                fontWeight: 700,
                background: '#6366f1',
                color: 'white',
                borderRadius: 10,
                padding: '1px 5px',
                minWidth: 14,
                textAlign: 'center',
              }}>
                {unreadCount > 99 ? '99+' : unreadCount}
              </span>
            )}
          </button>
        ))}
      </nav>
    </div>
  )
}

// ──────────────────────────────────────────────────────────────────────────────
// Main Mail App
// ──────────────────────────────────────────────────────────────────────────────

export default function MailApp() {
  const [folder, setFolder] = useState('inbox')
  const [messages, setMessages] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [identity, setIdentity] = useState(null)
  // identityChecked: false until the /api/identity/identity fetch resolves.
  // Lets us distinguish "not yet checked" from "checked and no identity".
  const [identityChecked, setIdentityChecked] = useState(false)
  const [selectedThreadIdx, setSelectedThreadIdx] = useState(null)
  const [view, setView] = useState('inbox') // 'inbox' | 'thread' | 'compose'
  const [composeProps, setComposeProps] = useState({})

  // Fetch identity once
  useEffect(() => {
    fetch('/api/identity/identity')
      .then(r => r.ok ? r.json() : null)
      .then(data => { setIdentity(data); setIdentityChecked(true) })
      .catch(() => { setIdentity(null); setIdentityChecked(true) })
  }, [])

  const loadMessages = useCallback(async (f) => {
    setLoading(true)
    setError(null)
    try {
      const params = new URLSearchParams({ folder: f, limit: 200 })
      const res = await fetch(`/api/identity/mailbox?${params}`)
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data = await res.json()
      const msgs = Array.isArray(data) ? data : (data.messages || [])
      setMessages(msgs)
    } catch (err) {
      // Backend not yet wired — use empty state (spec: degrade gracefully)
      setMessages([])
      if (err.message && !err.message.includes('404') && !err.message.includes('fetch')) {
        setError('Could not load messages.')
      }
    }
    setLoading(false)
  }, [])

  useEffect(() => {
    loadMessages(folder)
  }, [folder, loadMessages])

  // Poll for new mail every 30 s
  useEffect(() => {
    const id = setInterval(() => loadMessages(folder), 30000)
    return () => clearInterval(id)
  }, [folder, loadMessages])

  // Build threads from messages
  const allThreads = groupIntoThreads(messages)

  const unreadCount = messages.filter(m => !m.read).length

  // Notify unread count via OS notification API (best-effort)
  useEffect(() => {
    try {
      window.dispatchEvent(new CustomEvent('vulos:badge', {
        detail: { appId: 'mail', count: unreadCount },
      }))
    } catch { /* noop */ }
  }, [unreadCount])

  // Inbox list items are thread reps
  const listMessages = allThreads.map(threadRep)

  const handleSelectMessage = (msg) => {
    // Find thread index
    const idx = allThreads.findIndex(t => t.some(m => m.id === msg.id))
    setSelectedThreadIdx(idx)
    setView('thread')
  }

  const handleMarkRead = async (msgId) => {
    try {
      await fetch(`/api/identity/mailbox/${msgId}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'read' }),
      })
    } catch { /* degrade */ }
    setMessages(prev => prev.map(m => m.id === msgId ? { ...m, read: true } : m))
  }

  const handleMessageAction = (action, msg) => {
    if (action === 'archive' || action === 'delete') {
      setMessages(prev => prev.filter(m => m.id !== msg.id))
      setView('inbox')
      setSelectedThreadIdx(null)
    }
  }

  const handleReply = (msg) => {
    setComposeProps({
      prefillTo: msg.from_address || msg.from || '',
      prefillSubject: `Re: ${msg.subject || ''}`,
      prefillBody: `\n\n— On ${new Date(msg.received_at || msg.date).toLocaleString()}, ${msg.from_address || msg.from} wrote:\n${(msg.body || '').split('\n').map(l => `> ${l}`).join('\n')}`,
    })
    setView('compose')
  }

  const handleCompose = () => {
    setComposeProps({})
    setView('compose')
  }

  const handleSent = () => {
    // Refresh sent folder if active
    if (folder === 'sent') loadMessages('sent')
  }

  const selectedThread = selectedThreadIdx !== null ? allThreads[selectedThreadIdx] : null
  const selectedId = selectedThread ? threadRep(selectedThread).id : null

  // No-identity empty state — rendered after the identity check completes and
  // no identity is configured.  Do NOT auto-redirect; show a friendly CTA
  // instead so local-only users understand what is needed.
  if (identityChecked && !identity?.address) {
    return (
      <div
        data-testid="mail-no-identity"
        style={{
          height: '100%',
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          justifyContent: 'center',
          background: '#0d0d0f',
          color: '#f5f5f5',
          fontFamily: "-apple-system, 'Inter', 'Segoe UI', sans-serif",
          gap: 16,
          padding: 32,
          textAlign: 'center',
        }}
      >
        <span style={{ fontSize: 48 }}>✉</span>
        <div style={{ fontSize: 20, fontWeight: 600 }}>Set up your Vulos Mail</div>
        {identity === null ? (
          // No cloud session at all — local-only install
          <>
            <p style={{ fontSize: 13, color: 'rgba(255,255,255,0.45)', maxWidth: 360, lineHeight: 1.6, margin: 0 }}>
              Local-only install — Vulos Mail requires a Vulos Cloud account.
              Sign up to enable end-to-end encrypted messaging with your Vulos identity.
            </p>
            <a
              href="/settings/cloud"
              data-testid="mail-setup-cta"
              style={{
                marginTop: 8,
                padding: '10px 24px',
                borderRadius: 10,
                fontSize: 13,
                fontWeight: 600,
                cursor: 'pointer',
                background: 'rgba(99,102,241,0.3)',
                border: '1px solid rgba(99,102,241,0.4)',
                color: '#a5b4fc',
                textDecoration: 'none',
                display: 'inline-block',
              }}
            >
              Sign up for Vulos Cloud →
            </a>
          </>
        ) : (
          // Cloud session exists but no identity address claimed yet
          <>
            <p style={{ fontSize: 13, color: 'rgba(255,255,255,0.45)', maxWidth: 360, lineHeight: 1.6, margin: 0 }}>
              You do not have a Vulos Mail identity yet.
              Complete the account setup to claim your <strong>@vulos.org</strong> address.
            </p>
            <a
              href="/settings/identity"
              data-testid="mail-setup-cta"
              style={{
                marginTop: 8,
                padding: '10px 24px',
                borderRadius: 10,
                fontSize: 13,
                fontWeight: 600,
                cursor: 'pointer',
                background: 'rgba(99,102,241,0.3)',
                border: '1px solid rgba(99,102,241,0.4)',
                color: '#a5b4fc',
                textDecoration: 'none',
                display: 'inline-block',
              }}
            >
              Set up mail →
            </a>
          </>
        )}
      </div>
    )
  }

  return (
    <div style={{
      height: '100%',
      display: 'flex',
      flexDirection: 'row',
      background: '#0d0d0f',
      color: '#f5f5f5',
      fontFamily: "-apple-system, 'Inter', 'Segoe UI', sans-serif",
      overflow: 'hidden',
    }}>
      {/* Sidebar */}
      <Sidebar
        folder={folder}
        onFolder={(f) => {
          setFolder(f)
          setView('inbox')
          setSelectedThreadIdx(null)
        }}
        onCompose={handleCompose}
        unreadCount={unreadCount}
        identity={identity}
      />

      {/* Message list */}
      <div style={{
        width: 220,
        flexShrink: 0,
        borderRight: '1px solid rgba(255,255,255,0.06)',
        overflow: 'hidden',
        display: 'flex',
        flexDirection: 'column',
      }}>
        {loading ? (
          <div style={{
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            height: '100%', gap: 8, color: 'rgba(255,255,255,0.25)', fontSize: 12,
          }}>
            <span style={{
              width: 14, height: 14,
              border: '2px solid rgba(255,255,255,0.08)',
              borderTopColor: '#818cf8',
              borderRadius: '50%',
              display: 'inline-block',
              animation: 'spin 0.8s linear infinite',
            }} />
            Loading...
          </div>
        ) : error ? (
          <div style={{
            display: 'flex', flexDirection: 'column', alignItems: 'center',
            justifyContent: 'center', height: '100%', gap: 10,
            color: '#f87171', fontSize: 12, padding: 16, textAlign: 'center',
          }}>
            <span style={{ fontSize: 22 }}>⚠</span>
            {error}
            <button
              onClick={() => loadMessages(folder)}
              style={{
                padding: '5px 14px', borderRadius: 7, fontSize: 11, cursor: 'pointer',
                background: 'rgba(239,68,68,0.12)', border: '1px solid rgba(239,68,68,0.25)',
                color: '#fca5a5',
              }}
            >
              Retry
            </button>
          </div>
        ) : (
          <InboxList
            messages={listMessages}
            selectedId={selectedId}
            onSelect={handleSelectMessage}
            folder={folder}
          />
        )}
      </div>

      {/* Main content pane */}
      <div style={{ flex: 1, overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
        {view === 'compose' ? (
          <ComposeView
            identity={identity}
            onClose={() => setView(selectedThread ? 'thread' : 'inbox')}
            onSent={handleSent}
            {...composeProps}
          />
        ) : view === 'thread' && selectedThread ? (
          <ThreadView
            thread={selectedThread}
            onBack={() => { setView('inbox'); setSelectedThreadIdx(null) }}
            onMarkRead={handleMarkRead}
            onAction={handleMessageAction}
            onReply={handleReply}
          />
        ) : (
          // Empty / welcome state
          <div style={{
            display: 'flex', flexDirection: 'column',
            alignItems: 'center', justifyContent: 'center',
            height: '100%', gap: 12,
            color: 'rgba(255,255,255,0.18)',
          }}>
            <span style={{ fontSize: 40 }}>✉</span>
            <span style={{ fontSize: 14, fontWeight: 500 }}>Vulos Mail</span>
            {identity?.address ? (
              <span style={{ fontSize: 11, color: 'rgba(255,255,255,0.25)' }}>
                {identity.address}
              </span>
            ) : (
              <span style={{ fontSize: 11, color: 'rgba(255,255,255,0.2)' }}>
                No mail identity configured
              </span>
            )}
            <button
              onClick={handleCompose}
              style={{
                marginTop: 8, padding: '8px 20px', borderRadius: 9, fontSize: 12,
                fontWeight: 600, cursor: 'pointer',
                background: 'rgba(99,102,241,0.3)',
                border: '1px solid rgba(99,102,241,0.4)',
                color: '#a5b4fc',
              }}
              onMouseEnter={e => e.currentTarget.style.background = 'rgba(99,102,241,0.45)'}
              onMouseLeave={e => e.currentTarget.style.background = 'rgba(99,102,241,0.3)'}
            >
              + Compose a message
            </button>
          </div>
        )}
      </div>

      <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
    </div>
  )
}
