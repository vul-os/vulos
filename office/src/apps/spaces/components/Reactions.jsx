/**
 * Reactions.jsx — Floating emoji reactions in a Vulos Meet call.
 *
 * Sent via the WebRTC data channel (unreliable / UDP-like — ok if dropped).
 * Rate-limited client-side: max 5 reactions per 10 seconds per user.
 *
 * Props:
 *   call     — active call object (from createCall)
 *   onSend   — optional callback(emoji) after local send
 */
import { useState, useEffect, useRef, useCallback } from 'react'
import { Tooltip } from '../../../components/ui'

export const REACTION_PALETTE = ['👍', '❤️', '😂', '🎉', '👏', '✋', '⚡']

// ── rate limiter (5 / 10 s) ─────────────────────────────────────────────────
function createRateLimiter(max, windowMs) {
  const ts = []
  return function allowed() {
    const now = Date.now()
    while (ts.length && now - ts[0] > windowMs) ts.shift()
    if (ts.length >= max) return false
    ts.push(now)
    return true
  }
}

/** Unique floating bubble id */
let _bubbleSeq = 0
function nextBubbleId() { return ++_bubbleSeq }

export default function Reactions({ call, onSend }) {
  const [bubbles, setBubbles] = useState([])
  const limiterRef = useRef(createRateLimiter(5, 10_000))
  const [showPalette, setShowPalette] = useState(false)

  // ── receive remote reactions via data channel ───────────────────────────
  useEffect(() => {
    if (!call) return
    const handler = ({ emoji }) => spawnBubble(emoji)
    call.on('reaction', handler)
    return () => call.off('reaction', handler)
  }, [call])

  function spawnBubble(emoji) {
    const id = nextBubbleId()
    const left = 10 + Math.random() * 80 // 10-90%
    setBubbles((prev) => [...prev, { id, emoji, left }])
    setTimeout(() => {
      setBubbles((prev) => prev.filter((b) => b.id !== id))
    }, 2800)
  }

  const sendReaction = useCallback((emoji) => {
    if (!limiterRef.current()) return // client-side rate limit
    spawnBubble(emoji)
    call?.sendDataChannelMsg({ type: 'reaction', emoji })
    onSend?.(emoji)
    setShowPalette(false)
  }, [call, onSend])

  return (
    <>
      {/* Floating bubbles overlay — positioned by the parent (absolute) */}
      <div className="pointer-events-none absolute inset-0 overflow-hidden" aria-hidden>
        {bubbles.map((b) => (
          <span
            key={b.id}
            className="absolute bottom-14 text-2xl select-none animate-float-up"
            style={{ left: `${b.left}%` }}
          >
            {b.emoji}
          </span>
        ))}
      </div>

      {/* Toggle button */}
      <Tooltip label="Reactions" side="top">
        <button
          type="button"
          onClick={() => setShowPalette((v) => !v)}
          aria-label="Reactions"
          aria-expanded={showPalette}
          className={[
            'inline-flex items-center justify-center w-10 h-10 rounded-md text-xl',
            'transition-[background,color] duration-fast ease-out',
            'focus-visible:outline-none focus-visible:shadow-focus',
            showPalette
              ? 'bg-accent text-white'
              : 'bg-paper/10 text-paper hover:bg-paper/20',
          ].join(' ')}
        >
          🎉
        </button>
      </Tooltip>

      {/* Palette popover */}
      {showPalette && (
        <div
          className="absolute bottom-16 left-1/2 -translate-x-1/2 z-50 flex gap-1 px-2 py-1.5 rounded-lg border border-paper/10 shadow-e3"
          style={{ background: 'rgba(26,25,22,.92)' }}
          role="toolbar"
          aria-label="Reaction palette"
        >
          {REACTION_PALETTE.map((emoji) => (
            <button
              key={emoji}
              type="button"
              onClick={() => sendReaction(emoji)}
              aria-label={`React with ${emoji}`}
              className="text-xl w-9 h-9 rounded-md hover:bg-paper/15 transition-colors duration-fast focus-visible:outline-none"
            >
              {emoji}
            </button>
          ))}
        </div>
      )}
    </>
  )
}
