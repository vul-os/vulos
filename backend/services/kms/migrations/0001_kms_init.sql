-- kms_config: singleton row (id always 1) holding this box owner's BYO-KMS
-- registration. encrypted_key_material holds the KMS_STORAGE_KEK-encrypted
-- copy of the owner's key/token material.
-- For KindHTTP this is the bearer token; for KindSymmetric it is the AES key.
CREATE TABLE IF NOT EXISTS kms_config (
  id                      INTEGER PRIMARY KEY CHECK (id = 1),
  kind                    TEXT NOT NULL DEFAULT 'symmetric', -- "symmetric" | "http"
  endpoint                TEXT,                              -- nullable (KindHTTP only)
  encrypted_key_material  TEXT NOT NULL DEFAULT '',           -- hex(nonce||ciphertext) under KMS_STORAGE_KEK
  kek_version             INTEGER NOT NULL DEFAULT 1,
  created_at              TEXT NOT NULL,
  updated_at              TEXT NOT NULL
);

-- kms_deks: wrapped DEKs, one per logical object/reference.
-- wrapped_dek is opaque bytes. The box cannot decrypt the associated data
-- without the owner's cooperation (their KEK).
CREATE TABLE IF NOT EXISTS kms_deks (
  id          TEXT PRIMARY KEY,         -- UUID assigned by caller
  object_ref  TEXT NOT NULL,            -- e.g. "bucket/key" or arbitrary label
  wrapped_dek BLOB NOT NULL,            -- opaque; owner KMS encrypted
  kek_version INTEGER NOT NULL DEFAULT 1,
  revoked     INTEGER NOT NULL DEFAULT 0,
  created_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_kms_deks_ref
  ON kms_deks(object_ref, revoked, created_at DESC);
