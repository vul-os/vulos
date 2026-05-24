// rtc.js — WebRTC 1:1 + group (mesh) voice/video calling for Vulos Spaces.
//
// Architecture
//   * Signaling: routed through the OFFICE-20 fabric session (offer/answer/
//     ICE candidates). When the fabric isn't available locally we fall back
//     to a BroadcastChannel stub (see fabricSignaling.js) so the build/dev
//     loop works standalone.
//   * Media: getUserMedia (audio + optional video). Each remote peer is one
//     RTCPeerConnection. Small-group mesh — every peer maintains N-1 PCs.
//     For >~6 participants the SFU upgrade is a future task (OFFICE-63 AC
//     calls out "3-party mesh"); the topology is contained here so it can
//     swap.
//   * ICE/STUN/TURN: ICE servers come from /api/turn/credentials (the cloud
//     issues short-lived TURN creds — the same path the OS fabric uses).
//     We force iceTransportPolicy='relay' as a fallback when a peer fails
//     to reach 'connected' via direct ICE, which is the Vulos relay/TURN
//     fallback equivalent for media.
//   * Active speaker: WebAudio AnalyserNodes on every incoming MediaStream;
//     the loudest peer ID is emitted on 'active-speaker' (throttled).
//
// Public API
//   const call = await createCall({ sessionId, identity, video })
//   call.localStream                — MediaStream
//   call.peers                      — Map<peerId, { identity, stream, state }>
//   call.on(event, cb)              — events:
//       'peer-update' (peerId, info)
//       'peers-changed' (peersArray)
//       'active-speaker' (peerId|null)
//       'state' ('connecting'|'connected'|'failed'|'closed')
//       'transport' ('p2p'|'relay')
//       'screen-share' (peerId|null)   — peerId of presenter, or null when stopped
//   call.toggleMute()               — returns new muted boolean
//   call.toggleCamera()             — returns new camera-off boolean
//   call.startScreenShare()         — returns Promise<void>; adds display track
//   call.stopScreenShare()          — stops + removes display track
//   call.screenSharing              — boolean
//   call.screenStream               — MediaStream|null (local screen capture)
//   call.muted / call.cameraOff
//   call.leave()                    — tears down

import { joinSignalingSession, fetchIceServers } from './fabricSignaling.js'

class Emitter {
  constructor() { this._h = {} }
  on(ev, cb) { (this._h[ev] = this._h[ev] || []).push(cb); return () => this.off(ev, cb) }
  off(ev, cb) { this._h[ev] = (this._h[ev] || []).filter(f => f !== cb) }
  emit(ev, ...a) { (this._h[ev] || []).forEach(f => { try { f(...a) } catch (e) { console.error(e) } }) }
}

const ICE_FAIL_GRACE_MS = 6000
const ACTIVE_SPEAKER_INTERVAL_MS = 400
const ACTIVE_SPEAKER_THRESHOLD = 0.02

async function getLocalMedia(video) {
  if (!navigator?.mediaDevices?.getUserMedia) {
    throw new Error('Media devices not available in this browser')
  }
  return navigator.mediaDevices.getUserMedia({
    audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    video: video ? { width: { ideal: 1280 }, height: { ideal: 720 } } : false,
  })
}

function tieBreakInitiator(localPeerId, remotePeerId) {
  // Deterministic offerer choice: whichever id sorts lower initiates.
  return localPeerId < remotePeerId
}

class PeerConn {
  constructor(call, remotePeerId, remoteIdentity, opts) {
    this.call = call
    this.remotePeerId = remotePeerId
    this.identity = remoteIdentity || null
    this.iceServers = opts.iceServers
    this.localStream = opts.localStream
    this.pc = null
    this.stream = null
    this.state = 'new'
    this.usingRelay = false
    this._iceFailTimer = null
    this._makingOffer = false
    this._ignoreOffer = false
    this._polite = !tieBreakInitiator(call.peerId, remotePeerId) // impolite = initiator
  }

