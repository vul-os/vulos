/**
 * Vulos — Notes Collaboration Layer (PEER-32, fixed protocol)
 *
 * Vanilla-JS collab module for the Notes app (apps/notes/index.html).
 * Speaks the SAME custom JSON-frame protocol as collab.go / useYDoc.js
 * (PEER-30) — NOT y-websocket binary protocol.
 *
 * Wire protocol (mirrors collab.go constants):
 *   { type: "collab:update"|"collab:awareness"|"collab:sync",
 *     doc_id, payload }
 *   where payload is a base64-encoded binary blob.
 *
 * Usage:
 *   const session = await startCollab(noteId, textarea, statusEl, displayName)
 *   session.destroy()   // when switching notes / unmounting
 *
 * This file intentionally has NO CDN dependencies. Yjs is imported via the
 * same-origin `<script type="importmap">` in index.html (yjs -> ./vendor/yjs.js),
 * vendored by scripts/vendor-apps.mjs. If that vendored bundle is not present
 * (importmap unresolved), the yjs import throws and collab degrades gracefully.
 */

/* ── Message type constants (mirror collab.go) ──────────────────────────── */
const MSG_UPDATE    = 'collab:update'
const MSG_AWARENESS = 'collab:awareness'
const MSG_SYNC      = 'collab:sync'

/* ── Helpers ─────────────────────────────────────────────────────────────── */

function toBase64(bytes) {
  let binary = ''
  for (let i = 0; i < bytes.byteLength; i++) binary += String.fromCharCode(bytes[i])
  return btoa(binary)
}

function fromBase64(b64) {
  const binary = atob(b64)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i)
  return bytes
}

function wsURL(docId) {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  return `${proto}://${location.host}/api/peering/collab/${encodeURIComponent(docId)}/sync`
}

/* ── Cursor colours ──────────────────────────────────────────────────────── */
const CURSOR_COLOURS = [
  '#3b82f6', '#ef4444', '#10b981', '#f59e0b',
  '#8b5cf6', '#ec4899', '#14b8a6', '#f97316',
]

function colourForClient(clientId) {
  return CURSOR_COLOURS[Number(clientId) % CURSOR_COLOURS.length]
}

/* ── Cursor overlay ──────────────────────────────────────────────────────── */
class CursorOverlay {
  constructor(textarea) {
    this.textarea = textarea
    this.cursors  = {}  // clientId → { el, label }

    this.mirror = document.createElement('div')
    Object.assign(this.mirror.style, {
      position: 'absolute', top: '0', left: '0',
      visibility: 'hidden', whiteSpace: 'pre-wrap', wordBreak: 'break-word',
      font: getComputedStyle(textarea).font,
      padding: getComputedStyle(textarea).padding,
      width: textarea.offsetWidth + 'px', boxSizing: 'border-box',
    })
    document.body.appendChild(this.mirror)

    this.container = document.createElement('div')
    Object.assign(this.container.style, {
      position: 'absolute', pointerEvents: 'none',
      top: '0', left: '0', width: '100%', height: '100%',
      overflow: 'hidden', zIndex: '10',
    })
    const parent = textarea.parentElement
    parent.style.position = 'relative'
    parent.appendChild(this.container)
  }

  coordsAt(index) {
    const ta = this.textarea
    this.mirror.style.width   = ta.offsetWidth + 'px'
    this.mirror.style.font    = getComputedStyle(ta).font
    this.mirror.style.padding = getComputedStyle(ta).padding
    this.mirror.textContent   = ta.value.slice(0, index)

    const span = document.createElement('span')
    span.textContent = '|'
    this.mirror.appendChild(span)
    const taRect = ta.getBoundingClientRect()
    const spRect = span.getBoundingClientRect()
    this.mirror.removeChild(span)
    return { x: spRect.left - taRect.left, y: spRect.top - taRect.top + ta.scrollTop }
  }

