/**
 * fabric.js — Vulos Office fabric client adapter (OFFICE-20).
 *
 * Joins a Vulos fabric session for a given document/session id:
 *  1. Fetches ICE/TURN credentials from the OS relay  (/api/peering/ice  or
 *     the cloud /api/turn/credentials as fallback).
 *  2. Opens a SignalingClient to the OS peering WebSocket stream.
 *  3. Negotiates a WebRTC RTCPeerConnection per remote peer (offer/answer/ICE
 *     via the "signal" channel).
 *  4. Opens an RTCDataChannel for duplex application messages.
 *  5. Falls back to a relay circuit (via the Vulos relay deposit/pickup HTTP
 *     API) when the data channel cannot be established within RELAY_TIMEOUT_MS.
 *
 * Usage:
 *   const fc = new FabricClient({ sessionId, peerId, signalingUrl, iceUrl })
 *   fc.addEventListener('message', ({ detail: { from, data } }) => …)
 *   fc.addEventListener('state',   ({ detail: { peerId, state } }) => …)
 *   await fc.join()
 *   fc.send(data)           // broadcast to all connected peers
 *   fc.sendTo(peerId, data) // unicast
 *   fc.leave()
 *
 * State values: 'connecting' | 'connected' | 'relay' | 'disconnected'
 *
 * Pure JS/JSX — no CGO, no native deps.
 */

import { SignalingClient } from './signaling.js'

const DATA_CHANNEL_LABEL = 'vulos-office-fabric'
const RELAY_TIMEOUT_MS = 8_000        // give P2P this long before falling back
const RELAY_POLL_MS = 2_000           // relay pickup polling interval
const RELAY_TTL_HOURS = 1

export class FabricClient extends EventTarget {
  /**
   * @param {object} opts
   * @param {string}   opts.sessionId      - document / room id
   * @param {string}   opts.peerId         - this peer's identity
   * @param {string}   opts.signalingUrl   - ws[s]://host/api/peering/stream
   * @param {string}  [opts.iceUrl]        - GET URL returning { ice_servers: [...] }
   *                                         defaults to /api/peering/ice
   * @param {string}  [opts.relayBaseUrl]  - base URL for relay deposit/pickup
   *                                         defaults to '' (same origin)
   * @param {string}  [opts.authToken]     - Bearer JWT (optional)
   */
  constructor({
    sessionId,
    peerId,
    signalingUrl,
    iceUrl = '/api/peering/ice',
    relayBaseUrl = '',
    authToken = null,
  }) {
    super()
    this._session = sessionId
    this._peerId = peerId
    this._iceUrl = iceUrl
    this._relayBase = relayBaseUrl
    this._authToken = authToken

    /** @type {Map<string, PeerState>} */
    this._peers = new Map()
    this._iceServers = []
    this._relayPollTimer = null
    this._stopped = false

    this._signaling = new SignalingClient({ signalingUrl, sessionId, peerId, authToken })
    this._signaling.addEventListener('signal', (ev) => this._onSignal(ev.detail))
    this._signaling.addEventListener('signaling-open', () => {
      // Re-offer to any existing peers after a reconnect.
      for (const [id, ps] of this._peers) {
        if (ps.state === 'disconnected') this._initiatePeer(id)
      }
    })
    this._signaling.addEventListener('signaling-close', () => {
      // Signaling dropped — data channels may still be alive.
    })
  }

  // ─── Public API ────────────────────────────────────────────────────────────

  /** Connect to the fabric session.  Resolves once signaling is up. */
  async join() {
    this._iceServers = await this._fetchICE()
    this._signaling.connect()
  }

  /** Broadcast a message to all connected peers. */
  send(data) {
    for (const ps of this._peers.values()) {
      this._sendToPeer(ps, data)
    }
  }

  /** Send a message to one specific peer. */
  sendTo(peerId, data) {
    const ps = this._peers.get(peerId)
    if (ps) this._sendToPeer(ps, data)
  }

  /** Disconnect from all peers and the signaling channel. */
  leave() {
    this._stopped = true
    clearInterval(this._relayPollTimer)
    for (const ps of this._peers.values()) {
      ps.dc?.close()
      ps.pc?.close()
    }
    this._peers.clear()
    this._signaling.close()
  }

  /** Read-only snapshot of current peer states. */
  get peerStates() {
    const out = {}
    for (const [id, ps] of this._peers) out[id] = ps.state
    return out
  }

  // ─── ICE / TURN ────────────────────────────────────────────────────────────

  async _fetchICE() {
    try {
      const headers = this._authToken ? { Authorization: `Bearer ${this._authToken}` } : {}
      const res = await fetch(this._iceUrl, { headers })
      if (!res.ok) throw new Error(`ICE fetch ${res.status}`)
      const body = await res.json()
      return body.ice_servers || []
    } catch (err) {
      console.warn('[fabric] ICE fetch failed, using STUN only:', err.message)
      return [{ urls: ['stun:stun.l.google.com:19302'] }]
    }
  }

