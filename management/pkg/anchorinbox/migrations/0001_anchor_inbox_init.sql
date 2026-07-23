-- 0001_anchor_inbox_init.sql
-- Anchor-inbox registry (ANCHOR-01 / CP-ANCHOR-01).
-- Idempotent: all DDL uses IF NOT EXISTS.
--
-- Every Vulos account gets exactly one anchor inbox: a small always-on
-- @vulos.org mailbox hosted on OUR the managed store bucket.  It is the durability
-- guarantee — "you can never lose access to your @vulos.org account".
--
-- This table records which accounts have been provisioned and their quota.
-- The actual mail is stored in the the managed store anchor bucket; this row is the
-- authoritative registry entry the CP reads at provisioning and lookup time.
--
-- quota_mb: default 1024 MB (1 GiB). Enforcement is on the delivery side.
CREATE TABLE IF NOT EXISTS anchor_inboxes (
    account_id    TEXT PRIMARY KEY,
    address       TEXT NOT NULL UNIQUE,   -- full address: <handle>@vulos.org
    provisioned_at TEXT NOT NULL,         -- RFC3339 UTC
    quota_mb      INTEGER NOT NULL DEFAULT 1024
);

CREATE INDEX IF NOT EXISTS ix_anchor_inboxes_address ON anchor_inboxes (address);
