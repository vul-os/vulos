-- Self-host integrations connection store — clean baseline.
-- One row per (owner-account, provider) link. Refresh + access tokens are stored
-- ENCRYPTED (base64 AES-GCM); plaintext never touches disk. All DDL is idempotent.

CREATE TABLE IF NOT EXISTS integrations_conn (
  user_id            TEXT NOT NULL,
  provider           TEXT NOT NULL,
  refresh_token_enc  TEXT NOT NULL,
  access_token_enc   TEXT NOT NULL DEFAULT '',
  access_expiry      INTEGER NOT NULL DEFAULT 0,
  scopes             TEXT NOT NULL DEFAULT '',
  account_email      TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL,
  PRIMARY KEY (user_id, provider)
);
