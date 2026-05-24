/**
 * RaiseHand.jsx — Raise / lower hand in a Vulos Meet call.
 *
 * Signals state via the WebRTC data channel (E2E by default — no server
 * intermediation). Peers receive a `{ type:'raise-hand', raised:bool }` message.
 *
 * Props:
 *   call           — the active call object returned by createCall()
 *   raised         — controlled boolean (caller owns state)
 *   onToggle       — called with the new boolean after signaling
 *   chimeEnabled   — play an audio chime when a remote peer raises (default true)
 *   identity       — { displayName } used in the chime label
 */
import { useEffect, useRef, useCallback } from 'react'
import { Hand } from 'lucide-react'
import { Tooltip } from '../../../components/ui'

// ── chime (AudioContext, lightweight) ──────────────────────────────────────
function playChime() {
  try {
    const ctx = new (window.AudioContext || window.webkitAudioContext)()
    const osc = ctx.createOscillator()
    const gain = ctx.createGain()
    osc.connect(gain)
    gain.connect(ctx.destination)
    osc.type = 'sine'
    osc.frequency.setValueAtTime(880, ctx.currentTime)
    osc.frequency.setValueAtTime(1100, ctx.currentTime + 0.1)
    gain.gain.setValueAtTime(0.18, ctx.currentTime)
    gain.gain.linearRampToValueAtTime(0, ctx.currentTime + 0.4)
    osc.start(ctx.currentTime)
    osc.stop(ctx.currentTime + 0.45)
    osc.onended = () => ctx.close()
  } catch (_) {
    // AudioContext not available — graceful degrade
  }
}

export default function RaiseHand({ call, raised, onToggle, chimeEnabled = true }) {
  const handledRef = useRef(new Set())

  // ── listen for remote peer hand events ──────────────────────────────────
  useEffect(() => {
    if (!call) return
    const handler = ({ peerId, raised: pRaised }) => {
      const key = `${peerId}:${pRaised}`
      if (handledRef.current.has(key)) return
      handledRef.current.add(key)
      if (pRaised && chimeEnabled) playChime()
    }
    call.on('raise-hand', handler)
    return () => call.off('raise-hand', handler)
  }, [call, chimeEnabled])

  const handleToggle = useCallback(() => {
    if (!call) return
    const next = !raised
    // Send via data channel — authenticated as part of the WebRTC session,
    // no plaintext server relay.
    call.sendDataChannelMsg({ type: 'raise-hand', raised: next })
    onToggle?.(next)
  }, [call, raised, onToggle])

  return (
    <Tooltip label={raised ? 'Lower hand' : 'Raise hand'} side="top">
      <button
        type="button"
        onClick={handleToggle}
        aria-label={raised ? 'Lower hand' : 'Raise hand'}
        aria-pressed={raised ? 'true' : 'false'}
        className={[
          'inline-flex items-center justify-center w-10 h-10 rounded-md',
          'transition-[background,color] duration-fast ease-out',
          'focus-visible:outline-none focus-visible:shadow-focus',
          raised
            ? 'bg-warning text-white hover:opacity-90'
            : 'bg-paper/10 text-paper hover:bg-paper/20',
        ].join(' ')}
      >
        <Hand size={17} className={raised ? 'animate-bounce-once' : ''} />
      </button>
    </Tooltip>
  )
}

/**
 * HandIndicator — overlay icon shown on a tile when the peer has their hand up.
 * Rendered inside the tile's absolute overlay area.
 */
export function HandIndicator() {
  return (
    <span
      title="Hand raised"
      className="inline-flex items-center justify-center w-5 h-5 rounded-pill bg-warning/90 text-white shadow-e1"
      style={{ fontSize: 11 }}
    >
      <Hand size={11} />
    </span>
  )
}
