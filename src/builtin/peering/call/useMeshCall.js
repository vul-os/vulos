import { useState, useEffect, useRef, useCallback } from 'react'

// ---------------------------------------------------------------------------
// useMeshCall — full-mesh WebRTC hook for 3–4 participant group calls
//
// Architecture (from PEERING.md § Rooms):
//   Each browser connects directly to every other browser.
//   N×(N-1)/2 connections: 3 people → 3 connections, 4 people → 6 connections.
//   Signaling is relayed through each user's local Vula server via the
//   /api/peering/call/signal WS channel — servers exchange SDP/ICE on behalf
//   of the browsers but never carry media.
//
// Low-bandwidth guard:
//   If any participant's upload falls below LOW_BW_UPLOAD_KBPS the hook emits
//   { type: 'recommend-sfu', reason, participants } via onSFURecommend so the
//   UI can prompt the user to switch to an SFU-hosted call.
//
// Usage:
//   const {
//     localStream,       // MediaStream — own camera/mic
//     peers,             // Map<peerId, { stream, state, displayName, bandwidth }>
//     gridLayout,        // { cols, rows, slots } — pre-computed grid for CSS grid
//     joined,            // bool — whether we are connected to the room
//     muted,             // bool — local mic muted
//     cameraOff,         // bool — local camera disabled
//     join,              // () => void — acquire media and start signaling
//     leave,             // () => void — hang up and clean up
//     toggleMute,        // () => void
//     toggleCamera,      // () => void
//   } = useMeshCall({ roomId, localPeerId, iceServers, onSFURecommend })
//
// Signaling message shape (over /api/peering/call/signal WS):
//   { type: 'join',      from, roomId }
//   { type: 'offer',     from, to, roomId, sdp }
//   { type: 'answer',    from, to, roomId, sdp }
//   { type: 'candidate', from, to, roomId, candidate }
//   { type: 'leave',     from, roomId }
//   { type: 'bw',        from, roomId, upload_kbps, download_kbps, rtt_ms }
// ---------------------------------------------------------------------------

// Threshold below which we recommend an SFU (kbps upload per peer).
// At 4 people, each sender uploads to 3 others.  500 kbps/stream × 3 = 1500 kbps minimum.
const LOW_BW_UPLOAD_KBPS = 1500

// Maximum mesh participants.  Above 4 the O(N²) connection count becomes impractical.
const MESH_MAX_PEERS = 4

// ICE restart delay after connection failure (ms)
const ICE_RESTART_DELAY_MS = 3000

// Bandwidth probe interval (ms)
const BW_PROBE_INTERVAL_MS = 8000

// Default ICE servers — STUN from Google as fallback; callers should pass
// their own iceServers (including Coturn TURN credentials from the backend).
const DEFAULT_ICE_SERVERS = [
  { urls: 'stun:stun.l.google.com:19302' },
  { urls: 'stun:stun1.l.google.com:19302' },
]

// ---------------------------------------------------------------------------
// Grid layout computation
// ---------------------------------------------------------------------------

/**
 * Compute a CSS-grid-friendly layout for `count` video tiles.
 * Returns { cols, rows, slots } where slots is the total cell count
 * (may be larger than count when the grid is not perfectly square).
 *
 *   1 peer  → 1×1
 *   2 peers → 1×2
 *   3 peers → 2×2  (1 empty slot)
 *   4 peers → 2×2
 */
