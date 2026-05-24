/**
 * src/lib/crdt/comments.js  — OFFICE-26
 *
 * Browser-side CRDT store for anchored, threaded, resolvable comments.
 *
 * Convergence rules
 * -----------------
 *   Comment edits     — LWW by SeqClock (string-sortable HLC)
 *   Resolve / Reopen  — LWW by SeqClock; "resolved" does NOT permanently
 *                       block "reopen" (unlike tombstone in messages.js)
 *   Reply edits       — LWW by SeqClock
 *   Reply deletes     — tombstone wins; body cleared, deleted=true
 *
 * Anchor survival
 * ---------------
 *   When CRDT ops are applied without a live editor (cold-join), anchors are
 *   stored verbatim. When the Docs/Sheets editor is live it should call
 *   store.remapAnchors(newMap) after each CRDT text merge to update from→to
 *   offsets so anchors survive concurrent edits. Orphaned anchors (remapped
 *   to null) are kept with anchor.orphaned=true so they render in the panel.
 *
 * Wire format (CommentOp)
 * -----------------------
 *   op: "add_comment" | "edit_comment" | "resolve_comment" | "reopen_comment"
 *     | "add_reply" | "edit_reply" | "delete_reply"
 *   comment / reply: the full object at time of op
 *   applied_at: ISO string
 *
 * Usage
 * -----
 *   const store = new CommentStore('node-id', { onOp: broadcast });
 *   store.loadFromServer(comments);   // hydrate from REST GET /files/:id/comments
 *   store.addComment(anchor, authorId, body);
 *   store.addReply(commentId, authorId, body);
 *   store.resolve(commentId);
 *   store.reopen(commentId);
 *   store.mergeOps(opsFromPeer);
 *   const all = store.list();         // [{...comment, replies:[...]}]
 */

// ---------------------------------------------------------------------------
// Hybrid Logical Clock (reused from messages.js)
// ---------------------------------------------------------------------------

class HLC {
  constructor(nodeId) {
    this.nodeId = nodeId || crypto.randomUUID().slice(0, 8);
    this.wallMs = 0;
    this.counter = 0;
  }

  tick() {
    const now = Date.now();
    if (now > this.wallMs) {
      this.wallMs = now;
      this.counter = 0;
    } else {
      this.counter += 1;
    }
    return this._fmt(this.wallMs, this.counter);
  }

  receive(remote) {
    const { wallMs: rw, counter: rc } = HLC._parse(remote);
    const now = Date.now();
    if (rw > this.wallMs && rw > now) {
      this.wallMs = rw;
      this.counter = rc + 1;
    } else if (rw === this.wallMs) {
      if (rc >= this.counter) this.counter = rc + 1;
    } else {
      if (now > this.wallMs) { this.wallMs = now; this.counter = 0; }
      else { this.counter += 1; }
    }
  }

  _fmt(wallMs, counter) {
    return (
      String(wallMs).padStart(20, '0') + '-' +
      String(counter).padStart(10, '0') + '-' +
      this.nodeId
    );
  }

  static _parse(clock) {
    if (!clock) return { wallMs: 0, counter: 0 };
    const parts = clock.split('-');
    return { wallMs: parseInt(parts[0], 10) || 0, counter: parseInt(parts[1], 10) || 0 };
  }
}

// ---------------------------------------------------------------------------
// Op type constants
// ---------------------------------------------------------------------------

export const OP_ADD_COMMENT     = 'add_comment';
export const OP_EDIT_COMMENT    = 'edit_comment';
export const OP_RESOLVE_COMMENT = 'resolve_comment';
export const OP_REOPEN_COMMENT  = 'reopen_comment';
export const OP_ADD_REPLY       = 'add_reply';
export const OP_EDIT_REPLY      = 'edit_reply';
export const OP_DELETE_REPLY    = 'delete_reply';

// ---------------------------------------------------------------------------
// CommentStore
// ---------------------------------------------------------------------------

export class CommentStore {
  /**
   * @param {string} nodeId     unique id for this replica
   * @param {object} [opts]
   * @param {(op: object) => void} [opts.onOp]  broadcast hook
   */
  constructor(nodeId, opts = {}) {
    this._clock = new HLC(nodeId);
    this._nodeId = nodeId;
    this._onOp = opts.onOp || null;

    // Map<commentId, comment>
    this._comments = new Map();
    // Map<commentId, Map<replyId, reply>>
    this._replies = new Map();
    // append-only op log for cold-joiner replay
    this._ops = [];
    this._opKeys = new Set();
  }

