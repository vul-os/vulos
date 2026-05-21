import { useState, useRef, useCallback, useEffect } from 'react'

// ---------------------------------------------------------------------------
// useSFUCall — SFU-hosted multi-party WebRTC call hook (PEER-29)
//
// Architecture:
//   Browser → POST /api/sfu/rooms/{id}/join  (SDP offer → answer)
//   Browser → POST /api/sfu/rooms/{id}/ice   (trickle ICE candidates)
//   Signaling for host-loss / handoff → /api/peering/call/signal WS
//
// Host handoff:
//   The "host" is the Vula instance running the SFU process.  When the host's
//   connection drops the signaling WS emits a { type: 'sfu-host-lost' } frame.
//   useSFUCall re-fetches the new host URL from the server (the backend
//   HandoffManager elects the best-upload peer) and reconnects all browsers
//   within a few seconds — no full call drop.
//
// 50-participant cap:
//   join() rejects early if participantCount >= MAX_SFU_PARTICIPANTS.
//   The backend Room.Join also enforces this cap; the client guard saves an
//   SDP round-trip for the 51st joiner.
//
// Usage:
//   const {
//     localStream,       // MediaStream — own camera/mic
//     participants,      // Map<participantId, { stream, state, bandwidth }>
//     joined,            // bool
//     participantCount,  // number — current participant count (includes self)
//     muted,             // bool
//     cameraOff,         // bool
//     hostLost,          // bool — true briefly during failover
//     join,              // () => Promise<void>
//     leave,             // () => void
//     toggleMute,        // () => void
//     toggleCamera,      // () => void
//   } = useSFUCall({ roomId, sfuBase, localPeerId, iceServers, signalingWs })
//
// Parameters:
//   roomId       — SFU room ID (string, obtained from POST /api/sfu/rooms)
//   sfuBase      — base URL of the SFU host, e.g. '' for same-origin
//   localPeerId  — this user's Vula peer ID
//   iceServers   — optional RTCIceServer[] override
//   signalingWs  — optional { send, subscribe } from usePeering(); if omitted
//                  the hook opens its own WS to /api/peering/call/signal
// ---------------------------------------------------------------------------

// Hard cap matching backend MaxParticipants (room.go).
const MAX_SFU_PARTICIPANTS = 50

// Trickle-ICE → backend endpoint polling interval (ms) while gathering.
const ICE_FLUSH_INTERVAL_MS = 200

// How long to wait between reconnect attempts after host loss (ms).
const HANDOFF_RETRY_DELAY_MS = 500

// Number of times to retry a failed /join request before giving up.
const JOIN_MAX_RETRIES = 3

// Bandwidth probe interval (ms) — feeds handoff manager data on backend.
const BW_PROBE_INTERVAL_MS = 8000

const DEFAULT_ICE_SERVERS = [
  { urls: 'stun:stun.l.google.com:19302' },
  { urls: 'stun:stun1.l.google.com:19302' },
]

// ---------------------------------------------------------------------------
// REST helpers
// ---------------------------------------------------------------------------

async function sfuJoin(sfuBase, roomId, offerSdp) {
  const res = await fetch(`${sfuBase}/api/sfu/rooms/${encodeURIComponent(roomId)}/join`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify({ sdp: offerSdp, type: 'offer' }),
  })
  if (!res.ok) {
    const text = await res.text().catch(() => String(res.status))
    throw Object.assign(new Error(`SFU join failed: ${text}`), { status: res.status })
  }
  return res.json() // { participant_id, sdp, type }
}

async function sfuICE(sfuBase, roomId, participantId, candidate, sdpMid, sdpMLineIndex) {
  await fetch(`${sfuBase}/api/sfu/rooms/${encodeURIComponent(roomId)}/ice`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify({
      participant_id: participantId,
      candidate,
      sdp_mid: sdpMid ?? '',
      sdp_mline_index: sdpMLineIndex ?? 0,
    }),
  }).catch(err => console.warn('[useSFUCall] ICE trickle error:', err))
}

// ---------------------------------------------------------------------------
// useSFUCall hook
// ---------------------------------------------------------------------------

/**
 * @param {object}   options
 * @param {string}   options.roomId         — SFU room ID
 * @param {string}   [options.sfuBase='']   — SFU host base URL (same-origin default)
 * @param {string}   options.localPeerId    — local Vula peer ID
 * @param {object[]} [options.iceServers]   — RTCIceServer list override
 * @param {object}   [options.signalingWs]  — { send, subscribe } from usePeering()
 */
