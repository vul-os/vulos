/**
 * useYDoc — React hook for Yjs collaborative document sync.
 *
 * Connects to the backend's collab WebSocket endpoint for a given docId and
 * keeps a Y.Doc in sync with all other connected peers in real time.  Also
 * handles awareness (cursor positions, user presence) and persists state
 * through the server on reconnect.
 *
 * Wire protocol
 *   The hook speaks JSON frames of the shape used by collab.go:
 *     { type: "collab:update"|"collab:awareness"|"collab:sync", doc_id, payload }
 *   where `payload` is the raw Yjs binary blob encoded as a base64 string.
 *
 * Usage
 *   const { doc, awareness, connected } = useYDoc('my-doc-id')
 *   // doc is a Y.Doc — wire it to a TipTap provider, Quill binding, etc.
 *   // awareness is a Y.Doc-level awareness object (local state key/value map).
 *
 * Awareness
 *   Call awareness.setLocalStateField(key, value) to broadcast your own
 *   cursor/user state.  All remote awareness states are available via
 *   awareness.getStates().  The hook subscribes to awareness changes and
 *   broadcasts them over the WS.  On WS close, the hook clears the local
 *   awareness state so remote peers see the disconnect.
 */
import { useEffect, useRef, useState, useCallback } from 'react'
import * as Y from 'yjs'

// ── Message type constants (mirror collab.go) ──────────────────────────────

const MSG_UPDATE    = 'collab:update'
const MSG_AWARENESS = 'collab:awareness'
const MSG_SYNC      = 'collab:sync'

// ── Helpers ────────────────────────────────────────────────────────────────

/** Encode a Uint8Array to a base64 string (browser-safe, no Node deps). */
function toBase64(bytes) {
  let binary = ''
  for (let i = 0; i < bytes.byteLength; i++) {
    binary += String.fromCharCode(bytes[i])
  }
  return btoa(binary)
}

/** Decode a base64 string to a Uint8Array. */
function fromBase64(b64) {
  const binary = atob(b64)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i)
  }
  return bytes
}

/** Build the WS URL for a given docId. */
function wsURL(docId) {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  return `${proto}://${location.host}/api/peering/collab/${encodeURIComponent(docId)}/sync`
}

// ── Simple awareness implementation ───────────────────────────────────────

/**
 * CollabAwareness is a lightweight awareness object backed by a Y.Map on the
 * shared document.  It broadcasts encoded state blobs over the WS channel
 * so that all peers receive cursor/user-presence updates in real time.
 *
 * API (subset of y-protocols/awareness for compatibility):
 *   setLocalStateField(key, value)  — update one field of the local state
 *   getLocalState()                 — current local state map
 *   getStates()                     — Map<clientId, stateObj> for all peers
 *   on('change', fn)                — subscribe to any state change
 *   off('change', fn)               — unsubscribe
 *   destroy()                       — remove listeners and clear local state
 */
class CollabAwareness {
  constructor(doc) {
    this.doc = doc
    this.clientID = doc.clientID
    this._localState = {}
    // Map<clientId (number), stateObj>
    this._states = new Map()
    this._states.set(this.clientID, this._localState)
    this._changeHandlers = new Set()
    // sendFn is set by useYDoc to relay blobs over the WS.
    this._sendFn = null
  }

  setLocalStateField(key, value) {
    this._localState = { ...this._localState, [key]: value }
    this._states.set(this.clientID, this._localState)
    this._emit()
    this._broadcastLocal()
  }

  getLocalState() {
    return this._localState
  }

  getStates() {
    return this._states
  }

  on(event, fn) {
    if (event === 'change') this._changeHandlers.add(fn)
  }

  off(event, fn) {
    if (event === 'change') this._changeHandlers.delete(fn)
  }

  /** Apply an encoded awareness blob received from the WS. */
  applyUpdate(encoded) {
    let updates
    try {
      updates = JSON.parse(new TextDecoder().decode(encoded))
    } catch {
      return
    }
    if (!Array.isArray(updates)) return
    for (const { clientID, state } of updates) {
      if (state === null) {
        this._states.delete(clientID)
      } else {
        this._states.set(clientID, state)
      }
    }
    this._emit()
  }

  /** Encode the local awareness state to a binary blob for the WS. */
  encodeLocalState() {
    const payload = [{ clientID: this.clientID, state: this._localState }]
    return new TextEncoder().encode(JSON.stringify(payload))
  }

  /** Clear local state (called on WS close so remote peers see the leave). */
  clearLocalState() {
    this._localState = {}
    this._states.set(this.clientID, this._localState)
    this._broadcastLocal()
    this._emit()
  }

  _emit() {
    for (const fn of this._changeHandlers) {
      try { fn() } catch { /* ignore */ }
    }
  }

  _broadcastLocal() {
    if (this._sendFn) {
      this._sendFn(this.encodeLocalState())
    }
  }

  destroy() {
    this._changeHandlers.clear()
    this.clearLocalState()
    this._sendFn = null
  }
}

// ── useYDoc hook ────────────────────────────────────────────────────────────

/**
 * useYDoc(docId, options?) — connect to a shared Y.Doc.
 *
 * @param {string} docId    - Unique document identifier.
 * @param {object} [opts]
 * @param {boolean} [opts.connect=true] - Set false to skip WS connection
 *   (useful for offline-only rendering).
 *
 * @returns {{ doc: Y.Doc, awareness: CollabAwareness, connected: boolean }}
 */
