-- CRDTSYNC-01: general-purpose, pure-Go, leaderless CRDT sync engine.
--
-- Four tables per the delta-state + op-log design (see doc.go):
--
--   crdt_op    the op log, keyed by ORIGIN actor + that actor's monotonic seq.
--              Foreign ops received from peers are stored here too, so this box
--              can relay them onward to a third box that never talked to the
--              origin directly.
--   crdt_reg   the materialised LWW-register state (one row per
--              domain/key/field). This is what reads hit.
--   crdt_ctr   the materialised PN-counter state (per-actor pos/neg columns).
--   crdt_meta  per (domain, actor) reconciliation bookkeeping: `seq` is the
--              version-vector entry (highest seq observed for that actor) and
--              `floor` is the lowest seq still retained in crdt_op (everything
--              at or below it has been compacted away, so a peer behind the
--              floor must be served a snapshot instead of a delta).

CREATE TABLE IF NOT EXISTS crdt_op (
    domain   TEXT    NOT NULL,
    actor    TEXT    NOT NULL,
    seq      INTEGER NOT NULL,
    key      TEXT    NOT NULL,
    field    TEXT    NOT NULL,
    kind     TEXT    NOT NULL,          -- 'set' | 'del' | 'inc'
    value    BLOB,                      -- set: payload; inc: signed decimal text
    wall     INTEGER NOT NULL,          -- HLC wall component (unix millis)
    logical  INTEGER NOT NULL,          -- HLC logical counter
    PRIMARY KEY (domain, actor, seq)
);

CREATE INDEX IF NOT EXISTS idx_crdt_op_domain_seq ON crdt_op(domain, actor, seq);

CREATE TABLE IF NOT EXISTS crdt_reg (
    domain   TEXT    NOT NULL,
    key      TEXT    NOT NULL,
    field    TEXT    NOT NULL,
    value    BLOB,
    deleted  INTEGER NOT NULL DEFAULT 0,
    wall     INTEGER NOT NULL,
    logical  INTEGER NOT NULL,
    actor    TEXT    NOT NULL,
    PRIMARY KEY (domain, key, field)
);

CREATE TABLE IF NOT EXISTS crdt_ctr (
    domain   TEXT    NOT NULL,
    key      TEXT    NOT NULL,
    field    TEXT    NOT NULL,
    actor    TEXT    NOT NULL,
    pos      INTEGER NOT NULL DEFAULT 0,
    neg      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (domain, key, field, actor)
);

CREATE TABLE IF NOT EXISTS crdt_meta (
    domain   TEXT    NOT NULL,
    actor    TEXT    NOT NULL,
    seq      INTEGER NOT NULL DEFAULT 0,   -- version-vector entry
    floor    INTEGER NOT NULL DEFAULT 0,   -- ops <= floor are no longer in crdt_op
    PRIMARY KEY (domain, actor)
);
