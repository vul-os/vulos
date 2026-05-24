/**
 * src/lib/crdt/tree.js
 *
 * Browser-side LWW ordered-tree CRDT for Slides (OFFICE-23).
 *
 * Mirrors backend/crdt/tree.go:
 *   - TreeOpInsert  — add a new slide node under root
 *   - TreeOpMove    — reorder / reparent (LWW on ordKey + parent)
 *   - TreeOpSetText — update slide content (LWW on value)
 *   - TreeOpDelete  — tombstone a slide
 *
 * Slide content is stored as a JSON-encoded value string per node
 * (matching how SlidesEditor represents each slide as an object).
 *
 * Usage
 * -----
 *   import { TreeSession } from './crdt/tree.js';
 *
 *   const session = new TreeSession({ sessionId: fileId, replicaId, fabricClient });
 *   session.addEventListener('remoteOp', () => { ... rerender ... });
 *
 *   // Local ops:
 *   const nodeId = session.insertSlide(ordKey, slideData);
 *   session.setSlide(nodeId, slideData);
 *   session.moveSlide(nodeId, newOrdKey);
 *   session.deleteSlide(nodeId);
 *
 *   // Read ordered slides:
 *   const slides = session.orderedSlides(); // → [{ nodeId, data }]
 *
 *   session.destroy();
 */

// ---------------------------------------------------------------------------
// Lamport clock (same as grid.js)
// ---------------------------------------------------------------------------

class LamportClock {
  constructor(replicaId) {
    this.replicaId = replicaId
    this.c = 0
  }

  tick() {
    this.c += 1
    return this._format(this.c)
  }

  observe(remoteCounter) {
    if (remoteCounter > this.c) this.c = remoteCounter
  }

  _format(counter) {
    return (
      String(Date.now()).padStart(20, '0') +
      '_' +
      String(counter).padStart(10, '0') +
      '_' +
      this.replicaId
    )
  }
}

function opIdLess(a, b) {
  const [, ac, ar] = a.split('_')
  const [, bc, br] = b.split('_')
  const ai = parseInt(ac, 10)
  const bi = parseInt(bc, 10)
  if (ai !== bi) return ai < bi
  return ar < br
}

// ---------------------------------------------------------------------------
// TreeOp kinds (matches backend/crdt/tree.go)
// ---------------------------------------------------------------------------

const TREE_OP_INSERT   = 1
const TREE_OP_MOVE     = 2
const TREE_OP_SET_TEXT = 3
const TREE_OP_DELETE   = 4

// The implicit root has this nodeId.
const ROOT_ID = ''

// ---------------------------------------------------------------------------
// TreeCRDT — in-memory LWW ordered tree
// ---------------------------------------------------------------------------

class TreeCRDT {
  constructor() {
    // nodeId (string) → { id, parent, ordKey, ordId, value, valueId, deleted }
    this._nodes = new Map()
  }

  apply(op) {
    switch (op.kind) {
      case TREE_OP_INSERT: {
        if (this._nodes.has(op.id)) {
          // Node exists; fill parent/ordKey if we don't have a positioning op yet.
          const n = this._nodes.get(op.id)
          if (!n.ordId || opIdLess(n.ordId, op.id)) {
            n.parent = op.parent
            n.ordKey = op.ordKey
            n.ordId  = op.id
          }
          return
        }
        this._nodes.set(op.id, {
          id: op.id, parent: op.parent, ordKey: op.ordKey, ordId: op.id,
          value: '', valueId: '', deleted: false,
        })
        break
      }
      case TREE_OP_MOVE: {
        let n = this._nodes.get(op.target)
        if (!n) {
          // Buffer node for late-arriving Insert.
          this._nodes.set(op.target, {
            id: op.target, parent: op.parent, ordKey: op.ordKey, ordId: op.id,
            value: '', valueId: '', deleted: false,
          })
          return
        }
        // LWW: keep current if its ordId >= op.id.
        if (n.ordId && !opIdLess(op.id, n.ordId) && op.id !== n.ordId) {
          if (this._wouldCycle(op.target, op.parent)) return
          n.parent = op.parent
          n.ordKey = op.ordKey
          n.ordId  = op.id
        } else if (!n.ordId) {
          if (this._wouldCycle(op.target, op.parent)) return
          n.parent = op.parent
          n.ordKey = op.ordKey
          n.ordId  = op.id
        }
        break
      }
      case TREE_OP_SET_TEXT: {
        let n = this._nodes.get(op.target)
        if (!n) {
          n = { id: op.target, parent: ROOT_ID, ordKey: '', ordId: '', value: '', valueId: '', deleted: false }
          this._nodes.set(op.target, n)
        }
        if (!n.valueId || opIdLess(n.valueId, op.id)) {
          n.value   = op.value
          n.valueId = op.id
        }
        break
      }
      case TREE_OP_DELETE: {
        let n = this._nodes.get(op.target)
        if (!n) {
          this._nodes.set(op.target, {
            id: op.target, parent: ROOT_ID, ordKey: '', ordId: '', value: '', valueId: '', deleted: true,
          })
          return
        }
        n.deleted = true
        break
      }
    }
  }

