-- 0001_storagesel_init.sql — initial storagesel schema (folded).
--
-- account_storage: per-account storage backend selector. This single initial
-- migration declares the final schema directly in the CREATE TABLE (no ALTER);
-- it supersedes the former 0002_sync_mode.sql (sync_mode column), now folded in.
--
-- kind is 'tigris' (managed default) or 'minio' (customer-provided BYO).
-- cred_ref is an opaque env-var or vault reference for credentials (not stored plaintext).
-- sync_mode is the orthogonal central/local axis the org-admin Backup tab toggles:
--   'central' → the box writes to the central bucket (Tigris) as source of truth
--   'local'   → the box runs local-MinIO as source of truth + syncs to the
--               central rendezvous (syncrz) opportunistically
-- Default 'central' preserves the historical behaviour for any account that
-- never toggled the setting.
CREATE TABLE IF NOT EXISTS account_storage (
  account_id  TEXT NOT NULL PRIMARY KEY,
  kind        TEXT NOT NULL DEFAULT 'tigris',  -- 'tigris' | 'minio'
  endpoint    TEXT NOT NULL DEFAULT '',
  region      TEXT NOT NULL DEFAULT 'auto',
  bucket      TEXT NOT NULL DEFAULT '',
  cred_ref    TEXT NOT NULL DEFAULT '',        -- opaque credential reference
  updated_at  TEXT NOT NULL                    -- RFC3339 UTC
, sync_mode TEXT NOT NULL DEFAULT 'central');
