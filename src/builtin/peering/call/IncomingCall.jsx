/**
 * IncomingCall.jsx — PEER-24
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

// ---------- helpers -------------------------------------------------------

const WS_URL = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/peering/stream`

/** Synthesise a short ringtone with the Web Audio API (no file dependency). */
function useRingtone(ringing) {
  const ctxRef = useRef(null)
  const intervalRef = useRef(null)

  const stopRing = useCallback(() => {
    clearInterval(intervalRef.current)
    intervalRef.current = null
    if (ctxRef.current) {
      ctxRef.current.close()
      ctxRef.current = null
    }
  }, [])

  const playBell = useCallback(() => {
    try {
      if (!ctxRef.current || ctxRef.current.state === 'closed') {
        ctxRef.current = new (window.AudioContext || window.webkitAudioContext)()
      }
      const ctx = ctxRef.current
      if (ctx.state === 'suspended') ctx.resume()

      // Two-tone ring: 880 Hz then 1046 Hz
      const freqs = [880, 1046]
      freqs.forEach((freq, idx) => {
        const osc = ctx.createOscillator()
        const gain = ctx.createGain()
        osc.connect(gain)
        gain.connect(ctx.destination)
        osc.frequency.value = freq
        osc.type = 'sine'
        const t = ctx.currentTime + idx * 0.18
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

function useCallHistory() {
  const [history, setHistory] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await fetch('/api/peering/call/history?limit=50')
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const data = await res.json()
      setHistory(data || [])
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { refresh() }, [refresh])

  return { history, loading, error, refresh }
}

// ---------- status badge --------------------------------------------------

function StatusBadge({ status }) {
  const cfg = {
    completed: { label: 'Completed', cls: 'text-success' },
    missed:    { label: 'Missed',    cls: 'text-danger' },
    rejected:  { label: 'Rejected',  cls: 'text-warning' },
    outgoing:  { label: 'Outgoing',  cls: 'accent-text' },
  }[status] || { label: status, cls: 'text-[var(--text-tertiary)]' }

  return <span className={`text-xs font-medium ${cfg.cls}`}>{cfg.label}</span>
}

// ---------- call history panel --------------------------------------------

function CallHistoryPanel({ onClose }) {
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

function CallHistoryRow({ entry }) {
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

function formatDuration(secs) {
  const m = Math.floor(secs / 60)
  const s = secs % 60
  return m > 0 ? `${m}m ${s}s` : `${s}s`
}

// ---------- incoming call modal -------------------------------------------

function IncomingCallModal({ call, onAccept, onReject }) {
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
              <p className="mt-0.5 text-[11px] text-neutral-500 font-mono truncate max-w-[220px]">{call.peerId}</p>
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
  const [incomingCall, setIncomingCall] = useState(null)   // { callId, peerId, peerDisplay, peerAvatar }
  const [historyOpen, setHistoryOpen] = useState(false)
  const wsRef = useRef(null)

  // Connect to the notification stream and watch for peering call signals.
  useEffect(() => {
    let alive = true

    function connect() {
      if (!alive) return
      const ws = new WebSocket(WS_URL)
      wsRef.current = ws

      ws.onmessage = (e) => {
        try {
          const frame = JSON.parse(e.data)
          // Peering hub wraps every message as { channel, from, payload }.
          // Call signals arrive on the "signal" channel; the inner payload
          // carries { type, call_id, from_id, ... }.
          if (frame.channel !== 'signal') return
          const sig = frame.payload || {}

          if (sig.type === 'incoming-call') {
            setIncomingCall({
              callId:      sig.call_id  || '',
              peerId:      sig.from_id  || frame.from || '',
              peerDisplay: sig.from_id  || frame.from || 'Unknown',
              peerAvatar:  null,
            })
          } else if (sig.type === 'hangup' || sig.type === 'reject') {
            // Remote peer cancelled or we got a reject — dismiss the modal.
            const cancelId = sig.call_id
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

  const handleAccept = useCallback(async () => {
    if (!incomingCall) return
    const { callId } = incomingCall
    setIncomingCall(null)
    try {
      await fetch('/api/peering/call/answer', {
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