  _wouldCycle(node, newParent) {
    let cur = newParent
    let limit = this._nodes.size + 1
    while (cur && cur !== ROOT_ID && limit-- > 0) {
      if (cur === node) return true
      const n = this._nodes.get(cur)
      if (!n) return false
      cur = n.parent
    }
    return limit <= 0
  }

  /** Return visible children of parent sorted by (ordKey, id). */
  children(parentId) {
    const out = []
    for (const [id, n] of this._nodes) {
      if (!n.deleted && n.parent === parentId) out.push(id)
    }
    out.sort((a, b) => {
      const na = this._nodes.get(a)
      const nb = this._nodes.get(b)
      if (na.ordKey !== nb.ordKey) return na.ordKey < nb.ordKey ? -1 : 1
      return a < b ? -1 : a > b ? 1 : 0
    })
    return out
  }

  /** Depth-first ordered list of visible node ids from root. */
  order() {
    const out = []
    this._walk(ROOT_ID, out)
    return out
  }

  _walk(parentId, out) {
    for (const id of this.children(parentId)) {
      out.push(id)
      this._walk(id, out)
    }
  }

  value(id) {
    const n = this._nodes.get(id)
    if (!n || n.deleted) return undefined
    return n.value
  }

  snapshot() {
    const out = []
    for (const [, n] of this._nodes) {
      out.push({ id: n.id, parent: n.parent, ordKey: n.ordKey, ordId: n.ordId,
                 value: n.value, valueId: n.valueId, deleted: n.deleted })
    }
    return out
  }

  restore(nodes) {
    this._nodes.clear()
    for (const n of nodes) {
      this._nodes.set(n.id, { ...n })
    }
  }
}

// ---------------------------------------------------------------------------
// Fractional-index helper (simple string-based)
// ---------------------------------------------------------------------------

/**
 * Return an ordKey string positioned between `before` and `after`.
 * Uses a simple midpoint-string approach; infinite precision.
 */
export function ordKeyBetween(before, after) {
  const b = before || 'a'
  const a = after  || 'z'
  if (b < a) {
    // Return a string midway between the two.
    const mid = midString(b, a)
    if (mid && mid > b && mid < a) return mid
  }
  // Fallback: append 'm' to before.
  return b + 'm'
}

function midString(lo, hi) {
  // Find first differing position.
  let i = 0
  while (i < lo.length && i < hi.length && lo[i] === hi[i]) i++
  if (i >= lo.length) {
    // lo is a prefix of hi — insert lo + mid char.
    const hc = hi.charCodeAt(i)
    const ac = 'a'.charCodeAt(0)
    if (hc > ac + 1) return lo + String.fromCharCode(Math.floor((ac + hc) / 2))
    return lo + 'a' + midChar(hi[i + 1])
  }
  const lc = lo.charCodeAt(i) || 'a'.charCodeAt(0)
  const hc = hi.charCodeAt(i)
  if (hc - lc > 1) return lo.slice(0, i) + String.fromCharCode(Math.floor((lc + hc) / 2))
  return lo + 'm'
}

