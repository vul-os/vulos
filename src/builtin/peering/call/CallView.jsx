/**
 * CallView — 1-on-1 voice call UI (PEER-22).
 *
 * Renders three distinct layouts based on call state:
 *   idle     — "Start a call" form (enter peer Vula ID)
 *   calling  — outgoing ring screen (waiting for answer)
 *   ringing  — incoming call screen (accept / reject)
 *   active   — in-call HUD (mute / hang up / duration)
 *   ended    — brief "Call ended" flash before returning to idle
 *
 * Props:
 *   myVulaId          {string}          — local user's Vula ID (passed straight through)
 *   peeringWS         {object|null}     — optional usePeering() { send, subscribe }
 *   onClose           {function}        — called when the user dismisses the view
 *   dialTo            {string|null}     — pre-fill the dial field and auto-start (quick-dial from contact card)
 *   onDialToConsumed  {function}        — called after dialTo has been consumed so parent can clear it
 *
 * The hidden <audio> element for remote audio playback is wired via the
 * setRemoteAudio callback from useWebRTCCall.
 */
import { useState, useRef, useCallback, useEffect } from 'react'
import { useWebRTCCall } from './useWebRTCCall'

// ---------------------------------------------------------------------------
// Tiny helpers
// ---------------------------------------------------------------------------

function formatDuration(seconds) {
  const m = Math.floor(seconds / 60)
  const s = seconds % 60
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
}

