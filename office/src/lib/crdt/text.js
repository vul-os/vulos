/**
 * src/lib/crdt/text.js
 *
 * Browser-side RGA text CRDT for Vulos Office (OFFICE-22).
 *
 * Mirrors backend/crdt/text.go and backend/crdt/id.go so that ops
 * encoded here are accepted by the Go Doc.ApplyRemote() and vice-versa.
 *
 * Wire format (JSON) matches the Go TextOp / OpID structs:
 *   { k: 1|2, id: {r,c}, p: {r,c}, v: <codepoint>, t: {r,c} }
 *   k=1 → Insert  k=2 → Delete
 *
 * Usage:
 *   const crdt = new TextCRDT('replica-a')
 *   const op = crdt.localInsert(0, 'H')   // insert 'H' at position 0
 *   crdt.apply(op)                         // apply locally
 *   // broadcast op to peers …
 *   crdt.apply(remoteOp)                   // merge remote op
 *   crdt.toString()                        // → current visible string
 */

// ---------------------------------------------------------------------------
// Lamport clock (matches Go LamportClock)
// ---------------------------------------------------------------------------

class LamportClock {
  constructor(replica) {
    this.replica = replica
    this.c = 0
  }

  tick() {
    this.c += 1
    return { r: this.replica, c: this.c }
  }

  observe(remoteC) {
    if (remoteC > this.c) this.c = remoteC
  }
}

// ---------------------------------------------------------------------------
// OpID helpers (match Go OpID.Less / Equal)
// ---------------------------------------------------------------------------

function opidLess(a, b) {
  if (a.c !== b.c) return a.c < b.c
  return a.r < b.r
}

function opidEqual(a, b) {
  return a.c === b.c && a.r === b.r
}

function opidZero(id) {
  return !id || (id.c === 0 && (!id.r || id.r === ''))
}

const ROOT_ID = { r: '', c: 0 }

// ---------------------------------------------------------------------------
// Op kind constants (match Go TextOpInsert / TextOpDelete)
// ---------------------------------------------------------------------------

export const TEXT_OP_INSERT = 1
export const TEXT_OP_DELETE = 2

// ---------------------------------------------------------------------------
// TextCRDT
// ---------------------------------------------------------------------------

export class TextCRDT {
  /**
   * @param {string} replicaId  - unique stable id for this peer
   */
  constructor(replicaId) {
    this._replica = replicaId
    this._clock = new LamportClock(replicaId)

    // Map<opid-key, node>  where node = { id, parent, value, deleted }
    this._nodes = new Map()
    // Map<opid-key, opid[]>  child lists kept sorted descending (RGA order)
    this._children = new Map()

    this._rootKey = _key(ROOT_ID)
    this._children.set(this._rootKey, [])
  }

  // ─── Local mutation helpers ─────────────────────────────────────────────

  /**
   * Produce an Insert op for character `ch` at visible index `i`
   * (0 = before all existing text).  Does NOT apply the op; caller
   * must call apply() after broadcasting.
   */
  localInsert(i, ch) {
    const id = this._clock.tick()
    const parent = this._parentAt(i)
    return { k: TEXT_OP_INSERT, id, p: parent, v: ch.codePointAt(0) }
  }

  /**
   * Produce a Delete op for the character at visible index `i`.
   * Returns null if the index is out of range.
   */
  localDelete(i) {
    const vis = this._visibleIds()
    if (i < 0 || i >= vis.length) return null
    const id = this._clock.tick()
    return { k: TEXT_OP_DELETE, id, t: vis[i] }
  }

  // ─── Apply (local + remote, idempotent) ─────────────────────────────────

