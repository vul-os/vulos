/**
 * src/lib/crdt/messages.js
 *
 * Browser-side CRDT message store for Vulos Spaces.
 *
 * Mirrors the convergence model in backend/spaces/store.go:
 *
 *   - Append    – insert if the message ID is unknown to this replica.
 *   - Edit      – LWW (last-write-wins by SeqClock) for the same message ID;
 *                 tombstones are terminal and cannot be un-deleted.
 *   - Tombstone – always wins; body cleared.
 *
 * SeqClock format (string-sortable, globally unique):
 *   "<20-digit wall-ms>-<10-digit counter>-<nodeId>"
 *
 * Usage
 * -----
 *   import { MessageStore } from './crdt/messages.js';
 *
 *   const store = new MessageStore('browser-node-1');
 *   store.send(channelId, authorId, 'Hello!');
 *   store.mergeOps(opsFromPeer);
 *   const msgs = store.listMessages(channelId);
 */

// ---------------------------------------------------------------------------
// Hybrid Logical Clock (wall + counter + nodeId)
// ---------------------------------------------------------------------------

class HLC {
  constructor(nodeId) {
    this.nodeId = nodeId || crypto.randomUUID().slice(0, 8);
    this.wallMs = 0;
    this.counter = 0;
  }

  /** Return the next clock tick as a string. */
  tick() {
    const now = Date.now();
    if (now > this.wallMs) {
      this.wallMs = now;
      this.counter = 0;
    } else {
      this.counter += 1;
    }
    return this._format(this.wallMs, this.counter);
  }

  /** Advance past a received remote clock value. */
  receive(remote) {
    const { wallMs: rw, counter: rc } = HLC._parse(remote);
    const now = Date.now();
    if (rw > this.wallMs && rw > now) {
      this.wallMs = rw;
      this.counter = rc + 1;
    } else if (rw === this.wallMs) {
      if (rc >= this.counter) this.counter = rc + 1;
    } else {
      if (now > this.wallMs) {
        this.wallMs = now;
        this.counter = 0;
      } else {
        this.counter += 1;
      }
    }
  }

  _format(wallMs, counter) {
    return (
      String(wallMs).padStart(20, '0') +
      '-' +
      String(counter).padStart(10, '0') +
      '-' +
      this.nodeId
    );
  }

  static _parse(clock) {
    const parts = clock.split('-');
    return {
      wallMs: parseInt(parts[0], 10) || 0,
      counter: parseInt(parts[1], 10) || 0,
    };
  }
}

// ---------------------------------------------------------------------------
// Op types (matches backend/models/spaces.go MessageOpType)
// ---------------------------------------------------------------------------

export const OP_APPEND = 'append';
export const OP_EDIT = 'edit';
export const OP_TOMBSTONE = 'tombstone';

export const STATE_ACTIVE = 'active';
export const STATE_EDITED = 'edited';
export const STATE_DELETED = 'deleted';

// ---------------------------------------------------------------------------
// MessageStore
// ---------------------------------------------------------------------------

export class MessageStore {
  /**
   * @param {string} nodeId  - unique id for this replica (e.g. browser tab)
   * @param {object} [opts]
   * @param {(op: object) => void} [opts.onOp] - called after each local op
   *        (use to broadcast to peers)
   */
  constructor(nodeId, opts = {}) {
    this._clock = new HLC(nodeId);
    this._nodeId = nodeId;
    this._onOp = opts.onOp || null;

    // channelId → Map<msgId, message>
    this._messages = new Map();
    // channelId → op[]  (ordered, append-only log for cold-joiner replay)
    this._ops = new Map();
  }

  // -------------------------------------------------------------------------
  // Local mutations
  // -------------------------------------------------------------------------

  /**
   * Send a new message in a channel.
   * @param {string} channelId
   * @param {string} authorId
   * @param {string} body
   * @param {string} [threadParent]  id of parent message for thread replies
   * @returns {object} the new message
   */
  send(channelId, authorId, body, threadParent = '') {
    const now = new Date().toISOString();
    const msg = {
      id: crypto.randomUUID(),
      channel_id: channelId,
      thread_parent: threadParent,
      author_id: authorId,
      body,
      state: STATE_ACTIVE,
      seq_clock: this._clock.tick(),
      created_at: now,
      updated_at: now,
    };
    const op = { op: OP_APPEND, channel_id: channelId, msg, applied_at: now };
    this._applyLocal(op);
    return msg;
  }

  /**
   * Edit the body of an existing message (LWW — higher SeqClock wins).
   * @param {string} channelId
   * @param {string} msgId
   * @param {string} newBody
   * @returns {object} updated message
   */
  edit(channelId, msgId, newBody) {
    const existing = this._getMsg(channelId, msgId);
    if (!existing) throw new Error(`message not found: ${msgId}`);
    if (existing.state === STATE_DELETED) throw new Error('cannot edit a deleted message');

    const now = new Date().toISOString();
    const updated = {
      ...existing,
      body: newBody,
      state: STATE_EDITED,
      seq_clock: this._clock.tick(),
      updated_at: now,
    };
    const op = { op: OP_EDIT, channel_id: channelId, msg: updated, applied_at: now };
    this._applyLocal(op);
    return updated;
  }

