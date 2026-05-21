/**
 * useWebRTCCall — 1-on-1 browser-to-browser WebRTC audio call hook (PEER-22).
 *
 * Signaling channel: peering WS hub at /api/peering/stream on the "signal"
 * channel.  If src/core/usePeering.js is available (PEER-05) it should be
 * wired in by the caller; this hook accepts an optional `peeringWS` object
 * exposing { send, subscribe } so the caller can pass usePeering() directly.
 * When peeringWS is null, the hook opens its own lightweight WS to the same
 * endpoint with identical framing.
 *
 * ICE config: fetched once per call from GET /api/peering/ice (PEER-21).
 *
 * Signal frame format (mirrors PEER-19 call.go):
 *   {
 *     channel: "signal",
 *     type:    "incoming-call" | "answer" | "reject" | "hangup" | "signal",
 *     call_id: string,
 *     from_id: string,
 *     payload: { sdp?, type?, candidate? }   // opaque SDP / ICE blob
 *   }
 *
 * REST endpoints used (PEER-19):
 *   POST /api/peering/call/initiate  { call_id, callee_id }
 *   POST /api/peering/call/answer    { call_id }
 *   POST /api/peering/call/reject    { call_id }
 *   POST /api/peering/call/hangup    { call_id }
 *   POST /api/peering/call/signal    { call_id, peer_id, payload }
 *
 * Usage:
 *   const call = useWebRTCCall({ myVulaId, peeringWS })
 *   call.startCall(peerId)      // outgoing
 *   call.acceptCall()           // incoming
 *   call.rejectCall()
 *   call.hangUp()
 *   call.toggleMute()
 *   call.state   // 'idle' | 'calling' | 'ringing' | 'active' | 'ended'
 *   call.isMuted
 *   call.remoteVulaId
 *   call.callId
 */