  /**
   * Apply a TextOp produced locally or received from a peer.
   * Safe to call multiple times for the same op (idempotent).
   * Returns true if the op changed the document state.
   */
  apply(op) {
    if (op.k === TEXT_OP_INSERT) {
      const k = _key(op.id)
      if (this._nodes.has(k)) return false  // already seen

      this._clock.observe(op.id.c)
      const parent = op.p && !opidZero(op.p) ? op.p : ROOT_ID
      const parentKey = _key(parent)

      this._nodes.set(k, { id: op.id, parent, value: String.fromCodePoint(op.v), deleted: false })
      if (!this._children.has(parentKey)) this._children.set(parentKey, [])
      this._insertChild(parentKey, op.id)
      return true
    }

    if (op.k === TEXT_OP_DELETE) {
      const targetKey = _key(op.t)
      const node = this._nodes.get(targetKey)
      if (!node || node.deleted) return false
      this._clock.observe(op.id.c)
      node.deleted = true
      return true
    }

    return false
  }

  // ─── Read ───────────────────────────────────────────────────────────────

  /** Returns the visible text string. */
  toString() {
    const parts = []
    this._walk(this._rootKey, parts)
    return parts.join('')
  }

  /**
   * Returns an array of opids for currently-visible characters in order.
   * Used to resolve a cursor offset to a stable anchor before broadcasting.
   */
  visibleIds() {
    return this._visibleIds()
  }

  // ─── Snapshot / restore (for bucket persistence) ────────────────────────

  /**
   * Export a plain-object snapshot of the full CRDT state.
   * Equivalent to Go TextCRDT.snapshot().
   */
  snapshot() {
    const nodes = []
    for (const [, node] of this._nodes) {
      nodes.push({ id: node.id, p: node.parent, v: node.value.codePointAt(0), d: node.deleted })
    }
    // Stable deterministic order by OpID ascending (matches Go).
    nodes.sort((a, b) => opidLess(a.id, b.id) ? -1 : 1)
    return { nodes }
  }

  /**
   * Restore state from a snapshot (replaces current state).
   */
  restore(snap) {
    this._nodes.clear()
    this._children.clear()
    this._children.set(this._rootKey, [])

    if (!snap || !Array.isArray(snap.nodes)) return

    // Sort ascending so parents are inserted before children.
    const sorted = [...snap.nodes].sort((a, b) => opidLess(a.id, b.id) ? -1 : 1)
    for (const n of sorted) {
      const k = _key(n.id)
      const parent = n.p && !opidZero(n.p) ? n.p : ROOT_ID
      const parentKey = _key(parent)
      this._nodes.set(k, { id: n.id, parent, value: String.fromCodePoint(n.v), deleted: !!n.d })
      if (!this._children.has(parentKey)) this._children.set(parentKey, [])
      this._insertChild(parentKey, n.id)
    }
  }

  // ─── Internal helpers ───────────────────────────────────────────────────

  _parentAt(i) {
    if (i <= 0) return ROOT_ID
    const vis = this._visibleIds()
    if (vis.length === 0) return ROOT_ID
    if (i - 1 < vis.length) return vis[i - 1]
    return vis[vis.length - 1]
  }

  _visibleIds() {
    const out = []
    this._collectVisible(this._rootKey, out)
    return out
  }

  _collectVisible(parentKey, out) {
    const children = this._children.get(parentKey) || []
    for (const cid of children) {
      const k = _key(cid)
      const node = this._nodes.get(k)
      if (node && !node.deleted) out.push(cid)
      this._collectVisible(k, out)
    }
  }

  _walk(parentKey, parts) {
    const children = this._children.get(parentKey) || []
    for (const cid of children) {
      const k = _key(cid)
      const node = this._nodes.get(k)
      if (node && !node.deleted) parts.push(node.value)
      this._walk(k, parts)
    }
  }

  /**
   * Insert cid into parent's child list keeping it sorted in descending
   * OpID order (mirrors Go TextCRDT.insertChild).
   */
  _insertChild(parentKey, cid) {
    const children = this._children.get(parentKey)
    // Find insertion index: first position where children[i] < cid (descending).
    let idx = 0
    while (idx < children.length && !opidLess(children[idx], cid)) idx++
    children.splice(idx, 0, cid)
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function _key(id) {
  if (!id || opidZero(id)) return '__root__'
  return `${id.r}@${id.c}`
}