  // -------------------------------------------------------------------------
  // Hydrate from server (REST GET /files/:id/comments)
  // -------------------------------------------------------------------------

  /**
   * Load the server-authoritative state.
   * Each item may have a `replies` array (as returned by the List endpoint).
   * This replaces any locally-merged state — call once on mount.
   */
  loadFromServer(items) {
    for (const item of items) {
      this._comments.set(item.id, { ...item });
      if (item.replies) {
        const rm = new Map();
        for (const r of item.replies) rm.set(r.id, { ...r });
        this._replies.set(item.id, rm);
      }
    }
  }

  // -------------------------------------------------------------------------
  // Local mutations
  // -------------------------------------------------------------------------

  addComment(anchor, authorId, body) {
    const now = new Date().toISOString();
    const comment = {
      id: crypto.randomUUID(),
      anchor: { ...anchor },
      author_id: authorId,
      body,
      state: 'open',
      seq_clock: this._clock.tick(),
      created_at: now,
      updated_at: now,
    };
    const op = { op: OP_ADD_COMMENT, comment, applied_at: now };
    this._applyLocal(op);
    return comment;
  }

  editComment(commentId, newBody) {
    const existing = this._comments.get(commentId);
    if (!existing) throw new Error(`comment not found: ${commentId}`);
    const now = new Date().toISOString();
    const updated = { ...existing, body: newBody, seq_clock: this._clock.tick(), updated_at: now };
    const op = { op: OP_EDIT_COMMENT, comment: updated, applied_at: now };
    this._applyLocal(op);
    return updated;
  }

  resolve(commentId) {
    return this._setState(commentId, 'resolved', OP_RESOLVE_COMMENT);
  }

  reopen(commentId) {
    return this._setState(commentId, 'open', OP_REOPEN_COMMENT);
  }

  _setState(commentId, state, opType) {
    const existing = this._comments.get(commentId);
    if (!existing) throw new Error(`comment not found: ${commentId}`);
    const now = new Date().toISOString();
    const updated = { ...existing, state, seq_clock: this._clock.tick(), updated_at: now };
    const op = { op: opType, comment: updated, applied_at: now };
    this._applyLocal(op);
    return updated;
  }

  addReply(commentId, authorId, body) {
    if (!this._comments.has(commentId)) throw new Error(`comment not found: ${commentId}`);
    const now = new Date().toISOString();
    const reply = {
      id: crypto.randomUUID(),
      comment_id: commentId,
      author_id: authorId,
      body,
      seq_clock: this._clock.tick(),
      deleted: false,
      created_at: now,
      updated_at: now,
    };
    const op = { op: OP_ADD_REPLY, reply, applied_at: now };
    this._applyLocal(op);
    return reply;
  }

  editReply(commentId, replyId, newBody) {
    const rm = this._replies.get(commentId);
    const existing = rm?.get(replyId);
    if (!existing) throw new Error(`reply not found: ${replyId}`);
    if (existing.deleted) throw new Error('cannot edit a deleted reply');
    const now = new Date().toISOString();
    const updated = { ...existing, body: newBody, seq_clock: this._clock.tick(), updated_at: now };
    const op = { op: OP_EDIT_REPLY, reply: updated, applied_at: now };
    this._applyLocal(op);
    return updated;
  }

  deleteReply(commentId, replyId) {
    const rm = this._replies.get(commentId);
    const existing = rm?.get(replyId);
    if (!existing) throw new Error(`reply not found: ${replyId}`);
    const now = new Date().toISOString();
    const tombed = { ...existing, body: '', deleted: true, seq_clock: this._clock.tick(), updated_at: now };
    const op = { op: OP_DELETE_REPLY, reply: tombed, applied_at: now };
    this._applyLocal(op);
  }

  // -------------------------------------------------------------------------
  // CRDT merge — apply ops from a remote peer (idempotent + commutative)
  // -------------------------------------------------------------------------

  mergeOps(ops) {
    for (const op of ops) {
      const clock = op.comment?.seq_clock || op.reply?.seq_clock;
      if (clock) this._clock.receive(clock);
      this._applyToIndex(op);
      this._appendToLog(op);
    }
  }

  // -------------------------------------------------------------------------
  // Anchor remap — call after CRDT text edits to move text-range anchors
  // -------------------------------------------------------------------------

  /**
   * newMap: Map<commentId, { from, to } | null>
   * null → anchor is orphaned (the anchored range was deleted)
   */
  remapAnchors(newMap) {
    for (const [id, range] of newMap.entries()) {
      const c = this._comments.get(id);
      if (!c) continue;
      if (range === null) {
        c.anchor = { ...c.anchor, orphaned: true };
      } else {
        c.anchor = { ...c.anchor, from: range.from, to: range.to, orphaned: false };
      }
    }
  }