function midChar(c) {
  const code = c ? c.charCodeAt(0) : 'z'.charCodeAt(0)
  return String.fromCharCode(Math.floor(('a'.charCodeAt(0) + code) / 2))
}

// ---------------------------------------------------------------------------
// TreeSession — ties TreeCRDT to a FabricClient
// ---------------------------------------------------------------------------

const SNAPSHOT_KEY = (id) => `crdt_tree_${id}`
const OP_LOG_KEY   = (id) => `crdt_tree_ops_${id}`
const MAX_OPLOG    = 500

export class TreeSession extends EventTarget {
  /**
   * @param {object} opts
   * @param {string}            opts.sessionId
   * @param {string}            opts.replicaId
   * @param {FabricClient|null} [opts.fabricClient]
   */
  constructor({ sessionId, replicaId, fabricClient = null }) {
    super()
    this._session   = sessionId
    this._replicaId = replicaId
    this._fabric    = fabricClient
    this._clock     = new LamportClock(replicaId)
    this._crdt      = new TreeCRDT()
    this._destroyed = false

    this._loadLocal()

    if (this._fabric) {
      this._onFabricMessage = (ev) => this._handleFabricMessage(ev.detail.data)
      this._fabric.addEventListener('message', this._onFabricMessage)
    }
  }

  // -------------------------------------------------------------------------
  // Local mutations
  // -------------------------------------------------------------------------

  /**
   * Insert a new slide node.
   * @param {string}  ordKey   - position key (use ordKeyBetween)
   * @param {object}  data     - slide data object (will be JSON-encoded as value)
   * @returns {string} the new nodeId
   */
  insertSlide(ordKey, data) {
    const id = this._clock.tick()
    const op = { kind: TREE_OP_INSERT, id, parent: ROOT_ID, ordKey }
    this._crdt.apply(op)
    this._broadcast({ type: 'tree_op', session: this._session, op })
    this._persistOp(op)

    // Set initial content.
    if (data !== undefined) {
      const setOp = { kind: TREE_OP_SET_TEXT, id: this._clock.tick(), target: id, value: JSON.stringify(data) }
      this._crdt.apply(setOp)
      this._broadcast({ type: 'tree_op', session: this._session, op: setOp })
      this._persistOp(setOp)
    }
    return id
  }

  /** Update the content of an existing slide node. */
  setSlide(nodeId, data) {
    const id = this._clock.tick()
    const op = { kind: TREE_OP_SET_TEXT, id, target: nodeId, value: JSON.stringify(data) }
    this._crdt.apply(op)
    this._broadcast({ type: 'tree_op', session: this._session, op })
    this._persistOp(op)
  }

  /** Move / reorder a slide. */
  moveSlide(nodeId, newOrdKey) {
    const id = this._clock.tick()
    const op = { kind: TREE_OP_MOVE, id, target: nodeId, parent: ROOT_ID, ordKey: newOrdKey }
    this._crdt.apply(op)
    this._broadcast({ type: 'tree_op', session: this._session, op })
    this._persistOp(op)
  }

  /** Delete a slide. */
  deleteSlide(nodeId) {
    const id = this._clock.tick()
    const op = { kind: TREE_OP_DELETE, id, target: nodeId }
    this._crdt.apply(op)
    this._broadcast({ type: 'tree_op', session: this._session, op })
    this._persistOp(op)
  }

  // -------------------------------------------------------------------------
  // Query
  // -------------------------------------------------------------------------

  /**
   * Return ordered slides as [{ nodeId, data }].
   * data is the parsed slide object stored by insertSlide / setSlide.
   */
  orderedSlides() {
    return this._crdt.order().map((nodeId) => {
      const raw = this._crdt.value(nodeId)
      let data
      try { data = raw ? JSON.parse(raw) : {} } catch { data = {} }
      return { nodeId, data }
    })
  }

  // -------------------------------------------------------------------------
  // Persistence (localStorage)
  // -------------------------------------------------------------------------

