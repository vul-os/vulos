-- auditlog_entries: append-only hash-chained admin audit log (SQLite).
--
-- tenant_id (folded from 0002) scopes org admin actions per-org
-- (ORGADMIN-AUDIT-01). Existing platform rows (pop.drain, ota.release, …) keep
-- tenant_id = '' (empty) and are NEVER returned by the org-scoped QueryOrg path —
-- that path filters on a non-empty tenant_id equal to the caller's own org, so a
-- blank-tenant platform row can never leak into an org's audit view.
--
-- Hash-chain compatibility: tenant_id is mixed into the entry hash ONLY when it
-- is non-empty (see computeHash). Blank-tenant rows therefore hash exactly as a
-- pre-tenant row would; new org rows (tenant_id != '') bind the tenant into the
-- tamper-evident hash so an org row cannot be re-pointed at another tenant
-- without breaking the chain.
CREATE TABLE IF NOT EXISTS auditlog_entries (
  seq           INTEGER PRIMARY KEY AUTOINCREMENT,
  entry_id      TEXT    NOT NULL UNIQUE,
  ts            TEXT    NOT NULL,          -- RFC3339Nano UTC
  actor         TEXT    NOT NULL DEFAULT '',
  action        TEXT    NOT NULL DEFAULT '',
  target        TEXT    NOT NULL DEFAULT '',
  metadata_json TEXT    NOT NULL DEFAULT '{}',
  prev_hash     TEXT    NOT NULL,          -- hex-SHA256 of prior row; zeros for first
  entry_hash    TEXT    NOT NULL           -- hex-SHA256 of this row's canonical fields
, tenant_id TEXT NOT NULL DEFAULT '');

CREATE INDEX IF NOT EXISTS ix_auditlog_ts     ON auditlog_entries(ts);
CREATE INDEX IF NOT EXISTS ix_auditlog_actor  ON auditlog_entries(actor, ts);
CREATE INDEX IF NOT EXISTS ix_auditlog_action ON auditlog_entries(action, ts);
CREATE INDEX IF NOT EXISTS ix_auditlog_tenant ON auditlog_entries(tenant_id, seq);
