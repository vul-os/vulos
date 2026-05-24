/**
 * SpacesApp — Vulos Spaces surface: channels, DMs, threads.
 * Routes: /spaces  /spaces/:channelId
 *
 * Channel sidebar (public/private channels + DMs) + ChannelView message pane.
 * Backed by the CRDT message store (OFFICE-60); presence hooks are stubs
 * pending OFFICE-24 being wired in (OFFICE-62 extends).
 *
 * Design pass: rebuilt against `src/components/ui/*` primitives (Sidebar,
 * Input, Modal, Button) and the warm-paper / single-teal-accent tokens —
 * matches the DocsEditor + CommentsPanel revamp.
 */
import { useEffect, useState, useCallback } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import {
  Hash, Lock, AtSign, Plus, Users, Search, ChevronDown, ChevronRight,
} from 'lucide-react'
import ChannelView from './ChannelView.jsx'
import { api } from '../../lib/api.js'
import { usePresence, STATUS_ONLINE } from '../../lib/presence.js'
import { PresenceDot, StatusPicker } from '../../components/PresenceBar.jsx'
import { Button, IconButton, Input, Modal, Sidebar } from '../../components/ui'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function ChannelIcon({ type, size = 14, className = '' }) {
  if (type === 'dm') return <AtSign size={size} className={`text-accent ${className}`} />
  if (type === 'private') return <Lock size={size} className={`text-warning ${className}`} />
  return <Hash size={size} className={`text-ink-faint ${className}`} />
}

// ---------------------------------------------------------------------------
// CreateChannelModal
// ---------------------------------------------------------------------------

