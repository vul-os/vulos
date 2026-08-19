/**
 * IncomingCall.tsx — PEER-24
 *
 * Shell-wide listener for peer (Vulos-to-Vulos) call signals. Mounted once near
 * the root of the authenticated shell (layouts/DesktopCanvas.tsx) so it sees a
 * signal no matter which app has focus.
 *
 * Signal framing (received via the /api/peering/stream WebSocket hub):
 *   Outer frame:  { channel: "signal", from: "<peerVulosId>", payload: <SignalPayload> }
 *   SignalPayload: { channel: "signal", type: "incoming-call", call_id, from_id, payload? }
 *   Cancellation:  type === "hangup" | "reject"
 *
 * ─── WHY THIS RENDERS NOTHING ────────────────────────────────────────────────
 *
 * It used to ring. A full-screen card at z-[300] with a synthesised two-tone
 * ringtone, an avatar, a pulsing ring and two buttons: Decline, and Answer.
 *
 * Answer called POST /api/peering/call/reject.
 *
 * That is not a bug in the handler — it was the honest end of a dishonest
 * sequence. The peer call CLIENT was retired on purpose (commit ef3e3175,
 * 2026-07-20: 4,687 lines across 10 files, removed because owning a WebRTC
 * calling stack contradicts the product's position that comms are third-party).
 * So there is nothing to answer WITH, and the old card knew it: it fired a
 * "calling is unavailable" notification and declined on the wire.
 *
 * But it did all of that AFTER ringing the user, showing them an Answer button
 * and letting them press it. Offering an action you will refuse is not honesty;
 * it is a promise broken a second later. A ringtone is a summons — "come and
 * answer this" — and repeating it every 2.2 seconds for a call that cannot be
 * answered is the same lie in audio.
 *
 * So the surface is gone and the BEHAVIOUR is kept:
 *
 *   • The call is declined on the wire immediately. That is exactly what
 *     pressing either button did, minus the wait — the caller gets a real,
 *     fast decline instead of ringing out.
 *   • The user is told, through the shell notification store: who called, that
 *     it was declined, and why. A notification is the right shape for "this
 *     happened to you", where a modal is the shape for "act on this now".
 *   • The box records it (backend/services/peering/call.go records every call
 *     outcome the relay sees), so it also lands in the Phone app's Recents as a
 *     missed Vulos call rather than existing only as a toast.
 *
 * Nothing is offered that cannot be done, and nothing that worked was removed.
 *
 * IF THE CLIENT COMES BACK: this is the mount point. Restore the media surface,
 * post to /api/peering/call/answer instead of declining here, and render the
 * card again — the whole backend it needs (initiate/answer/reject/signal/
 * hangup, mesh, lobby, ICE) is live and unchanged.
 */

import { useEffect, useRef } from 'react'
import { notify } from '../../../core/notificationStore'
import { declinedCallNotice } from './callNotice'

// ---------- helpers -------------------------------------------------------

const WS_URL = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/peering/stream`

// isRecord narrows `unknown` boundary values (the peering WS frame) without
// any/casts — same pattern as src/lib/offlineAuth.ts and
// src/builtin/peering/Messages.tsx.
function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// The call history that used to live here — a fetcher, a status badge, a modal
// panel and a permanently-visible round button pinned to the bottom-right
// corner of the desktop — has moved into the Phone app's Recents tab
// (src/builtin/phone/RecentsTab.tsx), where it sits beside the GSM call log in
// one merged, time-ordered list.

// ---------- main export ---------------------------------------------------

/**
 * IncomingCall
 *
 * Mount once inside the authenticated shell. Renders nothing: it is a listener,
 * not a surface. See the file header for why.
 *
 * Usage:
 *   <IncomingCall />
 */
export default function IncomingCall() {
  const wsRef = useRef<WebSocket | null>(null)
  // Call IDs already answered-for. The hub pushes to every open connection for
  // this user, and a user with two tabs open would otherwise decline twice and
  // be told twice about one call. Per-tab dedupe cannot fix the two-tab case on
  // its own — the second decline gets a 404 from the relay, which records
  // nothing — but it does stop a redelivered frame turning into two notices.
  const handled = useRef<Set<string>>(new Set())

  useEffect(() => {
    let alive = true

    function connect() {
      if (!alive) return
      const ws = new WebSocket(WS_URL)
      wsRef.current = ws

      ws.onmessage = (e: MessageEvent) => {
        try {
          const parsed: unknown = JSON.parse(e.data)
          if (!isRecord(parsed)) return
          // Peering hub wraps every message as { channel, from, payload }.
          // Call signals arrive on the "signal" channel; the inner payload
          // carries { type, call_id, from_id, ... }.
          if (parsed.channel !== 'signal') return
          const sig = isRecord(parsed.payload) ? parsed.payload : {}
          if (sig.type !== 'incoming-call') return

          const callId = typeof sig.call_id === 'string' ? sig.call_id : ''
          const fromId = typeof sig.from_id === 'string' ? sig.from_id : ''
          const from = typeof parsed.from === 'string' ? parsed.from : ''
          const key = callId || `from:${fromId || from}`
          if (handled.current.has(key)) return
          handled.current.add(key)

          const who = fromId || from || 'A Vulos peer'
          const { title, body } = declinedCallNotice(who)
          // Told first, declined second. The notification is the part the user
          // needs and it must not be lost to a signalling failure.
          notify({
            // Same id for the same call, so a redelivery updates in place
            // rather than stacking a second row in the Notification Center.
            id: `peer-call:${key}`,
            title,
            body,
            source: 'peering',
            level: 'warning',
          })
          void declineOnTheWire(callId)
        } catch {
          // malformed frame — ignore
        }
      }

      ws.onclose = () => { if (alive) setTimeout(connect, 3000) }
      ws.onerror = () => ws.close()
    }

    connect()
    return () => {
      alive = false
      wsRef.current?.close()
    }
  }, [])

  return null
}

/**
 * Decline on the wire. The caller's box shows a real decline and stops ringing;
 * this box's relay records the outcome, which is what puts the call in Recents.
 */
async function declineOnTheWire(callId: string): Promise<void> {
  if (!callId) return
  try {
    await fetch('/api/peering/call/reject', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ call_id: callId }),
    })
  } catch {
    // Signalling error — the relay times the session out. The user has already
    // been told about the call, which is the part that matters here.
  }
}