  updateCursor(clientId, index, name, colour) {
    if (!this.cursors[clientId]) {
      const el    = document.createElement('div')
      const label = document.createElement('div')
      Object.assign(el.style, {
        position: 'absolute', width: '2px', backgroundColor: colour, pointerEvents: 'none',
      })
      Object.assign(label.style, {
        position: 'absolute', top: '-18px', left: '0',
        background: colour, color: '#fff', fontSize: '10px',
        padding: '1px 4px', borderRadius: '3px', whiteSpace: 'nowrap', pointerEvents: 'none',
      })
      label.textContent = name || 'Peer'
      el.appendChild(label)
      this.container.appendChild(el)
      this.cursors[clientId] = { el, label }
    }
    const { el } = this.cursors[clientId]
    const lineHeight = parseInt(getComputedStyle(this.textarea).lineHeight) || 20
    try {
      const { x, y } = this.coordsAt(index)
      Object.assign(el.style, {
        left: x + 'px', top: y + 'px', height: lineHeight + 'px', display: 'block',
      })
    } catch (_) {
      el.style.display = 'none'
    }
  }

  removeCursor(clientId) {
    if (this.cursors[clientId]) {
      this.cursors[clientId].el.remove()
      delete this.cursors[clientId]
    }
  }

  destroy() {
    this.container.remove()
    this.mirror.remove()
  }
}

/* ── Lightweight awareness (JSON-blob over WS, mirrors useYDoc.js) ───────── */
class SimpleAwareness {
  constructor(clientID) {
    this.clientID    = clientID
    this._local      = {}
    this._states     = new Map([[clientID, {}]])
    this._handlers   = new Set()
    this._sendFn     = null
  }

  setLocalStateField(key, value) {
    this._local = { ...this._local, [key]: value }
    this._states.set(this.clientID, this._local)
    this._emit()
    this._broadcastLocal()
  }

  getStates() { return this._states }

  on(event, fn)  { if (event === 'change') this._handlers.add(fn) }
  off(event, fn) { if (event === 'change') this._handlers.delete(fn) }

  applyUpdate(bytes) {
    let updates
    try { updates = JSON.parse(new TextDecoder().decode(bytes)) } catch { return }
    if (!Array.isArray(updates)) return
    for (const { clientID, state } of updates) {
      if (state === null) this._states.delete(clientID)
      else this._states.set(clientID, state)
    }
    this._emit()
  }

  encodeLocalState() {
    const payload = [{ clientID: this.clientID, state: this._local }]
    return new TextEncoder().encode(JSON.stringify(payload))
  }

  clearLocalState() {
    this._local = {}
    this._states.set(this.clientID, this._local)
    this._broadcastLocal()
    this._emit()
  }

  _emit() {
    for (const fn of this._handlers) { try { fn() } catch (_) {} }
  }

  _broadcastLocal() {
    if (this._sendFn) this._sendFn(this.encodeLocalState())
  }

  destroy() {
    this._handlers.clear()
    this.clearLocalState()
    this._sendFn = null
  }
}

/* ── Main entry point ────────────────────────────────────────────────────── */
/**
 * @param {string}              noteId      - The note's ID (= collab doc_id)
 * @param {HTMLTextAreaElement} textarea
 * @param {HTMLElement}         statusEl    - Status bar element
 * @param {string}              displayName - Local user's display name
 * @returns {Promise<{ destroy: Function }>}
 */
