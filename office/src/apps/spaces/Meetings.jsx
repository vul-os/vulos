// Meetings.jsx — OFFICE-MEET: Google Meet-parity meetings dashboard.
//
// Shows scheduled/active/ended meeting rooms, lets the host create new ones,
// and provides a copy-to-clipboard join link per meeting.
// "New meeting" modal: title, date/time, duration, invitees (autocomplete),
// lobby on/off, recording stub on/off, require sign-in toggle.
// Clicking "Join" navigates to /room/<sessionId> which renders Room.jsx.
//
// Design pass: warm paper dashboard, Cards for each meeting, Modal for create,
// Tooltip-driven "copied" confirmation, sage/honey/dusty-navy status pills.

import { useEffect, useState, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  Calendar, Clock, Copy, Plus, Trash2, Video, Users, Check,
  Lock, ShieldCheck, Circle,
} from 'lucide-react'
import { Button, Card, IconButton, Input, Modal, Topbar } from '../../components/ui'

const API = '/api/meetings'

async function apiFetch(path, opts = {}) {
  const headers = { 'Content-Type': 'application/json', ...(opts.headers || {}) }
  const res = await fetch(path, { ...opts, headers, credentials: 'include' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error || `HTTP ${res.status}`)
  }
  return res.json()
}

function formatDt(isoStr) {
  if (!isoStr) return null
  const d = new Date(isoStr)
  return d.toLocaleString(undefined, {
    month: 'short', day: 'numeric', year: 'numeric',
    hour: '2-digit', minute: '2-digit',
  })
}

// Warm-signal status pill (sage / honey / dusty navy / persimmon).
function StatusPill({ status }) {
  const map = {
    active:    { label: 'Active',    bg: 'var(--signal-success-bg)', fg: 'var(--signal-success)' },
    scheduled: { label: 'Scheduled', bg: 'var(--signal-warning-bg)', fg: 'var(--signal-warning)' },
    ended:     { label: 'Ended',     bg: 'var(--bg-elev-2)',         fg: 'var(--ink-faint)'      },
    cancelled: { label: 'Cancelled', bg: 'var(--signal-error-bg)',   fg: 'var(--signal-error)'   },
  }
  const tone = map[status] || map.scheduled
  return (
    <span
      className="inline-flex items-center px-2 py-0.5 rounded-pill text-2xs font-medium uppercase tracking-eyebrow"
      style={{ background: tone.bg, color: tone.fg }}
    >
      {tone.label}
    </span>
  )
}

