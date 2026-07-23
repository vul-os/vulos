-- 0001_keydir_init.sql
-- MAIL-07: vumail.org key directory — vumail address → Ed25519 public key + peer metadata.
-- Queried by vulos-relay peering transport for Vulos-to-Vulos peer discovery.
-- Idempotent: safe to run against an existing DB (CREATE TABLE IF NOT EXISTS).

-- keydir_entries maps a verified vumail address to the instance's Ed25519 public key.
-- Only one active entry per address is allowed (UNIQUE on vumail_address).
-- The public key is stored as base64url-encoded 32-byte Ed25519 public key.
CREATE TABLE IF NOT EXISTS keydir_entries (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id      TEXT NOT NULL UNIQUE,     -- cp auth user ID (owner)
  vumail_address  TEXT NOT NULL UNIQUE,     -- e.g. alice@vumail.org
  public_key_b64  TEXT NOT NULL,            -- base64url Ed25519 public key (32 bytes)
  peer_endpoint   TEXT NOT NULL DEFAULT '', -- relay-reachable endpoint hint (optional)
  discoverable    INTEGER NOT NULL DEFAULT 1, -- 1 = listed in lookup, 0 = key-only
  state           TEXT NOT NULL DEFAULT 'active',  -- 'active' | 'revoked'
  created_at      TEXT NOT NULL,            -- RFC3339 UTC
  updated_at      TEXT NOT NULL             -- RFC3339 UTC
);

CREATE INDEX IF NOT EXISTS ix_keydir_address ON keydir_entries(vumail_address);
CREATE INDEX IF NOT EXISTS ix_keydir_state   ON keydir_entries(state);