export function useYDoc(docId, opts = {}) {
  const { connect: shouldConnect = true } = opts

  // Stable Y.Doc per docId — recreate only when docId changes.
  const docRef      = useRef(null)
  const awarenessRef = useRef(null)
  const wsRef       = useRef(null)
  const retryRef    = useRef(0)
  const aliveRef    = useRef(true)
  const connectRef  = useRef(null)

  const [connected, setConnected] = useState(false)

  // Initialise doc + awareness lazily (or when docId changes).
  /* eslint-disable react-hooks/refs */
  if (!docRef.current) {
    docRef.current = new Y.Doc()
    awarenessRef.current = new CollabAwareness(docRef.current)
  }
  /* eslint-enable react-hooks/refs */

  // ── Send helpers ──────────────────────────────────────────────────────────

  const sendFrame = useCallback((type, payload) => {
    const ws = wsRef.current
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    try {
      ws.send(JSON.stringify({ type, doc_id: docId, payload: toBase64(payload) }))
    } catch (err) {
      console.error('[useYDoc] send error', err)
    }
  }, [docId])

  const sendUpdate    = useCallback(u => sendFrame(MSG_UPDATE, u),    [sendFrame])
  const sendAwareness = useCallback(u => sendFrame(MSG_AWARENESS, u), [sendFrame])

  // ── Connect ───────────────────────────────────────────────────────────────

  const connect = useCallback(() => {
    if (!aliveRef.current || !shouldConnect) return
    if (wsRef.current && wsRef.current.readyState < WebSocket.CLOSING) return

    const ws = new WebSocket(wsURL(docId))
    wsRef.current = ws

    ws.onopen = () => {
      setConnected(true)
      retryRef.current = 0
      // Wire awareness sender now that the WS is open.
      awarenessRef.current._sendFn = sendAwareness
      // Request current persisted state.
      const syncReq = JSON.stringify({ type: MSG_SYNC, doc_id: docId, payload: '' })
      ws.send(syncReq)
    }

    ws.onmessage = (event) => {
      let frame
      try { frame = JSON.parse(event.data) } catch { return }
      if (!frame || frame.doc_id !== docId) return

      const payloadBytes = frame.payload ? fromBase64(frame.payload) : null

      switch (frame.type) {
        case MSG_UPDATE:
          // Apply the remote Yjs update to the local doc — suppress our own
          // update listener to avoid echo-looping back to the server.
          if (payloadBytes && payloadBytes.length > 0) {
            Y.applyUpdate(docRef.current, payloadBytes, 'remote')
          }
          break

        case MSG_SYNC:
          // Server sent the full persisted state.  Apply only if non-empty so
          // we don't overwrite a locally-initialised new doc.
          if (payloadBytes && payloadBytes.length > 0) {
            Y.applyUpdate(docRef.current, payloadBytes, 'remote')
          }
          break

        case MSG_AWARENESS:
          if (payloadBytes && payloadBytes.length > 0) {
            awarenessRef.current.applyUpdate(payloadBytes)
          }
          break

        default:
          break
      }
    }

    ws.onclose = () => {
      setConnected(false)
      wsRef.current = null
      // Detach awareness sender and broadcast leave.
      if (awarenessRef.current) {
        awarenessRef.current._sendFn = null
        awarenessRef.current.clearLocalState()
      }
      if (!aliveRef.current) return
      // Exponential back-off: 1s → 2s → 4s … capped at 30s.
      const delay = Math.min(1000 * 2 ** retryRef.current, 30_000)
      retryRef.current = Math.min(retryRef.current + 1, 10)
      setTimeout(() => connectRef.current?.(), delay)
    }

    ws.onerror = () => ws.close()
  }, [docId, shouldConnect, sendAwareness])

  useEffect(() => { connectRef.current = connect }, [connect])

  // ── Doc update → WS ──────────────────────────────────────────────────────

  useEffect(() => {
    const doc = docRef.current
    const onUpdate = (update, origin) => {
      // Don't echo updates that came from the server.
      if (origin === 'remote') return
      sendUpdate(update)
    }
    doc.on('update', onUpdate)
    return () => doc.off('update', onUpdate)
  }, [sendUpdate])

  // ── Lifecycle ─────────────────────────────────────────────────────────────

  useEffect(() => {
    aliveRef.current = true
    connect()
    return () => {
      // Don't close on every component remount — only on explicit teardown.
    }
  }, [connect])

  // Recreate doc + awareness if docId changes.
  const prevDocIdRef = useRef(docId)
  useEffect(() => {
    if (prevDocIdRef.current === docId) return
    prevDocIdRef.current = docId
    // Tear down old doc.
    if (awarenessRef.current) awarenessRef.current.destroy()
    if (wsRef.current) {
      aliveRef.current = false
      wsRef.current.close()
      wsRef.current = null
    }
    // Create fresh doc + awareness.
    docRef.current = new Y.Doc()
    awarenessRef.current = new CollabAwareness(docRef.current)
    retryRef.current = 0
    aliveRef.current = true
    connect()
  }, [docId, connect])

  /**
   * destroy() — permanently close the WS and destroy doc/awareness.
   * Call this when the component/context that owns the doc is unmounted.
   */
  const destroy = useCallback(() => {
    aliveRef.current = false
    if (awarenessRef.current) awarenessRef.current.destroy()
    if (wsRef.current) wsRef.current.close()
  }, [])

  /* eslint-disable react-hooks/refs */
  return {
    doc:       docRef.current,
    awareness: awarenessRef.current,
    connected,
    destroy,
  }
  /* eslint-enable react-hooks/refs */
}
