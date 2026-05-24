/**
 * src/lib/crdt/index.js
 *
 * Thin collab-session orchestrator for Vulos Office (OFFICE-22).
 *
 * DocsCollabSession ties together:
 *   - TextCRDT  (local CRDT state machine)
 *   - FabricClient  (WebRTC P2P + relay transport, OFFICE-20)
 *   - LocalStorage snapshot persistence (cold-join / offline recovery)
 *
 * Wire protocol (plain JSON over FabricClient data channel):
 *   { type: 'op',       op:   <TextOp>   }   — single CRDT op broadcast
 *   { type: 'snap-req'                   }   — request a full snapshot
 *   { type: 'snap',     snap: <snapshot> }   — full snapshot response
 *
 * Usage (inside DocsEditor.jsx):
 *   const session = new DocsCollabSession({ fileId, peerId, signalingUrl })
 *   session.on('change', (text) => { /* apply to editor *\/ })
 *   session.on('state',  ({ peerId, state }) => { /* update UI *\/ })
 *   await session.join()
 *
 *   // on local editor change:
 *   const ops = session.applyLocal(prevText, nextText)
 *   // ops are already broadcast; caller doesn't need to do anything else.
 *
 *   session.leave()
 */

import { FabricClient } from '../fabric.js'
import { TextCRDT, TEXT_OP_INSERT, TEXT_OP_DELETE } from './text.js'

const SNAP_KEY_PREFIX = 'vulos_crdt_snap_'

// How long after the last edit to flush a snapshot to localStorage.
const SNAPSHOT_DEBOUNCE_MS = 3000

export class DocsCollabSession extends EventTarget {
  /**
   * @param {object} opts
   * @param {string}   opts.fileId         - document id (used as session key)
   * @param {string}   opts.peerId         - stable id for this peer/tab
   * @param {string}  [opts.signalingUrl]  - ws[s]://host/api/peering/stream
   *                                         defaults to computed from window.location
   * @param {string}  [opts.iceUrl]        - ICE credentials endpoint
   * @param {string}  [opts.relayBaseUrl]  - relay base URL (same-origin default)
   * @param {string}  [opts.authToken]     - Bearer JWT (optional)
   */
  constructor({ fileId, peerId, signalingUrl, iceUrl, relayBaseUrl, authToken }) {
    super()

    this._fileId = fileId
    this._peerId = peerId
    this._crdt = new TextCRDT(peerId)

    // Build default signalingUrl from current origin if not provided.
    const wsBase =
      signalingUrl ||
      (typeof window !== 'undefined'
        ? window.location.origin.replace(/^http/, 'ws') + '/api/peering/stream'
        : 'ws://localhost:8080/api/peering/stream')

    this._fabric = new FabricClient({
      sessionId: fileId,
      peerId,
      signalingUrl: wsBase,
      iceUrl: iceUrl || '/api/peering/ice',
      relayBaseUrl: relayBaseUrl || '',
      authToken: authToken || null,
    })

    // Forward peer state events to callers.
    this._fabric.addEventListener('state', (ev) => {
      this.dispatchEvent(new CustomEvent('state', { detail: ev.detail }))
    })

    // Inbound messages from remote peers.
    this._fabric.addEventListener('message', (ev) => {
      this._onPeerMessage(ev.detail)
    })

    this._snapTimer = null
    this._joined = false
  }

  // ─── Public API ───────────────────────────────────────────────────────────

  /** Connect to the fabric session and restore any stored snapshot. */
  async join() {
    if (this._joined) return
    this._joined = true

    // Restore last snapshot from localStorage (offline / cold start).
    this._restoreSnapshot()

    await this._fabric.join()

    // Ask all current peers for the latest snapshot (late-joiner bootstrap).
    this._fabric.send(JSON.stringify({ type: 'snap-req' }))
  }

  /**
   * Compute ops that turn `prevText` into `nextText`, apply them locally,
   * and broadcast them to peers.  Call this on every TipTap `onUpdate`.
   *
   * Returns the array of TextOps emitted (mainly for testing).
   *
   * NOTE: `prevText` and `nextText` are the plain-text representations of
   * the document (editor.getText()).  We diff them character-by-character
   * using a simple LCS-based diff so we produce minimal insert/delete ops.
   */
  applyLocal(prevText, nextText) {
    const ops = diffToOps(prevText, nextText, this._crdt)
    for (const op of ops) {
      this._crdt.apply(op)
      this._fabric.send(JSON.stringify({ type: 'op', op }))
    }
    this._scheduleSnapshotFlush()
    return ops
  }

  /**
   * Returns the current visible text from the CRDT.
   * Editors can use this to reconcile after applying remote ops.
   */
  getText() {
    return this._crdt.toString()
  }

