-- 0001_secx_init.sqlite.sql
-- App-visibility and audit tables for the vulos.cloud secx subsystem (SQLite).
-- Idempotent: safe to run against an existing DB (CREATE TABLE IF NOT EXISTS).

CREATE TABLE IF NOT EXISTS app_visibility (
  app_id      TEXT NOT NULL,              -- opaque app identifier
  ulid        TEXT NOT NULL,              -- device ULID (26-char canonical)
  account_id  TEXT NOT NULL,              -- owning account
  visibility  TEXT NOT NULL DEFAULT 'private',  -- 'private'|'local'|'public'
  updated_at  TEXT NOT NULL,
  PRIMARY KEY (app_id, ulid)
);

CREATE INDEX IF NOT EXISTS ix_secx_vis_ulid ON app_visibility(ulid);
CREATE INDEX IF NOT EXISTS ix_secx_vis_acct ON app_visibility(account_id);

CREATE TABLE IF NOT EXISTS secx_audit (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  app_id       TEXT NOT NULL,
  ulid         TEXT NOT NULL,
  account_id   TEXT NOT NULL,             -- actor who made the change
  old_vis      TEXT NOT NULL,             -- previous visibility value
  new_vis      TEXT NOT NULL,             -- new visibility value
  ip           TEXT NOT NULL DEFAULT '',  -- caller IP address
  changed_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_secx_audit_ulid ON secx_audit(ulid, changed_at);
CREATE INDEX IF NOT EXISTS ix_secx_audit_app  ON secx_audit(app_id, changed_at);
