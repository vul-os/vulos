import { useState, useEffect, useCallback, useRef } from 'react'
import QRCanvas from './QRCanvas.jsx'
import ContactCard from './ContactCard.jsx'
import RequestQueue from './RequestQueue.jsx'

// ---------------------------------------------------------------------------
// API helpers
// ---------------------------------------------------------------------------
async function apiFetch(url, options = {}) {
  const res = await fetch(url, {
    headers: { 'Content-Type': 'application/json', ...options.headers },
    ...options,
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(`${res.status} ${text || res.statusText}`)
  }
  return res.json()
}

// ---------------------------------------------------------------------------
// Tab nav
// ---------------------------------------------------------------------------
const TABS = [
  { id: 'identity', label: 'Identity' },
  { id: 'contacts', label: 'Contacts' },
  { id: 'requests', label: 'Requests' },
]

// ---------------------------------------------------------------------------
// Identity panel
// ---------------------------------------------------------------------------
function IdentityPanel({ identity, onRefresh }) {
  const [copied, setCopied] = useState(false)
  const [showQR, setShowQR] = useState(false)

  const vulaId = identity?.vula_id || ''
  const displayName = identity?.display_name || 'You'
  const verified = identity?.verified || false

  const copyId = async () => {
    if (!vulaId) return
    try {
      await navigator.clipboard.writeText(vulaId)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // fallback: select text
    }
  }

  return (
    <div className="space-y-6">
      {/* Identity card */}
      <div className="bg-neutral-900/60 border border-neutral-800/60 rounded-2xl p-5">
        <div className="flex items-start gap-4">
          {/* Avatar block */}
          <div className="w-14 h-14 rounded-full bg-gradient-to-br from-blue-600/40 to-violet-600/40 border border-blue-500/20 flex items-center justify-center text-xl font-bold text-white shrink-0">
            {displayName[0]?.toUpperCase() || 'V'}
          </div>

          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 mb-0.5">
              <h2 className="text-base font-semibold text-neutral-100">{displayName}</h2>
              {verified && (
                <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-blue-600/20 border border-blue-500/30 text-blue-400">
                  Verified
                </span>
              )}
            </div>
            {identity?.slug && (
              <p className="text-xs text-neutral-500 mb-2">{identity.slug}</p>
            )}
            <p className="text-[11px] text-neutral-600 font-mono break-all leading-relaxed">{vulaId || 'Loading…'}</p>
          </div>
        </div>

        {/* Actions */}
        <div className="flex gap-2 mt-4 pt-4 border-t border-neutral-800/40">
          <button
            onClick={copyId}
            className="flex-1 py-2 rounded-lg text-sm text-center border transition-colors
              bg-neutral-800/60 border-neutral-700/40 text-neutral-300 hover:bg-neutral-700/60 hover:text-white"
          >
            {copied ? 'Copied!' : 'Copy Vula ID'}
          </button>
          <button
            onClick={() => setShowQR(v => !v)}
            className={`flex-1 py-2 rounded-lg text-sm text-center border transition-colors
              ${showQR
                ? 'bg-blue-600/20 border-blue-500/40 text-blue-300'
                : 'bg-neutral-800/60 border-neutral-700/40 text-neutral-300 hover:bg-neutral-700/60 hover:text-white'}`}
          >
            {showQR ? 'Hide QR' : 'Show QR'}
          </button>
        </div>
      </div>

      {/* QR code */}
      {showQR && vulaId && (
        <div className="flex flex-col items-center gap-3 bg-neutral-900/60 border border-neutral-800/60 rounded-2xl p-6">
          <p className="text-xs text-neutral-500 mb-2">Scan to add this Vula instance</p>
          <div className="rounded-xl overflow-hidden p-3 bg-neutral-900 border border-neutral-800">
            <QRCanvas value={vulaId} size={220} darkColor="#ffffff" lightColor="#111827" />
          </div>
          <p className="text-[10px] text-neutral-700 text-center max-w-xs break-all font-mono">{vulaId}</p>
        </div>
      )}

      {/* Trust model legend */}
      <div className="bg-neutral-900/40 border border-neutral-800/40 rounded-xl p-4">
        <h3 className="text-xs font-medium text-neutral-400 mb-3 uppercase tracking-wider">Trust Model</h3>
        <div className="flex items-center gap-2 text-xs text-neutral-500">
          <span className="text-neutral-600">Unknown</span>
          <span className="text-neutral-700">──►</span>
          <span className="text-amber-400">Pending</span>
          <span className="text-neutral-700">──►</span>
          <span className="text-emerald-400">Approved</span>
          <span className="text-neutral-700">──►</span>
          <span className="text-red-400">Blocked</span>
        </div>
        <p className="text-[11px] text-neutral-700 mt-2">
          No open inboxes. You don't receive anything from anyone until you approve them.
        </p>
      </div>

      <button
        onClick={onRefresh}
        className="text-xs text-neutral-600 hover:text-neutral-400 transition-colors"
      >
        Refresh identity
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Contacts panel
// ---------------------------------------------------------------------------
function ContactsPanel({ contacts, onUpdatePerms, onRemove, loading }) {
  const [search, setSearch] = useState('')

  const filtered = (contacts || []).filter(c => {
    const q = search.toLowerCase()
    return (
      !q ||
      (c.display_name || '').toLowerCase().includes(q) ||
      (c.vula_id || '').toLowerCase().includes(q)
    )
  })

  return (
    <div className="space-y-4">
      {/* Search */}
      <input
        value={search}
        onChange={e => setSearch(e.target.value)}
        placeholder="Search contacts…"
        className="w-full bg-neutral-900/60 border border-neutral-800/60 rounded-xl px-4 py-2.5 text-sm
          text-neutral-200 placeholder-neutral-600 focus:outline-none focus:border-neutral-600/60"
      />

      {/* Count */}
      <div className="flex items-center justify-between">
        <p className="text-xs text-neutral-600">
          {filtered.length} {filtered.length === 1 ? 'contact' : 'contacts'}
          {search && ` matching "${search}"`}
        </p>
      </div>

      {/* List */}
      {loading ? (
        <div className="flex justify-center py-10">
          <span className="w-5 h-5 border-2 border-neutral-700 border-t-blue-500 rounded-full animate-spin" />
        </div>
      ) : filtered.length === 0 ? (
        <div className="text-center py-10 text-neutral-600 text-sm">
          {contacts?.length === 0
            ? 'No approved contacts yet. Approve a request to add someone.'
            : `No contacts match "${search}"`}
        </div>
      ) : (
        <div className="space-y-2">
          {filtered.map(c => (
            <ContactCard
              key={c.vula_id}
              contact={c}
              onUpdatePerms={onUpdatePerms}
              onRemove={onRemove}
            />
          ))}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Send request panel (sub-section of requests tab)
// ---------------------------------------------------------------------------
function SendRequestForm({ onSent }) {
  const [targetId, setTargetId] = useState('')
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(false)

  const send = async (e) => {
    e.preventDefault()
    if (!targetId.trim()) return
    setBusy(true)
    setError(null)
    setSuccess(false)
    try {
      await apiFetch('/api/peering/contacts/request', {
        method: 'POST',
        body: JSON.stringify({ vula_id: targetId.trim(), message: message.trim() || undefined }),
      })
      setSuccess(true)
      setTargetId('')
      setMessage('')
      onSent?.()
      setTimeout(() => setSuccess(false), 3000)
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={send} className="bg-neutral-900/60 border border-neutral-800/60 rounded-2xl p-4 space-y-3">
      <h3 className="text-sm font-medium text-neutral-300">Send Contact Request</h3>
      <input
        value={targetId}
        onChange={e => setTargetId(e.target.value)}
        placeholder="vula:ed25519:… or alice.vulos.org"
        className="w-full bg-neutral-900 border border-neutral-800 rounded-lg px-3 py-2 text-sm
          text-neutral-200 placeholder-neutral-600 focus:outline-none focus:border-neutral-600 font-mono"
        disabled={busy}
      />
      <input
        value={message}
        onChange={e => setMessage(e.target.value)}
        placeholder="Optional message…"
        maxLength={120}
        className="w-full bg-neutral-900 border border-neutral-800 rounded-lg px-3 py-2 text-sm
          text-neutral-200 placeholder-neutral-600 focus:outline-none focus:border-neutral-600"
        disabled={busy}
      />
      {error && <p className="text-xs text-red-400">{error}</p>}
      {success && <p className="text-xs text-emerald-400">Request sent!</p>}
      <button
        type="submit"
        disabled={busy || !targetId.trim()}
        className="w-full py-2 rounded-lg text-sm font-medium transition-colors
          bg-blue-600/20 border border-blue-500/30 text-blue-300
          hover:bg-blue-600/30 disabled:opacity-40 disabled:cursor-not-allowed"
      >
        {busy ? 'Sending…' : 'Send Request'}
      </button>
    </form>
  )
}

// ---------------------------------------------------------------------------
// Requests panel
// ---------------------------------------------------------------------------
function RequestsPanel({ requests, onApprove, onBlock, loading, onSent }) {
  return (
    <div className="space-y-5">
      <SendRequestForm onSent={onSent} />

      <div>
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-sm font-medium text-neutral-300">Incoming Requests</h3>
          {requests?.length > 0 && (
            <span className="text-xs px-2 py-0.5 rounded-full bg-amber-500/15 border border-amber-500/30 text-amber-400">
              {requests.length}
            </span>
          )}
        </div>
        {loading ? (
          <div className="flex justify-center py-8">
            <span className="w-5 h-5 border-2 border-neutral-700 border-t-blue-500 rounded-full animate-spin" />
          </div>
        ) : (
          <RequestQueue
            requests={requests}
            onApprove={onApprove}
            onBlock={onBlock}
          />
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Root component
// ---------------------------------------------------------------------------
export default function Peering() {
  const [tab, setTab] = useState('identity')
  const [identity, setIdentity] = useState(null)
  const [contacts, setContacts] = useState([])
  const [requests, setRequests] = useState([])
  const [loadingId, setLoadingId] = useState(true)
  const [loadingContacts, setLoadingContacts] = useState(true)
  const [loadingRequests, setLoadingRequests] = useState(true)
  const [error, setError] = useState(null)
  const pollRef = useRef(null)

  // Fetch identity
  const fetchIdentity = useCallback(async () => {
    setLoadingId(true)
    setError(null)
    try {
      const data = await apiFetch('/api/peering/identity')
      setIdentity(data)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoadingId(false)
    }
  }, [])

  // Fetch approved contacts
  const fetchContacts = useCallback(async () => {
    setLoadingContacts(true)
    try {
      const data = await apiFetch('/api/peering/contacts')
      setContacts(Array.isArray(data?.contacts) ? data.contacts : Array.isArray(data) ? data : [])
    } catch {
      setContacts([])
    } finally {
      setLoadingContacts(false)
    }
  }, [])

  // Fetch pending requests
  const fetchRequests = useCallback(async () => {
    setLoadingRequests(true)
    try {
      const data = await apiFetch('/api/peering/contacts/requests')
      setRequests(Array.isArray(data?.requests) ? data.requests : Array.isArray(data) ? data : [])
    } catch {
      setRequests([])
    } finally {
      setLoadingRequests(false)
    }
  }, [])

  // Initial load
  useEffect(() => {
    fetchIdentity()
    fetchContacts()
    fetchRequests()
  }, [fetchIdentity, fetchContacts, fetchRequests])

  // Poll requests every 30s for live badge updates
  useEffect(() => {
    pollRef.current = setInterval(() => {
      fetchRequests()
    }, 30_000)
    return () => clearInterval(pollRef.current)
  }, [fetchRequests])

  // Approve a request
  const handleApprove = useCallback(async (id) => {
    await apiFetch(`/api/peering/contacts/approve/${id}`, { method: 'POST' })
    setRequests(prev => prev.filter(r => r.id !== id))
    await fetchContacts()
  }, [fetchContacts])

  // Block a request
  const handleBlock = useCallback(async (id) => {
    await apiFetch(`/api/peering/contacts/block/${id}`, { method: 'POST' })
    setRequests(prev => prev.filter(r => r.id !== id))
  }, [])

  // Update contact permissions
  const handleUpdatePerms = useCallback(async (vulaId, permissions) => {
    const enc = encodeURIComponent(vulaId)
    await apiFetch(`/api/peering/contacts/${enc}/permissions`, {
      method: 'PUT',
      body: JSON.stringify({ permissions }),
    })
    setContacts(prev => prev.map(c =>
      c.vula_id === vulaId ? { ...c, permissions } : c
    ))
  }, [])

  // Remove a contact
  const handleRemove = useCallback(async (vulaId) => {
    if (!window.confirm('Remove this contact?')) return
    const enc = encodeURIComponent(vulaId)
    await apiFetch(`/api/peering/contacts/${enc}`, { method: 'DELETE' })
    setContacts(prev => prev.filter(c => c.vula_id !== vulaId))
  }, [])

  const pendingCount = requests.length

  return (
    <div className="flex flex-col h-full bg-neutral-950 text-neutral-200 overflow-hidden">
      {/* Header */}
      <div className="shrink-0 px-6 pt-5 pb-0 border-b border-neutral-800/50">
        <div className="flex items-center justify-between mb-4">
          <div>
            <h1 className="text-lg font-semibold tracking-tight">Peering</h1>
            <p className="text-xs text-neutral-600 mt-0.5">Direct Vula-to-Vula connections</p>
          </div>
          {error && (
            <div className="text-xs text-amber-400 bg-amber-500/10 border border-amber-500/20 rounded-lg px-3 py-1.5 max-w-xs truncate">
              {error}
            </div>
          )}
        </div>

        {/* Tabs */}
        <div className="flex gap-1">
          {TABS.map(t => (
            <button
              key={t.id}
              onClick={() => setTab(t.id)}
              className={`relative px-4 py-2.5 text-sm font-medium transition-colors rounded-t-lg
                ${tab === t.id
                  ? 'text-white bg-neutral-800/50 border-t border-l border-r border-neutral-700/50'
                  : 'text-neutral-500 hover:text-neutral-300'}`}
            >
              {t.label}
              {t.id === 'requests' && pendingCount > 0 && (
                <span className="absolute -top-1 -right-1 w-4 h-4 rounded-full bg-amber-500 text-black text-[9px] font-bold flex items-center justify-center">
                  {pendingCount > 9 ? '9+' : pendingCount}
                </span>
              )}
            </button>
          ))}
        </div>
      </div>

      {/* Body */}
      <div className="flex-1 overflow-y-auto px-6 py-5">
        {tab === 'identity' && (
          loadingId && !identity ? (
            <div className="flex justify-center pt-16">
              <span className="w-6 h-6 border-2 border-neutral-700 border-t-blue-500 rounded-full animate-spin" />
            </div>
          ) : (
            <IdentityPanel identity={identity} onRefresh={fetchIdentity} />
          )
        )}

        {tab === 'contacts' && (
          <ContactsPanel
            contacts={contacts}
            onUpdatePerms={handleUpdatePerms}
            onRemove={handleRemove}
            loading={loadingContacts}
          />
        )}

        {tab === 'requests' && (
          <RequestsPanel
            requests={requests}
            onApprove={handleApprove}
            onBlock={handleBlock}
            loading={loadingRequests}
            onSent={fetchRequests}
          />
        )}
      </div>
    </div>
  )
}