  _createPC(forceRelay = false) {
    const cfg = {
      iceServers: this.iceServers,
      iceCandidatePoolSize: 4,
      iceTransportPolicy: forceRelay ? 'relay' : 'all',
      bundlePolicy: 'max-bundle',
    }
    const pc = new RTCPeerConnection(cfg)
    this.pc = pc

    for (const track of this.localStream.getTracks()) {
      pc.addTrack(track, this.localStream)
    }

    pc.onicecandidate = (e) => {
      if (!e.candidate) return
      this.call._sendSignal({
        kind: 'ice',
        to: this.remotePeerId,
        data: e.candidate.toJSON ? e.candidate.toJSON() : e.candidate,
      })
    }

    pc.ontrack = (e) => {
      // Aggregate remote tracks into a single MediaStream
      if (!this.stream) this.stream = new MediaStream()
      if (e.streams && e.streams[0]) {
        this.stream = e.streams[0]
      } else {
        this.stream.addTrack(e.track)
      }
      this.call._attachAnalyser(this.remotePeerId, this.stream)
      this.call._notifyPeer(this.remotePeerId)
    }

    pc.onnegotiationneeded = async () => {
      if (this._polite) return // polite peer waits for the offer
      try {
        this._makingOffer = true
        await pc.setLocalDescription()
        this.call._sendSignal({
          kind: 'sdp',
          to: this.remotePeerId,
          data: pc.localDescription,
        })
      } catch (err) {
        console.warn('negotiationneeded failed', err)
      } finally {
        this._makingOffer = false
      }
    }

    pc.oniceconnectionstatechange = () => {
      const s = pc.iceConnectionState
      this.state = s
      this.call._notifyPeer(this.remotePeerId)
      if (s === 'connected' || s === 'completed') {
        if (this._iceFailTimer) { clearTimeout(this._iceFailTimer); this._iceFailTimer = null }
        this.call._evaluateTransport()
      }
      if (s === 'failed') {
        if (!this.usingRelay) {
          // Tear down and rebuild forcing TURN relay — Vulos relay fallback.
          console.warn('[call] ICE failed; retrying via TURN relay for', this.remotePeerId)
          this.usingRelay = true
          this._rebuild(true)
        } else {
          this.call._notifyPeer(this.remotePeerId)
        }
      }
      if (s === 'disconnected' && !this._iceFailTimer && !this.usingRelay) {
        this._iceFailTimer = setTimeout(() => {
          if (this.pc && (this.pc.iceConnectionState === 'disconnected' || this.pc.iceConnectionState === 'failed')) {
            this.usingRelay = true
            this._rebuild(true)
          }
        }, ICE_FAIL_GRACE_MS)
      }
    }

    return pc
  }

  async start() {
    this._createPC(false)
    if (!this._polite) {
      // impolite peer kicks off the offer
      try {
        this._makingOffer = true
        await this.pc.setLocalDescription()
        this.call._sendSignal({
          kind: 'sdp',
          to: this.remotePeerId,
          data: this.pc.localDescription,
        })
      } catch (e) {
        console.warn('initial offer failed', e)
      } finally {
        this._makingOffer = false
      }
    }
  }

  async _rebuild(forceRelay) {
    try { this.pc?.close() } catch {}
    this.stream = null
    this._createPC(forceRelay)
    try {
      this._makingOffer = true
      await this.pc.setLocalDescription()
      this.call._sendSignal({
        kind: 'sdp',
        to: this.remotePeerId,
        data: this.pc.localDescription,
      })
    } catch (e) {
      console.warn('rebuild offer failed', e)
    } finally {
      this._makingOffer = false
    }
    this.call._evaluateTransport()
  }

  async handleSignal(msg) {
    if (msg.kind === 'sdp') {
      const desc = msg.data
      try {
        const offerCollision =
          desc.type === 'offer' && (this._makingOffer || this.pc.signalingState !== 'stable')
        this._ignoreOffer = !this._polite && offerCollision
        if (this._ignoreOffer) return
        await this.pc.setRemoteDescription(desc)
        if (desc.type === 'offer') {
          await this.pc.setLocalDescription()
          this.call._sendSignal({
            kind: 'sdp',
            to: this.remotePeerId,
            data: this.pc.localDescription,
          })
        }
      } catch (err) {
        console.warn('sdp handle failed', err)
      }
    } else if (msg.kind === 'ice') {
      try {
        if (!this._ignoreOffer) await this.pc.addIceCandidate(msg.data)
      } catch (err) {
        console.warn('ice handle failed', err)
      }
    }
  }

  replaceVideoTrack(newTrack) {
    if (!this.pc) return
    const sender = this.pc.getSenders().find((s) => s.track && s.track.kind === 'video')
    if (sender) sender.replaceTrack(newTrack)
  }

  close() {
    if (this._iceFailTimer) clearTimeout(this._iceFailTimer)
    try { this.pc?.close() } catch {}
    this.pc = null
  }
}

