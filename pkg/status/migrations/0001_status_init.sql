-- 0001_status_init.sql
-- Durable backing for the status page. Until now the aggregator kept only an
-- in-memory ring of ~256 samples (~4h at the 60s poll) that died on every restart,
-- yet still published a 7-day uptime figure it could not possibly know. These
-- tables make the history real: raw samples for recent detail, a daily rollup so a
-- 30/90-day query never scans the raw table, and operator-authored incidents +
-- maintenance windows (the old "incidents" were auto-derived from status flips and
-- evaporated on restart).
--
-- Dialect-neutral: TEXT/INTEGER/REAL + RFC3339 TEXT timestamps only, no PRAGMA and
-- no AUTOINCREMENT, so the single file applies to both SQLite (self-host) and
-- Postgres (cloud). Idempotent via IF NOT EXISTS.

-- One row per component per poll cycle. checked_at is RFC3339 UTC.
CREATE TABLE IF NOT EXISTS status_samples (
  component   TEXT    NOT NULL,
  checked_at  TEXT    NOT NULL,
  status      TEXT    NOT NULL,           -- operational | degraded | down | unknown
  latency_ms  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (component, checked_at)
);
CREATE INDEX IF NOT EXISTS ix_status_samples_comp_time ON status_samples (component, checked_at);

-- Daily rollup: one row per component per UTC day. up_count/total_count exclude
-- "unknown" samples (they are neither up nor down), matching uptimePct. day is the
-- YYYY-MM-DD the samples fall in. A 90-day uptime is SUM(up)/SUM(total) over 90 rows.
CREATE TABLE IF NOT EXISTS status_daily (
  component   TEXT    NOT NULL,
  day         TEXT    NOT NULL,           -- YYYY-MM-DD (UTC)
  up_count    INTEGER NOT NULL DEFAULT 0,
  total_count INTEGER NOT NULL DEFAULT 0, -- up + down (unknown excluded)
  down_count  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (component, day)
);

-- Operator-authored incidents. Unlike the old auto-derived log these persist, carry
-- a severity and a human title/body, and have an explicit lifecycle. resolved_at is
-- NULL while ongoing.
CREATE TABLE IF NOT EXISTS status_incidents (
  id          TEXT    NOT NULL PRIMARY KEY,   -- ULID
  title       TEXT    NOT NULL,
  body        TEXT    NOT NULL DEFAULT '',
  severity    TEXT    NOT NULL DEFAULT 'minor', -- minor | major | critical | maintenance
  components  TEXT    NOT NULL DEFAULT '',      -- comma-separated component ids ('' = general)
  started_at  TEXT    NOT NULL,
  resolved_at TEXT,                             -- NULL while ongoing
  created_at  TEXT    NOT NULL,
  updated_at  TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_status_incidents_started ON status_incidents (started_at);

-- Append-only update timeline for an incident (the "we are investigating / fixed"
-- posts). Ordered by posted_at.
CREATE TABLE IF NOT EXISTS status_incident_updates (
  id          TEXT    NOT NULL PRIMARY KEY,   -- ULID
  incident_id TEXT    NOT NULL,
  status      TEXT    NOT NULL DEFAULT 'update', -- investigating | identified | monitoring | resolved | update
  body        TEXT    NOT NULL,
  posted_at   TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_status_updates_incident ON status_incident_updates (incident_id, posted_at);

-- Scheduled maintenance windows.
CREATE TABLE IF NOT EXISTS status_maintenance (
  id          TEXT    NOT NULL PRIMARY KEY,   -- ULID
  title       TEXT    NOT NULL,
  body        TEXT    NOT NULL DEFAULT '',
  components  TEXT    NOT NULL DEFAULT '',
  starts_at   TEXT    NOT NULL,
  ends_at     TEXT    NOT NULL,
  created_at  TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_status_maint_window ON status_maintenance (starts_at, ends_at);
