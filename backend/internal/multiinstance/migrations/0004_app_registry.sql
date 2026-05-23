-- MINST-04: app_registry — per-instance app install state, replicated via
-- pure-Go CRDT changesets (LWW on version; OR-set for installed flag).
-- All objects use IF NOT EXISTS so migration is idempotent.

-- app_registry holds the CRDT-merged view of installed apps across instances.
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