class Call extends Emitter {
  constructor({ sessionId, identity, video }) {
    super()
    this.sessionId = sessionId
    this.identity = identity
    this.video = !!video
    this.peerId = null
    this.session = null
    this.iceServers = []
    this.localStream = null
    this.peers = new Map() // peerId -> PeerConn
    this.muted = false
    this.cameraOff = !video
    this.screenSharing = false
    this.screenStream = null
    this.state = 'connecting'
    this.transport = 'p2p'
    this._audioCtx = null
    this._analysers = new Map() // peerId -> { analyser, data }
    this._activeSpeaker = null
    this._activeTimer = null
  }

  async _init() {
    this.iceServers = await fetchIceServers()
    this.localStream = await getLocalMedia(this.video)
    this.session = await joinSignalingSession(this.sessionId, this.identity)
    this.peerId = this.session.peerId

    this.session.on('peer-join', (peerId, peerIdentity) => {
      if (peerId === this.peerId) return
      if (this.peers.has(peerId)) return
      const pc = new PeerConn(this, peerId, peerIdentity, {
        iceServers: this.iceServers,
        localStream: this.localStream,
      })
      this.peers.set(peerId, pc)
      pc.start().catch((e) => console.warn('peer start', e))
      this._notifyPeer(peerId)
    })

    this.session.on('peer-leave', (peerId) => {
      const pc = this.peers.get(peerId)
      if (pc) { pc.close(); this.peers.delete(peerId) }
      this._analysers.delete(peerId)
      if (this._activeSpeaker === peerId) {
        this._activeSpeaker = null
        this.emit('active-speaker', null)
      }
      this.emit('peers-changed', this._peersArray())
    })

    this.session.on('message', (msg) => {
      if (msg.kind === 'screen-share') {
        // A remote peer started or stopped sharing.
        const fromId = msg.from
        const pc = this.peers.get(fromId)
        if (pc) {
          pc.isPresenting = !!msg.data.presenting
          this._notifyPeer(fromId)
        }
        this.emit('screen-share', msg.data.presenting ? fromId : null)
        return
      }
      if (msg.kind !== 'sdp' && msg.kind !== 'ice') return
      const fromId = msg.from
      let pc = this.peers.get(fromId)
      if (!pc) {
        pc = new PeerConn(this, fromId, msg.identity || null, {
          iceServers: this.iceServers,
          localStream: this.localStream,
        })
        this.peers.set(fromId, pc)
        // For polite peer the first 'start' just creates PC; the offer comes via signal.
        if (pc._polite) pc._createPC(false)
        else pc.start().catch(() => {})
      }
      pc.handleSignal(msg).catch((e) => console.warn('handleSignal', e))
    })

    this.session.on('state', (s) => {
      if (s === 'closed') this._setState('closed')
    })

    this._startActiveSpeakerLoop()
    this._setState('connected')
  }

  _peersArray() {
    return [...this.peers.values()].map((pc) => ({
      peerId: pc.remotePeerId,
      identity: pc.identity,
      stream: pc.stream,
      state: pc.state,
      usingRelay: pc.usingRelay,
      isPresenting: !!pc.isPresenting,
    }))
  }

  _notifyPeer(peerId) {
    const pc = this.peers.get(peerId)
    if (!pc) return
    this.emit('peer-update', peerId, {
      identity: pc.identity,
      stream: pc.stream,
      state: pc.state,
      usingRelay: pc.usingRelay,
      isPresenting: !!pc.isPresenting,
    })
    this.emit('peers-changed', this._peersArray())
  }

  _sendSignal(msg) {
    this.session?.send({ ...msg, identity: this.identity })
  }

  _setState(s) {
    if (this.state === s) return
    this.state = s
    this.emit('state', s)
  }

  _evaluateTransport() {
    const anyRelay = [...this.peers.values()].some((p) => p.usingRelay)
    const next = anyRelay ? 'relay' : 'p2p'
    if (next !== this.transport) {
      this.transport = next
      this.emit('transport', next)
    }
  }

