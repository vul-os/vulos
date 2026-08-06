/**
 * IncomingCall.tsx — PEER-24
 *
 * Shell-wide modal that surfaces when a remote peer initiates a call.
 *
 * Signal framing (received via the /api/peering/stream WebSocket hub):
 *   Outer frame:  { channel: "signal", from: "<peerVulaId>", payload: <SignalPayload> }
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

// isRecord/errMessage narrow `unknown` boundary values (fetch JSON bodies,
// the peering WS frame, caught errors under strict's useUnknownInCatchVariables)
// without any/casts — same pattern as src/lib/offlineAuth.ts and
// src/builtin/peering/Messages.tsx.
function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function errMessage(e: unknown, fallback: string): string {
  return (isRecord(e) && typeof e.message === 'string' && e.message) || fallback
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

// ---------- call history fetcher ------------------------------------------

// Backend shape (GET /api/peering/call/history) — narrowed field-by-field
// from `unknown` rather than trusted, since it comes straight off the wire.
interface CallHistoryEntry {
  id: string
  direction: string // 'inbound' | 'outbound'
  started_at: string | null
  duration_sec: number
  peer_display?: string
  peer_id?: string
  status: string // 'completed' | 'missed' | 'rejected' | 'outgoing' | other
}

function toCallHistoryEntry(x: unknown): CallHistoryEntry | null {
  if (!isRecord(x)) return null
  return {
    id: typeof x.id === 'string' ? x.id : '',
    direction: typeof x.direction === 'string' ? x.direction : '',
    started_at: typeof x.started_at === 'string' ? x.started_at : null,
    duration_sec: typeof x.duration_sec === 'number' ? x.duration_sec : 0,
    peer_display: typeof x.peer_display === 'string' ? x.peer_display : undefined,
    peer_id: typeof x.peer_id === 'string' ? x.peer_id : undefined,
    status: typeof x.status === 'string' ? x.status : '',
  }
}

function useCallHistory() {
  const [history, setHistory] = useState<CallHistoryEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await fetch('/api/peering/call/history?limit=50')
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data: unknown = await res.json()
      const list = Array.isArray(data) ? data : []
      setHistory(list.map(toCallHistoryEntry).filter((e): e is CallHistoryEntry => e !== null))
    } catch (err) {
      setError(errMessage(err, 'Failed to load history'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { refresh() }, [refresh])

  return { history, loading, error, refresh }
}

// ---------- status badge --------------------------------------------------

const STATUS_CFG: Record<string, { label: string; cls: string }> = {
  completed: { label: 'Completed', cls: 'text-success' },
  missed:    { label: 'Missed',    cls: 'text-danger' },
  rejected:  { label: 'Rejected',  cls: 'text-warning' },
  outgoing:  { label: 'Outgoing',  cls: 'accent-text' },
}

function StatusBadge({ status }: { status: string }) {
  const cfg = STATUS_CFG[status] || { label: status, cls: 'text-[var(--text-tertiary)]' }

  return <span className={`text-xs font-medium ${cfg.cls}`}>{cfg.label}</span>
}

// ---------- call history panel --------------------------------------------

function CallHistoryPanel({ onClose }: { onClose: () => void }) {
  const { history, loading, error, refresh } = useCallHistory()

  return (
    <div className="fixed inset-0 z-[200] flex items-center justify-center bg-black/60 backdrop-blur-sm">
      <div className="w-full max-w-md mx-4 bg-neutral-900 border border-neutral-700/60 rounded-2xl shadow-2xl overflow-hidden">
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-neutral-800">
          <h2 className="text-sm font-semibold text-neutral-100">Call History</h2>
          <button
            onClick={onClose}
            className="w-6 h-6 flex items-center justify-center rounded-full text-neutral-500 hover:text-neutral-200 hover:bg-neutral-700/60 transition-colors text-lg leading-none"
            aria-label="Close call history"
          >
            {'×'}
          </button>
        </div>

        {/* Body */}
        <div className="max-h-[60vh] overflow-y-auto">
          {loading && (
            <div className="flex justify-center items-center py-10">
              <span className="w-5 h-5 spinner" />
            </div>
          )}
          {error && (
            <div className="px-5 py-4 text-sm text-danger">
              Failed to load history: {error}
              <button onClick={refresh} className="ml-2 underline hover:no-underline">Retry</button>
            </div>
          )}
          {!loading && !error && history.length === 0 && (
            <div className="px-5 py-10 text-center text-sm text-neutral-500">No call history yet.</div>
          )}
          {!loading && !error && history.map((entry) => (
            <CallHistoryRow key={entry.id} entry={entry} />
          ))}
        </div>

        {/* Footer */}
        <div className="px-5 py-3 border-t border-neutral-800 flex justify-end">
          <button
            onClick={onClose}
            className="px-4 py-1.5 text-sm bg-neutral-800 hover:bg-neutral-700 text-neutral-300 rounded-lg transition-colors"
          >
            Close
          </button>
        </div>
      </div>
    </div>
  )
}

