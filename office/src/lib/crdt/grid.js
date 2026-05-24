/**
 * src/lib/crdt/grid.js
 *
 * Browser-side LWW grid CRDT for Sheets (OFFICE-23).
 *
 * Mirrors backend/crdt/grid.go:
 *   - GridOpSet   — write a cell value (LWW: higher OpID wins)
 *   - GridOpClear — tombstone a cell
 *
 * OpID format (string-sortable, globally unique):
 *   "<20-digit wall-ms>_<10-digit counter>_<replicaId>"
 * Counter tiebreak: replica string comparison for total order (same as Go).
 *
 * Usage
 * -----
 *   import { GridSession } from './crdt/grid.js';
 *
 *   // fabricClient is a FabricClient instance (OFFICE-20); null → local-only.
 *   const session = new GridSession({ sessionId: fileId, replicaId, fabricClient });
 *   session.addEventListener('remoteOp', () => { ... rerender ... });
 *
 *   // Apply a cell edit locally and broadcast it:
 *   session.setCell(row, col, value);
 *   session.clearCell(row, col);
 *
 *   // Read the current grid state (for serialising to FortuneSheet celldata):
 *   const cells = session.snapshot(); // → [{ r, c, v }]
 *
 *   // When done:
 *   session.destroy();
 */

// ---------------------------------------------------------------------------
// Lamport clock (mirrors backend/crdt/id.go LamportClock)
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

// Compare two OpID strings — returns true if a < b (mirrors OpID.Less).
function opIdLess(a, b) {
  // Format: "wallMs_counter_replicaId"
  const [, ac, ar] = a.split('_')
  const [, bc, br] = b.split('_')
  const ai = parseInt(ac, 10)
  const bi = parseInt(bc, 10)
  if (ai !== bi) return ai < bi
  return ar < br
}

// ---------------------------------------------------------------------------
// GridOp kinds (matches backend/crdt/grid.go)
// ---------------------------------------------------------------------------

const GRID_OP_SET   = 1
const GRID_OP_CLEAR = 2

// ---------------------------------------------------------------------------
// GridCRDT — in-memory LWW map keyed by "r,c"
// ---------------------------------------------------------------------------

class GridCRDT {
  constructor() {
    // key: "r,c" → { opId, value, deleted }
    this._cells = new Map()
  }

  apply(op) {
    const key = `${op.key.r},${op.key.c}`
    const existing = this._cells.get(key)
    if (existing) {
      if (existing.opId === op.id || opIdLess(op.id, existing.opId)) return false
    }
    if (op.kind === GRID_OP_SET) {
      this._cells.set(key, { opId: op.id, value: op.v || '', deleted: false })
    } else if (op.kind === GRID_OP_CLEAR) {
      this._cells.set(key, { opId: op.id, value: '', deleted: true })
    }
    return true
  }

  get(r, c) {
    const cell = this._cells.get(`${r},${c}`)
    if (!cell || cell.deleted) return undefined
    return cell.value
  }

  /** Export non-deleted cells as { r, c, v } array (FortuneSheet celldata format). */
  cells() {
    const out = []
    for (const [key, cell] of this._cells) {
      if (cell.deleted) continue
      const [r, c] = key.split(',').map(Number)
      out.push({ r, c, v: cell.value })
    }
    out.sort((a, b) => a.r !== b.r ? a.r - b.r : a.c - b.c)
    return out
  }

  /** Restore from a snapshot array of { r, c, opId, value, deleted }. */
  restore(cells) {
    this._cells.clear()
    for (const cell of cells) {
      this._cells.set(`${cell.r},${cell.c}`, {
        opId: cell.opId, value: cell.value, deleted: cell.deleted,
      })
    }
  }

  /** Snapshot for persistence / cold-join. */
  snapshot() {
    const out = []
    for (const [key, cell] of this._cells) {
      const [r, c] = key.split(',').map(Number)
      out.push({ r, c, opId: cell.opId, value: cell.value, deleted: cell.deleted })
    }
    return out
  }
}

// ---------------------------------------------------------------------------
// GridSession — ties GridCRDT to a FabricClient (OFFICE-20 transport)
// ---------------------------------------------------------------------------

const SNAPSHOT_KEY = (id) => `crdt_grid_${id}`
const OP_LOG_KEY   = (id) => `crdt_grid_ops_${id}`
const MAX_OPLOG    = 500   // cap persisted log entries

export class GridSession extends EventTarget {
  /**
   * @param {object} opts
   * @param {string}        opts.sessionId     - file / document id
   * @param {string}        opts.replicaId     - stable per-tab id (use sessionStorage)
   * @param {FabricClient|null} [opts.fabricClient] - OFFICE-20 transport; null = local-only
   */
  constructor({ sessionId, replicaId, fabricClient = null }) {
    super()
    this._session   = sessionId
    this._replicaId = replicaId
    this._fabric    = fabricClient
    this._clock     = new LamportClock(replicaId)
    this._crdt      = new GridCRDT()
    this._destroyed = false

    // Bootstrap from localStorage (cold-join persistence).
    this._loadLocal()

    // Wire fabric message handler.
    if (this._fabric) {
      this._onFabricMessage = (ev) => this._handleFabricMessage(ev.detail.data)
      this._fabric.addEventListener('message', this._onFabricMessage)
    }
  }

