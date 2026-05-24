/**
 * MentionPicker.jsx — @mention suggestion popup.
 * Shows channel members filtered by the current @query.
 * Arrow-key navigate, Tab/Enter to insert.
 * ~120 LoC.
 */
import { useEffect, useRef, useState } from 'react'
import { AtSign } from 'lucide-react'
import { PresenceDot } from '../../components/PresenceBar.jsx'

/**
 * Parse the current @mention query from a textarea value + cursor position.
 * Returns { query, atStart } or null if the cursor isn't inside a @mention.
 */
export function parseMentionQuery(value, cursorPos) {
  const textBefore = value.slice(0, cursorPos)
  const match = textBefore.match(/@(\w*)$/)
  if (!match) return null
  return { query: match[1], atStart: cursorPos - match[0].length }
}

/**
 * Replace the @mention at [atStart, cursorPos) with the chosen mention string.
 */
export function insertMention(value, atStart, cursorPos, mentionText) {
  return value.slice(0, atStart) + mentionText + ' ' + value.slice(cursorPos)
}

/**
 * MentionPicker — dropdown component rendered by ChannelView composer.
 *
 * Props:
 *   members   — array of { accountId, displayName, status }
 *   query     — current text after @
 *   onSelect  — (accountId) => void
 *   onClose   — () => void
 */
export default function MentionPicker({ members = [], query = '', onSelect, onClose }) {
  const [idx, setIdx] = useState(0)
  const ref = useRef(null)

  const SPECIAL = [{ accountId: 'channel', displayName: '@channel', status: 'online' }]
  const pool = [...SPECIAL, ...members]

  const q = query.toLowerCase()
  const filtered = pool.filter(
    (m) =>
      m.displayName.toLowerCase().includes(q) ||
      m.accountId.toLowerCase().includes(q)
  ).slice(0, 8)

  // Reset selection when filtered list changes
  useEffect(() => {
    setIdx(0)
  }, [query])

  // Arrow + tab/enter navigation
  useEffect(() => {
    function onKey(e) {
      if (!filtered.length) return
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setIdx((i) => (i + 1) % filtered.length)
      } else if (e.key === 'ArrowUp') {
        e.preventDefault()
        setIdx((i) => (i - 1 + filtered.length) % filtered.length)
      } else if (e.key === 'Tab' || e.key === 'Enter') {
        e.preventDefault()
        onSelect(filtered[idx]?.accountId || '')
      } else if (e.key === 'Escape') {
        onClose()
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [filtered, idx, onSelect, onClose])

  if (filtered.length === 0) return null

  return (
    <div
      ref={ref}
      className="absolute bottom-full mb-1 left-0 z-50 bg-paper border border-line rounded-lg shadow-e3 py-1 w-64 animate-scale-in"
      role="listbox"
      aria-label="Mention suggestions"
    >
      {filtered.map((m, i) => {
        const isChannel = m.accountId === 'channel'
        return (
          <button
            key={m.accountId}
            type="button"
            role="option"
            aria-selected={i === idx}
            onMouseEnter={() => setIdx(i)}
            onClick={() => onSelect(m.accountId)}
            className={[
              'w-full flex items-center gap-2.5 px-3 py-1.5 text-sm transition-colors duration-fast text-left',
              i === idx ? 'bg-accent-tint text-ink' : 'text-ink-muted hover:bg-bg-elev2',
            ].join(' ')}
          >
            {isChannel ? (
              <AtSign size={14} className="text-accent flex-shrink-0" />
            ) : (
              <span className="relative flex-shrink-0">
                <span
                  className="inline-flex items-center justify-center w-6 h-6 rounded-full text-white text-xs font-bold"
                  style={{ backgroundColor: `hsl(${(m.accountId.charCodeAt(0) || 0) % 360},55%,45%)` }}
                >
                  {(m.displayName || m.accountId || '?')[0].toUpperCase()}
                </span>
                {m.status && (
                  <span className="absolute -bottom-0.5 -right-0.5">
                    <PresenceDot status={m.status} size={6} />
                  </span>
                )}
              </span>
            )}
            <span className="flex-1 min-w-0">
              <span className="block font-medium tracking-tightish truncate">
                {m.displayName}
              </span>
              {!isChannel && (
                <span className="block text-2xs text-ink-faint truncate">
                  @{m.accountId}
                </span>
              )}
            </span>
          </button>
        )
      })}
      <div className="px-3 pt-1.5 pb-0.5 border-t border-line mt-1">
        <p className="text-2xs text-ink-faint">
          Tab/Enter to insert · Esc to dismiss
        </p>
      </div>
    </div>
  )
}