function CallHistoryRow({ entry }: { entry: CallHistoryEntry }) {
  const isInbound = entry.direction === 'inbound'
  const started = entry.started_at ? new Date(entry.started_at) : null
  const dateStr = started
    ? started.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
    : '--'
  const durStr = entry.duration_sec > 0
    ? formatDuration(entry.duration_sec)
    : null

  return (
    <div className="flex items-center gap-3 px-5 py-3 border-b border-neutral-800/60 hover:bg-neutral-800/30 transition-colors">
      {/* Direction arrow */}
      <div className={`w-7 h-7 rounded-full flex items-center justify-center shrink-0 ${isInbound ? 'accent-bg-soft' : 'bg-neutral-800'}`}>
        {isInbound ? (
          <svg viewBox="0 0 16 16" className="w-3.5 h-3.5 accent-text" fill="none" stroke="currentColor" strokeWidth="1.5">
            <path d="M3 3l10 10M13 3v10H3" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        ) : (
          <svg viewBox="0 0 16 16" className="w-3.5 h-3.5 text-neutral-400" fill="none" stroke="currentColor" strokeWidth="1.5">
            <path d="M13 13L3 3M3 13V3h10" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        )}
      </div>

      {/* Info */}
      <div className="flex-1 min-w-0">
        <div className="text-sm text-neutral-200 truncate font-medium">
          {entry.peer_display || entry.peer_id || 'Unknown'}
        </div>
        <div className="flex items-center gap-2 mt-0.5">
          <StatusBadge status={entry.status} />
          {durStr && <span className="text-xs text-neutral-500">{durStr}</span>}
        </div>
      </div>

      {/* Date */}
      <div className="text-xs text-neutral-500 shrink-0">{dateStr}</div>
    </div>
  )
}

function formatDuration(secs: number): string {
  const m = Math.floor(secs / 60)
  const s = secs % 60
  return m > 0 ? `${m}m ${s}s` : `${s}s`
}

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
  const [historyOpen, setHistoryOpen] = useState(false)
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
  // This accepts the call on the wire and then dismisses the banner without
  // mounting any media UI, so the user answers and sees nothing happen. The
  // media surface is CallView (./CallView.jsx), which still exists but is
  // orphaned: its only importer was Peering.jsx, and Peering.jsx was dropped
  // from the builtin registry when Messages replaced it. Nothing renders a
  // call.
  //
  // Fixing this is a product decision — restore the peering surface, or mount
  // CallView from DesktopCanvas alongside this banner, or retire peer calling
  // and remove this component. Do NOT "fix" it by deleting CallView; that
  // makes the breakage permanent rather than latent.
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

      {/* Call history toggle — small persistent button in the shell (bottom-right area) */}
      <button
        onClick={() => setHistoryOpen(true)}
        title="Call history"
        aria-label="View call history"
        className="fixed bottom-4 right-4 z-[100] w-9 h-9 rounded-full bg-neutral-800/80 hover:bg-neutral-700/90 border border-neutral-700/50 flex items-center justify-center backdrop-blur-sm shadow-lg transition-colors"
      >
        <svg viewBox="0 0 24 24" className="w-4 h-4 text-neutral-400" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
          <path d="M22 16.92v3a2 2 0 01-2.18 2 19.79 19.79 0 01-8.63-3.07 19.5 19.5 0 01-6-6 19.79 19.79 0 01-3.07-8.67A2 2 0 014.11 2h3a2 2 0 012 1.72 12.84 12.84 0 00.7 2.81 2 2 0 01-.45 2.11L8.09 9.91a16 16 0 006 6l1.27-1.27a2 2 0 012.11-.45 12.84 12.84 0 002.81.7A2 2 0 0122 16.92z" />
          <polyline points="8 12 12 12 12 16" />
          <line x1="3" y1="3" x2="12" y2="12" />
        </svg>
      </button>

      {/* Call history panel */}
      {historyOpen && (
        <CallHistoryPanel onClose={() => setHistoryOpen(false)} />
      )}
    </>
  )
}