  // -------------------------------------------------------------------------
  // Queries
  // -------------------------------------------------------------------------

  /**
   * Returns comments sorted by created_at, each with a replies array.
   * @param {string} [filterState]  'open' | 'resolved' | undefined = all
   */
  list(filterState) {
    const comments = [...this._comments.values()];
    comments.sort((a, b) =>
      (a.created_at < b.created_at ? -1 : a.created_at > b.created_at ? 1 : 0)
    );
    return comments
      .filter((c) => !filterState || c.state === filterState)
      .map((c) => ({
        ...c,
        replies: this._listReplies(c.id),
      }));
  }

  _listReplies(commentId) {
    const rm = this._replies.get(commentId);
    if (!rm) return [];
    return [...rm.values()].sort((a, b) =>
      a.seq_clock < b.seq_clock ? -1 : a.seq_clock > b.seq_clock ? 1 : 0
    );
  }

  exportOps(afterClock = '') {
    if (!afterClock) return [...this._ops];
    return this._ops.filter((op) => {
      const clock = op.comment?.seq_clock || op.reply?.seq_clock || '';
      return clock > afterClock;
    });
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
    switch (op.op) {
      case OP_ADD_COMMENT: {
        if (!this._comments.has(op.comment.id)) {
          this._comments.set(op.comment.id, { ...op.comment });
        }
        break;
      }
      case OP_EDIT_COMMENT:
      case OP_RESOLVE_COMMENT:
      case OP_REOPEN_COMMENT: {
        const existing = this._comments.get(op.comment.id);
        if (!existing) {
          this._comments.set(op.comment.id, { ...op.comment });
          break;
        }
        // LWW: higher SeqClock wins.
        if (!existing.seq_clock || op.comment.seq_clock > existing.seq_clock) {
          this._comments.set(op.comment.id, { ...existing, ...op.comment });
        }
        break;
      }
      case OP_ADD_REPLY: {
        const { comment_id: cid, id: rid } = op.reply;
        if (!this._replies.has(cid)) this._replies.set(cid, new Map());
        const rm = this._replies.get(cid);
        if (!rm.has(rid)) rm.set(rid, { ...op.reply });
        break;
      }
      case OP_EDIT_REPLY: {
        const { comment_id: cid, id: rid } = op.reply;
        if (!this._replies.has(cid)) this._replies.set(cid, new Map());
        const rm = this._replies.get(cid);
        const existing = rm.get(rid);
        if (!existing) { rm.set(rid, { ...op.reply }); break; }
        if (existing.deleted) break; // tombstone is terminal
        if (op.reply.seq_clock > existing.seq_clock) rm.set(rid, { ...op.reply });
        break;
      }
      case OP_DELETE_REPLY: {
        const { comment_id: cid, id: rid } = op.reply;
        if (!this._replies.has(cid)) this._replies.set(cid, new Map());
        const rm = this._replies.get(cid);
        const existing = rm.get(rid);
        if (!existing) { rm.set(rid, { ...op.reply, body: '', deleted: true }); break; }
        // Tombstone always wins; use higher clock.
        const finalClock = op.reply.seq_clock > existing.seq_clock
          ? op.reply.seq_clock : existing.seq_clock;
        rm.set(rid, { ...existing, body: '', deleted: true, seq_clock: finalClock });
        break;
      }
      default:
        console.warn('[CommentStore] unknown op:', op.op);
    }
  }

  _appendToLog(op) {
    const clock = op.comment?.seq_clock || op.reply?.seq_clock || '';
    const id = op.comment?.id || op.reply?.id || '';
    const key = `${op.op}:${id}:${clock}`;
    if (!this._opKeys.has(key)) {
      this._opKeys.add(key);
      this._ops.push(op);
    }
  }
}

// ---------------------------------------------------------------------------
// Per-file singletons (one store per open file)
// ---------------------------------------------------------------------------

const _stores = new Map();

export function getCommentStore(fileId, opts = {}) {
  if (_stores.has(fileId)) return _stores.get(fileId);
  let nodeId = sessionStorage.getItem('comments_node_id');
  if (!nodeId) {
    nodeId = crypto.randomUUID().slice(0, 8);
    sessionStorage.setItem('comments_node_id', nodeId);
  }
  const store = new CommentStore(nodeId, opts);
  _stores.set(fileId, store);
  return store;
}

export function evictCommentStore(fileId) {
  _stores.delete(fileId);
}
