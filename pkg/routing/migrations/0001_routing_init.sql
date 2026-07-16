-- 0001_routing_init.sql
-- Routing tables for the vulos.cloud control-plane routing subsystem.
-- Idempotent: safe to run against an existing DB (CREATE TABLE IF NOT EXISTS).

-- bindings: canonical ULID → account binding plus routing state.
-- ulid is stored in 26-char uppercase Crockford form (CanonULID).
CREATE TABLE IF NOT EXISTS bindings (
    ulid        TEXT    NOT NULL PRIMARY KEY,
    account_id  TEXT    NOT NULL,
    mode        TEXT    NOT NULL,           -- 'fabric' | 'direct' | 'own-domain'
    direct_ip   TEXT    NOT NULL DEFAULT '', -- only meaningful for mode=direct
    created_at  TEXT    NOT NULL,           -- RFC3339 UTC
    updated_at  TEXT    NOT NULL            -- RFC3339 UTC
);

CREATE INDEX IF NOT EXISTS ix_bindings_account
    ON bindings (account_id);

-- devices: enrolled devices (one ULID may have many devices).
CREATE TABLE IF NOT EXISTS devices (
    id          TEXT    NOT NULL PRIMARY KEY,
    ulid        TEXT    NOT NULL,
    account_id  TEXT    NOT NULL,
    mode        TEXT    NOT NULL,
    last_seen   TEXT    NOT NULL            -- RFC3339 UTC
);

CREATE INDEX IF NOT EXISTS ix_devices_ulid
    ON devices (ulid);

-- pops: relay points-of-presence for geo-DNS health gating.
CREATE TABLE IF NOT EXISTS pops (
    id          TEXT    NOT NULL PRIMARY KEY,
    region      TEXT    NOT NULL DEFAULT '',
    ip          TEXT    NOT NULL DEFAULT '',
    lat         REAL    NOT NULL DEFAULT 0,
    lon         REAL    NOT NULL DEFAULT 0,
    healthy     INTEGER NOT NULL DEFAULT 0, -- boolean: 1=healthy, 0=unhealthy
    last_health TEXT    NOT NULL DEFAULT '' -- RFC3339 UTC
);

-- vanity: paid-tier vanity name → ULID redirect records.
CREATE TABLE IF NOT EXISTS vanity (
    name        TEXT    NOT NULL PRIMARY KEY,
    ulid        TEXT    NOT NULL,
    account_id  TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_vanity_ulid
    ON vanity (ulid);
