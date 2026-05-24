/**
 * PresenceBar.jsx — Presence roster component (OFFICE-24/62).
 *
 * Design treatment:
 *   - Serif italic small-caps name in tooltip-style hover.
 *   - Accent dots aligned to the warm-token palette (no generic green/red/indigo).
 *   - StatusPicker uses design-system vocabulary.
 *
 * Props:
 *   roster  — array from usePresence().roster
 *             each entry: { accountId, displayName, color, online, isSelf?, isGuest? }
 *   max     — max avatars before "+N" overflow (default 5)
 *
 * JSX only — no .tsx.
 */

import { useState } from 'react'
import { STATUS_ONLINE, STATUS_AWAY, STATUS_DND, STATUS_IN_CALL } from '../lib/presence.js'

// ─── Status dot tokens (warm-signal palette, not generic Tailwind greens) ─────
const STATUS_DOT_STYLE = {
  [STATUS_ONLINE]:  { bg: 'var(--signal-success)', label: 'Online'          },
  [STATUS_AWAY]:    { bg: 'var(--signal-warning)', label: 'Away'            },
  [STATUS_DND]:     { bg: 'var(--signal-error)',   label: 'Do not disturb' },
  [STATUS_IN_CALL]: { bg: 'var(--accent)',         label: 'In a call'      },
}

// ─── PresenceDot ──────────────────────────────────────────────────────────────
/**
 * PresenceDot — small status badge rendered next to a user avatar or name.
 *
 * Props:
 *   status   — 'online' | 'away' | 'dnd' | 'in-a-call'
 *   size     — diameter in px (default 9)
 *   className — extra classes
 */
export function PresenceDot({ status = STATUS_ONLINE, size = 9, className = '' }) {
  const style = STATUS_DOT_STYLE[status] || STATUS_DOT_STYLE[STATUS_ONLINE]
  return (
    <span
      className={`inline-block rounded-full flex-shrink-0 ${className}`}
      style={{
        width:  size,
        height: size,
        background: style.bg,
        boxShadow: '0 0 0 1.5px var(--bg-elev-1)',
      }}
      title={style.label}
      aria-label={style.label}
    />
  )
}

// ─── StatusPicker ─────────────────────────────────────────────────────────────
/**
 * StatusPicker — dropdown to change your own presence status + custom text.
 */
export function StatusPicker({ currentStatus, currentText = '', onStatusChange, onClose }) {
  const [text, setText] = useState(currentText)

  const options = [
    { value: STATUS_ONLINE,  label: 'Online'         },
    { value: STATUS_AWAY,    label: 'Away'            },
    { value: STATUS_DND,     label: 'Do not disturb' },
    { value: STATUS_IN_CALL, label: 'In a call'      },
  ]

  return (
    <div
      className="absolute bottom-full mb-1.5 left-0 z-50 bg-paper border border-line rounded-lg shadow-e2 py-2 w-56 animate-scale-in"
      onMouseLeave={onClose}
    >
      <p className="px-3 pb-1.5 text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase">
        Set status
      </p>
      {options.map(o => {
        const active = currentStatus === o.value
        const style = STATUS_DOT_STYLE[o.value]
        return (
          <button
            key={o.value}
            onClick={() => { onStatusChange(o.value, text); onClose() }}
            className={[
              'w-full flex items-center gap-2.5 px-3 py-1.5 text-sm transition-colors duration-fast',
              active ? 'text-ink font-semibold bg-accent-tint' : 'text-ink-muted hover:bg-bg-elev2',
            ].join(' ')}
          >
            <span
              className="inline-block w-2.5 h-2.5 rounded-full flex-shrink-0"
              style={{ background: style.bg }}
            />
            {o.label}
            {active && (
              <span className="ml-auto text-accent text-xs">✓</span>
            )}
          </button>
        )
      })}
      <div className="mx-3 mt-2 border-t border-line pt-2">
        <input
          type="text"
          placeholder="Custom status…"
          value={text}
          maxLength={60}
          onChange={e => setText(e.target.value)}
          onKeyDown={e => {
            if (e.key === 'Enter') { onStatusChange(currentStatus, text); onClose() }
            if (e.key === 'Escape') onClose()
          }}
          className={[
            'w-full text-xs bg-paper border border-line rounded-md px-2 py-1.5',
            'outline-none focus:border-accent focus:shadow-focus',
            'placeholder:text-ink-faint text-ink',
            'transition-[border-color,box-shadow] duration-fast',
          ].join(' ')}
        />
      </div>
    </div>
  )
}