  // ─── Peer management ───────────────────────────────────────────────────────

  _getOrCreatePeer(remoteId) {
    if (this._peers.has(remoteId)) return this._peers.get(remoteId)
    const ps = new PeerState(remoteId)
    this._peers.set(remoteId, ps)
    return ps
  }

  /** Initiate an offer to a remote peer (called when we see their 'join'). */
  async _initiatePeer(remoteId) {
    const ps = this._getOrCreatePeer(remoteId)
    if (ps.state === 'connected') return
    ps.reset()

    const pc = this._buildPC(remoteId, ps)
    ps.pc = pc

    // Polite peer: lexicographically smaller peerId defers (impolite = offerer).
    const impolite = this._peerId < remoteId
    if (!impolite) return  // wait for the other side to offer

    const dc = pc.createDataChannel(DATA_CHANNEL_LABEL)
    ps.dc = dc
    this._wireDataChannel(dc, remoteId, ps)

    try {
      const offer = await pc.createOffer()
      await pc.setLocalDescription(offer)
      this._setRelayTimer(remoteId, ps)
      this._signaling.signal('offer', remoteId, { sdp: pc.localDescription.sdp })
      this._setPeerState(remoteId, ps, 'connecting')
    } catch (err) {
      console.error('[fabric] offer error:', err)
    }
  }

  _buildPC(remoteId, ps) {
    const pc = new RTCPeerConnection({ iceServers: this._iceServers })

    pc.addEventListener('icecandidate', ({ candidate }) => {
      if (candidate) {
        this._signaling.signal('ice', remoteId, { candidate: candidate.toJSON() })
      }
    })

    pc.addEventListener('connectionstatechange', () => {
      const s = pc.connectionState
      if (s === 'connected') {
        clearTimeout(ps.relayTimer)
        this._setPeerState(remoteId, ps, 'connected')
      } else if (s === 'failed' || s === 'closed') {
        clearTimeout(ps.relayTimer)
        if (!this._stopped && ps.state !== 'relay') {
          this._activateRelay(remoteId, ps)
        }
      }
    })

    pc.addEventListener('datachannel', ({ channel }) => {
      ps.dc = channel
      this._wireDataChannel(channel, remoteId, ps)
    })

    return pc
  }

  _wireDataChannel(dc, remoteId, ps) {
    dc.binaryType = 'arraybuffer'

    dc.addEventListener('open', () => {
      clearTimeout(ps.relayTimer)
      this._setPeerState(remoteId, ps, 'connected')
    })

    dc.addEventListener('message', ({ data }) => {
      this.dispatchEvent(new CustomEvent('message', { detail: { from: remoteId, data } }))
    })

    dc.addEventListener('close', () => {
      if (!this._stopped) {
        this._setPeerState(remoteId, ps, 'disconnected')
        // Attempt reconnect after a short back-off.
        setTimeout(() => {
          if (!this._stopped) this._initiatePeer(remoteId)
        }, 2_000)
      }
    })

    dc.addEventListener('error', (ev) => {
      console.warn('[fabric] data channel error:', ev)
    })
  }

  // ─── Signaling handler ─────────────────────────────────────────────────────

  async _onSignal({ from, payload }) {
    const { type, sdp, candidate } = payload
    if (from === this._peerId) return   // ignore self-echoes

    if (type === 'join') {
      await this._initiatePeer(from)
      return
    }

    if (type === 'leave') {
      const ps = this._peers.get(from)
      if (ps) {
        ps.dc?.close()
        ps.pc?.close()
        this._setPeerState(from, ps, 'disconnected')
      }
      return
    }

    const ps = this._getOrCreatePeer(from)

    if (type === 'offer') {
      ps.reset()
      const pc = this._buildPC(from, ps)
      ps.pc = pc
      try {
        await pc.setRemoteDescription({ type: 'offer', sdp })
        // Apply any queued candidates.
        for (const c of ps.pendingCandidates) await pc.addIceCandidate(c)
        ps.pendingCandidates = []
        const answer = await pc.createAnswer()
        await pc.setLocalDescription(answer)
        this._setRelayTimer(from, ps)
        this._signaling.signal('answer', from, { sdp: pc.localDescription.sdp })
        this._setPeerState(from, ps, 'connecting')
      } catch (err) {
        console.error('[fabric] answer error:', err)
      }
    } else if (type === 'answer') {
      if (!ps.pc) return
      try {
        await ps.pc.setRemoteDescription({ type: 'answer', sdp })
        for (const c of ps.pendingCandidates) await ps.pc.addIceCandidate(c)
        ps.pendingCandidates = []
      } catch (err) {
        console.error('[fabric] setRemoteDescription answer error:', err)
      }
    } else if (type === 'ice') {
      if (!candidate) return
      if (ps.pc && ps.pc.remoteDescription) {
        try { await ps.pc.addIceCandidate(candidate) } catch { /* ignore stale */ }
      } else {
        ps.pendingCandidates.push(candidate)
      }
    }
  }

