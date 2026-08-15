/**
 * IncomingCall.tsx — PEER-24
 *
 * Shell-wide modal that surfaces when a remote peer initiates a call.
 *
 * Signal framing (received via the /api/peering/stream WebSocket hub):
 *   Outer frame:  { channel: "signal", from: "<peerVulosId>", payload: <SignalPayload> }
 *   SignalPayload: { channel: "signal", type: "incoming-call", call_id, from_id, payload? }
 *   Cancellation:  type === "hangup" | "reject"
 *
 * Accept path: POST /api/peering/call/answer  { call_id }
 * Reject path: POST /api/peering/call/reject  { call_id }
 * History:     GET  /api/peering/call/history
 *
 * The component is self-contained — mount it once near the root of the
 * authenticated shell so it can intercept signals regardless of which app
 * is currently in focus.
 */

import { useState, useEffect, useRef, useCallback } from 'react'
import { notify } from '../../../core/notificationStore'

// ---------- helpers -------------------------------------------------------

const WS_URL = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/peering/stream`

// isRecord narrows `unknown` boundary values (the peering WS frame) without
// any/casts — same pattern as src/lib/offlineAuth.ts and
// src/builtin/peering/Messages.tsx.
function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// window.webkitAudioContext is the vendor-prefixed constructor Safari still
// requires; it's absent from lib.dom.d.ts. Augmenting the ambient DOM type
// (rather than casting `window`) fixes this honestly — same idiom as
// StreamViewer.tsx's RTCRtpReceiver.playoutDelayHint augmentation.
declare global {
  interface Window {
    webkitAudioContext?: typeof AudioContext
  }
}

/** Synthesise a short ringtone with the Web Audio API (no file dependency). */
function useRingtone(ringing: boolean): void {
  const ctxRef = useRef<AudioContext | null>(null)
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const stopRing = useCallback(() => {
    if (intervalRef.current) clearInterval(intervalRef.current)
    intervalRef.current = null
    if (ctxRef.current) {
      ctxRef.current.close()
      ctxRef.current = null
    }
  }, [])

  const playBell = useCallback(() => {
    try {
      let ctx = ctxRef.current
      if (!ctx || ctx.state === 'closed') {
        const AudioCtor = window.AudioContext ?? window.webkitAudioContext
        if (!AudioCtor) return
        ctx = new AudioCtor()
        ctxRef.current = ctx
      }
      const activeCtx = ctx
      if (activeCtx.state === 'suspended') activeCtx.resume()

      // Two-tone ring: 880 Hz then 1046 Hz
      const freqs = [880, 1046]
      freqs.forEach((freq, idx) => {
        const osc = activeCtx.createOscillator()
        const gain = activeCtx.createGain()
        osc.connect(gain)
        gain.connect(activeCtx.destination)
        osc.frequency.value = freq
        osc.type = 'sine'
        const t = activeCtx.currentTime + idx * 0.18
        gain.gain.setValueAtTime(0, t)
        gain.gain.linearRampToValueAtTime(0.22, t + 0.04)
        gain.gain.linearRampToValueAtTime(0, t + 0.16)
        osc.start(t)
        osc.stop(t + 0.2)
      })
    } catch {
      // AudioContext not available (test environment, etc.) — silently skip.
    }
  }, [])

  useEffect(() => {
    if (ringing) {
      playBell()
      intervalRef.current = setInterval(playBell, 2200)
    } else {
      stopRing()
    }
    return stopRing
  }, [ringing, playBell, stopRing])
}

// The call history that used to live here — a fetcher, a status badge, a modal
// panel and a permanently-visible round button pinned to the bottom-right
// corner of the desktop — has moved into the Phone app's Recents tab
// (src/builtin/phone/RecentsTab.tsx), where it sits beside the GSM call log in
// one merged, time-ordered list.
//
// The button was the "floating action button" the founder asked to be an app
// instead. It was `fixed bottom-4 right-4 z-[100]`, rendered unconditionally on
// every desktop session — not gated on a call, a peering capability or a flag —
// and its only job was to open a modal over whatever you were doing. Worse, it
// fronted a subsystem that cannot complete a call: nothing in this codebase
// initiates a peer call, and handleAccept below rejects on the wire rather than
// strand the user with no media UI. A permanent piece of shell chrome pointing
// at that is exactly the thing to delete.
//
// What remains here is the one thing that must be shell-wide: the incoming-call
// banner, which has to intercept a signal no matter which app has focus.

// ---------- incoming call modal -------------------------------------------

interface IncomingCallState {
  callId: string
  peerId: string
  peerDisplay: string
  peerAvatar: string | null
}

interface IncomingCallModalProps {
  call: IncomingCallState
  onAccept: () => void
  onReject: () => void
}

function IncomingCallModal({ call, onAccept, onReject }: IncomingCallModalProps) {
  useRingtone(true)

  return (
    <div className="fixed inset-0 z-[300] flex items-center justify-center">
      {/* Blur overlay */}
      <div className="absolute inset-0 bg-black/70 backdrop-blur-md" />

      {/* Card */}
      <div className="relative z-10 w-80 max-w-[calc(100vw-2rem)] bg-neutral-900 border border-neutral-700/60 rounded-3xl shadow-[0_32px_80px_rgba(0,0,0,0.7)] overflow-hidden animate-[fadeScaleIn_0.25s_ease-out] motion-reduce:animate-none">
        {/* Animated ring pulse */}
        <div className="absolute inset-0 pointer-events-none">
          <div className="absolute top-1/4 left-1/2 -translate-x-1/2 -translate-y-1/2 w-48 h-48 rounded-full border accent-border-soft animate-ping motion-reduce:animate-none" />
        </div>

        {/* Avatar / identity */}
        <div className="flex flex-col items-center pt-10 pb-6 px-6 gap-3">
          <div className="relative">
            {call.peerAvatar ? (
              <img
                src={call.peerAvatar}
                alt={call.peerDisplay}
                className="w-20 h-20 rounded-full object-cover ring-2 ring-[color-mix(in_srgb,var(--accent)_40%,transparent)]"
              />
            ) : (
              <div className="w-20 h-20 rounded-full bg-neutral-700 flex items-center justify-center ring-2 ring-[color-mix(in_srgb,var(--accent)_40%,transparent)]">
                <span className="text-3xl text-neutral-300 select-none">
                  {(call.peerDisplay || '?').charAt(0).toUpperCase()}
                </span>
              </div>
            )}
            {/* Ringing indicator dot */}
            <span className="absolute -bottom-1 -right-1 w-4 h-4 rounded-full bg-[var(--status-success)] ring-2 ring-neutral-900 animate-pulse motion-reduce:animate-none" />
          </div>

          <div className="text-center">
            <p className="text-xs text-neutral-500 tracking-wide uppercase">Incoming call</p>
            <h3 className="mt-1 text-lg font-semibold text-neutral-100 truncate max-w-[220px]">
              {call.peerDisplay || call.peerId || 'Unknown peer'}
            </h3>
            {call.peerId && call.peerId !== call.peerDisplay && (
              <p className="mt-0.5 text-[12px] text-neutral-500 font-mono truncate max-w-[220px]">{call.peerId}</p>
            )}
          </div>
        </div>

        {/* Accept / Reject */}
        <div className="flex items-center justify-center gap-8 px-6 pb-8">
          {/* Reject */}
          <button
            onClick={onReject}
            className="flex flex-col items-center gap-2 group"
            aria-label="Reject call"
          >
            <span className="w-14 h-14 rounded-full bg-[var(--status-danger)] hover:brightness-110 flex items-center justify-center shadow-lg transition-[filter,transform] group-hover:scale-105 active:scale-95 motion-reduce:group-hover:scale-100">
              <svg viewBox="0 0 24 24" className="w-6 h-6 text-white" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <path d="M10.68 13.31a16 16 0 003.41 2.6l1.27-1.27a2 2 0 012.11-.45 12.84 12.84 0 002.81.7 2 2 0 011.72 2v3a2 2 0 01-2.18 2 19.79 19.79 0 01-8.63-3.07A19.42 19.42 0 013.43 5.39 2 2 0 015.41 3h3a2 2 0 012 1.72 12.84 12.84 0 00.7 2.81 2 2 0 01-.45 2.11L9.39 10.9" />
                <line x1="23" y1="1" x2="1" y2="23" />
              </svg>
            </span>
            <span className="text-xs text-neutral-400">Decline</span>
          </button>

          {/* Accept */}
          <button
            onClick={onAccept}
            className="flex flex-col items-center gap-2 group"
            aria-label="Accept call"
          >
            <span className="w-14 h-14 rounded-full bg-[var(--status-success)] hover:brightness-110 flex items-center justify-center shadow-lg transition-[filter,transform] group-hover:scale-105 active:scale-95 motion-reduce:group-hover:scale-100">
              <svg viewBox="0 0 24 24" className="w-6 h-6 text-white" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <path d="M22 16.92v3a2 2 0 01-2.18 2 19.79 19.79 0 01-8.63-3.07 19.5 19.5 0 01-6-6 19.79 19.79 0 01-3.07-8.67A2 2 0 014.11 2h3a2 2 0 012 1.72 12.84 12.84 0 00.7 2.81 2 2 0 01-.45 2.11L8.09 9.91a16 16 0 006 6l1.27-1.27a2 2 0 012.11-.45 12.84 12.84 0 002.81.7A2 2 0 0122 16.92z" />
              </svg>
            </span>
            <span className="text-xs text-neutral-300 font-medium">Answer</span>
          </button>
        </div>
      </div>
    </div>
  )
}

// ---------- main export ---------------------------------------------------

/**
 * IncomingCall
 *
 * Mount once inside the authenticated shell, at a high z-index layer.
 * Handles incoming call signals and exposes a call-history toggle button.
 *
 * Usage:
 *   <IncomingCall />
 *
 * The component is fully self-contained — it opens its own WebSocket
 * connection to the notification stream and manages all state internally.
 */
export default function IncomingCall() {
  const [incomingCall, setIncomingCall] = useState<IncomingCallState | null>(null)
  const wsRef = useRef<WebSocket | null>(null)

  // Connect to the notification stream and watch for peering call signals.
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
          const from = typeof parsed.from === 'string' ? parsed.from : ''

          if (sig.type === 'incoming-call') {
            const callId = typeof sig.call_id === 'string' ? sig.call_id : ''
            const fromId = typeof sig.from_id === 'string' ? sig.from_id : ''
            setIncomingCall({
              callId,
              peerId:      fromId || from,
              peerDisplay: fromId || from || 'Unknown',
              peerAvatar:  null,
            })
          } else if (sig.type === 'hangup' || sig.type === 'reject') {
            // Remote peer cancelled or we got a reject — dismiss the modal.
            const cancelId = typeof sig.call_id === 'string' ? sig.call_id : undefined
            setIncomingCall((cur) =>
              cur && cur.callId === cancelId ? null : cur
            )
          }
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

  // KNOWN BREAKAGE (peer calling is non-functional end-to-end).
  //
  // There is NO media surface at all. This comment used to say the surface was
  // CallView (./CallView.jsx) and that it "still exists but is orphaned" — it
  // does not exist; neither does Peering.jsx. Both are gone, and so is
  // useMeshCall. Accepting a call would dismiss the banner and mount nothing.
  //
  // Nothing initiates a peer call either: no code anywhere in frontend/ posts
  // to /api/peering/call/{initiate,signal,hangup}, and there is no
  // RTCPeerConnection. Peer calling is a signalling backend with no client.
  //
  // Fixing this is a product decision — build the media surface, or retire peer
  // calling and remove this component. Until then the honest behaviour is
  // below: decline on the wire so the caller sees a real decline rather than a
  // call that rings forever, and say why. The call HISTORY this feature does
  // produce is rendered in the Phone app's Recents tab.
  //
  // Interim honesty fix: until CallView is remounted, "Answer" must not
  // silently accept-then-strand the user with no media UI. Decline on the
  // wire instead (the caller sees a real decline rather than a call that
  // rings forever) and tell the user why via the shell notification store.
  const handleAccept = useCallback(async () => {
    if (!incomingCall) return
    const { callId, peerDisplay, peerId } = incomingCall
    setIncomingCall(null)
    notify({
      title: 'Calling is unavailable',
      body: `Couldn't connect the call from ${peerDisplay || peerId || 'this peer'} — voice/video calling isn't wired up yet.`,
      source: 'peering',
      level: 'warning',
    })
    try {
      await fetch('/api/peering/call/reject', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ call_id: callId }),
      })
    } catch {
      // Signalling error — backend will handle timeout/cleanup.
    }
  }, [incomingCall])

  const handleReject = useCallback(async () => {
    if (!incomingCall) return
    const { callId } = incomingCall
    setIncomingCall(null)
    try {
      await fetch('/api/peering/call/reject', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ call_id: callId }),
      })
    } catch { /* noop */ }
  }, [incomingCall])

  return (
    <>
      {/* Incoming call modal — rendered at z-[300] above all other shell chrome */}
      {incomingCall && (
        <IncomingCallModal
          call={incomingCall}
          onAccept={handleAccept}
          onReject={handleReject}
        />
      )}

    </>
  )
}