import { useState, useRef, useEffect, useCallback } from 'react'

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const SIGNAL_CHANNEL = 'signal'
const WS_URL = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/peering/stream`
const ICE_CONFIG_URL = '/api/peering/ice'
const MAX_WS_BACKOFF_MS = 30_000

// ICE restart is attempted when the connection drops while in an active call.
const ICE_RESTART_MAX_ATTEMPTS = 3

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function generateCallId() {
  // Compact UUID-v4-ish — browser crypto where available
  if (typeof crypto !== 'undefined' && crypto.randomUUID) return crypto.randomUUID()
  return ([1e7] + -1e3 + -4e3 + -8e3 + -1e11).replace(/[018]/g, c =>
    (c ^ (crypto.getRandomValues(new Uint8Array(1))[0] & (15 >> (c / 4)))).toString(16)
  )
}

async function fetchICEConfig() {
  try {
    const res = await fetch(ICE_CONFIG_URL, { credentials: 'include' })
    if (!res.ok) throw new Error(`ICE config ${res.status}`)
    const data = await res.json()
    // Backend returns { ice_servers: [...RTCIceServer] }
    return data.ice_servers || []
  } catch (err) {
    console.warn('[useWebRTCCall] fetchICEConfig failed, using Google STUN:', err)
    return [{ urls: 'stun:stun.l.google.com:19302' }]
  }
}

async function postSignal(path, body) {
  const res = await fetch(path, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(`${path} → ${res.status}: ${text}`)
  }
  return res.json()
}

// ---------------------------------------------------------------------------
// Internal lightweight WS (used when peeringWS is not supplied)
// ---------------------------------------------------------------------------

class InternalPeeringWS {
  constructor() {
    this._ws = null
    this._handlers = new Map()   // channel → Set<fn>
    this._retryCount = 0
    this._alive = true
    this._connect()
  }

  _connect() {
    if (!this._alive) return
    if (this._ws && this._ws.readyState < WebSocket.CLOSING) return

    const ws = new WebSocket(WS_URL)
    this._ws = ws

    ws.onopen = () => { this._retryCount = 0 }

    ws.onmessage = (event) => {
      let frame
      try { frame = JSON.parse(event.data) } catch { return }
      if (!frame || !frame.channel) return
      const set = this._handlers.get(frame.channel)
      if (set) for (const fn of set) { try { fn(frame) } catch {} }
      const all = this._handlers.get('*')
      if (all) for (const fn of all) { try { fn(frame) } catch {} }
    }

    ws.onclose = () => {
      this._ws = null
      if (!this._alive) return
      const delay = Math.min(1000 * 2 ** this._retryCount, MAX_WS_BACKOFF_MS)
      this._retryCount = Math.min(this._retryCount + 1, 10)
      setTimeout(() => this._connect(), delay)
    }

    ws.onerror = () => ws.close()
  }

  subscribe(channel, handler) {
    if (!this._handlers.has(channel)) this._handlers.set(channel, new Set())
    this._handlers.get(channel).add(handler)
    return () => {
      const s = this._handlers.get(channel)
      if (s) s.delete(handler)
    }
  }

  send(frame) {
    const ws = this._ws
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    try { ws.send(JSON.stringify(frame)) } catch {}
  }

  close() {
    this._alive = false
    this._ws?.close()
  }
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

/**
 * @param {object} opts
 * @param {string}  opts.myVulaId   - local user's Vula ID (used in signal frames)
 * @param {object} [opts.peeringWS] - optional usePeering() result: { send, subscribe }
 */
export function useWebRTCCall({ myVulaId, peeringWS = null } = {}) {
  // ── State ────────────────────────────────────────────────────────────────
  const [state, setState] = useState('idle')    // idle|calling|ringing|active|ended
  const [isMuted, setIsMuted] = useState(false)
  const [remoteVulaId, setRemoteVulaId] = useState(null)
  const [callId, setCallId] = useState(null)

  // ── Refs (avoid stale closures in async callbacks) ───────────────────────
  const pcRef = useRef(null)               // RTCPeerConnection
  const localStreamRef = useRef(null)     // MediaStream (microphone)
  const remoteAudioRef = useRef(null)     // <audio> element — caller passes via setRemoteAudio
  const remoteVideoRef = useRef(null)     // <video> element — caller passes via setRemoteVideo (PEER-23)
  const internalWSRef = useRef(null)      // InternalPeeringWS instance (if peeringWS not given)
  const unsubRef = useRef(null)           // cleanup fn for signal subscription
  const callIdRef = useRef(null)          // mirrors callId state (sync access in callbacks)
  const remoteVulaIdRef = useRef(null)    // mirrors remoteVulaId state
  const stateRef = useRef('idle')         // mirrors state (sync access in callbacks)
  const iceRestartAttemptsRef = useRef(0)
  const pendingCandidatesRef = useRef([]) // ICE candidates queued before remoteDescription is set
  const makingOfferRef = useRef(false)    // perfect-negotiation "polite peer" flag

  // ── Sync refs with state ─────────────────────────────────────────────────
  useEffect(() => { stateRef.current = state }, [state])
  useEffect(() => { callIdRef.current = callId }, [callId])
  useEffect(() => { remoteVulaIdRef.current = remoteVulaId }, [remoteVulaId])

  // ── WS send/subscribe (delegates to peeringWS or internal) ───────────────
  const getWS = useCallback(() => {
    if (peeringWS) return peeringWS
    if (!internalWSRef.current) {
      internalWSRef.current = new InternalPeeringWS()
    }
    return internalWSRef.current
  }, [peeringWS])

  const sendFrame = useCallback((frame) => {
    getWS().send(frame)
  }, [getWS])

  // ── Relay SDP/ICE opaque payload to the server ────────────────────────────
  const relaySignal = useCallback(async (payload) => {
    try {
      await postSignal('/api/peering/call/signal', {
        call_id: callIdRef.current,
        peer_id: remoteVulaIdRef.current,
        payload,
      })
    } catch (err) {
      console.error('[useWebRTCCall] relaySignal failed:', err)
    }
  }, [])

  // ── Drain queued ICE candidates once remoteDescription is set ────────────
  const drainCandidates = useCallback(async (pc) => {
    for (const candidate of pendingCandidatesRef.current) {
      try { await pc.addIceCandidate(new RTCIceCandidate(candidate)) } catch {}
    }
    pendingCandidatesRef.current = []
  }, [])

  // ── Build RTCPeerConnection ───────────────────────────────────────────────
  const createPC = useCallback(async () => {
    const iceServers = await fetchICEConfig()
    const pc = new RTCPeerConnection({ iceServers, bundlePolicy: 'max-bundle' })
    pcRef.current = pc

    // Send ICE candidates to the remote peer via signaling server
    pc.onicecandidate = ({ candidate }) => {
      if (candidate) {
        relaySignal({ type: 'candidate', candidate: candidate.toJSON() })
      }
    }

    // Remote tracks — audio + video (PEER-23 adds video path)
    pc.ontrack = ({ track, streams }) => {
      if (!streams[0]) return
      if (track.kind === 'audio') {
        if (remoteAudioRef.current) {
          remoteAudioRef.current.srcObject = streams[0]
          remoteAudioRef.current.play().catch(() => {})
        }
      } else if (track.kind === 'video') {
        if (remoteVideoRef.current) {
          remoteVideoRef.current.srcObject = streams[0]
          remoteVideoRef.current.play().catch(() => {})
        }
      }
    }

    // ICE restart on transient drop
    pc.oniceconnectionstatechange = () => {
      const s = pc.iceConnectionState
      if (s === 'failed') {
        if (
          stateRef.current === 'active' &&
          iceRestartAttemptsRef.current < ICE_RESTART_MAX_ATTEMPTS
        ) {
          iceRestartAttemptsRef.current++
          console.info('[useWebRTCCall] ICE failed, attempting restart', iceRestartAttemptsRef.current)
          triggerICERestart(pc)
        } else {
          console.warn('[useWebRTCCall] ICE failed permanently, hanging up')
          hangUp()
        }
      } else if (s === 'connected' || s === 'completed') {
        iceRestartAttemptsRef.current = 0
      }
    }

    pc.onconnectionstatechange = () => {
      const s = pc.connectionState
      if (s === 'connected') {
        setState('active')
      } else if (s === 'disconnected' || s === 'failed') {
        if (stateRef.current === 'active') hangUp()
      }
    }

    return pc
  }, [relaySignal]) // eslint-disable-line react-hooks/exhaustive-deps

  // ── ICE restart ──────────────────────────────────────────────────────────
  const triggerICERestart = useCallback(async (pc) => {
    if (!pc || pc.signalingState === 'closed') return
    try {
      makingOfferRef.current = true
      const offer = await pc.createOffer({ iceRestart: true })
      await pc.setLocalDescription(offer)
      relaySignal({ type: 'offer', sdp: offer.sdp })
    } catch (err) {
      console.error('[useWebRTCCall] ICE restart failed:', err)
    } finally {
      makingOfferRef.current = false
    }
  }, [relaySignal])

  // ── Acquire microphone ───────────────────────────────────────────────────
  const acquireMic = useCallback(async () => {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: false })
    localStreamRef.current = stream
    return stream
  }, [])

  // ── Attach local stream tracks to PC ─────────────────────────────────────
  const addLocalTracks = useCallback((pc, stream) => {
    for (const track of stream.getAudioTracks()) {
      pc.addTrack(track, stream)
    }
  }, [])

  // ── Clean up PC and local stream ─────────────────────────────────────────
  const cleanup = useCallback(() => {
    pcRef.current?.close()
    pcRef.current = null
    localStreamRef.current?.getTracks().forEach(t => t.stop())
    localStreamRef.current = null
    pendingCandidatesRef.current = []
    makingOfferRef.current = false
    iceRestartAttemptsRef.current = 0
  }, [])

  // ── Handle incoming signal frames (SDP/ICE) ───────────────────────────────
  const handleSignalFrame = useCallback(async (frame) => {
    const { type, call_id, from_id, payload } = frame

    // Only handle frames for our active call
    if (call_id && callIdRef.current && call_id !== callIdRef.current) return

    // ── Incoming call ────────────────────────────────────────────────────────
    if (type === 'incoming-call') {
      if (stateRef.current !== 'idle') {
        // Already in a call — auto-reject
        await postSignal('/api/peering/call/reject', { call_id }).catch(() => {})
        return
      }
      callIdRef.current = call_id
      setCallId(call_id)
      remoteVulaIdRef.current = from_id
      setRemoteVulaId(from_id)
      setState('ringing')
      return
    }

    // ── Answer received (we are the caller) ──────────────────────────────────
    if (type === 'answer' && stateRef.current === 'calling') {
      // payload contains the SDP answer
      if (payload?.sdp && pcRef.current) {
        await pcRef.current.setRemoteDescription(
          new RTCSessionDescription({ type: 'answer', sdp: payload.sdp })
        )
        await drainCandidates(pcRef.current)
      }
      return
    }

    // ── Reject received ──────────────────────────────────────────────────────
    if (type === 'reject') {
      cleanup()
      setState('ended')
      setTimeout(() => setState('idle'), 2000)
      return
    }

    // ── Hangup received ──────────────────────────────────────────────────────
    if (type === 'hangup') {
      cleanup()
      setState('ended')
      setTimeout(() => setState('idle'), 2000)
      return
    }

    // ── Opaque SDP / ICE candidate relay ─────────────────────────────────────
    if (type === 'signal' && payload) {
      const pc = pcRef.current
      if (!pc) return

      // ICE candidate
      if (payload.type === 'candidate' && payload.candidate) {
        if (pc.remoteDescription) {
          try { await pc.addIceCandidate(new RTCIceCandidate(payload.candidate)) } catch {}
        } else {
          // Queue until remoteDescription is set
          pendingCandidatesRef.current.push(payload.candidate)
        }
        return
      }

      // SDP offer (could be an ICE restart from the remote side)
      if (payload.type === 'offer' && payload.sdp) {
        const offerCollision =
          makingOfferRef.current ||
          (pc.signalingState !== 'stable')

        // Polite peer: we are the callee (ringing/active) — yield to offer
        const isPolite = stateRef.current !== 'calling'
        if (offerCollision && !isPolite) return

        await pc.setRemoteDescription(new RTCSessionDescription({ type: 'offer', sdp: payload.sdp }))
        await drainCandidates(pc)
        const answer = await pc.createAnswer()
        await pc.setLocalDescription(answer)
        // Send SDP answer back via REST relay
        await postSignal('/api/peering/call/signal', {
          call_id: callIdRef.current,
          peer_id: from_id || remoteVulaIdRef.current,
          payload: { type: 'answer', sdp: answer.sdp },
        }).catch(() => {})
        return
      }

      // SDP answer (edge case: received via signal channel rather than REST ACK)
      if (payload.type === 'answer' && payload.sdp) {
        if (pc.signalingState === 'have-local-offer') {
          await pc.setRemoteDescription(new RTCSessionDescription({ type: 'answer', sdp: payload.sdp }))
          await drainCandidates(pc)
        }
      }
    }
  }, [cleanup, drainCandidates]) // eslint-disable-line react-hooks/exhaustive-deps

  // ── Subscribe to the signal channel ──────────────────────────────────────
  useEffect(() => {
    const ws = getWS()
    const unsub = ws.subscribe(SIGNAL_CHANNEL, handleSignalFrame)
    unsubRef.current = unsub
    return () => {
      unsub()
      // Only destroy internal WS on full unmount (not on re-render)
      if (!peeringWS) {
        internalWSRef.current?.close()
        internalWSRef.current = null
      }
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // ── Public API ────────────────────────────────────────────────────────────

  /**
   * startCall(calleeVulaId) — initiate an outgoing call.
   */
  const startCall = useCallback(async (calleeVulaId) => {
    if (stateRef.current !== 'idle') return
    const id = generateCallId()
    callIdRef.current = id
    setCallId(id)
    remoteVulaIdRef.current = calleeVulaId
    setRemoteVulaId(calleeVulaId)
    setState('calling')
    setIsMuted(false)

    try {
      const stream = await acquireMic()
      const pc = await createPC()
      addLocalTracks(pc, stream)

      // Inform server — it notifies the callee's server (PEER-19)
      await postSignal('/api/peering/call/initiate', {
        call_id: id,
        callee_id: calleeVulaId,
      })

      // Create and send the SDP offer
      makingOfferRef.current = true
      const offer = await pc.createOffer()
      await pc.setLocalDescription(offer)
      makingOfferRef.current = false

      await postSignal('/api/peering/call/signal', {
        call_id: id,
        peer_id: calleeVulaId,
        payload: { type: 'offer', sdp: offer.sdp },
      })
    } catch (err) {
      console.error('[useWebRTCCall] startCall failed:', err)
      makingOfferRef.current = false
      cleanup()
      setState('idle')
    }
  }, [acquireMic, addLocalTracks, cleanup, createPC])

  /**
   * acceptCall() — accept the incoming ringing call.
   */
  const acceptCall = useCallback(async () => {
    if (stateRef.current !== 'ringing') return
    setState('active')
    setIsMuted(false)

    try {
      const stream = await acquireMic()
      const pc = await createPC()
      addLocalTracks(pc, stream)

      // Tell the server we answered — it relays to the caller's server
      await postSignal('/api/peering/call/answer', {
        call_id: callIdRef.current,
      })

      // The SDP offer arrives via the signal channel (handleSignalFrame handles it)
    } catch (err) {
      console.error('[useWebRTCCall] acceptCall failed:', err)
      cleanup()
      setState('idle')
    }
  }, [acquireMic, addLocalTracks, cleanup, createPC])

  /**
   * rejectCall() — reject an incoming ringing call.
   */
  const rejectCall = useCallback(async () => {
    if (stateRef.current !== 'ringing') return
    try {
      await postSignal('/api/peering/call/reject', {
        call_id: callIdRef.current,
      })
    } catch (err) {
      console.error('[useWebRTCCall] rejectCall failed:', err)
    } finally {
      cleanup()
      setState('idle')
    }
  }, [cleanup])

  /**
   * hangUp() — end an active (or ringing/calling) call.
   */
  const hangUp = useCallback(async () => {
    const currentState = stateRef.current
    if (currentState === 'idle' || currentState === 'ended') return
    try {
      await postSignal('/api/peering/call/hangup', {
        call_id: callIdRef.current,
      })
    } catch (err) {
      console.error('[useWebRTCCall] hangUp failed:', err)
    } finally {
      cleanup()
      setState('ended')
      setTimeout(() => {
        setCallId(null)
        setRemoteVulaId(null)
        setState('idle')
      }, 2000)
    }
  }, [cleanup])

  /**
   * toggleMute() — mute / unmute the local microphone.
   */
  const toggleMute = useCallback(() => {
    const stream = localStreamRef.current
    if (!stream) return
    const newMuted = !isMuted
    for (const track of stream.getAudioTracks()) {
      track.enabled = !newMuted
    }
    setIsMuted(newMuted)
  }, [isMuted])

  /**
   * setRemoteAudio(audioElement) — attach an <audio> element for remote audio.
   * Call this from CallView via a ref callback.
   */
  const setRemoteAudio = useCallback((audioEl) => {
    remoteAudioRef.current = audioEl
  }, [])

  // PEER-23: allow CallView to attach a <video> element for remote video.
  const setRemoteVideo = useCallback((videoEl) => {
    remoteVideoRef.current = videoEl
  }, [])

  // ── Cleanup on unmount ────────────────────────────────────────────────────
  useEffect(() => {
    return () => {
      cleanup()
    }
  }, [cleanup])

  return {
    // State
    state,
    isMuted,
    remoteVulaId,
    callId,
    connected: state === 'active',  // PEER-23: consumed by useVideoCall

    // Refs — exposed for useVideoCall (PEER-23)
    peerConnectionRef: pcRef,

    // Actions
    startCall,
    acceptCall,
    rejectCall,
    hangUp,
    toggleMute,
    setRemoteAudio,
    setRemoteVideo,  // PEER-23
  }
}
