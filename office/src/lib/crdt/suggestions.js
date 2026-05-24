/**
 * src/lib/crdt/suggestions.js  — OFFICE-27
 *
 * Browser-side CRDT store for suggestion (track-changes) annotations.
 *
 * Each Suggestion records an insertion or deletion proposal:
 *   kind    : "insert" | "delete"
 *   state   : "pending" | "accepted" | "rejected"
 *   from/to : character-offset range in the base document
 *   text    : proposed text (empty for delete)
 *   author  : string id
 *   seq_clock: HLC for LWW convergence
 *
 * Convergence rules
 * -----------------
 *   add        — first-write wins (ID-keyed, idempotent)
 *   accept/reject — LWW by seq_clock; "accepted"/"rejected" are both terminal
 *                   and beat "pending".
 *
 * Wire format (SuggestionOp)
 * --------------------------
 *   op: "add" | "accept" | "reject"
 *   suggestion: the full Suggestion object
 *   applied_at: ISO string
 *
 * Usage
 * -----
 *   const store = new SuggestionStore('peer-node-id', { onOp: broadcast });
 *   store.loadFromServer(suggestions);
 *   store.addInsert(from, to, text, authorId);
 *   store.addDelete(from, to, authorId);
 *   store.accept(id, reviewerId);
 *   store.reject(id, reviewerId);
 *   store.mergeOps(opsFromPeer);
 *   const pending = store.list('pending');
 */

// ---------------------------------------------------------------------------
// Hybrid Logical Clock (same pattern as comments.js)
// ---------------------------------------------------------------------------

class HLC {
  constructor(nodeId) {
    this.nodeId = nodeId || crypto.randomUUID().slice(0, 8)
    this.wallMs = 0
    this.counter = 0
  }

  tick() {
    const now = Date.now()
    if (now > this.wallMs) {
      this.wallMs = now
      this.counter = 0
    } else {
      this.counter += 1
    }
    return this._fmt(this.wallMs, this.counter)
  }

  receive(remote) {
    const { wallMs: rw, counter: rc } = HLC._parse(remote)
    const now = Date.now()
    if (rw > this.wallMs && rw > now) {
      this.wallMs = rw
      this.counter = rc + 1
    } else if (rw === this.wallMs) {
      if (rc >= this.counter) this.counter = rc + 1
    } else {
      if (now > this.wallMs) { this.wallMs = now; this.counter = 0 }
      else { this.counter += 1 }
    }
  }

  _fmt(wallMs, counter) {
    return (
      String(wallMs).padStart(20, '0') + '-' +
      String(counter).padStart(10, '0') + '-' +
      this.nodeId
    )
  }

  static _parse(clock) {
    if (!clock) return { wallMs: 0, counter: 0 }
    const parts = clock.split('-')
    return { wallMs: parseInt(parts[0], 10) || 0, counter: parseInt(parts[1], 10) || 0 }
  }
}

// ---------------------------------------------------------------------------
// Op type constants
// ---------------------------------------------------------------------------

export const OP_ADD_SUGGESTION    = 'add'
export const OP_ACCEPT_SUGGESTION = 'accept'
export const OP_REJECT_SUGGESTION = 'reject'

// ---------------------------------------------------------------------------
// SuggestionStore
// ---------------------------------------------------------------------------

export class SuggestionStore {
  /**
   * @param {string} nodeId     unique id for this replica
   * @param {object} [opts]
   * @param {(op: object) => void} [opts.onOp]  broadcast hook
   */
  constructor(nodeId, opts = {}) {
    this._clock = new HLC(nodeId)
    this._nodeId = nodeId
    this._onOp = opts.onOp || null

    // Map<suggestionId, suggestion>
    this._items = new Map()
    // append-only op log for cold-join replay
    this._ops = []
    this._opKeys = new Set()
  }

  // -------------------------------------------------------------------------
  // Hydrate from server (REST GET /files/:id/suggestions)
  // -------------------------------------------------------------------------

  loadFromServer(items) {
    for (const item of (items || [])) {
      this._items.set(item.id, { ...item })
    }
  }

  // -------------------------------------------------------------------------
  // Local mutations
  // -------------------------------------------------------------------------

  addInsert(from, to, text, authorId) {
    return this._add('insert', from, to, text, authorId)
  }

  addDelete(from, to, authorId) {
    return this._add('delete', from, to, '', authorId)
  }

