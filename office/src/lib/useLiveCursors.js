/**
 * useLiveCursors.js — OFFICE-25: Live cursors + selections.
 *
 * Design treatment (updated):
 *   - Cursor colour palette uses warm-leaning HSL values aligned to the
 *     design tokens: hue rotated from the base, lightness capped at 52%
 *     (so cursors read against both oat-light and warm-dark backgrounds),
 *     saturation reduced to ~55% to avoid a rainbow-on-paper clash.
 *   - The colour derivation mirrors DocsEditor's existing approach but is
 *     now token-aware (never pure #6366f1 indigo as a fallback).
 *
 * Channel: "cursors" (separate from the "presence" channel)
 *
 * Message shape (JSON):
 *   { channel: 'cursors', payload: { accountId, from, to, slideId? } }
 *
 * Usage:
 *   const { remoteCursors, broadcastDocCursor, broadcastSheetCursor, broadcastSlideCursor }
 *     = useLiveCursors({ fabric, localIdentity, color })
 *
 * remoteCursors: Map<accountId, { accountId, displayName, color, from, to, slideId? }>
 *
 * JSX only — no .tsx.
 */

import { useEffect, useRef, useState, useCallback } from 'react'

const CURSOR_CHANNEL = 'cursors'
const THROTTLE_MS = 80   // max one broadcast per 80 ms

/**
 * Derive a warm-leaning HSL colour for a peer from their accountId string.
 *
 * Design constraints:
 *   - Hue: full 360° rotation — we want variety, not a mono palette.
 *   - Saturation: 50–58% — muted enough to not clash with oat paper.
 *   - Lightness: 38–48% — readable as both caret line and selection highlight
 *     against both light (oat-50) and dark (#131110) backgrounds.
 *
 * This replaces the old `hsl(h, 65%, 50%)` which produced oversaturated colours.
 */
export function peerColor(accountId) {
  if (!accountId) return 'var(--accent)'   // fallback to system accent
  let h = 0
  for (const c of accountId) {
    h = (h << 5) - h + c.charCodeAt(0)
    h |= 0
  }
  const hue = Math.abs(h) % 360
  // Avoid the teal-600 hue band (168–192°) — too close to the system accent.
  // Shift it to a neighbouring, distinct hue.
  const adjustedHue = (hue >= 168 && hue <= 192) ? (hue + 50) % 360 : hue
  const sat = 52 + (Math.abs(h >> 8) % 8)  // 52–59%
  const lig = 40 + (Math.abs(h >> 4) % 10) // 40–49% — punchy but not neon
  return `hsl(${adjustedHue},${sat}%,${lig}%)`
}

export function useLiveCursors({ fabric, localIdentity, color }) {
  /** @type {[Map<string, object>, Function]} */
  const [remoteCursors, setRemoteCursors] = useState(new Map())
  const lastSentRef = useRef(0)
  const pendingRef  = useRef(null)

  // Listen for remote cursor frames on the fabric.
  useEffect(() => {
    if (!fabric) {
      setRemoteCursors(new Map())
      return
    }

    const onMessage = ({ detail: { data } }) => {
      let text
      try { text = typeof data === 'string' ? data : new TextDecoder().decode(data) } catch { return }
      let frame
      try { frame = JSON.parse(text) } catch { return }
      if (frame.channel !== CURSOR_CHANNEL) return
      const p = frame.payload
      if (!p || !p.accountId) return
      // Ignore own echoes.
      if (localIdentity && p.accountId === localIdentity.accountId) return

      setRemoteCursors((prev) => {
        const next = new Map(prev)
        next.set(p.accountId, p)
        return next
      })
    }

    fabric.addEventListener('message', onMessage)
    return () => fabric.removeEventListener('message', onMessage)
  }, [fabric]) // eslint-disable-line react-hooks/exhaustive-deps

  // Internal: send a cursor frame immediately or schedule one.
  const _sendCursor = useCallback((payload) => {
    if (!fabric || !localIdentity) return
    const now = Date.now()
    const send = () => {
      lastSentRef.current = Date.now()
      pendingRef.current = null
      const frame = JSON.stringify({ channel: CURSOR_CHANNEL, payload })
      fabric.send(frame)
    }
    const elapsed = now - lastSentRef.current
    if (elapsed >= THROTTLE_MS) {
      send()
    } else {
      clearTimeout(pendingRef.current)
      pendingRef.current = setTimeout(send, THROTTLE_MS - elapsed)
    }
  }, [fabric, localIdentity])

  /** Broadcast a Docs (TipTap) caret / selection. */
  const broadcastDocCursor = useCallback((from, to) => {
    if (!localIdentity) return
    _sendCursor({
      accountId:   localIdentity.accountId,
      displayName: localIdentity.displayName,
      color,
      from,
      to,
      type: 'doc',
    })
  }, [_sendCursor, localIdentity, color])

  /** Broadcast a Sheets cell selection. */
  const broadcastSheetCursor = useCallback((row, col) => {
    if (!localIdentity) return
    _sendCursor({
      accountId:   localIdentity.accountId,
      displayName: localIdentity.displayName,
      color,
      from: `${row},${col}`,
      to:   `${row},${col}`,
      type: 'sheet',
    })
  }, [_sendCursor, localIdentity, color])

  /** Broadcast the active slide id in SlidesEditor. */
  const broadcastSlideCursor = useCallback((slideId) => {
    if (!localIdentity) return
    _sendCursor({
      accountId:   localIdentity.accountId,
      displayName: localIdentity.displayName,
      color,
      from:    slideId,
      to:      slideId,
      type:    'slide',
      slideId,
    })
  }, [_sendCursor, localIdentity, color])

  return { remoteCursors, broadcastDocCursor, broadcastSheetCursor, broadcastSlideCursor }
}