export function useSFUCall({
  roomId,
  sfuBase = '',
  localPeerId,
  iceServers = DEFAULT_ICE_SERVERS,
  signalingWs = null,
}) {
  // ── Exposed state ─────────────────────────────────────────────────────────
  const [localStream,      setLocalStream]      = useState(null)
  const [participants,     setParticipants]     = useState(new Map())
  const [joined,           setJoined]           = useState(false)
  const [muted,            setMuted]            = useState(false)
  const [cameraOff,        setCameraOff]        = useState(false)
  const [hostLost,         setHostLost]         = useState(false)
  const [participantCount, setParticipantCount] = useState(0)

  // ── Internal refs ─────────────────────────────────────────────────────────
  const pcRef            = useRef(null)      // RTCPeerConnection
  const participantIdRef = useRef(null)      // own SFU participant ID
  const localStreamRef   = useRef(null)
  const joinedRef        = useRef(false)
  const sfuBaseRef       = useRef(sfuBase)
  const roomIdRef        = useRef(roomId)
  const localPeerIdRef   = useRef(localPeerId)
  const iceServersRef    = useRef(iceServers)
  const signalingRef     = useRef(signalingWs)
  const bwTimerRef       = useRef(null)
  const ownWsRef         = useRef(null)      // self-owned signaling WS (when signalingWs=null)
  const pendingICERef    = useRef([])        // queued ICE candidates before remote desc set

  // Keep refs current without triggering re-renders.
  useEffect(() => { sfuBaseRef.current    = sfuBase    }, [sfuBase])
  useEffect(() => { roomIdRef.current     = roomId     }, [roomId])
  useEffect(() => { localPeerIdRef.current = localPeerId }, [localPeerId])
  useEffect(() => { iceServersRef.current = iceServers }, [iceServers])
  useEffect(() => { signalingRef.current  = signalingWs }, [signalingWs])

  // ── Participant map helpers ────────────────────────────────────────────────

  const upsertParticipant = useCallback((id, fields) => {
    setParticipants(prev => {
      const next = new Map(prev)
      const existing = next.get(id) || { stream: null, state: 'connecting', bandwidth: null }
      next.set(id, { ...existing, ...fields })
      return next
    })
  }, [])

  const removeParticipant = useCallback((id) => {
    setParticipants(prev => {
      const next = new Map(prev)
      next.delete(id)
      return next
    })
  }, [])

  // Sync participantCount whenever participants map changes (+1 for self when joined).
  useEffect(() => {
    setParticipantCount(participants.size + (joinedRef.current ? 1 : 0))
  }, [participants.size])

  // ── Bandwidth probing ─────────────────────────────────────────────────────

  /**
   * Estimate own upload bandwidth from WebRTC getStats and report it to the
   * backend via the signaling channel.  The backend HandoffManager uses this
   * data to elect the best-upload peer when a host handoff is needed.
   */
  const probeBandwidth = useCallback(async () => {
    const pc = pcRef.current
    if (!pc || pc.connectionState !== 'connected') return

    let totalBytesSent = 0
    let samples = 0
    try {
      const stats = await pc.getStats()
      stats.forEach(r => {
        if (r.type === 'outbound-rtp' && r.bytesSent) {
          totalBytesSent += r.bytesSent
          samples++
        }
      })
    } catch { /* connection may be closing */ }

    const uploadKbps = samples > 0
      ? Math.round((totalBytesSent * 8) / (BW_PROBE_INTERVAL_MS / 1000) / 1000 / samples)
      : 0

    // Report to backend via signaling so the HandoffManager can rank peers.
    signalSend({
      type: 'sfu-bw',
      from: localPeerIdRef.current,
      roomId: roomIdRef.current,
      upload_kbps: uploadKbps,
    })
  }, [])  // signalSend added below

  // ── Signaling helpers ─────────────────────────────────────────────────────

  /** Send a frame on the signaling channel (owned WS or injected signalingWs). */
  const signalSend = useCallback((msg) => {
    const inj = signalingRef.current
    if (inj?.send) {
      inj.send(msg)
      return
    }
    const ws = ownWsRef.current
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg))
    }
  }, [])

  /** Handle an inbound signaling frame. */
  const handleSignal = useCallback((msg) => {
    if (!msg || typeof msg !== 'object') return
    const { type } = msg

    switch (type) {
      case 'sfu-participant-joined': {
        // A new browser joined the SFU room — add to our display map.
        if (msg.participant_id) {
          upsertParticipant(msg.participant_id, { state: 'connected' })
        }
        break
      }
      case 'sfu-participant-left': {
        if (msg.participant_id) {
          removeParticipant(msg.participant_id)
        }
        break
      }
      case 'sfu-bw': {
        // Another participant reported bandwidth — store for display.
        if (msg.from && msg.upload_kbps != null) {
          upsertParticipant(msg.from, {
            bandwidth: { upload_kbps: msg.upload_kbps },
          })
        }
        break
      }
      case 'sfu-host-lost': {
        // The SFU host dropped.  msg.new_host_url carries the new host's base URL.
        // We reconnect by re-joining after a short delay.
        if (!joinedRef.current) break
        setHostLost(true)
        const newBase = msg.new_host_url ?? sfuBaseRef.current
        const newRoomId = msg.new_room_id ?? roomIdRef.current
        sfuBaseRef.current = newBase
        roomIdRef.current  = newRoomId

        setTimeout(async () => {
          try {
            await reconnectToSFU(newBase, newRoomId)
            setHostLost(false)
          } catch (err) {
            console.error('[useSFUCall] handoff reconnect failed:', err)
            setHostLost(false)
          }
        }, HANDOFF_RETRY_DELAY_MS)
        break
      }
      default:
        break
    }
  }, [upsertParticipant, removeParticipant])  // reconnectToSFU added below

  // ── PeerConnection factory ────────────────────────────────────────────────

  /**
   * Build a new RTCPeerConnection and wire it to the local media stream.
   * ontrack is wired to map incoming remote tracks to named participants.
   */
  const createPeerConnection = useCallback(() => {
    // Close any previous connection cleanly.
    const prev = pcRef.current
    if (prev) {
      try { prev.close() } catch { /* ignore */ }
      pcRef.current = null
    }

    const pc = new RTCPeerConnection({ iceServers: iceServersRef.current })
    pcRef.current = pc

    // Add local tracks.
    const stream = localStreamRef.current
    if (stream) {
      stream.getTracks().forEach(track => pc.addTrack(track, stream))
    }

    // Trickle ICE to backend.
    pc.onicecandidate = (event) => {
      if (!event.candidate) return
      const { candidate, sdpMid, sdpMLineIndex } = event.candidate
      const pid = participantIdRef.current
      if (pid) {
        sfuICE(sfuBaseRef.current, roomIdRef.current, pid, candidate, sdpMid, sdpMLineIndex)
      } else {
        // Queue until we have a participant ID from the join response.
        pendingICERef.current.push({ candidate, sdpMid, sdpMLineIndex })
      }
    }

    // Map incoming remote tracks to synthetic participant entries.
    pc.ontrack = (event) => {
      const stream = event.streams?.[0]
      if (!stream) return
      // Use the stream ID as a stable participant key.
      upsertParticipant(stream.id, { stream, state: 'connected' })
    }

    pc.onconnectionstatechange = () => {
      if (pc.connectionState === 'failed' || pc.connectionState === 'closed') {
        if (joinedRef.current) {
          console.warn('[useSFUCall] PeerConnection', pc.connectionState)
        }
      }
    }

    return pc
  }, [upsertParticipant])

  // ── SFU join / reconnect ──────────────────────────────────────────────────

  /**
   * Core join sequence: create offer → POST to SFU → set remote answer.
   * Retried up to JOIN_MAX_RETRIES times on transient failures.
   */
  const connectToSFU = useCallback(async (base, rid) => {
    let lastErr = null

    for (let attempt = 0; attempt < JOIN_MAX_RETRIES; attempt++) {
      try {
        const pc = createPeerConnection()

        const offer = await pc.createOffer()
        await pc.setLocalDescription(offer)

        const { participant_id, sdp, type } = await sfuJoin(base, rid, offer.sdp)

        participantIdRef.current = participant_id
        await pc.setRemoteDescription(new RTCSessionDescription({ type, sdp }))

        // Flush any ICE candidates queued before we had the participant ID.
        const pending = pendingICERef.current.splice(0)
        for (const c of pending) {
          sfuICE(base, rid, participant_id, c.candidate, c.sdpMid, c.sdpMLineIndex)
        }

        return // success
      } catch (err) {
        lastErr = err
        // 503 = room full — no point retrying.
        if (err.status === 503) throw err
        // brief backoff before retry
        await new Promise(r => setTimeout(r, 300 * (attempt + 1)))
      }
    }
    throw lastErr
  }, [createPeerConnection])

  /** Reconnect after host handoff — re-creates the PeerConnection and re-joins. */
  const reconnectToSFU = useCallback(async (newBase, newRid) => {
    await connectToSFU(newBase, newRid)
  }, [connectToSFU])

  // ── join / leave ──────────────────────────────────────────────────────────

  /**
   * Acquire local media, open signaling, and join the SFU room.
   * Rejects with an Error if the room is full (51st participant).
   */
  const join = useCallback(async () => {
    if (joinedRef.current) return

    // Client-side cap guard — mirrors backend HandoffCheckCap.
    // We add 1 for self (about to join).
    if (participantCount >= MAX_SFU_PARTICIPANTS) {
      throw new Error(`SFU room is full (max ${MAX_SFU_PARTICIPANTS} participants)`)
    }

    // 1. Acquire media.
    let stream
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true })
    } catch {
      try {
        stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: false })
      } catch (err) {
        console.error('[useSFUCall] getUserMedia failed:', err)
        throw err
      }
    }
    localStreamRef.current = stream
    setLocalStream(stream)

    // 2. Open own signaling WS if no injected signalingWs.
    if (!signalingRef.current) {
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const ws = new WebSocket(
        `${proto}//${location.host}/api/peering/call/signal?room=${encodeURIComponent(roomIdRef.current)}&peer=${encodeURIComponent(localPeerIdRef.current)}`
      )
      ownWsRef.current = ws
      ws.onmessage = (event) => {
        if (!event.data) return
        try { handleSignal(JSON.parse(event.data)) } catch { /* ignore malformed */ }
      }
      ws.onerror = (err) => console.error('[useSFUCall] signaling WS error:', err)
    } else {
      // Subscribe on the injected signaling channel.
      signalingRef.current.subscribe?.(handleSignal)
    }

    // 3. Connect to SFU.
    await connectToSFU(sfuBaseRef.current, roomIdRef.current)

    joinedRef.current = true
    setJoined(true)

    // 4. Announce to room peers so they can show us in their participant lists.
    signalSend({
      type: 'sfu-participant-joined',
      from: localPeerIdRef.current,
      participant_id: participantIdRef.current,
      roomId: roomIdRef.current,
    })

    // 5. Start bandwidth probing for handoff election data.
    bwTimerRef.current = setInterval(probeBandwidth, BW_PROBE_INTERVAL_MS)
  }, [participantCount, handleSignal, connectToSFU, signalSend, probeBandwidth])

  /**
   * Leave the SFU room: stop all media, close PeerConnection, clean up.
   */
  const leave = useCallback(() => {
    if (!joinedRef.current) return

    // Announce departure.
    signalSend({
      type: 'sfu-participant-left',
      from: localPeerIdRef.current,
      participant_id: participantIdRef.current,
      roomId: roomIdRef.current,
    })

    // Stop bandwidth probe.
    clearInterval(bwTimerRef.current)
    bwTimerRef.current = null

    // Close PeerConnection.
    try { pcRef.current?.close() } catch { /* ignore */ }
    pcRef.current = null
    participantIdRef.current = null
    pendingICERef.current = []

    // Stop local media.
    localStreamRef.current?.getTracks().forEach(t => t.stop())
    localStreamRef.current = null

    // Close own WS.
    ownWsRef.current?.close()
    ownWsRef.current = null

    joinedRef.current = false
    setJoined(false)
    setLocalStream(null)
    setParticipants(new Map())
    setHostLost(false)
    setParticipantCount(0)
  }, [signalSend])

  // ── Mute / camera controls ────────────────────────────────────────────────

  const toggleMute = useCallback(() => {
    localStreamRef.current?.getAudioTracks().forEach(t => {
      t.enabled = !t.enabled
    })
    setMuted(m => !m)
  }, [])

  const toggleCamera = useCallback(() => {
    localStreamRef.current?.getVideoTracks().forEach(t => {
      t.enabled = !t.enabled
    })
    setCameraOff(c => !c)
  }, [])

  // ── Cleanup on unmount ────────────────────────────────────────────────────

  useEffect(() => {
    return () => {
      // eslint-disable-next-line react-hooks/exhaustive-deps
      leave()
    }
  }, []) // intentionally only on unmount

  // ── Public API ────────────────────────────────────────────────────────────

  return {
    localStream,
    participants,
    joined,
    participantCount,
    muted,
    cameraOff,
    hostLost,
    join,
    leave,
    toggleMute,
    toggleCamera,
  }
}
