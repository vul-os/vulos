// RecentsTab.tsx — ONE call log.
//
// A Vulos box can have calls from two entirely different systems, and the
// founder asked for one app, so this is one list:
//
//   • GSM calls, from whichever line is active (the box's own modem, or the
//     handset SIM when the shell runs inside the Android app).
//   • Vulos-to-Vulos (peer) calls, from the peering service's own history.
//
// They are merged by time and each row says which it was, because they are not
// interchangeable: a GSM row carries a dialable number, a peer row carries a
// Vulos identity and no number at all. Redialling is therefore only offered on
// rows that can actually be redialled.
//
// Why peer calls are shown but not OFFERED: peer calling does not work
// end-to-end in this codebase — nothing initiates a call, and the incoming-call
// banner's Answer button is wired to reject with an "unavailable" notice. A
// "Call over Vulos" button would be a button that cannot work. Showing the
// history costs nothing and is honest; offering the action would not be.

import { useState, useMemo } from 'react'
import { Avatar, CallButton, ErrorNotice, EmptyNote } from './PhoneChrome'
import type { Size } from './phoneLayout'
import { formatRelative, formatDuration, displayNumber } from './phoneUtils'
import { callTimeMs } from './usePhoneData'
import type { CallEntry } from './telephonyApi'

// The arrow is a GRAPHIC (an inline SVG), not a text glyph, and the outcome is
// always spelled out beside it.
//
// As coloured text at 12.5px these needed 4.5:1 and measured 3.1 (incoming) and
// 3.97 (missed on a selected row) — and colouring the contact's NAME red for a
// missed call put a person's name at 3.97:1 in light. Colour is now a second
// cue on a graphic, which is held to 3:1, while every word in the row sits on
// the normal text ramp. Nothing is conveyed by colour alone.
const DIRECTION: Record<string, { path: string; stroke: string; label: string; emphasis: boolean }> = {
  incoming: { path: 'M17 7 L7 17 M7 11 v6 h6', stroke: 'var(--status-success)', label: 'Incoming', emphasis: false },
  outgoing: { path: 'M7 17 L17 7 M11 7 h6 v6', stroke: 'var(--accent)', label: 'Outgoing', emphasis: false },
  missed: { path: 'M7 7 L17 17 M17 11 v6 h-6', stroke: 'var(--status-danger)', label: 'Missed', emphasis: true },
}

function DirectionMark({ path, stroke }: { path: string; stroke: string }) {
  return (
    <svg aria-hidden="true" viewBox="0 0 24 24" width="13" height="13" fill="none"
      stroke={stroke} strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" className="shrink-0">
      <path d={path} />
    </svg>
  )
}

interface RecentsTabProps {
  calls: CallEntry[]
  loading: boolean
  error: string
  size: Size
  canCall: boolean
  /** Why calling is unavailable, when it is — shown on the disabled button. */
  callBlockedReason: string
  onCall: (number: string) => void
  onMessage: (number: string) => void
  onRetry: () => void
}

function label(c: CallEntry): string {
  return c.name || (c.number ? displayNumber(c.number) : 'Unknown')
}