export async function startCollab(noteId, textarea, statusEl, displayName = 'Me') {
  // Import Yjs from the project's installed package (served via vite / importmap).
  // Falls back gracefully if yjs is not available in this context.
  let Y
  try {
    Y = await import('yjs')
  } catch (_) {
    statusEl.textContent = 'Collab unavailable'
    return { destroy: () => {} }
  }

  const ydoc  = new Y.Doc()
  const ytext = ydoc.getText('content')
  const awareness = new SimpleAwareness(ydoc.clientID)

  // OFFLINE-DATA-01: persist the Y.Doc locally so notes survive reloads / dead
  // zones and re-converge with the server on reconnect (Yjs union-merge). Loaded
  // BEFORE we connect, so cached content shows immediately offline. Best-effort +
  // graceful (like the yjs import): works standalone with plaintext at rest
  // (device encryption); seals the cache at rest when running under Vulos (the
  // per-app key from window.vulos.offline). See roadmap/OFFLINE-DATA.md.
  let offlinePersist = null
  try {
    const { persistYDoc, resolveOfflineKey } = await import('./vendor/vulos-offline.js')
    const key = await resolveOfflineKey()
    offlinePersist = await persistYDoc(ydoc, { name: `notes:${noteId}`, key })
    await offlinePersist.whenSynced
  } catch (_) { /* offline persistence unavailable — collab still works online */ }

  let ws        = null
  let retries   = 0
  let alive     = true
  let synced    = false
  let destroying = false

  /* ── Send helpers ─────────────────────────────────────────────────────── */
  function sendFrame(type, payload) {
    if (!ws || ws.readyState !== WebSocket.OPEN) return
    try {
      ws.send(JSON.stringify({ type, doc_id: noteId, payload: toBase64(payload) }))
    } catch (_) {}
  }

  function sendAwareness(bytes) { sendFrame(MSG_AWARENESS, bytes) }

  /* ── Connect ──────────────────────────────────────────────────────────── */
  function connect() {
    if (!alive) return
    if (ws && ws.readyState < WebSocket.CLOSING) return

    ws = new WebSocket(wsURL(noteId))

    ws.onopen = () => {
      retries = 0
      awareness._sendFn = sendAwareness
      // Announce local user immediately.
      awareness.setLocalStateField('user', {
        name: displayName,
        colour: colourForClient(ydoc.clientID),
        cursor: null,
      })
      // Request current persisted state from server.
      ws.send(JSON.stringify({ type: MSG_SYNC, doc_id: noteId, payload: '' }))
      statusEl.textContent = 'Collab: connected'
    }

    ws.onmessage = (event) => {
      let frame
      try { frame = JSON.parse(event.data) } catch { return }
      if (!frame || frame.doc_id !== noteId) return

      const payloadBytes = frame.payload ? fromBase64(frame.payload) : null

      switch (frame.type) {
        case MSG_UPDATE:
          if (payloadBytes && payloadBytes.length > 0) {
            Y.applyUpdate(ydoc, payloadBytes, 'remote')
          }
          break

        case MSG_SYNC:
          if (!synced) {
            synced = true
            if (payloadBytes && payloadBytes.length > 0) {
              Y.applyUpdate(ydoc, payloadBytes, 'remote')
              // Remote state wins on first sync.
              ignoreYjs = true
              textarea.value = ytext.toString()
              ignoreYjs = false
            } else if (ytext.length === 0 && textarea.value.length > 0) {
              // Server has no snapshot AND the Y.Text is empty — seed it from the
              // textarea. The `ytext.length === 0` guard is essential now that the
              // offline cache (OFFLINE-DATA-01) can pre-populate ytext before this
              // runs: without it we would insert the textarea on top of restored
              // content and duplicate it. When the offline cache already holds
              // content, ytext is non-empty here and we leave it as the source.
              ydoc.transact(() => { ytext.insert(0, textarea.value) })
            } else if (ytext.length > 0) {
              // Offline-restored content is authoritative for the view.
              ignoreYjs = true
              textarea.value = ytext.toString()
              ignoreYjs = false
            }
          } else if (payloadBytes && payloadBytes.length > 0) {
            Y.applyUpdate(ydoc, payloadBytes, 'remote')
          }
          break

        case MSG_AWARENESS:
          if (payloadBytes && payloadBytes.length > 0) {
            awareness.applyUpdate(payloadBytes)
          }
          break
      }
    }

    ws.onclose = () => {
      ws = null
      awareness._sendFn = null
      awareness.clearLocalState()
      if (!destroying) statusEl.textContent = 'Collab: offline'
      if (!alive) return
      const delay = Math.min(1000 * 2 ** retries, 30_000)
      retries = Math.min(retries + 1, 10)
      setTimeout(connect, delay)
    }

    ws.onerror = () => ws && ws.close()
  }

  /* ── Y.Doc update → WS ─────────────────────────────────────────────────── */
  ydoc.on('update', (update, origin) => {
    if (origin === 'remote') return
    sendFrame(MSG_UPDATE, update)
  })

  /* ── Y.Text → textarea (remote edits arrive) ─────────────────────────── */
  let ignoreYjs = false
  let ignoreInput = false

  ytext.observe(() => {
    if (ignoreYjs || destroying) return
    const newVal = ytext.toString()
    if (textarea.value === newVal) return
    const start = textarea.selectionStart
    const end   = textarea.selectionEnd
    ignoreInput = true
    textarea.value = newVal
    ignoreInput = false
    const newLen = newVal.length
    textarea.setSelectionRange(Math.min(start, newLen), Math.min(end, newLen))
  })

  /* ── textarea → Y.Text (local edits) ─────────────────────────────────── */
  function onInput() {
    if (ignoreInput || destroying) return
    const newVal = textarea.value
    const old    = ytext.toString()
    if (newVal === old) return

    let s = 0
    while (s < old.length && s < newVal.length && old[s] === newVal[s]) s++
    let eOld = old.length - 1
    let eNew = newVal.length - 1
    while (eOld >= s && eNew >= s && old[eOld] === newVal[eNew]) { eOld--; eNew-- }

    const deleteCount = eOld - s + 1
    const insertText  = newVal.slice(s, eNew + 1)

    ydoc.transact(() => {
      if (deleteCount > 0) ytext.delete(s, deleteCount)
      if (insertText.length > 0) ytext.insert(s, insertText)
    })

    awareness.setLocalStateField('user', {
      name: displayName,
      colour: colourForClient(ydoc.clientID),
      cursor: textarea.selectionStart,
    })
  }

  function onSelectionChange() {
    if (destroying) return
    awareness.setLocalStateField('user', {
      name: displayName,
      colour: colourForClient(ydoc.clientID),
      cursor: textarea.selectionStart,
    })
  }

  textarea.addEventListener('input',   onInput)
  textarea.addEventListener('keyup',   onSelectionChange)
  textarea.addEventListener('mouseup', onSelectionChange)

  /* ── Awareness: remote cursors ──────────────────────────────────────────── */
  const overlay = new CursorOverlay(textarea)

  function renderAwareness() {
    if (destroying) return
    const states   = awareness.getStates()
    const localId  = awareness.clientID

    states.forEach((state, clientId) => {
      if (clientId === localId) return
      const user = state.user
      if (!user) return
      const colour = user.colour || colourForClient(clientId)
      const name   = user.name || 'Peer'
      if (user.cursor != null) overlay.updateCursor(clientId, user.cursor, name, colour)
    })

    // Remove stale cursors for departed peers.
    const activeIds = new Set(states.keys())
    Object.keys(overlay.cursors).forEach((id) => {
      if (!activeIds.has(Number(id))) overlay.removeCursor(Number(id))
    })
  }

  awareness.on('change', renderAwareness)

  /* ── Kick off ────────────────────────────────────────────────────────────── */
  connect()

  /* ── Destroy ─────────────────────────────────────────────────────────────── */
  function destroy() {
    destroying = true
    alive      = false
    textarea.removeEventListener('input',   onInput)
    textarea.removeEventListener('keyup',   onSelectionChange)
    textarea.removeEventListener('mouseup', onSelectionChange)
    awareness.off('change', renderAwareness)
    awareness.destroy()
    if (ws) { ws.close(); ws = null }
    if (offlinePersist) { try { offlinePersist.flush(); offlinePersist.destroy() } catch (_) {} }
    ydoc.destroy()
    overlay.destroy()
    statusEl.textContent = 'Ready'
  }

  return { destroy }
}

/* ── Share dialog ──────────────────────────────────────────────────────────── */
export async function shareNote(noteId, noteTitle) {
  const peerId = prompt(
    `Share "${noteTitle || noteId}" with a peer.\n\nEnter their Vulos ID or slug (e.g. alice.vulos.org):`
  )
  if (!peerId) return

  const permission = confirm('Grant edit access? (Cancel = view-only)') ? 'edit' : 'view'

  try {
    const res = await fetch('/api/peering/collab/share', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        doc_id:   noteId,
        doc_type: 'note',
        title:    noteTitle || noteId,
        shared_with: [peerId],
        permission,
      }),
    })
    if (res.ok) {
      alert(`Shared with ${peerId} (${permission}). They will receive an invitation.`)
    } else {
      const err = await res.text()
      alert(`Share failed: ${err || res.statusText}`)
    }
  } catch (e) {
    alert(`Share failed: ${e.message}`)
  }
}
