-- 0001_telemetry_init.postgres.sql
-- Root-cause telemetry catalog tables — Postgres variant.
-- BIGSERIAL for auto-increment primary keys.

CREATE TABLE IF NOT EXISTS telemetry_samples (
  id                    BIGSERIAL PRIMARY KEY,
  ulid                  TEXT NOT NULL,
  org_id                TEXT NOT NULL DEFAULT '',
  sampled_at            TEXT NOT NULL,

  ip_reputation_score   BIGINT NOT NULL DEFAULT 100,
  blocklist_spamhaus    BIGINT NOT NULL DEFAULT 0,
  blocklist_barracuda   BIGINT NOT NULL DEFAULT 0,
  blocklist_sorbs       BIGINT NOT NULL DEFAULT 0,

  disk_used_pct         BIGINT NOT NULL DEFAULT 0,

  sync_queue_depth      BIGINT NOT NULL DEFAULT 0,
  mail_queue_depth      BIGINT NOT NULL DEFAULT 0,

  dkim_expiry_days      BIGINT NOT NULL DEFAULT 999,
  dmarc_pass_rate_pct   BIGINT NOT NULL DEFAULT 100,

  uptime_sec            BIGINT NOT NULL DEFAULT 0,
  last_backup_age_sec   BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS ix_telem_ulid_time  ON telemetry_samples(ulid, sampled_at DESC);
CREATE INDEX IF NOT EXISTS ix_telem_org_time   ON telemetry_samples(org_id, sampled_at DESC);

CREATE TABLE IF NOT EXISTS telemetry_alerts (
  id          BIGSERIAL PRIMARY KEY,
  ulid        TEXT NOT NULL,
  org_id      TEXT NOT NULL DEFAULT '',
  signal      TEXT NOT NULL,
  severity    TEXT NOT NULL,
  detail      TEXT NOT NULL DEFAULT '',
  fired_at    TEXT NOT NULL,
  resolved_at TEXT
);

CREATE INDEX IF NOT EXISTS ix_telem_alert_ulid ON telemetry_alerts(ulid, fired_at DESC);
CREATE INDEX IF NOT EXISTS ix_telem_alert_open ON telemetry_alerts(resolved_at) WHERE resolved_at IS NULL;
