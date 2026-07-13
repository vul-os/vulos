-- Multi-instance registry — clean initial schema.
--
-- This is the folded initial schema: the incremental 0001–0006 migration chain
-- (and the Go-side additive-column shims that patched pre-0006 databases) have
-- been collapsed into a single CREATE-only definition. There are no live
-- databases predating this file, so every table is created in its final shape.
-- All objects use IF NOT EXISTS so applying the schema repeatedly is a no-op.

-- MINST-01: instance registry — local manifest of all account instances.
-- Carries the FABRIC-KEY-01 key-rotation/revocation columns
-- (prev_ed25519_public_key, prev_key_expires_at, revoked) and the Phase-0
-- multi-region `region` column.
CREATE TABLE IF NOT EXISTS instances (
    ulid             TEXT PRIMARY KEY,
    display_name     TEXT NOT NULL DEFAULT '',
    kind             TEXT NOT NULL DEFAULT 'device'
                         CHECK (kind IN ('device', 'cloud')),
    endpoint_url     TEXT NOT NULL DEFAULT '',
    ed25519_public_key TEXT NOT NULL DEFAULT '',
    role             TEXT NOT NULL DEFAULT 'peer'
                         CHECK (role IN ('owner', 'peer')),
    status           TEXT NOT NULL DEFAULT 'unknown'
                         CHECK (status IN ('online', 'offline', 'unknown')),
    last_seen_at     TEXT NOT NULL DEFAULT '',
    prev_ed25519_public_key TEXT NOT NULL DEFAULT '',
    prev_key_expires_at TEXT NOT NULL DEFAULT '',
    revoked          INTEGER NOT NULL DEFAULT 0,
    region           TEXT NOT NULL DEFAULT 'eu'
);

CREATE INDEX IF NOT EXISTS idx_instances_status ON instances(status);

-- MINST-05: provision_requests — tracks Fly Machine cloud instance provisioning jobs.
CREATE TABLE IF NOT EXISTS provision_requests (
    id          TEXT PRIMARY KEY,          -- request ULID (client-generated)
    region      TEXT NOT NULL DEFAULT '',  -- Fly region slug (e.g. "fra", "iad")
    plan        TEXT NOT NULL DEFAULT '',  -- Fly machine size (e.g. "shared-cpu-1x", "performance-1x")
    status      TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'provisioning', 'ready', 'failed')),
    instance_ulid TEXT NOT NULL DEFAULT '', -- filled once cloud returns the new ULID
    endpoint_url  TEXT NOT NULL DEFAULT '', -- filled once the instance is ready
    error_message TEXT NOT NULL DEFAULT '',
    requested_at  TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_provision_status ON provision_requests(status);

-- MINST-06: seen_notifications — dedup table for fan-out notifications.
CREATE TABLE IF NOT EXISTS seen_notifications (
    notification_id TEXT PRIMARY KEY,
    seen_at         TEXT NOT NULL DEFAULT '',
    expires_at      TEXT NOT NULL DEFAULT ''  -- 7-day TTL from seen_at
);

CREATE INDEX IF NOT EXISTS idx_seen_notif_expires ON seen_notifications(expires_at);

-- MINST-04: app_registry — per-instance app install state, replicated via
-- pure-Go CRDT changesets (LWW on version; OR-set for installed flag).
-- One row per (instance_ulid, app_id) pair.
CREATE TABLE IF NOT EXISTS app_registry (
    instance_ulid   TEXT NOT NULL,
    app_id          TEXT NOT NULL,
    app_version     TEXT NOT NULL DEFAULT '',
    installed       INTEGER NOT NULL DEFAULT 1,  -- 1 = installed, 0 = uninstalled
    installed_by    TEXT NOT NULL DEFAULT '',    -- node ID that last wrote this row
    updated_at      TEXT NOT NULL DEFAULT '',    -- RFC3339Nano; used for LWW ordering
    PRIMARY KEY (instance_ulid, app_id)
);

CREATE INDEX IF NOT EXISTS idx_app_registry_instance ON app_registry(instance_ulid);
CREATE INDEX IF NOT EXISTS idx_app_registry_app      ON app_registry(app_id);

-- CRDT-QUORUM-01: distinct-origin uninstall observation set (observed-remove /
-- OR-set accumulator). The quorum decision is driven by the number of DISTINCT
-- originating instances that independently reported an uninstall, so a single
-- malicious peer can only ever contribute ONE observation (its own origin ULID).
--
-- The `epoch` column tags every observation with the install GENERATION it was
-- made against (see app_install_generation); only observations whose epoch
-- matches the app's current generation are counted, so a re-install instantly
-- invalidates stale observations. `observer_pubkey` records the rostered
-- Ed25519 key the observation was verified against (signed-observation
-- provenance). Both were folded in from migration 0006.
CREATE TABLE IF NOT EXISTS app_uninstall_observations (
    instance_ulid   TEXT NOT NULL,            -- the app's host instance (uninstall target)
    app_id          TEXT NOT NULL,            -- the app being uninstalled
    observer_ulid   TEXT NOT NULL,            -- the DISTINCT origin instance that reported it
    observed_at     TEXT NOT NULL DEFAULT '', -- RFC3339Nano of the observed uninstall (telemetry/LWW)
    epoch           INTEGER NOT NULL DEFAULT 0,  -- install generation this observation was made against
    observer_pubkey TEXT NOT NULL DEFAULT '',    -- rostered Ed25519 pubkey the observation was verified against
    PRIMARY KEY (instance_ulid, app_id, observer_ulid)
);

CREATE INDEX IF NOT EXISTS idx_app_uninstall_obs_key
    ON app_uninstall_observations(instance_ulid, app_id);
CREATE INDEX IF NOT EXISTS idx_app_uninstall_obs_epoch
    ON app_uninstall_observations(instance_ulid, app_id, epoch);

-- CRDT-QUORUM-01 GC: current install generation/epoch per (instance_ulid,
-- app_id). Bumped via max-merge (so it converges) whenever an install is
-- applied with a strictly-newer timestamp than the generation's stamp.
CREATE TABLE IF NOT EXISTS app_install_generation (
    instance_ulid   TEXT NOT NULL,
    app_id          TEXT NOT NULL,
    generation      INTEGER NOT NULL DEFAULT 0, -- current install generation/epoch
    generation_at   TEXT NOT NULL DEFAULT '',   -- RFC3339Nano of the install that set it (LWW)
    PRIMARY KEY (instance_ulid, app_id)
);
