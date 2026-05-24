/**
 * PresenterFocus.jsx — Pin / spotlight presenter in a Vulos Meet call.
 *
 * Two modes:
 *  - "Pin presenter" (local only): the calling user pins a peer as the main tile.
 *    Other tiles shrink to a strip. This is purely local UI state.
 *  - "Spotlight for everyone" (organizer only): signals all peers via data channel
 *    to force the same layout.
 *
 * Screen-share auto-spotlight: when a peer starts sharing their screen the call
 * surface in CallView.jsx auto-pins them. This component provides the manual controls.
 *
 * Props:
 *   peers        — array of peer objects from the call
 *   pinnedId     — controlled: currently pinned peer id (null = no pin)
 *   onPin        — called with peerId (or null to unpin)
 *   isOrganizer  — whether the current user may spotlight for everyone
 *   call         — active call object
 */
import { useCallback } from 'react'
import { Pin, PinOff, Maximize2 } from 'lucide-react'
import { Tooltip } from '../../../components/ui'

export default function PresenterFocus({ peers, pinnedId, onPin, isOrganizer, call }) {
  const handleSpotlight = useCallback((peerId) => {
    if (!call || !isOrganizer) return
    call.sendDataChannelMsg({ type: 'spotlight', peerId })
    onPin?.(peerId)
  }, [call, isOrganizer, onPin])

  return (
    <div className="flex items-center gap-1">
      {pinnedId ? (
        <Tooltip label="Unpin presenter" side="top">
          <button
            type="button"
            onClick={() => onPin?.(null)}
            aria-label="Unpin presenter"
            className="inline-flex items-center justify-center w-10 h-10 rounded-md bg-accent text-white hover:opacity-90 transition-opacity duration-fast focus-visible:outline-none focus-visible:shadow-focus"
          >
            <PinOff size={16} />
          </button>
        </Tooltip>
      ) : (
        peers.length > 0 && (
          <Tooltip label="Pin a presenter" side="top">
            <button
              type="button"
              onClick={() => onPin?.(peers[0].peerId)}
              aria-label="Pin presenter"
              className="inline-flex items-center justify-center w-10 h-10 rounded-md bg-paper/10 text-paper hover:bg-paper/20 transition-colors duration-fast focus-visible:outline-none focus-visible:shadow-focus"
            >
              <Pin size={16} />
            </button>
          </Tooltip>
        )
      )}

      {isOrganizer && peers.length > 0 && (
        <Tooltip label="Spotlight for everyone (organizer)" side="top">
          <button
            type="button"
            onClick={() => handleSpotlight(pinnedId || peers[0].peerId)}
            aria-label="Spotlight for everyone"
            className="inline-flex items-center justify-center w-10 h-10 rounded-md bg-paper/10 text-paper hover:bg-paper/20 transition-colors duration-fast focus-visible:outline-none focus-visible:shadow-focus"
          >
            <Maximize2 size={16} />
          </button>
        </Tooltip>
      )}
    </div>
  )
}

/**
 * usePinnedLayout — computes tile layout based on pinned peer.
 * Returns { mainPeer, stripPeers, cols }.
 */
export function usePinnedLayout({ peers, pinnedId, screenPresenter }) {
  // Auto-spotlight screen sharer
  const effectivePinnedId = screenPresenter && screenPresenter !== 'local'
    ? screenPresenter
    : pinnedId

  if (!effectivePinnedId) {
    const total = peers.length + 1
    const cols = total <= 1 ? 1 : total <= 4 ? 2 : total <= 9 ? 3 : 3
    return { mainPeer: null, stripPeers: peers, cols }
  }

  const mainPeer = peers.find((p) => p.peerId === effectivePinnedId) ?? null
  const stripPeers = peers.filter((p) => p.peerId !== effectivePinnedId)
  return { mainPeer, stripPeers, cols: 1 }
}