  _add(kind, from, to, text, authorId) {
    const now = new Date().toISOString()
    const suggestion = {
      id: crypto.randomUUID(),
      kind,
      state: 'pending',
      author_id: authorId,
      from,
      to,
      text,
      seq_clock: this._clock.tick(),
      created_at: now,
      updated_at: now,
    }
    const op = { op: OP_ADD_SUGGESTION, suggestion, applied_at: now }
    this._applyLocal(op)
    return suggestion
  }

  accept(suggestionId, reviewerId = '') {
    return this._decide(suggestionId, 'accepted', reviewerId, OP_ACCEPT_SUGGESTION)
  }

  reject(suggestionId, reviewerId = '') {
    return this._decide(suggestionId, 'rejected', reviewerId, OP_REJECT_SUGGESTION)
  }

  _decide(suggestionId, state, reviewerId, opType) {
    const existing = this._items.get(suggestionId)
    if (!existing) throw new Error(`suggestion not found: ${suggestionId}`)
    const now = new Date().toISOString()
    const updated = {
      ...existing,
      state,
      reviewer_id: reviewerId,
      seq_clock: this._clock.tick(),
      updated_at: now,
    }
    const op = { op: opType, suggestion: updated, applied_at: now }
    this._applyLocal(op)
    return updated
  }

  // -------------------------------------------------------------------------
  // CRDT merge — apply ops from a remote peer (idempotent + commutative)
  // -------------------------------------------------------------------------

  mergeOps(ops) {
    for (const op of ops) {
      const clock = op.suggestion?.seq_clock
      if (clock) this._clock.receive(clock)
      this._applyToIndex(op)
      this._appendToLog(op)
    }
  }

  // -------------------------------------------------------------------------
  // Queries
  // -------------------------------------------------------------------------

  /**
   * Returns suggestions sorted by created_at.
   * @param {string} [filterState]  'pending' | 'accepted' | 'rejected' | undefined = all
   */
  list(filterState) {
    const items = [...this._items.values()]
    items.sort((a, b) =>
      (a.created_at < b.created_at ? -1 : a.created_at > b.created_at ? 1 : 0)
    )
    return filterState ? items.filter((s) => s.state === filterState) : items
  }

  get(id) {
    return this._items.get(id) || null
  }

  exportOps(afterClock = '') {
    if (!afterClock) return [...this._ops]
    return this._ops.filter((op) => {
      const clock = op.suggestion?.seq_clock || ''
      return clock > afterClock
    })
  }

  // -------------------------------------------------------------------------
  // Internal helpers
  // -------------------------------------------------------------------------

  _applyLocal(op) {
    this._applyToIndex(op)
    this._appendToLog(op)
    if (this._onOp) this._onOp(op)
  }

  _applyToIndex(op) {
    switch (op.op) {
      case OP_ADD_SUGGESTION: {
        if (!this._items.has(op.suggestion.id)) {
          this._items.set(op.suggestion.id, { ...op.suggestion })
        }
        break
      }
      case OP_ACCEPT_SUGGESTION:
      case OP_REJECT_SUGGESTION: {
        const existing = this._items.get(op.suggestion.id)
        if (!existing) {
          this._items.set(op.suggestion.id, { ...op.suggestion })
          break
        }
        // LWW: higher seq_clock wins; terminal states beat 'pending'.
        const incomingIsTerminal = op.suggestion.state !== 'pending'
        const existingIsTerminal = existing.state !== 'pending'
        if (incomingIsTerminal && !existingIsTerminal) {
          this._items.set(op.suggestion.id, { ...existing, ...op.suggestion })
        } else if (op.suggestion.seq_clock > existing.seq_clock) {
          this._items.set(op.suggestion.id, { ...existing, ...op.suggestion })
        }
        break
      }
      default:
        console.warn('[SuggestionStore] unknown op:', op.op)
    }
  }

  _appendToLog(op) {
    const clock = op.suggestion?.seq_clock || ''
    const id = op.suggestion?.id || ''
    const key = `${op.op}:${id}:${clock}`
    if (!this._opKeys.has(key)) {
      this._opKeys.add(key)
      this._ops.push(op)
    }
  }
}

// ---------------------------------------------------------------------------
// Per-file singletons
// ---------------------------------------------------------------------------

const _stores = new Map()

export function getSuggestionStore(fileId, opts = {}) {
  if (_stores.has(fileId)) return _stores.get(fileId)
  let nodeId = sessionStorage.getItem('suggestions_node_id')
  if (!nodeId) {
    nodeId = crypto.randomUUID().slice(0, 8)
    sessionStorage.setItem('suggestions_node_id', nodeId)
  }
  const store = new SuggestionStore(nodeId, opts)
  _stores.set(fileId, store)
  return store
}

export function evictSuggestionStore(fileId) {
  _stores.delete(fileId)
}