export default function Meetings() {
  const [meetings, setMeetings] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [creating, setCreating] = useState(false)
  const [copied, setCopied] = useState(null)
  const navigate = useNavigate()

  const load = useCallback(async () => {
    try {
      setLoading(true)
      const data = await apiFetch(API)
      setMeetings(Array.isArray(data) ? data : [])
    } catch (e) {
      setError(e.message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { load() }, [load])

  const handleDelete = useCallback(async (id) => {
    if (!window.confirm('Delete this meeting room?')) return
    try {
      await apiFetch(`${API}/${id}`, { method: 'DELETE' })
      setMeetings((m) => m.filter((x) => x.id !== id))
    } catch (e) {
      alert(`Delete failed: ${e.message}`)
    }
  }, [])

  const handleJoin = useCallback((m) => {
    navigate(`/room/${encodeURIComponent(m.session_id)}`)
  }, [navigate])

  const handleCopyLink = useCallback(async (m) => {
    const url = `${window.location.origin}/room/${encodeURIComponent(m.session_id)}`
    try {
      await navigator.clipboard.writeText(url)
      setCopied(m.id)
      setTimeout(() => setCopied(null), 2000)
    } catch {
      prompt('Copy this link:', url)
    }
  }, [])

  const handleCreated = useCallback((m) => {
    setMeetings((prev) => [m, ...prev])
    setCreating(false)
  }, [])

  return (
    <div className="flex flex-col h-full bg-bg">
      {/* Topbar — Vulos Office pattern */}
      <Topbar
        title={
          <h1 className="inline-flex items-center gap-2 text-md font-semibold text-ink tracking-tightish">
            <Video size={16} className="text-accent" />
            Meet
          </h1>
        }
        actions={
          <Button variant="primary" onClick={() => setCreating(true)}>
            <Plus size={14} />
            New meeting
          </Button>
        }
      />

      <CreateModal
        open={creating}
        onCreated={handleCreated}
        onClose={() => setCreating(false)}
      />

      <div className="flex-1 overflow-y-auto px-6 py-6">
        {loading && (
          <div className="flex items-center justify-center h-32 text-ink-faint text-sm font-serif italic">
            Loading…
          </div>
        )}
        {error && !loading && (
          <p className="text-danger text-sm bg-danger-bg border border-line rounded-md px-3 py-2">
            {error}
          </p>
        )}
        {!loading && !error && meetings.length === 0 && (
          <div className="flex flex-col items-center justify-center h-64 gap-3">
            <Video size={36} className="text-ink-faint opacity-50" />
            <p className="text-sm text-ink-muted font-serif italic">
              No meetings yet. Create one to get started.
            </p>
            <Button variant="primary" onClick={() => setCreating(true)}>
              <Plus size={14} />
              New meeting
            </Button>
          </div>
        )}
        {!loading && meetings.length > 0 && (
          <ul className="grid grid-cols-1 lg:grid-cols-2 gap-3 max-w-5xl">
            {meetings.map((m) => (
              <MeetingCard
                key={m.id}
                meeting={m}
                copied={copied === m.id}
                onJoin={handleJoin}
                onCopyLink={handleCopyLink}
                onDelete={handleDelete}
              />
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}

function MeetingCard({ meeting: m, copied, onJoin, onCopyLink, onDelete }) {
  return (
    <li className="list-none">
      <Card className="transition-colors duration-fast ease-out hover:border-line-strong">
        <Card.Body className="space-y-3">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0 flex-1">
              <h2 className="text-md font-semibold text-ink tracking-tightish truncate">
                {m.title}
              </h2>
              <div className="mt-1 flex items-center gap-2 flex-wrap">
                <StatusPill status={m.status} />
                {m.scheduled_at && (
                  <span className="inline-flex items-center gap-1 text-2xs text-ink-muted tracking-tightish">
                    <Calendar size={11} className="text-ink-faint" />
                    {formatDt(m.scheduled_at)}
                  </span>
                )}
                {m.duration_min > 0 && (
                  <span className="inline-flex items-center gap-1 text-2xs text-ink-muted tracking-tightish">
                    <Clock size={11} className="text-ink-faint" />
                    {m.duration_min} min
                  </span>
                )}
              </div>
            </div>

            <div className="flex items-center gap-1 flex-shrink-0">
              {/* Tooltip-driven copy confirmation */}
              <span className="relative inline-flex">
                <IconButton
                  size="sm"
                  title="Copy join link"
                  onClick={() => onCopyLink(m)}
                  className={copied ? 'text-success' : ''}
                >
                  {copied ? <Check size={14} /> : <Copy size={14} />}
                </IconButton>
                {copied && (
                  <span
                    role="status"
                    className="pointer-events-none absolute bottom-full mb-1.5 left-1/2 -translate-x-1/2 whitespace-nowrap px-2 py-1 rounded-sm text-2xs font-medium tracking-tightish bg-ink text-paper shadow-e2 animate-fade-in"
                  >
                    Link copied
                  </span>
                )}
              </span>
              <IconButton
                size="sm"
                title="Delete room"
                onClick={() => onDelete(m.id)}
                className="hover:bg-danger-bg hover:text-danger"
              >
                <Trash2 size={14} />
              </IconButton>
            </div>
          </div>

          {(m.host_vumail ||
            (Array.isArray(m.invitees) && m.invitees.length > 0)) && (
            <div className="flex flex-wrap items-center gap-2 text-2xs">
              {m.host_vumail && (
                <span className="text-ink-muted">
                  <span className="text-ink-faint uppercase tracking-eyebrow mr-1">Host</span>
                  {m.host_vumail}
                </span>
              )}
              {Array.isArray(m.invitees) && m.invitees.length > 0 && (
                <span className="inline-flex items-center gap-1 text-ink-muted">
                  <Users size={11} className="text-ink-faint" />
                  {m.invitees.length} invitee{m.invitees.length !== 1 ? 's' : ''}
                </span>
              )}
            </div>
          )}

          {Array.isArray(m.invitees) && m.invitees.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {m.invitees.slice(0, 6).map((inv) => (
                <span
                  key={inv}
                  className="inline-flex items-center bg-accent-tint border border-line rounded-pill px-2 py-0.5 text-2xs text-ink-muted tracking-tightish"
                >
                  {inv}
                </span>
              ))}
              {m.invitees.length > 6 && (
                <span className="text-2xs text-ink-faint self-center">
                  +{m.invitees.length - 6}
                </span>
              )}
            </div>
          )}
        </Card.Body>
        <Card.Footer className="justify-end">
          <Button variant="primary" onClick={() => onJoin(m)}>
            <Video size={13} />
            Join
          </Button>
        </Card.Footer>
      </Card>
    </li>
  )
}

function ToggleRow({ label, hint, checked, onChange, icon: Icon }) {
  return (
    <label className="flex items-start gap-3 cursor-pointer group">
      <div className="mt-0.5 shrink-0">
        <div
          className={[
            'w-9 h-5 rounded-pill relative transition-colors duration-fast',
            checked ? 'bg-accent' : 'bg-line-strong',
          ].join(' ')}
          onClick={onChange}
          role="switch"
          aria-checked={checked}
          tabIndex={0}
          onKeyDown={(e) => (e.key === ' ' || e.key === 'Enter') && onChange()}
        >
          <span
            className={[
              'absolute top-0.5 w-4 h-4 rounded-pill bg-white shadow transition-transform duration-fast',
              checked ? 'translate-x-4' : 'translate-x-0.5',
            ].join(' ')}
          />
        </div>
      </div>
      <div className="min-w-0">
        <span className="flex items-center gap-1 text-sm text-ink font-medium tracking-tightish">
          {Icon && <Icon size={13} className="text-ink-faint" />}
          {label}
        </span>
        {hint && <p className="text-2xs text-ink-faint mt-0.5">{hint}</p>}
      </div>
    </label>
  )
}

function CreateModal({ open, onCreated, onClose }) {
  const [form, setForm] = useState({
    title: '',
    host_vumail: '',
    invitees_raw: '',
    scheduled_at: '',
    duration_min: 60,
    lobby_required: true,
    signin_required: false,
    recording_enabled: false,
  })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState(null)

  const update = (k, v) => setForm((f) => ({ ...f, [k]: v }))

  const handleSubmit = async (e) => {
    e.preventDefault()
    if (!form.title.trim()) return
    setBusy(true)
    setErr(null)
    try {
      const body = {
        title: form.title.trim(),
        host_vumail: form.host_vumail.trim() || undefined,
        invitees: form.invitees_raw
          .split(',')
          .map((s) => s.trim())
          .filter(Boolean),
        duration_min: form.duration_min || 0,
        lobby_required: form.lobby_required,
        signin_required: form.signin_required,
        recording_enabled: false, // always false — stub
      }
      if (form.scheduled_at) {
        body.scheduled_at = new Date(form.scheduled_at).toISOString()
        body.start_unix = Math.floor(new Date(form.scheduled_at).getTime() / 1000)
      }
      // POST to new Meet schedule API first, fall back to old /meetings endpoint
      let m
      try {
        const schedRes = await apiFetch('/api/meeting/schedule', {
          method: 'POST',
          body: JSON.stringify({
            title: body.title,
            start_unix: body.start_unix || Math.floor(Date.now() / 1000),
            duration_min: body.duration_min,
            invitees: body.invitees,
            lobby_required: body.lobby_required,
            signin_required: body.signin_required,
          }),
        })
        // Merge into the old Meeting shape for the card list
        m = {
          ...schedRes.meeting,
          session_id: `meeting:${schedRes.meeting?.room_id || schedRes.meeting?.id}`,
          join_link: schedRes.join_link,
          host_vumail: body.host_vumail,
          invitees: body.invitees,
        }
      } catch (_) {
        // Fall back to legacy endpoint if new one not wired
        m = await apiFetch(API, { method: 'POST', body: JSON.stringify(body) })
      }
      onCreated(m)
      setForm({
        title: '', host_vumail: '', invitees_raw: '', scheduled_at: '', duration_min: 60,
        lobby_required: true, signin_required: false, recording_enabled: false,
      })
    } catch (e) {
      setErr(e.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="New meeting" size="md">
      <form onSubmit={handleSubmit}>
        <Modal.Body className="space-y-4">
          <Input
            label="Title"
            placeholder="Weekly sync"
            value={form.title}
            onChange={(e) => update('title', e.target.value)}
            autoFocus
            required
          />
          <Input
            label="Your Vulos account address"
            placeholder="you@vulos"
            value={form.host_vumail}
            onChange={(e) => update('host_vumail', e.target.value)}
          />
          <Input
            label="Invitees"
            hint="Comma-separated Vulos account addresses (@vulos.org)"
            placeholder="alice@vulos, bob@vulos"
            value={form.invitees_raw}
            onChange={(e) => update('invitees_raw', e.target.value)}
          />
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs text-ink-muted font-medium mb-1.5 tracking-tightish">
                Scheduled time (optional)
              </label>
              <input
                type="datetime-local"
                value={form.scheduled_at}
                onChange={(e) => update('scheduled_at', e.target.value)}
                className="w-full h-9 px-3 text-sm bg-paper border border-line rounded-md text-ink outline-none focus:border-accent focus:shadow-focus transition-[border-color,box-shadow] duration-fast ease-out"
              />
            </div>
            <Input
              type="number"
              min={0}
              max={480}
              label="Duration (min)"
              value={form.duration_min}
              onChange={(e) => update('duration_min', parseInt(e.target.value, 10) || 0)}
            />
          </div>

          {/* Security + access controls */}
          <div className="rounded-md border border-line p-3 space-y-3 bg-bg-elev-1">
            <p className="text-2xs text-ink-faint uppercase tracking-eyebrow font-semibold">
              Access controls
            </p>
            <ToggleRow
              label="Lobby"
              hint="Participants wait in a lobby until you admit them"
              checked={form.lobby_required}
              onChange={() => update('lobby_required', !form.lobby_required)}
              icon={Users}
            />
            <ToggleRow
              label="Require sign-in"
              hint="Anonymous joins are blocked; a Vulos account is required"
              checked={form.signin_required}
              onChange={() => update('signin_required', !form.signin_required)}
              icon={ShieldCheck}
            />
            <ToggleRow
              label="Recording"
              hint="Coming soon — stub only. All participants will be notified before recording starts."
              checked={false}
              onChange={() => {}}
              icon={Circle}
            />
          </div>

          {err && <p className="text-danger text-xs">{err}</p>}
        </Modal.Body>
        <Modal.Footer>
          <Button variant="secondary" onClick={onClose} type="button">Cancel</Button>
          <Button variant="primary" type="submit" disabled={busy || !form.title.trim()}>
            {busy ? 'Creating…' : 'Create meeting'}
          </Button>
        </Modal.Footer>
      </form>
    </Modal>
  )
}
