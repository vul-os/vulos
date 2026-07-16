-- audit_entries: append-only audit spool (Postgres).
-- Entries are written here first (durable, queryable), then shipped to the
-- retention bucket by the Flusher.  The flushed column tracks what has been
-- sent to object storage.
CREATE TABLE IF NOT EXISTS audit_entries (
  seq         BIGSERIAL PRIMARY KEY,
  entry_id    TEXT    NOT NULL UNIQUE,
  ts          TEXT    NOT NULL,          -- RFC3339Nano UTC
  kind        TEXT    NOT NULL,
  account_id  TEXT,                      -- nullable (system-level events)
  actor       TEXT,                      -- nullable
  detail_json TEXT    NOT NULL DEFAULT '{}',
  prev_hash   TEXT    NOT NULL,          -- hex SHA-256 of prior entry; zeros for first
  hash        TEXT    NOT NULL,          -- hex SHA-256 of this entry (hash="" during compute)
  flushed     INTEGER NOT NULL DEFAULT 0 -- 1 once shipped to retention bucket
);

CREATE INDEX IF NOT EXISTS ix_audit_account ON audit_entries(account_id, ts);
CREATE INDEX IF NOT EXISTS ix_audit_kind    ON audit_entries(kind, ts);
CREATE INDEX IF NOT EXISTS ix_audit_flushed ON audit_entries(flushed, seq);