  // -------------------------------------------------------------------------
  // Local mutations — called by the editor
  // -------------------------------------------------------------------------

  /** Write a cell value and broadcast the op. */
  setCell(row, col, value) {
    const id = this._clock.tick()
    const op = { kind: GRID_OP_SET, id, key: { r: row, c: col }, v: String(value) }
    this._crdt.apply(op)
    this._broadcast({ type: 'grid_op', session: this._session, op })
    this._persistOp(op)
  }

  /** Tombstone a cell and broadcast the op. */
  clearCell(row, col) {
    const id = this._clock.tick()
    const op = { kind: GRID_OP_CLEAR, id, key: { r: row, c: col } }
    this._crdt.apply(op)
    this._broadcast({ type: 'grid_op', session: this._session, op })
    this._persistOp(op)
  }

  // -------------------------------------------------------------------------
  // Query
  // -------------------------------------------------------------------------

  /**
   * Return non-deleted cells as [{ r, c, v }].
   * Use to build FortuneSheet celldata on re-render.
   */
  cells() { return this._crdt.cells() }

  // -------------------------------------------------------------------------
  // Snapshot persistence (localStorage)
  // -------------------------------------------------------------------------

  saveLocal() {
    try {
      const snap = this._crdt.snapshot()
      localStorage.setItem(SNAPSHOT_KEY(this._session), JSON.stringify(snap))
    } catch { /* quota exceeded — ignore */ }
  }

  _loadLocal() {
    try {
      const raw = localStorage.getItem(SNAPSHOT_KEY(this._session))
      if (raw) {
        const cells = JSON.parse(raw)
        this._crdt.restore(cells)
        // Re-seed clock from max counter in the snapshot.
        for (const cell of cells) {
          if (cell.opId) {
            const parts = cell.opId.split('_')
            const counter = parseInt(parts[1], 10)
            if (!isNaN(counter)) this._clock.observe(counter)
          }
        }
      }
      // Replay persisted op-log for ops that arrived after last snapshot.
      const logRaw = localStorage.getItem(OP_LOG_KEY(this._session))
      if (logRaw) {
        const ops = JSON.parse(logRaw)
        for (const op of ops) this._crdt.apply(op)
      }
    } catch { /* corrupt storage — ignore */ }
  }

  _persistOp(op) {
    try {
      const logRaw = localStorage.getItem(OP_LOG_KEY(this._session))
      const ops = logRaw ? JSON.parse(logRaw) : []
      ops.push(op)
      // Cap log to avoid unbounded growth; a periodic saveLocal() resets it.
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
    } catch { /* disconnected — silently queue nothing; op is in localStorage */ }
  }

  _handleFabricMessage(raw) {
    if (this._destroyed) return
    let msg
    try { msg = typeof raw === 'string' ? JSON.parse(raw) : raw } catch { return }
    if (!msg || msg.session !== this._session) return

    if (msg.type === 'grid_op' && msg.op) {
      const op = msg.op
      // Advance clock past remote counter.
      if (op.id) {
        const parts = op.id.split('_')
        this._clock.observe(parseInt(parts[1], 10) || 0)
      }
      const changed = this._crdt.apply(op)
      if (changed) {
        this._persistOp(op)
        this.dispatchEvent(new CustomEvent('remoteOp', { detail: { op } }))
      }
    } else if (msg.type === 'grid_snapshot_request') {
      // Send our current snapshot to the requester.
      this._broadcast({
        type: 'grid_snapshot',
        session: this._session,
        cells: this._crdt.snapshot(),
      })
    } else if (msg.type === 'grid_snapshot' && msg.cells) {
      // Cold-join: merge incoming snapshot cells.
      for (const cell of msg.cells) {
        if (cell.opId) {
          const kind = cell.deleted ? GRID_OP_CLEAR : GRID_OP_SET
          this._crdt.apply({ kind, id: cell.opId, key: { r: cell.r, c: cell.c }, v: cell.value })
        }
      }
      this.saveLocal()
      this.dispatchEvent(new CustomEvent('remoteOp', { detail: { snapshot: true } }))
    }
  }

  /** Request the current snapshot from peers (call on first join). */
  requestSnapshot() {
    this._broadcast({ type: 'grid_snapshot_request', session: this._session })
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

export function getGridReplicaId() {
  let id = sessionStorage.getItem('crdt_grid_replica')
  if (!id) {
    id = crypto.randomUUID().slice(0, 8)
    sessionStorage.setItem('crdt_grid_replica', id)
  }
  return id
}
