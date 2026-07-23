-- 0001_profile_mgmt_init.sqlite.sql
-- SQLite variant: uses AUTOINCREMENT for profile_audit.id.
CREATE TABLE IF NOT EXISTS profile_entries (
  ulid         TEXT NOT NULL,
  profile_name TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  PRIMARY KEY (ulid, profile_name)
);

CREATE TABLE IF NOT EXISTS profile_audit (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id   TEXT NOT NULL,
  actor_id     TEXT NOT NULL,
  ulid         TEXT NOT NULL,
  profile_name TEXT NOT NULL,
  action       TEXT NOT NULL,
  created_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_profile_audit_ulid ON profile_audit(ulid, created_at);