function shortId(vulaId) {
  if (!vulaId) return 'Unknown'
  // Show display portion or last 12 chars of the vula ID
  const parts = vulaId.split(':')
  if (parts.length >= 3) {
    const key = parts[parts.length - 1]
    return key.slice(0, 8) + '…'
  }
  return vulaId.slice(-12)
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

function Avatar({ label, size = 'lg' }) {
  const dim = size === 'lg' ? 'w-20 h-20 text-3xl' : 'w-12 h-12 text-lg'
  const initial = label ? label[0].toUpperCase() : '?'
  return (
    <div
      className={`${dim} rounded-full bg-neutral-700 flex items-center justify-center font-semibold text-neutral-200 select-none ring-2 ring-neutral-600`}
    >
      {initial}
    </div>
  )
}

function RingAnimation() {
  return (
    <span className="relative flex h-4 w-4">
      <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-green-400 opacity-75" />
      <span className="relative inline-flex rounded-full h-4 w-4 bg-green-500" />
    </span>
  )
}

function CallBtn({ onClick, label, icon, color = 'bg-neutral-700 hover:bg-neutral-600', disabled = false }) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      className={`flex flex-col items-center gap-1 px-4 py-2 rounded-xl transition-colors ${color} disabled:opacity-40 disabled:cursor-not-allowed`}
    >
      <span className="text-2xl leading-none">{icon}</span>
      <span className="text-xs text-neutral-300 font-medium">{label}</span>
    </button>
  )
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export default function CallView({ myVulaId = '', peeringWS = null, onClose, dialTo = null, onDialToConsumed }) {
  const call = useWebRTCCall({ myVulaId, peeringWS })

  // Remote <audio> element ref
  const audioRef = useRef(null)

  // Dial input state
  const [dialTarget, setDialTarget] = useState('')
  const [dialError, setDialError] = useState('')

  // In-call duration counter
  const [duration, setDuration] = useState(0)
  const timerRef = useRef(null)

  // Wire the <audio> element into the hook once mounted
  const setAudioRef = useCallback((el) => {
    audioRef.current = el
    call.setRemoteAudio(el)
  }, [call])

  // Auto-dial when dialTo prop is set (quick-dial from ContactCard).
  // Consume once so navigating back does not re-trigger.
  useEffect(() => {
    if (dialTo && call.state === 'idle') {
      setDialTarget(dialTo)
      onDialToConsumed?.()
      // Small delay to ensure the input is rendered before calling
      const t = setTimeout(() => {
        call.startCall(dialTo)
      }, 50)
      return () => clearTimeout(t)
    }
  }, [dialTo]) // eslint-disable-line react-hooks/exhaustive-deps

  // Start/stop duration timer based on call state
  useEffect(() => {
    if (call.state === 'active') {
      setDuration(0)
      timerRef.current = setInterval(() => setDuration(d => d + 1), 1000)
    } else {
      clearInterval(timerRef.current)
      timerRef.current = null
      if (call.state !== 'active') setDuration(0)
    }
    return () => clearInterval(timerRef.current)
  }, [call.state])

  // Validate and initiate outgoing call
  const handleDial = useCallback(() => {
    const target = dialTarget.trim()
    if (!target) {
      setDialError('Enter a Vula ID to call')
      return
    }
    setDialError('')
    call.startCall(target)
  }, [call, dialTarget])

  const handleKeyDown = useCallback((e) => {
    if (e.key === 'Enter') handleDial()
  }, [handleDial])

  // ---------------------------------------------------------------------------
  // Layout: idle — dial screen
  // ---------------------------------------------------------------------------
  if (call.state === 'idle') {
    return (
      <div className="flex flex-col h-full bg-neutral-900 text-white select-none">
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-neutral-800">
          <h2 className="text-sm font-semibold text-neutral-200">Voice Call</h2>
          {onClose && (
            <button
              onClick={onClose}
              className="text-neutral-500 hover:text-neutral-200 transition-colors text-lg leading-none"
              aria-label="Close"
            >
              ×
            </button>
          )}
        </div>

        {/* Dial form */}
        <div className="flex-1 flex flex-col items-center justify-center gap-5 px-6">
          <Avatar label="?" size="lg" />

          <div className="w-full max-w-xs space-y-2">
            <label className="text-xs text-neutral-500 uppercase tracking-wider">
              Peer Vula ID
            </label>
            <input
              type="text"
              value={dialTarget}
              onChange={e => { setDialTarget(e.target.value); setDialError('') }}
              onKeyDown={handleKeyDown}
              placeholder="vula:ed25519:abc123…"
              className="w-full bg-neutral-800 border border-neutral-700 rounded-lg px-3 py-2 text-sm text-neutral-100 placeholder-neutral-600 focus:outline-none focus:border-blue-500 transition-colors"
              autoComplete="off"
              spellCheck={false}
            />
            {dialError && (
              <p className="text-xs text-red-400">{dialError}</p>
            )}
          </div>

          <button
            onClick={handleDial}
            className="flex items-center gap-2 bg-green-600 hover:bg-green-500 transition-colors text-white font-medium px-6 py-2.5 rounded-full text-sm"
          >
            <span>📞</span> Call
          </button>

          {myVulaId && (
            <p className="text-xs text-neutral-600 text-center break-all max-w-xs">
              Your ID: <span className="text-neutral-500">{myVulaId}</span>
            </p>
          )}
        </div>

        {/* Hidden remote audio */}
        <audio ref={setAudioRef} autoPlay playsInline className="hidden" />
      </div>
    )
  }

  // ---------------------------------------------------------------------------
  // Layout: calling — outgoing ring
  // ---------------------------------------------------------------------------
  if (call.state === 'calling') {
    return (
      <div className="flex flex-col h-full bg-neutral-900 text-white select-none">
        <div className="flex items-center justify-between px-4 py-3 border-b border-neutral-800">
          <h2 className="text-sm font-semibold text-neutral-200">Calling…</h2>
        </div>

        <div className="flex-1 flex flex-col items-center justify-center gap-6 px-6">
          <Avatar label={shortId(call.remoteVulaId)} size="lg" />

          <div className="text-center space-y-1">
            <p className="text-neutral-100 font-medium">{shortId(call.remoteVulaId)}</p>
            <div className="flex items-center justify-center gap-2 text-neutral-500 text-sm">
              <RingAnimation />
              <span>Calling…</span>
            </div>
          </div>

          <CallBtn
            onClick={call.hangUp}
            label="Cancel"
            icon="📵"
            color="bg-red-700 hover:bg-red-600"
          />
        </div>

        <audio ref={setAudioRef} autoPlay playsInline className="hidden" />
      </div>
    )
  }

  // ---------------------------------------------------------------------------
  // Layout: ringing — incoming call
  // ---------------------------------------------------------------------------
  if (call.state === 'ringing') {
    return (
      <div className="flex flex-col h-full bg-neutral-900 text-white select-none">
        <div className="flex items-center justify-between px-4 py-3 border-b border-neutral-800">
          <h2 className="text-sm font-semibold text-neutral-200">Incoming Call</h2>
        </div>

        <div className="flex-1 flex flex-col items-center justify-center gap-6 px-6">
          {/* Pulsing ring effect */}
          <div className="relative">
            <span className="absolute inset-0 rounded-full bg-green-500/20 animate-ping" />
            <Avatar label={shortId(call.remoteVulaId)} size="lg" />
          </div>

          <div className="text-center space-y-1">
            <p className="text-neutral-100 font-medium">{shortId(call.remoteVulaId)}</p>
            <p className="text-neutral-500 text-sm">Incoming voice call</p>
          </div>

          <div className="flex gap-8">
            <CallBtn
              onClick={call.rejectCall}
              label="Decline"
              icon="📵"
              color="bg-red-700 hover:bg-red-600"
            />
            <CallBtn
              onClick={call.acceptCall}
              label="Accept"
              icon="📞"
              color="bg-green-700 hover:bg-green-600"
            />
          </div>
        </div>

        <audio ref={setAudioRef} autoPlay playsInline className="hidden" />
      </div>
    )
  }

  // ---------------------------------------------------------------------------
  // Layout: active — in-call HUD
  // ---------------------------------------------------------------------------
  if (call.state === 'active') {
    return (
      <div className="flex flex-col h-full bg-neutral-900 text-white select-none">
        <div className="flex items-center justify-between px-4 py-3 border-b border-neutral-800">
          <div className="flex items-center gap-2">
            <span className="w-2 h-2 rounded-full bg-green-400 animate-pulse" />
            <h2 className="text-sm font-semibold text-neutral-200">In Call</h2>
          </div>
          <span className="text-xs text-neutral-500 font-mono">{formatDuration(duration)}</span>
        </div>

        <div className="flex-1 flex flex-col items-center justify-center gap-6 px-6">
          <Avatar label={shortId(call.remoteVulaId)} size="lg" />

          <div className="text-center space-y-1">
            <p className="text-neutral-100 font-medium">{shortId(call.remoteVulaId)}</p>
            <p className="text-neutral-500 text-sm">
              {call.isMuted ? '🔇 Muted' : '🎙 Speaking'}
            </p>
          </div>

          <div className="flex gap-6">
            <CallBtn
              onClick={call.toggleMute}
              label={call.isMuted ? 'Unmute' : 'Mute'}
              icon={call.isMuted ? '🔇' : '🎙'}
              color={
                call.isMuted
                  ? 'bg-yellow-700 hover:bg-yellow-600'
                  : 'bg-neutral-700 hover:bg-neutral-600'
              }
            />
            <CallBtn
              onClick={call.hangUp}
              label="Hang Up"
              icon="📵"
              color="bg-red-700 hover:bg-red-600"
            />
          </div>
        </div>

        <audio ref={setAudioRef} autoPlay playsInline className="hidden" />
      </div>
    )
  }

  // ---------------------------------------------------------------------------
  // Layout: ended — brief "Call ended" flash
  // ---------------------------------------------------------------------------
  if (call.state === 'ended') {
    return (
      <div className="flex flex-col h-full bg-neutral-900 text-white select-none">
        <div className="flex items-center justify-between px-4 py-3 border-b border-neutral-800">
          <h2 className="text-sm font-semibold text-neutral-200">Call Ended</h2>
        </div>

        <div className="flex-1 flex flex-col items-center justify-center gap-4 px-6">
          <Avatar label={shortId(call.remoteVulaId)} size="lg" />
          <p className="text-neutral-400 text-sm">Call ended</p>
          {duration > 0 && (
            <p className="text-xs text-neutral-600">{formatDuration(duration)}</p>
          )}
        </div>

        <audio ref={setAudioRef} autoPlay playsInline className="hidden" />
      </div>
    )
  }

  // Fallback (should not be reached)
  return null
}