  /**
   * Delete a message (tombstone).  Terminal — cannot be un-deleted.
   * @param {string} channelId
   * @param {string} msgId
   */
  delete(channelId, msgId) {
    const existing = this._getMsg(channelId, msgId);
    if (!existing) throw new Error(`message not found: ${msgId}`);

    const now = new Date().toISOString();
    const tombed = {
      ...existing,
      body: '',
      state: STATE_DELETED,
      seq_clock: this._clock.tick(),
      updated_at: now,
    };
    const op = { op: OP_TOMBSTONE, channel_id: channelId, msg: tombed, applied_at: now };
    this._applyLocal(op);
  }

  // -------------------------------------------------------------------------
  // CRDT merge — apply ops from a remote peer
  // -------------------------------------------------------------------------

  /**
   * Apply a batch of ops received from a peer.
   * Idempotent and commutative: any order of application converges.
   * @param {object[]} ops
   */
  mergeOps(ops) {
    for (const op of ops) {
      this._clock.receive(op.msg.seq_clock);
      this._applyToIndex(op);
      this._appendToLog(op);
    }
  }

  // -------------------------------------------------------------------------
  // Queries
  // -------------------------------------------------------------------------

  /**
   * List messages in a channel, sorted by SeqClock ascending.
   * @param {string} channelId
   * @returns {object[]}
   */
  listMessages(channelId) {
    const map = this._messages.get(channelId);
    if (!map) return [];
    return [...map.values()].sort((a, b) =>
      a.seq_clock < b.seq_clock ? -1 : a.seq_clock > b.seq_clock ? 1 : 0
    );
  }

  /**
   * Export ops for a channel with seq_clock > afterClock.
   * Used for catch-up sync / cold joiner replay.
   * @param {string} channelId
   * @param {string} [afterClock]
   * @returns {object[]}
   */
  exportOps(channelId, afterClock = '') {
    const log = this._ops.get(channelId) || [];
    if (!afterClock) return [...log];
    return log.filter((op) => op.msg.seq_clock > afterClock);
  }

  // -------------------------------------------------------------------------
  // Internal helpers
  // -------------------------------------------------------------------------

  _applyLocal(op) {
    this._applyToIndex(op);
    this._appendToLog(op);
    if (this._onOp) this._onOp(op);
  }

  _applyToIndex(op) {
    const { channel_id: chId, msg } = op;
    if (!this._messages.has(chId)) this._messages.set(chId, new Map());
    const map = this._messages.get(chId);
    const existing = map.get(msg.id);

    switch (op.op) {
      case OP_APPEND:
        if (!existing) map.set(msg.id, { ...msg });
        // If already present — idempotent, ignore.
        break;

      case OP_EDIT:
        if (!existing) {
          map.set(msg.id, { ...msg });
          break;
        }
        // Tombstone is terminal.
        if (existing.state === STATE_DELETED) break;
        // LWW: higher SeqClock wins.
        if (msg.seq_clock > existing.seq_clock) {
          map.set(msg.id, { ...msg });
        }
        break;

      case OP_TOMBSTONE: {
        if (!existing) {
          map.set(msg.id, { ...msg, body: '', state: STATE_DELETED });
          break;
        }
        // Tombstone always wins — merge in highest clock.
        const finalClock =
          msg.seq_clock > existing.seq_clock ? msg.seq_clock : existing.seq_clock;
        map.set(msg.id, {
          ...existing,
          body: '',
          state: STATE_DELETED,
          seq_clock: finalClock,
          updated_at: msg.updated_at,
        });
        break;
      }

      default:
        console.warn('[MessageStore] unknown op type:', op.op);
    }
  }

  _appendToLog(op) {
    const { channel_id: chId } = op;
    if (!this._ops.has(chId)) this._ops.set(chId, []);
    const log = this._ops.get(chId);
    // Deduplicate by (op, msg.id, msg.seq_clock).
    const key = `${op.op}:${op.msg.id}:${op.msg.seq_clock}`;
    if (!log._keys) log._keys = new Set();
    if (!log._keys.has(key)) {
      log._keys.add(key);
      log.push(op);
    }
  }

  _getMsg(channelId, msgId) {
    return this._messages.get(channelId)?.get(msgId) || null;
  }
}

// ---------------------------------------------------------------------------
// Default singleton (convenience for single-user / single-channel contexts)
// ---------------------------------------------------------------------------

let _defaultStore = null;

/**
 * Get or create the default MessageStore for this browser session.
 * nodeId is derived from sessionStorage so it survives hot-reload.
 */
export function getDefaultStore(opts = {}) {
  if (_defaultStore) return _defaultStore;
  let nodeId = sessionStorage.getItem('spaces_node_id');
  if (!nodeId) {
    nodeId = crypto.randomUUID().slice(0, 8);
    sessionStorage.setItem('spaces_node_id', nodeId);
  }
  _defaultStore = new MessageStore(nodeId, opts);
  return _defaultStore;
}