// ─── Initials helper ──────────────────────────────────────────────────────────
function initials(name) {
  if (!name) return '?'
  const parts = name.trim().split(/\s+/)
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
  return name.slice(0, 2).toUpperCase()
}

// ─── Avatar ───────────────────────────────────────────────────────────────────
function Avatar({ peer, size = 32 }) {
  const [showTooltip, setShowTooltip] = useState(false)
  const label = peer.isSelf ? `${peer.displayName} (you)` : peer.displayName

  return (
    <div
      className="relative flex-shrink-0"
      onMouseEnter={() => setShowTooltip(true)}
      onMouseLeave={() => setShowTooltip(false)}
    >
      {/* Avatar circle — uses the peer's CRDT colour */}
      <div
        className="flex items-center justify-center rounded-full text-white font-semibold select-none cursor-default"
        style={{
          width:  size,
          height: size,
          fontSize: size * 0.38,
          background: peer.color,
          opacity: peer.isSelf ? 0.72 : 1,
          boxShadow: peer.isSelf
            ? 'none'
            : '0 0 0 2px var(--bg-elev-1), 0 0 0 2.5px rgba(36,28,16,0.12)',
        }}
        aria-label={label}
      >
        {initials(peer.displayName)}
      </div>

      {/* Status dot — warm-token PresenceDot */}
      {peer.online && !peer.isSelf && (
        <span className="absolute bottom-0 right-0 block">
          <PresenceDot status={peer.status} size={8} />
        </span>
      )}

      {/* Tooltip — serif italic name in warm ink */}
      {showTooltip && (
        <div
          role="tooltip"
          className={[
            'absolute z-50 bottom-full mb-2 left-1/2 -translate-x-1/2',
            'whitespace-nowrap rounded-sm px-2.5 py-1.5',
            'bg-ink shadow-e2 pointer-events-none',
            'animate-fade-in',
          ].join(' ')}
        >
          {/* Name in serif italic — the editorial moment per spec */}
          <span
            className="block font-serif italic text-paper text-xs leading-tight"
            style={{ fontVariant: 'small-caps' }}
          >
            {label}
          </span>
          {(peer.isGuest || (peer.status && peer.status !== STATUS_ONLINE) || peer.statusText) && (
            <span className="block text-2xs text-ink-muted mt-0.5 tracking-tightish">
              {peer.isGuest && 'guest'}
              {peer.status && peer.status !== STATUS_ONLINE && (
                <span className="capitalize">{peer.isGuest ? ' · ' : ''}{peer.status}</span>
              )}
              {peer.statusText && <span> "{peer.statusText}"</span>}
            </span>
          )}
        </div>
      )}
    </div>
  )
}

// ─── PresenceBar ──────────────────────────────────────────────────────────────
/**
 * PresenceBar — avatar strip for a session's presence roster.
 */
export default function PresenceBar({ roster = [], max = 5, className = '' }) {
  if (!roster || roster.length === 0) return null

  const visible  = roster.slice(0, max)
  const overflow = roster.length - max

  return (
    <div
      className={`flex items-center ${className}`}
      aria-label={`${roster.length} collaborator${roster.length !== 1 ? 's' : ''} online`}
    >
      <div className="flex items-center">
        {visible.map((peer, idx) => (
          <div
            key={peer.accountId}
            style={{ marginLeft: idx === 0 ? 0 : -8, zIndex: visible.length - idx }}
            className="relative"
          >
            <Avatar peer={peer} size={28} />
          </div>
        ))}
      </div>

      {overflow > 0 && (
        <div
          className="flex items-center justify-center rounded-full bg-bg-elev2 border border-line text-ink-muted font-medium select-none"
          style={{ width: 28, height: 28, fontSize: 11, marginLeft: -8 }}
          title={`${overflow} more`}
        >
          +{overflow}
        </div>
      )}
    </div>
  )
}