  /** Disconnect and release resources. */
  leave() {
    this._joined = false
    clearTimeout(this._snapTimer)
    this._fabric.leave()
  }

  /** Expose the FabricClient for presence/cursor layers (OFFICE-25). */
  get fabric() {
    return this._fabric
  }

  // ─── Inbound messages ────────────────────────────────────────────────────

  _onPeerMessage({ from, data }) {
    let msg
    try {
      msg = JSON.parse(typeof data === 'string' ? data : new TextDecoder().decode(data))
    } catch {
      return  // malformed frame
    }

    if (msg.type === 'op' && msg.op) {
      const changed = this._crdt.apply(msg.op)
      if (changed) {
        this.dispatchEvent(new CustomEvent('change', { detail: { text: this._crdt.toString(), remote: true } }))
        this._scheduleSnapshotFlush()
      }
    } else if (msg.type === 'snap-req') {
      // Peer wants our snapshot — send it back (unicast if we know `from`).
      const snap = this._crdt.snapshot()
      const reply = JSON.stringify({ type: 'snap', snap })
      if (from) {
        this._fabric.sendTo(from, reply)
      } else {
        this._fabric.send(reply)
      }
    } else if (msg.type === 'snap' && msg.snap) {
      // Received a snapshot from a peer; restore only if it has more nodes
      // than our current state (prevents regressing a richer local state).
      const remoteNodeCount = msg.snap.nodes ? msg.snap.nodes.length : 0
      const localNodeCount = this._crdt.snapshot().nodes.length
      if (remoteNodeCount > localNodeCount) {
        this._crdt.restore(msg.snap)
        this.dispatchEvent(new CustomEvent('change', { detail: { text: this._crdt.toString(), remote: true } }))
        this._scheduleSnapshotFlush()
      }
    }
  }

  // ─── Snapshot persistence (localStorage) ────────────────────────────────

  _snapKey() {
    return SNAP_KEY_PREFIX + this._fileId
  }

  _restoreSnapshot() {
    try {
      const raw = localStorage.getItem(this._snapKey())
      if (!raw) return
      const snap = JSON.parse(raw)
      this._crdt.restore(snap)
    } catch {
      // Corrupt snapshot — ignore.
    }
  }

  _scheduleSnapshotFlush() {
    clearTimeout(this._snapTimer)
    this._snapTimer = setTimeout(() => {
      try {
        const snap = this._crdt.snapshot()
        localStorage.setItem(this._snapKey(), JSON.stringify(snap))
      } catch {
        // Storage full or unavailable — best-effort.
      }
    }, SNAPSHOT_DEBOUNCE_MS)
  }
}

// ---------------------------------------------------------------------------
// diffToOps — convert a text change into minimal CRDT ops
// ---------------------------------------------------------------------------
//
// Uses a simple Myers-style diff on the common prefix/suffix to produce
// delete + insert ops in the right order.
//
// Strategy:
//  1. Strip common prefix.
//  2. Strip common suffix.
//  3. Delete the middle range of `prevText` (right-to-left to keep indices stable).
//  4. Insert the middle range of `nextText` (left-to-right).
//
// This is O(n) and correct for the majority of editor ops (single
// insertion/deletion/paste).  For true concurrent divergence the CRDT
// itself provides convergence regardless of this simplification.

function diffToOps(prevText, nextText, crdt) {
  const ops = []

  if (prevText === nextText) return ops

  // Find common prefix length.
  let prefixLen = 0
  while (
    prefixLen < prevText.length &&
    prefixLen < nextText.length &&
    prevText[prefixLen] === nextText[prefixLen]
  ) {
    prefixLen++
  }

  // Find common suffix length (don't overlap with prefix).
  let suffixLen = 0
  while (
    suffixLen < prevText.length - prefixLen &&
    suffixLen < nextText.length - prefixLen &&
    prevText[prevText.length - 1 - suffixLen] === nextText[nextText.length - 1 - suffixLen]
  ) {
    suffixLen++
  }

  const prevMid = prevText.slice(prefixLen, prevText.length - suffixLen)
  const nextMid = nextText.slice(prefixLen, nextText.length - suffixLen)

  // Delete old middle (right-to-left preserves indices).
  for (let i = prevMid.length - 1; i >= 0; i--) {
    const op = crdt.localDelete(prefixLen + i)
    if (op) ops.push(op)
  }

  // Insert new middle (left-to-right).
  for (let i = 0; i < nextMid.length; i++) {
    const op = crdt.localInsert(prefixLen + i, nextMid[i])
    if (op) ops.push(op)
  }

  return ops
}
