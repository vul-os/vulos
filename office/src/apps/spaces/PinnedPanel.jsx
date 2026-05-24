/**
 * PinnedPanel.jsx — pinned messages tab in channel header.
 * Right-click → Pin; pinned messages show a pin badge.
 * Clicking a pinned entry calls onJump(msg).
 */
import { Pin, X } from 'lucide-react'
import { api } from '../../lib/api.js'

function formatDate(ts) {
  if (!ts) return ''
  return new Date(ts).toLocaleDateString([], { month: 'short', day: 'numeric' })
}

/**
 * PinnedPanel — shows all pinned messages for a channel.
 *
 * Props:
 *   channelId   — string
 *   pinnedMsgs  — array of { message_id, pinned_by, pinned_at, body, author_id }
 *   onJump      — (msg) => void
 *   onUnpin     — (messageId) => void
 *   onClose     — () => void
 */
export default function PinnedPanel({ pinnedMsgs = [], onJump, onUnpin, onClose }) {
  return (
    <aside className="w-72 flex-shrink-0 border-l border-line bg-bg-elev2 flex flex-col overflow-hidden animate-slide-in-right">
      <div className="flex items-center justify-between px-3 h-11 border-b border-line bg-paper flex-shrink-0">
        <div className="flex items-center gap-2">
          <Pin size={13} className="text-ink-muted" />
          <span className="text-sm font-semibold text-ink tracking-tightish">Pinned</span>
          {pinnedMsgs.length > 0 && (
            <span className="text-2xs bg-bg-elev2 text-ink-faint rounded-pill px-1.5 py-0.5 font-medium">
              {pinnedMsgs.length}
            </span>
          )}
        </div>
        <button
          type="button"
          onClick={onClose}
          className="p-1 rounded-sm text-ink-faint hover:text-ink hover:bg-accent-tint transition-colors"
          title="Close pinned panel"
        >
          <X size={14} />
        </button>
      </div>

      <div className="flex-1 overflow-y-auto py-2 divide-y divide-line">
        {pinnedMsgs.length === 0 && (
          <p className="text-xs text-ink-faint text-center py-8 font-serif italic px-4">
            No pinned messages yet. Right-click a message to pin it.
          </p>
        )}
        {pinnedMsgs.map((pm) => (
          <div key={pm.message_id} className="px-3 py-2 group hover:bg-paper transition-colors">
            <div className="flex items-start gap-2">
              <Pin size={11} className="text-accent mt-0.5 flex-shrink-0" />
              <div className="flex-1 min-w-0">
                <div className="flex items-baseline gap-1.5 mb-0.5">
                  <span className="text-xs font-semibold text-ink tracking-tightish truncate">
                    {pm.author_id}
                  </span>
                  <span className="text-2xs text-ink-faint flex-shrink-0">{formatDate(pm.pinned_at)}</span>
                </div>
                <button
                  type="button"
                  onClick={() => onJump(pm)}
                  className="text-xs text-ink-muted line-clamp-3 text-left hover:text-ink transition-colors leading-snug"
                >
                  {pm.body}
                </button>
                <div className="flex items-center gap-2 mt-1.5 opacity-0 group-hover:opacity-100 transition-opacity">
                  <button
                    type="button"
                    onClick={() => onJump(pm)}
                    className="text-2xs text-accent hover:text-accent-press transition-colors"
                  >
                    Jump to message
                  </button>
                  <button
                    type="button"
                    onClick={() => onUnpin(pm.message_id)}
                    className="text-2xs text-ink-faint hover:text-danger transition-colors"
                  >
                    Unpin
                  </button>
                </div>
              </div>
            </div>
          </div>
        ))}
      </div>
    </aside>
  )
}

/**
 * PinBadge — tiny indicator on a pinned message.
 */
export function PinBadge() {
  return (
    <span title="Pinned">
      <Pin size={10} className="text-accent opacity-70" />
    </span>
  )
}
