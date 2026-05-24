/**
 * signaling.js — Vulos RELAY signaling client for vulos-office.
 *
 * Opens a WebSocket to the Vulos OS signaling stream
 * (GET /api/peering/stream) and multiplexes offer/answer/ICE frames
 * over the "signal" channel defined by the OS ws.go Hub.
 *
 * Frame envelope (mirrors ws.go):
 *   { channel: "signal", from: <userID>, payload: <SignalPayload> }
 *
 * SignalPayload:
 *   { type: "offer"|"answer"|"ice"|"join"|"leave",
 *     session: <sessionID>,
 *     to: <peerID>,          // targeted delivery (optional; omit = broadcast)
 *     sdp: <string>,         // offer / answer
 *     candidate: <RTCIceCandidateInit>,
 *   }
 */

const RECONNECT_BASE_MS = 1_000
const RECONNECT_MAX_MS = 30_000
const SIGNAL_CHANNEL = 'signal'

export class SignalingClient extends EventTarget {
  /**
   * @param {object} opts
   * @param {string}   opts.signalingUrl  - WebSocket URL, e.g. "ws://localhost:8080/api/peering/stream"
   * @param {string}   opts.sessionId     - fabric session / document id
   * @param {string}   opts.peerId        - this client's identity token (injected by auth)
   * @param {string}  [opts.authToken]    - Bearer JWT (if auth is enabled)
   */
  constructor({ signalingUrl, sessionId, peerId, authToken = null }) {
    super()
    this._url = signalingUrl
    this._session = sessionId
    this._peerId = peerId
    this._authToken = authToken
    this._ws = null
    this._reconnectDelay = RECONNECT_BASE_MS
    this._stopped = false
    this._messageHandlers = new Set()
  }

  /** Connect (or reconnect) to the signaling WebSocket. */
  connect() {
    if (this._stopped) return
    const url = this._authToken
      ? `${this._url}?token=${encodeURIComponent(this._authToken)}`
      : this._url

    const ws = new WebSocket(url)
    this._ws = ws

    ws.addEventListener('open', () => {
      this._reconnectDelay = RECONNECT_BASE_MS
      this.dispatchEvent(new CustomEvent('signaling-open'))
      // Announce ourselves to the session room.
      this._send({ type: 'join', session: this._session })
    })

    ws.addEventListener('message', (ev) => {
      let frame
      try { frame = JSON.parse(ev.data) } catch { return }
      if (frame.channel !== SIGNAL_CHANNEL) return
      // Only deliver frames addressed to this session and this peer (or broadcast).
      const p = frame.payload
      if (!p) return
      if (p.session && p.session !== this._session) return
      if (p.to && p.to !== this._peerId) return
      this.dispatchEvent(new CustomEvent('signal', { detail: { from: frame.from, payload: p } }))
    })

    ws.addEventListener('close', () => {
      if (this._stopped) return
      this.dispatchEvent(new CustomEvent('signaling-close'))
      this._scheduleReconnect()
    })

    ws.addEventListener('error', () => {
      // 'close' will follow; handled there.
    })
  }

  /** Send a signal payload to a specific peer (or broadcast to session). */
  signal(type, toId, data = {}) {
    this._send({ type, session: this._session, to: toId, ...data })
  }

  /** Cleanly stop reconnecting and close the socket. */
  close() {
    this._stopped = true
    if (this._ws) {
      this._send({ type: 'leave', session: this._session })
      this._ws.close()
      this._ws = null
    }
  }

  // ─── private ───────────────────────────────────────────────────────────────

  _send(payload) {
    if (!this._ws || this._ws.readyState !== WebSocket.OPEN) return
    const frame = JSON.stringify({
      channel: SIGNAL_CHANNEL,
      payload,
    })
    this._ws.send(frame)
  }

  _scheduleReconnect() {
    const delay = this._reconnectDelay
    this._reconnectDelay = Math.min(this._reconnectDelay * 2, RECONNECT_MAX_MS)
    setTimeout(() => this.connect(), delay)
  }
}