function CreateChannelModal({ open, onClose, onCreated }) {
  const [name, setName] = useState('')
  const [type, setType] = useState('public')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  async function submit(e) {
    e.preventDefault()
    const n = name.trim().toLowerCase().replace(/\s+/g, '-')
    if (!n) return
    setLoading(true)
    setError(null)
    try {
      const ch = await api.spacesCreateChannel(n, type)
      onCreated(ch)
      onClose()
    } catch (err) {
      setError(err.message || 'Create failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="Create a channel">
      <form onSubmit={submit}>
        <Modal.Body className="space-y-4">
          {error && (
            <p className="text-xs text-danger bg-danger-bg rounded-sm px-3 py-2">{error}</p>
          )}
          <Input
            label="Name"
            placeholder="e.g. team-design"
            value={name}
            onChange={(e) => setName(e.target.value)}
            leading={<Hash size={13} />}
            autoFocus
          />
          <div>
            <label className="block text-xs text-ink-muted font-medium mb-1.5 tracking-tightish">
              Type
            </label>
            <div className="flex gap-2">
              {[
                { v: 'public',  label: 'Public',  hint: 'Anyone can join' },
                { v: 'private', label: 'Private', hint: 'Invite only' },
              ].map((o) => (
                <button
                  key={o.v}
                  type="button"
                  onClick={() => setType(o.v)}
                  className={[
                    'flex-1 text-left rounded-md border px-3 py-2 transition-colors duration-fast ease-out',
                    type === o.v
                      ? 'border-accent bg-accent-tint text-ink'
                      : 'border-line hover:border-line-strong text-ink-muted',
                  ].join(' ')}
                >
                  <div className="text-sm font-medium tracking-tightish">{o.label}</div>
                  <div className="text-2xs text-ink-faint">{o.hint}</div>
                </button>
              ))}
            </div>
          </div>
        </Modal.Body>
        <Modal.Footer>
          <Button variant="secondary" onClick={onClose} type="button">Cancel</Button>
          <Button variant="primary" type="submit" disabled={loading || !name.trim()}>
            {loading ? 'Creating…' : 'Create channel'}
          </Button>
        </Modal.Footer>
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// NewDMModal
// ---------------------------------------------------------------------------

function NewDMModal({ open, onClose, onCreated }) {
  const [recipient, setRecipient] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  async function submit(e) {
    e.preventDefault()
    const r = recipient.trim()
    if (!r) return
    setLoading(true)
    setError(null)
    try {
      const dmName = ['me', r].sort().join('-')
      const ch = await api.spacesCreateChannel(dmName, 'dm', ['me', r])
      onCreated(ch)
      onClose()
    } catch (err) {
      setError(err.message || 'Create failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="New direct message">
      <form onSubmit={submit}>
        <Modal.Body className="space-y-4">
          {error && (
            <p className="text-xs text-danger bg-danger-bg rounded-sm px-3 py-2">{error}</p>
          )}
          <Input
            label="To"
            placeholder="account id, e.g. alice"
            value={recipient}
            onChange={(e) => setRecipient(e.target.value)}
            leading={<AtSign size={13} />}
            autoFocus
          />
        </Modal.Body>
        <Modal.Footer>
          <Button variant="secondary" onClick={onClose} type="button">Cancel</Button>
          <Button variant="primary" type="submit" disabled={loading || !recipient.trim()}>
            {loading ? 'Opening…' : 'Open DM'}
          </Button>
        </Modal.Footer>
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// Sidebar
// ---------------------------------------------------------------------------

function SpacesSidebar({
  channels, activeId, onSelect, onRefresh,
  roster, localStatus, localStatusText, onSetStatus,
}) {
  const [showCreateChannel, setShowCreateChannel] = useState(false)
  const [showNewDM, setShowNewDM] = useState(false)
  const [channelsOpen, setChannelsOpen] = useState(true)
  const [dmsOpen, setDmsOpen] = useState(true)
  const [search, setSearch] = useState('')
  const [showStatusPicker, setShowStatusPicker] = useState(false)

  const publicChannels = channels.filter((c) => c.type !== 'dm')
  const dms = channels.filter((c) => c.type === 'dm')

  const filtered = (list) =>
    search ? list.filter((c) => c.name.toLowerCase().includes(search.toLowerCase())) : list

  function SectionToggle({ label, open, onToggle, onAdd, addTitle }) {
    return (
      <div className="flex items-center justify-between pl-2 pr-1 pt-1 pb-1 group">
        <button
          onClick={onToggle}
          className="flex items-center gap-1 text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow hover:text-ink-muted transition-colors"
        >
          {open ? <ChevronDown size={11} /> : <ChevronRight size={11} />}
          {label}
        </button>
        <button
          onClick={onAdd}
          title={addTitle}
          aria-label={addTitle}
          className="opacity-0 group-hover:opacity-100 rounded-xs p-0.5 text-ink-faint hover:text-ink hover:bg-accent-tint transition-[opacity,background,color] duration-fast"
        >
          <Plus size={12} />
        </button>
      </div>
    )
  }

  function ChannelRow({ channel }) {
    const isActive = channel.id === activeId
    const dmPeer = channel.type === 'dm'
      ? roster.find((p) => !p.isSelf && channel.name.includes(p.displayName || p.accountId))
      : null
    return (
      <button
        type="button"
        onClick={() => onSelect(channel)}
        className={[
          'relative flex items-center gap-2 h-7 pl-3 pr-2 rounded-md text-left',
          'transition-colors duration-fast ease-out',
          isActive
            ? 'bg-paper text-ink shadow-e1'
            : 'text-ink-muted hover:bg-accent-tint hover:text-ink',
        ].join(' ')}
      >
        <span
          aria-hidden
          className={[
            'absolute left-0 top-1 bottom-1 w-[2px] rounded-r-full',
            isActive ? 'bg-accent' : 'bg-transparent',
          ].join(' ')}
        />
        <span className="relative flex-shrink-0">
          <ChannelIcon type={channel.type} />
          {dmPeer && (
            <span className="absolute -bottom-0.5 -right-0.5">
              <PresenceDot status={dmPeer.status} size={6} />
            </span>
          )}
        </span>
        <span className="truncate text-sm tracking-tightish">{channel.name}</span>
      </button>
    )
  }

  const peersOnline = roster.filter((p) => !p.isSelf)

  return (
    <Sidebar collapsed={false} className="w-60">
      {/* Search */}
      <div className="px-2 pt-3 pb-2 border-b border-line">
        <Input
          size="sm"
          placeholder="Find channel…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          leading={<Search size={12} />}
        />
      </div>

      {/* Channel list */}
      <div className="flex-1 overflow-y-auto py-2 px-1.5 space-y-0.5">
        <SectionToggle
          label="Channels"
          open={channelsOpen}
          onToggle={() => setChannelsOpen(!channelsOpen)}
          onAdd={() => setShowCreateChannel(true)}
          addTitle="Create channel"
        />
        {channelsOpen && filtered(publicChannels).map((ch) => (
          <ChannelRow key={ch.id} channel={ch} />
        ))}

        <div className="mt-3" />
        <SectionToggle
          label="Direct Messages"
          open={dmsOpen}
          onToggle={() => setDmsOpen(!dmsOpen)}
          onAdd={() => setShowNewDM(true)}
          addTitle="New direct message"
        />
        {dmsOpen && filtered(dms).map((ch) => (
          <ChannelRow key={ch.id} channel={ch} />
        ))}

        {filtered(publicChannels).length === 0 &&
          filtered(dms).length === 0 &&
          search && (
            <p className="text-2xs text-ink-faint px-3 py-2 font-serif italic">
              No channels found.
            </p>
        )}
      </div>

      {/* Presence footer */}
      <Sidebar.Footer>
        {peersOnline.length > 0 && (
          <div className="flex items-center gap-1.5 px-2 py-1 text-2xs text-ink-faint">
            <Users size={11} />
            <span className="font-medium text-ink-muted">
              {peersOnline.length} online
            </span>
            <div className="flex flex-wrap gap-1 ml-1">
              {peersOnline.slice(0, 5).map((p) => (
                <span
                  key={p.accountId}
                  className="flex items-center gap-1 bg-bg-elev2 border border-line rounded-pill px-1.5 py-0.5 text-ink-muted"
                  title={p.statusText ? `${p.displayName} — ${p.statusText}` : p.displayName}
                >
                  <PresenceDot status={p.status} size={6} />
                  <span className="truncate max-w-[60px]">{p.displayName}</span>
                </span>
              ))}
            </div>
          </div>
        )}

        <div className="relative">
          <button
            type="button"
            onClick={() => setShowStatusPicker((v) => !v)}
            className="w-full flex items-center gap-2 h-8 px-3 rounded-md text-ink-muted hover:bg-accent-tint hover:text-ink transition-colors duration-fast ease-out"
          >
            <PresenceDot status={localStatus} size={8} />
            <span className="text-xs truncate tracking-tightish">
              {localStatusText || localStatus || STATUS_ONLINE}
            </span>
          </button>
          {showStatusPicker && (
            <StatusPicker
              currentStatus={localStatus}
              currentText={localStatusText}
              onStatusChange={onSetStatus}
              onClose={() => setShowStatusPicker(false)}
            />
          )}
        </div>
      </Sidebar.Footer>

      <CreateChannelModal
        open={showCreateChannel}
        onClose={() => setShowCreateChannel(false)}
        onCreated={(ch) => { onRefresh(); onSelect(ch) }}
      />
      <NewDMModal
        open={showNewDM}
        onClose={() => setShowNewDM(false)}
        onCreated={(ch) => { onRefresh(); onSelect(ch) }}
      />
    </Sidebar>
  )
}

// ---------------------------------------------------------------------------
// SpacesApp — root component
// ---------------------------------------------------------------------------

export default function SpacesApp() {
  const { channelId } = useParams()
  const navigate = useNavigate()
  const [channels, setChannels] = useState([])
  const [activeChannel, setActiveChannel] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)

  // OFFICE-62: presence — fabric is null until OFFICE-20 is wired.
  const { roster, manager: presenceManager } = usePresence({ fabric: null })
  const [localStatus, setLocalStatus] = useState(STATUS_ONLINE)
  const [localStatusText, setLocalStatusText] = useState('')

  function handleSetStatus(status, text) {
    setLocalStatus(status)
    setLocalStatusText(text)
    if (presenceManager) presenceManager.setStatus(status, text)
  }

  const currentUser = 'me'

  const loadChannels = useCallback(async () => {
    try {
      const chs = await api.spacesListChannels()
      setChannels(chs || [])
      return chs || []
    } catch (e) {
      setError(e.message || 'Failed to load channels')
      return []
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    loadChannels().then((chs) => {
      if (channelId) {
        const found = chs.find((c) => c.id === channelId)
        if (found) setActiveChannel(found)
      } else if (chs.length > 0) {
        setActiveChannel(chs[0])
        navigate(`/spaces/${chs[0].id}`, { replace: true })
      }
    })
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  function selectChannel(ch) {
    setActiveChannel(ch)
    navigate(`/spaces/${ch.id}`)
  }

  if (loading) {
    return (
      <div className="flex-1 flex items-center justify-center bg-bg">
        <div className="w-5 h-5 border-2 border-accent border-t-transparent rounded-full animate-spin" />
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex-1 flex items-center justify-center text-danger text-sm bg-bg">
        {error}
      </div>
    )
  }

  return (
    <div className="flex flex-1 min-h-0 bg-bg">
      <SpacesSidebar
        channels={channels}
        activeId={activeChannel?.id}
        onSelect={selectChannel}
        onRefresh={loadChannels}
        roster={roster}
        localStatus={localStatus}
        localStatusText={localStatusText}
        onSetStatus={handleSetStatus}
      />
      <ChannelView
        channel={activeChannel}
        currentUser={currentUser}
        roster={roster}
      />
    </div>
  )
}
