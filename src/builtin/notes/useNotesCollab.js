/**
 * useNotesCollab — Notes/Docs-specific collaboration hook (PEER-32).
 *
 * Wraps useYDoc (PEER-30) and adds Notes-specific concerns:
 *   - Y.Text binding for plain-text note content
 *   - Awareness: user name + cursor position broadcast
 *   - Shared-doc badge state (connected / connecting / offline)
 *   - shareDoc() helper that calls POST /api/peering/collab/share
 *
 * This hook owns ZERO WebSocket or Yjs protocol logic — all of that lives
 * in useYDoc.  We only add the Notes-layer semantics on top.
 *
 * Usage (React component):
 *   const {
 *     text,            // current document text (string)
 *     setText,         // (newText: string) => void  — local edit
 *     connected,       // boolean
 *     peers,           // Array<{ clientId, name, colour, cursor }>
 *     shareDoc,        // (peerId, permission) => Promise<void>
 *     destroy,         // () => void  — call on unmount
 *   } = useNotesCollab(noteId, { displayName: 'Alice' })
 */
import { useEffect, useRef, useCallback, useState } from 'react'
import * as Y from 'yjs'
import { useYDoc } from '../../core/useYDoc.js'

// ── Deterministic cursor colours ───────────────────────────────────────────
const CURSOR_COLOURS = [
  '#3b82f6', '#ef4444', '#10b981', '#f59e0b',
  '#8b5cf6', '#ec4899', '#14b8a6', '#f97316',
]

function colourForClient(clientId) {
  return CURSOR_COLOURS[Number(clientId) % CURSOR_COLOURS.length]
}

// ── Hook ───────────────────────────────────────────────────────────────────

/**
 * @param {string} noteId      - Unique note/doc identifier (passed to useYDoc).
 * @param {object} [opts]
 * @param {string}  [opts.displayName='Me']  - Local user's display name.
 * @param {boolean} [opts.connect=true]      - Forwarded to useYDoc.
 */
export function useNotesCollab(noteId, opts = {}) {
  const { displayName = 'Me', connect = true } = opts

  // ── Delegate WS + Yjs lifecycle to useYDoc ────────────────────────────────
  const { doc, awareness, connected, destroy } = useYDoc(noteId, { connect })

  // ── Y.Text for note content ───────────────────────────────────────────────
  // Always use the same shared Y.Text key so all peers see the same map entry.
  const ytextRef = useRef(null)
  if (!ytextRef.current || ytextRef.current.doc !== doc) {
    ytextRef.current = doc.getText('content')
  }

  const [text, setTextState] = useState(() => ytextRef.current.toString())
  const [peers, setPeers] = useState([])

  // ── Broadcast local awareness (name + colour) on connect ─────────────────
  useEffect(() => {
    if (!connected) return
    awareness.setLocalStateField('user', {
      name: displayName,
      colour: colourForClient(awareness.clientID),
      cursor: null,
    })
  }, [connected, displayName, awareness])

  // ── Observe remote Y.Text changes → update local state ───────────────────
  useEffect(() => {
    const ytext = ytextRef.current
    const onObserve = () => {
      setTextState(ytext.toString())
    }
    ytext.observe(onObserve)
    return () => ytext.unobserve(onObserve)
  }, [doc])

  // ── Observe awareness changes → update peers list ─────────────────────────
  useEffect(() => {
    const onAwareness = () => {
      const states = awareness.getStates()
      const localId = awareness.clientID
      const list = []
      states.forEach((state, clientId) => {
        if (clientId === localId) return
        const user = state.user || {}
        list.push({
          clientId,
          name: user.name || 'Peer',
          colour: user.colour || colourForClient(clientId),
          cursor: user.cursor ?? null,
        })
      })
      setPeers(list)
    }
    awareness.on('change', onAwareness)
    return () => awareness.off('change', onAwareness)
  }, [awareness])

  // ── setText: apply a local edit to Y.Text with minimal diff ──────────────
  const setText = useCallback((newVal) => {
    const ytext = ytextRef.current
    const old = ytext.toString()
    if (newVal === old) return

    // Find common prefix and suffix, replace only the changed middle region.
    let s = 0
    while (s < old.length && s < newVal.length && old[s] === newVal[s]) s++
    let eOld = old.length - 1
    let eNew = newVal.length - 1
    while (eOld >= s && eNew >= s && old[eOld] === newVal[eNew]) { eOld--; eNew-- }

    const deleteCount = eOld - s + 1
    const insertText = newVal.slice(s, eNew + 1)

    doc.transact(() => {
      if (deleteCount > 0) ytext.delete(s, deleteCount)
      if (insertText.length > 0) ytext.insert(s, insertText)
    })
  }, [doc])

  // ── setCursor: broadcast local cursor position via awareness ─────────────
  const setCursor = useCallback((pos) => {
    awareness.setLocalStateField('user', {
      name: displayName,
      colour: colourForClient(awareness.clientID),
      cursor: pos,
    })
  }, [awareness, displayName])

  // ── shareDoc: POST /api/peering/collab/share ──────────────────────────────
  const shareDoc = useCallback(async (peerId, permission = 'edit', title = noteId) => {
    const res = await fetch('/api/peering/collab/share', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        doc_id: noteId,
        doc_type: 'note',
        title,
        owner: displayName,
        shared_with: [peerId],
        permission,
      }),
    })
    if (!res.ok) {
      const err = await res.text()
      throw new Error(`share failed: ${err || res.statusText}`)
    }
    return res.json()
  }, [noteId, displayName])

  return {
    text,
    setText,
    setCursor,
    connected,
    peers,
    shareDoc,
    /** Y.Doc — expose for advanced consumers (e.g. TipTap/ProseMirror binding). */
    doc,
    /** CollabAwareness — expose for custom awareness rendering. */
    awareness,
    destroy,
  }
}