export default function RecentsTab({ calls, loading, error, size, canCall, callBlockedReason, onCall, onMessage, onRetry }: RecentsTabProps) {
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const wide = size === 'wide'
  const selected = useMemo(() => calls.find((c) => c.id === selectedId) ?? null, [calls, selectedId])

  // Every call with the same party, for the detail pane.
  const related = useMemo(() => {
    if (!selected) return []
    return calls.filter((c) =>
      selected.number ? c.number === selected.number : c.peerId && c.peerId === selected.peerId)
  }, [calls, selected])

  const list = (
    <div className="h-full overflow-y-auto">
      {loading && calls.length === 0 ? (
        <div className="p-4 flex items-center gap-2 text-[13px]" style={{ color: 'var(--text-tertiary)' }}>
          <span className="w-3.5 h-3.5 spinner" /> Loading…
        </div>
      ) : calls.length === 0 && !error ? (
        <EmptyNote glyph="🕘" title="No calls yet"
          body="Calls you place and calls that reach this line will appear here." />
      ) : (
        <ul>
          {calls.map((c) => {
            const meta = DIRECTION[c.direction] ?? DIRECTION.incoming
            const on = c.id === selectedId
            const dialable = c.origin === 'gsm' && !!c.number
            return (
              <li key={c.id}>
                <div className="w-full flex items-center gap-3 px-3.5 py-2.5 transition-colors"
                  style={{ background: on ? 'var(--bg-selected)' : 'transparent', borderBottom: '1px solid var(--border-subtle)' }}>
                  <button type="button" onClick={() => setSelectedId(on ? null : c.id)}
                    className="min-w-0 flex-1 flex items-center gap-3 text-left focus-primary rounded-md">
                    <Avatar name={c.name || ''} number={c.number} size={38} />
                    <span className="min-w-0 flex-1">
                      <span data-recent-name className="block text-[13.5px] truncate"
                        style={{ color: 'var(--text-primary)', fontWeight: meta.emphasis ? 600 : 500 }}>
                        {label(c)}
                      </span>
                      <span className="flex items-center gap-1.5 text-[12.5px] mt-0.5" style={{ color: 'var(--text-tertiary)' }}>
                        <DirectionMark path={meta.path} stroke={meta.stroke} />
                        <span className="truncate">
                          {meta.label}
                          {c.duration > 0 ? ` · ${formatDuration(c.duration)}` : ''}
                          {c.origin === 'peer' ? ' · over Vulos' : ''}
                        </span>
                      </span>
                    </span>
                    <span className="shrink-0 text-[12.5px]" style={{ color: 'var(--text-tertiary)' }}>
                      {formatRelative(callTimeMs(c))}
                    </span>
                  </button>
                  {dialable && (
                    <CallButton size={34} onClick={() => onCall(c.number)} disabled={!canCall}
                      label={`Call ${label(c)}`} reason={callBlockedReason} />
                  )}
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )

  const detail = selected ? (
    <div className="h-full overflow-y-auto p-5">
      <div className="flex items-center gap-3.5">
        <Avatar name={selected.name || ''} number={selected.number} size={56} />
        <div className="min-w-0">
          <div className="text-[16px] font-semibold truncate" style={{ color: 'var(--text-primary)' }}>{label(selected)}</div>
          <div className="text-[13px] truncate" style={{ color: 'var(--text-tertiary)' }}>
            {selected.number ? displayNumber(selected.number) : selected.peerId || 'Vulos peer'}
          </div>
        </div>
      </div>

      {selected.origin === 'gsm' && selected.number ? (
        <div className="flex items-center gap-2 mt-4">
          <CallButton size={40} onClick={() => onCall(selected.number)} disabled={!canCall}
            label={`Call ${label(selected)}`} reason={callBlockedReason} />
          <button type="button" onClick={() => onMessage(selected.number)}
            className="px-3 py-2 rounded-lg text-[13px] font-medium focus-primary transition-colors"
            style={{ background: 'var(--bg-elevated)', color: 'var(--text-primary)', border: '1px solid var(--border-default)' }}>
            Message
          </button>
        </div>
      ) : (
        <p className="mt-4 text-[12.5px] leading-relaxed px-3 py-2.5 rounded-lg"
          style={{ background: 'var(--bg-elevated)', color: 'var(--text-tertiary)', border: '1px solid var(--border-default)' }}>
          This was a Vulos-to-Vulos call. It has no phone number to redial, and placing
          Vulos calls isn’t available yet — only the history is.
        </p>
      )}

      <div className="mt-6 text-[11.5px] font-semibold uppercase tracking-wide" style={{ color: 'var(--text-muted)' }}>History</div>
      <ul className="mt-2">
        {related.map((c) => {
          const meta = DIRECTION[c.direction] ?? DIRECTION.incoming
          return (
            <li key={c.id} className="flex items-center gap-2 py-2 text-[13px]" style={{ borderBottom: '1px solid var(--border-subtle)' }}>
              <DirectionMark path={meta.path} stroke={meta.stroke} />
              <span style={{ color: 'var(--text-primary)' }}>{meta.label}</span>
              {c.duration > 0 && <span style={{ color: 'var(--text-tertiary)' }}>· {formatDuration(c.duration)}</span>}
              <span className="ml-auto" style={{ color: 'var(--text-tertiary)' }}>{formatRelative(callTimeMs(c))}</span>
            </li>
          )
        })}
      </ul>
    </div>
  ) : (
    <EmptyNote glyph="🕘" title="Pick a call" body="Select an entry to see everything for that number." />
  )

  return (
    <div className="h-full flex flex-col min-h-0">
      <ErrorNotice message={error} onRetry={onRetry} />
      {wide ? (
        <div className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: 'minmax(17rem, 22rem) 1fr' }}>
          <div className="min-h-0" style={{ borderRight: '1px solid var(--border-default)' }}>{list}</div>
          <div className="min-h-0">{detail}</div>
        </div>
      ) : (
        <div className="flex-1 min-h-0">{list}</div>
      )}
    </div>
  )
}
