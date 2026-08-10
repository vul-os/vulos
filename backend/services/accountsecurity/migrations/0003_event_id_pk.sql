-- 0003_event_id_pk.sql — make event_id the PRIMARY KEY so the audit log can
-- replicate without losing entries.
--
-- 0002 gave every row a random event_id, but that was not enough on its own:
-- the SQLite session extension keys captured changes on the PRIMARY KEY, and
-- the primary key was still `id INTEGER PRIMARY KEY AUTOINCREMENT` — allocated
-- independently on every box. Two machines therefore assigned the SAME key to
-- two DIFFERENT events, so a CRDT merge saw one key with two conflicting values
-- and silently discarded one box's real audit entry. An audit log that looks
-- complete and is missing entries is worse than one that never replicated.
--
-- It also made the grow-only residual maximally exploitable. Grow-only keeps
-- the FIRST writer, so a hostile peer can suppress an entry whose key it can
-- PREDICT — and 1, 2, 3 is as predictable as a key gets. A random event_id
-- leaves nothing to predict.
--
-- SQLite cannot ALTER a PRIMARY KEY, so this is the standard rebuild: create
-- the new shape, copy, drop, rename. It runs inside the migration runner's
-- transaction, so an interruption leaves the old table intact rather than a
-- half-migrated one.
--
-- The integer `id` is DROPPED rather than kept alongside. Keeping it would
-- leave a second, per-box identity on a replicated row — two boxes' copies of
-- the same event disagreeing about `id` — which is exactly the ambiguity this
-- migration exists to remove. Recency ordering moves to `ts`, which is what the
-- read path actually meant by "newest first"; `event_id` breaks ties within a
-- timestamp so the order is deterministic on every box rather than depending on
-- local insertion sequence.

CREATE TABLE acctsec_sensitive_actions_new (
    event_id   TEXT    PRIMARY KEY,
    ts         TEXT    NOT NULL,
    user_id    TEXT    NOT NULL,
    action     TEXT    NOT NULL,
    client_ip  TEXT    NOT NULL DEFAULT '',
    user_agent TEXT    NOT NULL DEFAULT ''
);

INSERT INTO acctsec_sensitive_actions_new (event_id, ts, user_id, action, client_ip, user_agent)
SELECT event_id, ts, user_id, action, client_ip, user_agent
  FROM acctsec_sensitive_actions
 WHERE event_id <> '';

DROP TABLE acctsec_sensitive_actions;

ALTER TABLE acctsec_sensitive_actions_new RENAME TO acctsec_sensitive_actions;

-- The user+time index the anomaly window queries rely on. The old index went
-- with the old table.
CREATE INDEX IF NOT EXISTS idx_acctsec_sensitive_actions_user
    ON acctsec_sensitive_actions(user_id, ts);