  saveLocal() {
    try {
      localStorage.setItem(SNAPSHOT_KEY(this._session), JSON.stringify(this._crdt.snapshot()))
    } catch { /* quota — ignore */ }
  }

  _loadLocal() {
    try {
      const raw = localStorage.getItem(SNAPSHOT_KEY(this._session))
      if (raw) {
        const nodes = JSON.parse(raw)
        this._crdt.restore(nodes)
        for (const n of nodes) {
          for (const opId of [n.ordId, n.valueId]) {
            if (opId) {
              const parts = opId.split('_')
              this._clock.observe(parseInt(parts[1], 10) || 0)
            }
          }
        }
      }
      const logRaw = localStorage.getItem(OP_LOG_KEY(this._session))
      if (logRaw) {
        const ops = JSON.parse(logRaw)
        for (const op of ops) this._crdt.apply(op)
      }
    } catch { /* corrupt — ignore */ }
  }

  _persistOp(op) {
    try {
      const logRaw = localStorage.getItem(OP_LOG_KEY(this._session))
      const ops = logRaw ? JSON.parse(logRaw) : []
      ops.push(op)
      if (ops.length > MAX_OPLOG) ops.splice(0, ops.length - MAX_OPLOG)
      localStorage.setItem(OP_LOG_KEY(this._session), JSON.stringify(ops))
    } catch { /* quota — ignore */ }
  }

  // -------------------------------------------------------------------------
  // Fabric transport
  // -------------------------------------------------------------------------

  _broadcast(msg) {
    if (!this._fabric) return
    try {
      this._fabric.send(JSON.stringify(msg))
    } catch { /* disconnected */ }
  }

  _handleFabricMessage(raw) {
    if (this._destroyed) return
    let msg
    try { msg = typeof raw === 'string' ? JSON.parse(raw) : raw } catch { return }
    if (!msg || msg.session !== this._session) return

    if (msg.type === 'tree_op' && msg.op) {
      const op = msg.op
      // Advance clock.
      for (const field of [op.id, op.target]) {
        if (field && typeof field === 'string') {
          const parts = field.split('_')
          this._clock.observe(parseInt(parts[1], 10) || 0)
        }
      }
      this._crdt.apply(op)
      this._persistOp(op)
      this.dispatchEvent(new CustomEvent('remoteOp', { detail: { op } }))
    } else if (msg.type === 'tree_snapshot_request') {
      this._broadcast({
        type: 'tree_snapshot',
        session: this._session,
        nodes: this._crdt.snapshot(),
      })
    } else if (msg.type === 'tree_snapshot' && msg.nodes) {
      for (const n of msg.nodes) {
        if (n.ordId) {
          this._crdt.apply({ kind: TREE_OP_INSERT, id: n.ordId, parent: n.parent, ordKey: n.ordKey })
        }
        if (n.valueId && n.value) {
          this._crdt.apply({ kind: TREE_OP_SET_TEXT, id: n.valueId, target: n.id, value: n.value })
        }
        if (n.deleted) {
          this._crdt.apply({ kind: TREE_OP_DELETE, id: n.ordId || n.id, target: n.id })
        }
      }
      this.saveLocal()
      this.dispatchEvent(new CustomEvent('remoteOp', { detail: { snapshot: true } }))
    }
  }

  /** Request a snapshot from peers on first join. */
  requestSnapshot() {
    this._broadcast({ type: 'tree_snapshot_request', session: this._session })
  }

  destroy() {
    this._destroyed = true
    if (this._fabric && this._onFabricMessage) {
      this._fabric.removeEventListener('message', this._onFabricMessage)
    }
    this.saveLocal()
  }
}

// ---------------------------------------------------------------------------
// Stable per-tab replicaId
// ---------------------------------------------------------------------------

export function getTreeReplicaId() {
  let id = sessionStorage.getItem('crdt_tree_replica')
  if (!id) {
    id = crypto.randomUUID().slice(0, 8)
    sessionStorage.setItem('crdt_tree_replica', id)
  }
  return id
}
