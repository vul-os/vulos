-- 0001_storage_init.sql — initial storage schema (folded).
--
-- This single initial migration declares the final storage schema directly in
-- the CREATE TABLE statements (no ALTER). It supersedes the former
-- 0002_home_region.sql (home_region column on storage_configs) and
-- 0003_cell_creds.sql (storage_cell_creds table), which have been folded in.
--
-- home_region is the logical region the bucket is homed to (e.g. 'eu'),
-- distinct from the 'region' column which carries the S3-compatible region code
-- used when provisioning the Tigris bucket (e.g. 'eu-west-1', 'auto').
-- home_region is denormalised from the owning account for fast placement
-- without cross-store joins.
CREATE TABLE IF NOT EXISTS storage_configs (
  account_id    TEXT PRIMARY KEY,                  -- cross-ref auth.users.id
  byo           INTEGER NOT NULL DEFAULT 0,        -- 0 = vulos-managed Tigris bucket
  endpoint      TEXT,                              -- nullable when byo=0
  region        TEXT NOT NULL DEFAULT 'auto',
  bucket        TEXT NOT NULL,
  access_key    TEXT,                              -- nullable when byo=0 (managed uses env creds)
  secret_key_enc TEXT,                             -- AES-GCM with STORAGE_KEK env (BYO only)
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
, home_region TEXT NOT NULL DEFAULT 'eu');

CREATE TABLE IF NOT EXISTS storage_usage (
  account_id   TEXT NOT NULL,
  bucket       TEXT NOT NULL,
  size_bytes   INTEGER NOT NULL,
  object_count INTEGER NOT NULL,
  sampled_at   TEXT NOT NULL,
  PRIMARY KEY (account_id, bucket, sampled_at)
);

CREATE INDEX IF NOT EXISTS ix_storage_usage_acct ON storage_usage(account_id, sampled_at);

-- storage_cell_creds — per-cell least-privilege scoped-credential bookkeeping
-- (STORAGE-ISOLATION.md §1.4). One row per provisioned managed cell that holds a
-- bucket-scoped minted credential. The SECRET is stored AES-GCM-encrypted under
-- STORAGE_KEK (same path as BYO secrets, store.go:aesGCMEncrypt) — never plaintext
-- in prod. This table is SEPARATE from storage_configs (which is the account's
-- bucket config); it tracks the mint lifecycle so a cell's key can be rotated and
-- revoked (incl. revoke-on-destroy).
CREATE TABLE IF NOT EXISTS storage_cell_creds (
  account_id  TEXT NOT NULL,
  ulid        TEXT NOT NULL,
  bucket      TEXT NOT NULL,
  cred_id     TEXT NOT NULL,   -- provider access-key id (for Revoke/rotate)
  policy_id   TEXT,            -- provider policy arn/id (for revoke cleanup)
  access_key  TEXT NOT NULL,   -- S3 access key id (safe to store)
  secret_enc  TEXT NOT NULL,   -- AES-GCM under STORAGE_KEK (store.go)
  endpoint    TEXT,            -- S3 endpoint stamped at mint time
  region      TEXT,            -- S3 region stamped at mint time
  expires_at  TEXT,            -- nullable = non-expiring scoped key
  created_at  TEXT NOT NULL,
  rotated_at  TEXT,
  PRIMARY KEY (account_id, ulid)
);

CREATE INDEX IF NOT EXISTS ix_storage_cell_creds_ulid ON storage_cell_creds(ulid);