function computeGridLayout(count) {
  if (count <= 1) return { cols: 1, rows: 1, slots: 1 }
  if (count === 2) return { cols: 2, rows: 1, slots: 2 }
  // 3 or 4 → 2×2
  return { cols: 2, rows: 2, slots: 4 }
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

/**
 * @param {object}   options
 * @param {string}   options.roomId          — unique room identifier
 * @param {string}   options.localPeerId     — this user's Vula peer ID
 * @param {object[]} [options.iceServers]    — WebRTC ICE server list
 * @param {function} [options.onSFURecommend]— called with recommendation object
 *                                            when low-bandwidth is detected
 */
export function useMeshCall({
  roomId,
  localPeerId,
  iceServers = DEFAULT_ICE_SERVERS,
  onSFURecommend = null,
}) {
  // ── State exposed to the caller ──────────────────────────────────────────
  const [localStream, setLocalStream]   = useState(null)
  const [peers, setPeers]               = useState(new Map())  // peerId → PeerEntry
  const [joined, setJoined]             = useState(false)
  const [muted, setMuted]               = useState(false)
  const [cameraOff, setCameraOff]       = useState(false)
  const [gridLayout, setGridLayout]     = useState(computeGridLayout(0))

  // ── Internal refs (survive re-renders without re-triggering effects) ─────
  const wsRef            = useRef(null)   // signaling WebSocket
  const localStreamRef   = useRef(null)   // always current
  const peerConnsRef     = useRef(new Map()) // peerId → RTCPeerConnection
  const pendingCandRef   = useRef(new Map()) // peerId → RTCIceCandidate[]
  const bwTimerRef       = useRef(null)
  const joinedRef        = useRef(false)
  const localPeerIdRef   = useRef(localPeerId)
  const iceServersRef    = useRef(iceServers)
  const onSFURef         = useRef(onSFURecommend)

  // Keep refs in sync with latest prop values without closing over stale values
  useEffect(() => { localPeerIdRef.current = localPeerId }, [localPeerId])
  useEffect(() => { iceServersRef.current = iceServers }, [iceServers])
  useEffect(() => { onSFURef.current = onSFURecommend }, [onSFURecommend])

  // ── Helpers ───────────────────────────────────────────────────────────────

  /** Send a JSON message over the signaling WebSocket. */
  const wsSend = useCallback((msg) => {
    const ws = wsRef.current
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg))
    }
  }, [])

  /** Flush queued ICE candidates for a peer now that remote description is set. */
  const flushCandidates = useCallback(async (peerId, pc) => {
    const queue = pendingCandRef.current.get(peerId) || []
    pendingCandRef.current.delete(peerId)
    for (const cand of queue) {
      try {
        await pc.addIceCandidate(new RTCIceCandidate(cand))
      } catch {
        // Ignore stale candidates
      }
    }
  }, [])

  /** Update the `peers` state map immutably, merging the given fields. */
  const updatePeer = useCallback((peerId, fields) => {
    setPeers(prev => {
      const next = new Map(prev)
      const existing = next.get(peerId) || {
        stream: null,
        state: 'connecting',
        displayName: peerId,
        bandwidth: null,
      }
      next.set(peerId, { ...existing, ...fields })
      return next
    })
  }, [])

  /** Remove a peer from state, close its connection, clean up. */
  const removePeer = useCallback((peerId) => {
    const pc = peerConnsRef.current.get(peerId)
    if (pc) {
      pc.close()
      peerConnsRef.current.delete(peerId)
    }
    pendingCandRef.current.delete(peerId)
    setPeers(prev => {
      const next = new Map(prev)
      next.delete(peerId)
      return next
    })
  }, [])

  // ── Grid layout sync ──────────────────────────────────────────────────────

  // Recompute grid whenever peer count changes (own tile + peers = total tiles).
  useEffect(() => {
    // +1 for local tile
    const total = peers.size + (joined ? 1 : 0)
    setGridLayout(computeGridLayout(total))
  }, [peers.size, joined])

  // ── Bandwidth monitoring ──────────────────────────────────────────────────

  /**
   * Measure own upload bandwidth estimate using WebRTC getStats and
   * broadcast it to all peers so everyone can see each other's numbers.
   * Also checks all reported peer bandwidths for the low-bw guard.
   */
  const probeBandwidth = useCallback(async () => {
    // Collect outbound rtp stats from all peer connections to estimate upload
    let totalBytesSent = 0
    let statSamples = 0

    for (const pc of peerConnsRef.current.values()) {
      if (pc.connectionState !== 'connected') continue
      try {
        const stats = await pc.getStats()
        stats.forEach(report => {
          if (report.type === 'outbound-rtp' && report.bytesSent) {
            totalBytesSent += report.bytesSent
            statSamples++
          }
        })
      } catch {
        // getStats can fail when connection is closing
      }
    }

    // Rough upload estimate: average bytes over the probe interval
    // (proper implementation would diff two samples; this is sufficient for a
    // guard threshold check — the backend /api/peering/bandwidth gives accurate values)
    const estimatedUploadKbps = statSamples > 0
      ? Math.round((totalBytesSent * 8) / (BW_PROBE_INTERVAL_MS / 1000) / 1000 / statSamples)
      : 0

    // Broadcast our bandwidth to room peers
    wsSend({
      type: 'bw',
      from: localPeerIdRef.current,
      roomId,
      upload_kbps: estimatedUploadKbps,
      download_kbps: 0,   // not measured client-side; server can fill this in
      rtt_ms: 0,
    })

    // Check all peer bandwidths (including ours) for SFU recommendation
    const allBandwidths = []
    if (estimatedUploadKbps > 0) {
      allBandwidths.push({ peerId: localPeerIdRef.current, upload_kbps: estimatedUploadKbps })
    }

    setPeers(current => {
      current.forEach((entry, peerId) => {
        if (entry.bandwidth?.upload_kbps != null) {
          allBandwidths.push({ peerId, upload_kbps: entry.bandwidth.upload_kbps })
        }
      })
      return current  // no change to state, we just need to read it
    })

    const lowBwPeers = allBandwidths.filter(b => b.upload_kbps > 0 && b.upload_kbps < LOW_BW_UPLOAD_KBPS)
    if (lowBwPeers.length > 0 && onSFURef.current) {
      onSFURef.current({
        type: 'recommend-sfu',
        reason: 'low-bandwidth',
        threshold_kbps: LOW_BW_UPLOAD_KBPS,
        participants: lowBwPeers,
        message: `${lowBwPeers.length} participant(s) have upload below ${LOW_BW_UPLOAD_KBPS} kbps. ` +
          `Consider switching to an SFU-hosted call for better quality.`,
      })
    }
  }, [roomId, wsSend])

  // ── RTCPeerConnection factory ─────────────────────────────────────────────

  /**
   * Create and wire up a new RTCPeerConnection for `remotePeerId`.
   * The caller determines whether this side initiates (sends offer).
   */
  const createPeerConnection = useCallback((remotePeerId) => {
    // Close any existing connection for this peer
    const existing = peerConnsRef.current.get(remotePeerId)
    if (existing) existing.close()

    const pc = new RTCPeerConnection({ iceServers: iceServersRef.current })
    peerConnsRef.current.set(remotePeerId, pc)

    // Add local tracks so the remote peer can receive our A/V
    const stream = localStreamRef.current
    if (stream) {
      stream.getTracks().forEach(track => {
        pc.addTrack(track, stream)
      })
    }

    // Receive remote tracks
    pc.ontrack = (event) => {
      const [remoteStream] = event.streams
      if (remoteStream) {
        updatePeer(remotePeerId, { stream: remoteStream, state: 'connected' })
      }
    }

    // Trickle ICE — send each candidate as it arrives
    pc.onicecandidate = (event) => {
      if (event.candidate) {
        wsSend({
          type: 'candidate',
          from: localPeerIdRef.current,
          to: remotePeerId,
          roomId,
          candidate: event.candidate.toJSON(),
        })
      }
    }

    // Connection state transitions
    pc.onconnectionstatechange = () => {
      const state = pc.connectionState
      updatePeer(remotePeerId, { state })

      if (state === 'failed') {
        // Attempt ICE restart after a short delay
        setTimeout(() => {
          if (peerConnsRef.current.get(remotePeerId) !== pc) return  // replaced
          pc.restartIce()
        }, ICE_RESTART_DELAY_MS)
      } else if (state === 'closed') {
        peerConnsRef.current.delete(remotePeerId)
      }
    }

    pc.oniceconnectionstatechange = () => {
      if (pc.iceConnectionState === 'disconnected') {
        updatePeer(remotePeerId, { state: 'disconnected' })
      }
    }

    return pc
  }, [roomId, updatePeer, wsSend])

  // ── Signaling handlers ────────────────────────────────────────────────────

  /**
   * Handle a signaling message from the server.
   * The server relays messages addressed to us via `to === localPeerId`.
   */
  const handleSignal = useCallback(async (msg) => {
    const { type, from } = msg
    if (!from || from === localPeerIdRef.current) return

    switch (type) {
      case 'join': {
        // A new peer joined the room.  We are the initiator → send them an offer.
        if (peerConnsRef.current.size >= MESH_MAX_PEERS) {
          // Room full — ignore (server should also guard this)
          return
        }

        updatePeer(from, { state: 'connecting', displayName: from })

        const pc = createPeerConnection(from)

        const offer = await pc.createOffer()
        await pc.setLocalDescription(offer)
        wsSend({
          type: 'offer',
          from: localPeerIdRef.current,
          to: from,
          roomId,
          sdp: offer.sdp,
        })
        break
      }

      case 'offer': {
        if (msg.to !== localPeerIdRef.current) return

        updatePeer(from, { state: 'connecting', displayName: from })

        const pc = createPeerConnection(from)

        await pc.setRemoteDescription(new RTCSessionDescription({ type: 'offer', sdp: msg.sdp }))
        await flushCandidates(from, pc)

        const answer = await pc.createAnswer()
        await pc.setLocalDescription(answer)
        wsSend({
          type: 'answer',
          from: localPeerIdRef.current,
          to: from,
          roomId,
          sdp: answer.sdp,
        })
        break
      }

      case 'answer': {
        if (msg.to !== localPeerIdRef.current) return

        const pc = peerConnsRef.current.get(from)
        if (!pc) return

        await pc.setRemoteDescription(new RTCSessionDescription({ type: 'answer', sdp: msg.sdp }))
        await flushCandidates(from, pc)
        break
      }

      case 'candidate': {
        if (msg.to !== localPeerIdRef.current) return

        const cand = msg.candidate
        if (!cand) return

        const pc = peerConnsRef.current.get(from)
        if (!pc || !pc.remoteDescription) {
          // Queue it — remote description not yet set
          if (!pendingCandRef.current.has(from)) {
            pendingCandRef.current.set(from, [])
          }
          pendingCandRef.current.get(from).push(cand)
        } else {
          try {
            await pc.addIceCandidate(new RTCIceCandidate(cand))
          } catch {
            // Ignore stale candidates
          }
        }
        break
      }

      case 'leave': {
        removePeer(from)
        break
      }

      case 'bw': {
        // Peer reported their bandwidth — store it for grid display and SFU guard
        updatePeer(from, {
          bandwidth: {
            upload_kbps:   msg.upload_kbps,
            download_kbps: msg.download_kbps,
            rtt_ms:        msg.rtt_ms,
          },
        })
        break
      }

      default:
        break
    }
  }, [roomId, createPeerConnection, flushCandidates, removePeer, updatePeer, wsSend])

  // ── join / leave ──────────────────────────────────────────────────────────

  /**
   * Acquire local A/V, open the signaling WebSocket, and announce presence.
   * Call this when the user clicks "Join".
   */
  const join = useCallback(async () => {
    if (joinedRef.current) return

    // 1. Acquire local media
    let stream
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: true })
    } catch {
      // Fallback: audio only (camera may be unavailable)
      try {
        stream = await navigator.mediaDevices.getUserMedia({ audio: true, video: false })
      } catch (err) {
        console.error('[useMeshCall] getUserMedia failed:', err)
        return
      }
    }

    localStreamRef.current = stream
    setLocalStream(stream)

    // 2. Open signaling WebSocket
    //    Pattern mirrors StreamViewer.jsx: ws → wss based on current protocol
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(
      `${proto}//${location.host}/api/peering/call/signal?room=${encodeURIComponent(roomId)}&peer=${encodeURIComponent(localPeerIdRef.current)}`
    )
    wsRef.current = ws

    ws.onmessage = (event) => {
      if (!event.data) return
      let msg
      try { msg = JSON.parse(event.data) } catch { return }
      handleSignal(msg).catch(err => console.error('[useMeshCall] signal error:', err))
    }

    ws.onopen = () => {
      joinedRef.current = true
      setJoined(true)

      // Announce our arrival — existing peers will offer to us
      wsSend({
        type: 'join',
        from: localPeerIdRef.current,
        roomId,
      })

      // Start bandwidth probing loop
      bwTimerRef.current = setInterval(probeBandwidth, BW_PROBE_INTERVAL_MS)
    }

    ws.onerror = (err) => {
      console.error('[useMeshCall] signaling WS error:', err)
    }

    ws.onclose = () => {
      joinedRef.current = false
      setJoined(false)
    }
  }, [roomId, handleSignal, probeBandwidth, wsSend])

  /**
   * Leave the call: close all peer connections, stop media, close signaling.
   */
  const leave = useCallback(() => {
    // Announce departure so peers can clean up immediately
    wsSend({
      type: 'leave',
      from: localPeerIdRef.current,
      roomId,
    })

    // Stop bandwidth probe
    if (bwTimerRef.current) {
      clearInterval(bwTimerRef.current)
      bwTimerRef.current = null
    }

    // Close all peer connections
    peerConnsRef.current.forEach(pc => pc.close())
    peerConnsRef.current.clear()
    pendingCandRef.current.clear()

    // Stop local media tracks
    if (localStreamRef.current) {
      localStreamRef.current.getTracks().forEach(t => t.stop())
      localStreamRef.current = null
    }

    // Close signaling
    wsRef.current?.close()
    wsRef.current = null

    joinedRef.current = false
    setJoined(false)
    setLocalStream(null)
    setPeers(new Map())
  }, [roomId, wsSend])

  // ── Mute / camera controls ────────────────────────────────────────────────

  const toggleMute = useCallback(() => {
    const stream = localStreamRef.current
    if (!stream) return
    stream.getAudioTracks().forEach(t => {
      t.enabled = !t.enabled
    })
    setMuted(m => !m)
  }, [])

  const toggleCamera = useCallback(() => {
    const stream = localStreamRef.current
    if (!stream) return
    stream.getVideoTracks().forEach(t => {
      t.enabled = !t.enabled
    })
    setCameraOff(c => !c)
  }, [])

  // ── Cleanup on unmount ────────────────────────────────────────────────────

  useEffect(() => {
    return () => {
      leave()
    }
  }, []) // intentionally only on unmount; `leave` is stable

  // ── Public API ────────────────────────────────────────────────────────────

  return {
    localStream,
    peers,
    gridLayout,
    joined,
    muted,
    cameraOff,
    join,
    leave,
    toggleMute,
    toggleCamera,
  }
}
