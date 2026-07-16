-- 0001_fleet_init.postgres.sql
-- Fleet tables for the vulos.cloud control-plane fleet subsystem (Postgres).
-- Idempotent: safe to run against an existing schema (CREATE TABLE IF NOT EXISTS).
--
-- display_name on fleet_org_members and fleet_invites is fleet-local (auth.users
-- is email+password only, LOCKED, with no name column). It was historically
-- added by follow-on migrations; it is declared directly here.

CREATE TABLE IF NOT EXISTS fleet_orgs (
  id            TEXT PRIMARY KEY,             -- ULID
  name          TEXT NOT NULL,
  owner_account TEXT NOT NULL,               -- cross-ref auth.users.id
  created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS fleet_org_members (
  org_id       TEXT NOT NULL,
  account_id   TEXT NOT NULL,
  role         TEXT NOT NULL,                -- 'owner'|'admin'|'member'
  joined_at    TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (org_id, account_id)
);

CREATE TABLE IF NOT EXISTS fleet_devices (
  ulid             TEXT PRIMARY KEY,         -- cross-ref routing.bindings.ulid
  org_id           TEXT,                     -- nullable for personal-tier
  account_id       TEXT NOT NULL,
  name             TEXT NOT NULL DEFAULT '',
  group_label      TEXT NOT NULL DEFAULT '',
  version          TEXT NOT NULL DEFAULT '',
  channel          TEXT NOT NULL DEFAULT 'stable',
  last_heartbeat   TEXT,                     -- nullable until first heartbeat
  health           TEXT NOT NULL DEFAULT 'unknown', -- 'healthy'|'degraded'|'unknown'
  crash_count      BIGINT NOT NULL DEFAULT 0,
  decommissioned   BIGINT NOT NULL DEFAULT 0,
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_fleet_dev_org ON fleet_devices(org_id);
CREATE INDEX IF NOT EXISTS ix_fleet_dev_acct ON fleet_devices(account_id);

CREATE TABLE IF NOT EXISTS fleet_heartbeats (
  id               BIGSERIAL PRIMARY KEY,
  ulid             TEXT NOT NULL,
  version          TEXT NOT NULL,
  health           TEXT NOT NULL,
  sync_lag_sec     BIGINT NOT NULL DEFAULT 0,
  uptime_sec       BIGINT NOT NULL DEFAULT 0,
  crash_count      BIGINT NOT NULL DEFAULT 0,
  received_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_fleet_hb_ulid ON fleet_heartbeats(ulid, received_at);

CREATE TABLE IF NOT EXISTS fleet_invites (
  id           TEXT PRIMARY KEY,             -- ULID
  org_id       TEXT NOT NULL,
  email        TEXT NOT NULL,
  role         TEXT NOT NULL,
  state        TEXT NOT NULL DEFAULT 'pending', -- 'pending'|'accepted'|'revoked'
  created_at   TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT ''
);
