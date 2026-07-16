-- kms_configs: one row per account. encrypted_key_material holds the
-- Vulos-STORAGE_KEK-encrypted copy of the customer's key/token.
-- For KindHTTP this is the bearer token; for KindSymmetric it is the AES key.
CREATE TABLE IF NOT EXISTS kms_configs (
  account_id              TEXT PRIMARY KEY,
  tier                    TEXT NOT NULL,                  -- "pro" | "team" | "enterprise"
  kind                    TEXT NOT NULL DEFAULT 'symmetric', -- "symmetric" | "http"
  endpoint                TEXT,                           -- nullable (KindHTTP only)
  encrypted_key_material  TEXT NOT NULL DEFAULT '',       -- hex(nonce||ciphertext) under STORAGE_KEK
  kek_version             INTEGER NOT NULL DEFAULT 1,
  created_at              TEXT NOT NULL,
  updated_at              TEXT NOT NULL
);

-- kms_deks: wrapped DEKs, one per logical object/reference.
-- wrapped_dek is opaque bytes. Vulos cannot decrypt data
-- without the customer's cooperation.
CREATE TABLE IF NOT EXISTS kms_deks (
  id          TEXT PRIMARY KEY,         -- ULID or UUID assigned by caller
  account_id  TEXT NOT NULL,
  object_ref  TEXT NOT NULL,            -- e.g. "bucket/key" or arbitrary label
  wrapped_dek BYTEA NOT NULL,           -- opaque; customer KMS encrypted
  kek_version INTEGER NOT NULL DEFAULT 1,
  revoked     INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_kms_deks_acct_ref
  ON kms_deks(account_id, object_ref, revoked, created_at DESC);