  _attachAnalyser(peerId, stream) {
    try {
      if (!this._audioCtx) {
        const Ctx = window.AudioContext || window.webkitAudioContext
        if (!Ctx) return
        this._audioCtx = new Ctx()
      }
      const audioTracks = stream.getAudioTracks()
      if (audioTracks.length === 0) return
      const src = this._audioCtx.createMediaStreamSource(new MediaStream([audioTracks[0]]))
      const analyser = this._audioCtx.createAnalyser()
      analyser.fftSize = 512
      src.connect(analyser)
      const data = new Uint8Array(analyser.fftSize)
      this._analysers.set(peerId, { analyser, data })
    } catch (e) {
      // Some browsers block AudioContext until user gesture — non-fatal.
    }
  }

  _startActiveSpeakerLoop() {
    if (this._activeTimer) clearInterval(this._activeTimer)
    this._activeTimer = setInterval(() => {
      let loudest = null
      let loudestLevel = ACTIVE_SPEAKER_THRESHOLD
      for (const [peerId, { analyser, data }] of this._analysers) {
        analyser.getByteTimeDomainData(data)
        let sum = 0
        for (let i = 0; i < data.length; i++) {
          const v = (data[i] - 128) / 128
          sum += v * v
        }
        const rms = Math.sqrt(sum / data.length)
        if (rms > loudestLevel) { loudestLevel = rms; loudest = peerId }
      }
      if (loudest !== this._activeSpeaker) {
        this._activeSpeaker = loudest
        this.emit('active-speaker', loudest)
      }
    }, ACTIVE_SPEAKER_INTERVAL_MS)
  }

  toggleMute() {
    this.muted = !this.muted
    this.localStream?.getAudioTracks().forEach((t) => (t.enabled = !this.muted))
    return this.muted
  }

  toggleCamera() {
    this.cameraOff = !this.cameraOff
    this.localStream?.getVideoTracks().forEach((t) => (t.enabled = !this.cameraOff))
    return this.cameraOff
  }

  async startScreenShare() {
    if (this.screenSharing) return
    if (!navigator?.mediaDevices?.getDisplayMedia) {
      throw new Error('getDisplayMedia not available in this browser')
    }
    const displayStream = await navigator.mediaDevices.getDisplayMedia({
      video: { cursor: 'always' },
      audio: false,
    })
    this.screenStream = displayStream
    this.screenSharing = true

    const screenTrack = displayStream.getVideoTracks()[0]
    // Add/replace screen track in every active PeerConn.
    for (const pc of this.peers.values()) {
      if (!pc.pc) continue
      const existingScreenSender = pc.pc.getSenders().find((s) => s._isScreen)
      if (existingScreenSender) {
        existingScreenSender.replaceTrack(screenTrack)
      } else {
        const sender = pc.pc.addTrack(screenTrack, displayStream)
        sender._isScreen = true
      }
    }

    // Notify remote peers via signaling.
    this._sendSignal({ kind: 'screen-share', data: { presenting: true } })
    this.emit('screen-share', this.peerId)

    // Auto-stop when user ends sharing from browser UI.
    screenTrack.addEventListener('ended', () => {
      this.stopScreenShare()
    }, { once: true })
  }

  stopScreenShare() {
    if (!this.screenSharing) return
    this.screenSharing = false

    // Stop the screen tracks.
    if (this.screenStream) {
      this.screenStream.getTracks().forEach((t) => t.stop())
      this.screenStream = null
    }

    // Remove the screen senders from all peers.
    for (const pc of this.peers.values()) {
      if (!pc.pc) continue
      const screenSender = pc.pc.getSenders().find((s) => s._isScreen)
      if (screenSender) {
        try { pc.pc.removeTrack(screenSender) } catch {}
      }
    }

    // Notify remote peers.
    this._sendSignal({ kind: 'screen-share', data: { presenting: false } })
    this.emit('screen-share', null)
  }

  leave() {
    if (this._activeTimer) { clearInterval(this._activeTimer); this._activeTimer = null }
    if (this.screenSharing) {
      this.screenSharing = false
      try { this.screenStream?.getTracks().forEach((t) => t.stop()) } catch {}
      this.screenStream = null
    }
    for (const pc of this.peers.values()) pc.close()
    this.peers.clear()
    this._analysers.clear()
    try { this.session?.close() } catch {}
    this.session = null
    try { this.localStream?.getTracks().forEach((t) => t.stop()) } catch {}
    this.localStream = null
    try { this._audioCtx?.close() } catch {}
    this._audioCtx = null
    this._setState('closed')
  }
}

export async function createCall({ sessionId, identity, video = true }) {
  if (!sessionId) throw new Error('sessionId required')
  const call = new Call({ sessionId, identity, video })
  await call._init()
  return call
}

export { Call }
