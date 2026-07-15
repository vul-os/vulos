import { useState } from 'react'

const PERMISSIONS = [
  { key: 'message', label: 'Messages' },
  { key: 'media',   label: 'Media' },
  { key: 'call',    label: 'Voice' },
  { key: 'video',   label: 'Video' },
]

const STATE_COLORS = {
  approved: 'bg-success-soft text-success border-success-soft',
  pending:  'bg-warning-soft text-warning border-warning-soft',
  blocked:  'bg-danger-soft  text-danger  border-danger-soft',
}

const STATE_LABEL = {
  approved: 'Approved',
  pending:  'Pending',
  blocked:  'Blocked',
}

function Avatar({ name, size = 10 }) {
  const initials = (name || '?')
    .split(' ')
    .map(w => w[0])
    .join('')
    .toUpperCase()
    .slice(0, 2)
  const hue = [...(name || '')].reduce((h, c) => (h * 31 + c.charCodeAt(0)) & 0xffffff, 0)
  const bg = `hsl(${hue % 360}, 55%, 30%)`
  // Tailwind can't see dynamically-built class names (`w-${size}`), so the size
  // was never emitted and the avatar collapsed. Size is a Tailwind spacing unit
  // (×0.25rem), applied inline so any size renders.
  const px = `${size * 0.25}rem`
  return (
    <div
      className="rounded-full flex items-center justify-center text-sm font-semibold text-white shrink-0"
      style={{ background: bg, width: px, height: px }}
    >
      {initials}
    </div>
  )
}

function PermToggle({ perm, active, onChange, disabled }) {
  return (
    <button
      onClick={() => !disabled && onChange(!active)}
      disabled={disabled}
      title={perm.label}
      className={`px-2 py-0.5 rounded text-[11px] font-medium border transition-colors
        ${active
          ? 'accent-bg-soft accent-border text-white'
          : 'bg-neutral-800/60 border-neutral-700/40 text-neutral-500 hover:text-neutral-400'}
        ${disabled ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer'}`}
    >
      {perm.label}
    </button>
  )
}

export default function ContactCard({ contact, onUpdatePerms, onRemove, onCall }) {
  const [expanded, setExpanded] = useState(false)
  const [saving, setSaving] = useState(false)
  const [perms, setPerms] = useState(contact.permissions || [])

  const shortId = contact.vula_id
    ? contact.vula_id.slice(0, 24) + '…'
    : '—'

  const togglePerm = async (key, val) => {
    const next = val ? [...perms, key] : perms.filter(p => p !== key)
    setPerms(next)
    setSaving(true)
    try {
      await onUpdatePerms(contact.vula_id, next)
    } finally {
      setSaving(false)
    }
  }

  const approvedAt = contact.approved_at
    ? new Date(contact.approved_at).toLocaleDateString()
    : null

  return (
    <div className="border border-neutral-800/50 rounded-xl overflow-hidden transition-colors hover:border-neutral-700/50">
      {/* Header row */}
      <button
        className="w-full flex items-center gap-3 px-4 py-3 text-left hover:bg-neutral-800/20 transition-colors"
        onClick={() => setExpanded(e => !e)}
      >
        <Avatar name={contact.display_name} />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium text-neutral-100 truncate">
              {contact.display_name || 'Unknown'}
            </span>
            <span className={`text-[10px] px-1.5 py-0.5 rounded-full border ${STATE_COLORS[contact.state] || STATE_COLORS.approved}`}>
              {STATE_LABEL[contact.state] || 'Approved'}
            </span>
          </div>
          <div className="text-[11px] text-neutral-600 truncate mt-0.5">{shortId}</div>
        </div>
        <span className="text-neutral-600 text-xs ml-2">{expanded ? '▴' : '▾'}</span>
      </button>

      {/* Expanded details */}
      {expanded && (
        <div className="px-4 pb-4 border-t border-neutral-800/40 pt-3 space-y-3">
          {/* Full ID */}
          <div>
            <p className="text-[10px] text-neutral-600 mb-1 uppercase tracking-wider">Vula ID</p>
            <p className="text-[11px] text-neutral-400 font-mono break-all">{contact.vula_id}</p>
          </div>

          {contact.server && (
            <div>
              <p className="text-[10px] text-neutral-600 mb-1 uppercase tracking-wider">Server</p>
              <p className="text-[11px] text-neutral-400 font-mono">{contact.server}</p>
            </div>
          )}

          {approvedAt && (
            <div>
              <p className="text-[10px] text-neutral-600 mb-1 uppercase tracking-wider">Approved</p>
              <p className="text-[11px] text-neutral-400">{approvedAt}</p>
            </div>
          )}

          {/* Permission toggles */}
          <div>
            <p className="text-[10px] text-neutral-600 mb-2 uppercase tracking-wider">
              Permissions {saving && <span className="accent-text ml-1">saving…</span>}
            </p>
            <div className="flex flex-wrap gap-1.5">
              {PERMISSIONS.map(p => (
                <PermToggle
                  key={p.key}
                  perm={p}
                  active={perms.includes(p.key)}
                  onChange={val => togglePerm(p.key, val)}
                  disabled={saving}
                />
              ))}
            </div>
          </div>

          {/* Actions */}
          <div className="flex gap-2 pt-1 flex-wrap">
            {onCall && perms.includes('call') && (
              <button
                onClick={() => onCall(contact.vula_id)}
                className="flex items-center gap-1 text-[11px] text-success hover:opacity-80 transition-colors"
              >
                <span>📞</span> Voice call
              </button>
            )}
            <button
              onClick={() => onRemove(contact.vula_id)}
              className="text-[11px] text-danger hover:opacity-80 transition-colors"
            >
              Remove contact
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
