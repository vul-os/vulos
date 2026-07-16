-- auditlog_entries: append-only hash-chained admin audit log (Postgres).
--
-- tenant_id (folded from 0002) scopes org admin actions per-org
-- (ORGADMIN-AUDIT-01). See the SQLite variant for the full rationale (blank-tenant
-- platform rows are never returned by QueryOrg; the hash mixes tenant_id in only
-- when non-empty so Verify stays clean).
CREATE TABLE IF NOT EXISTS auditlog_entries (
  seq           BIGSERIAL PRIMARY KEY,
  entry_id      TEXT    NOT NULL UNIQUE,
  ts            TEXT    NOT NULL,          -- RFC3339Nano UTC
  actor         TEXT    NOT NULL DEFAULT '',
  action        TEXT    NOT NULL DEFAULT '',
  target        TEXT    NOT NULL DEFAULT '',
  metadata_json TEXT    NOT NULL DEFAULT '{}',
  prev_hash     TEXT    NOT NULL,          -- hex-SHA256 of prior row; zeros for first
  entry_hash    TEXT    NOT NULL,          -- hex-SHA256 of this row's canonical fields
  tenant_id     TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS ix_auditlog_ts     ON auditlog_entries(ts);
CREATE INDEX IF NOT EXISTS ix_auditlog_actor  ON auditlog_entries(actor, ts);
CREATE INDEX IF NOT EXISTS ix_auditlog_action ON auditlog_entries(action, ts);
CREATE INDEX IF NOT EXISTS ix_auditlog_tenant ON auditlog_entries(tenant_id, seq);