  // ─── Relay fallback ────────────────────────────────────────────────────────

  _setRelayTimer(remoteId, ps) {
    clearTimeout(ps.relayTimer)
    ps.relayTimer = setTimeout(() => {
      if (ps.state !== 'connected') {
        this._activateRelay(remoteId, ps)
      }
    }, RELAY_TIMEOUT_MS)
  }

  _activateRelay(remoteId, ps) {
    if (ps.state === 'relay') return
    console.info(`[fabric] P2P failed for ${remoteId}; switching to relay circuit`)
    this._setPeerState(remoteId, ps, 'relay')
    this._startRelayPolling()
  }

  /** Start polling the relay pickup endpoint for any peers in relay mode. */
  _startRelayPolling() {
    if (this._relayPollTimer) return
    this._relayPollTimer = setInterval(() => this._relayPoll(), RELAY_POLL_MS)
  }

  async _relayPoll() {
    const relayPeers = [...this._peers.entries()].filter(([, ps]) => ps.state === 'relay')
    if (relayPeers.length === 0) {
      clearInterval(this._relayPollTimer)
      this._relayPollTimer = null
      return
    }
    try {
      const headers = { 'Content-Type': 'application/json' }
      if (this._authToken) headers['Authorization'] = `Bearer ${this._authToken}`

      const res = await fetch(`${this._relayBase}/api/peering/relay/pickup`, {
        method: 'GET',
        headers: {
          ...headers,
          // Relay auth header: "Vula-Relay <peerId>.<ts>" (simplified for browser)
          Authorization: `Vula-Relay ${this._peerId}.${Math.floor(Date.now() / 1000)}`,
        },
      })
      if (!res.ok) return
      const { blobs } = await res.json()
      if (!Array.isArray(blobs)) return

      const ackIds = []
      for (const blob of blobs) {
        try {
          const msg = JSON.parse(atob(blob.blob_b64))
          if (msg.session !== this._session) continue
          this.dispatchEvent(new CustomEvent('message', { detail: { from: blob.from, data: msg.data } }))
          ackIds.push(blob.id)
        } catch { /* malformed blob, skip */ }
      }

      if (ackIds.length) {
        await fetch(`${this._relayBase}/api/peering/relay/ack`, {
          method: 'POST',
          headers,
          body: JSON.stringify({ blob_ids: ackIds }),
        }).catch(() => { /* best-effort */ })
      }
    } catch (err) {
      console.warn('[fabric] relay poll error:', err.message)
    }
  }

  /** Deposit a message via the Vulos relay for a specific peer. */
  async _relayDeposit(toPeerId, data) {
    try {
      const headers = { 'Content-Type': 'application/json' }
      if (this._authToken) headers['Authorization'] = `Bearer ${this._authToken}`
      const payload = { session: this._session, data }
      const blob_b64 = btoa(JSON.stringify(payload))
      await fetch(`${this._relayBase}/api/peering/relay/deposit`, {
        method: 'POST',
        headers,
        body: JSON.stringify({
          to: toPeerId,
          blob_b64,
          ttl_hours: RELAY_TTL_HOURS,
          nonce: crypto.randomUUID(),
          // Signature omitted in browser — relay accept policy depends on server trust config.
        }),
      })
    } catch (err) {
      console.warn('[fabric] relay deposit error:', err.message)
    }
  }

  // ─── Helpers ───────────────────────────────────────────────────────────────

  _setPeerState(peerId, ps, state) {
    ps.state = state
    this.dispatchEvent(new CustomEvent('state', { detail: { peerId, state } }))
  }

  _sendToPeer(ps, data) {
    if (ps.state === 'connected' && ps.dc && ps.dc.readyState === 'open') {
      ps.dc.send(data)
    } else if (ps.state === 'relay') {
      // Relay path: encode and deposit.
      this._relayDeposit(ps.id, data)
    }
    // 'connecting' / 'disconnected' → silently drop (caller should buffer).
  }
}

// ─── Internal per-peer state ────────────────────────────────────────────────

class PeerState {
  constructor(id) {
    this.id = id
    this.pc = null            // RTCPeerConnection
    this.dc = null            // RTCDataChannel
    this.state = 'disconnected'
    this.relayTimer = null
    this.pendingCandidates = []
  }

  reset() {
    clearTimeout(this.relayTimer)
    this.dc?.close()
    this.pc?.close()
    this.pc = null
    this.dc = null
    this.pendingCandidates = []
  }
}
