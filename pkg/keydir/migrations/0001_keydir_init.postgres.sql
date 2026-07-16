-- 0001_keydir_init.postgres.sql
-- Postgres variant: AUTOINCREMENT→BIGSERIAL.
CREATE TABLE IF NOT EXISTS keydir_entries (
  id              BIGSERIAL PRIMARY KEY,
  account_id      TEXT NOT NULL UNIQUE,
  vumail_address  TEXT NOT NULL UNIQUE,
  public_key_b64  TEXT NOT NULL,
  peer_endpoint   TEXT NOT NULL DEFAULT '',
  discoverable    INTEGER NOT NULL DEFAULT 1,
  state           TEXT NOT NULL DEFAULT 'active',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_keydir_address ON keydir_entries(vumail_address);
CREATE INDEX IF NOT EXISTS ix_keydir_state   ON keydir_entries(state);
