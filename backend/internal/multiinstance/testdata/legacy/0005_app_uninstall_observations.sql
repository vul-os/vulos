-- CRDT-QUORUM-01 fix: distinct-origin uninstall observation set.
--
-- The uninstall quorum decision must be driven by the number of DISTINCT
-- originating instances that have independently reported an uninstall for a
-- given (instance_ulid, app_id), NOT by any self-reported AppChangeset.PeerCount.
-- A single malicious peer can therefore no longer force a removal by inflating
-- a count: it only ever contributes ONE observation (its own origin ULID).
--
-- This is an observed-remove / OR-set style accumulator: each row records that
-- `observer_ulid` observed the uninstall of `app_id` on `instance_ulid`. The
-- set is monotonic and convergent — applying the same observation twice is a
-- no-op (PRIMARY KEY), and the merge of two registries is the union of their
-- observation sets, so it survives restarts and converges deterministically.
-- All objects use IF NOT EXISTS so migration is idempotent.
CREATE TABLE IF NOT EXISTS app_uninstall_observations (
    instance_ulid   TEXT NOT NULL,            -- the app's host instance (uninstall target)
    app_id          TEXT NOT NULL,            -- the app being uninstalled
    observer_ulid   TEXT NOT NULL,            -- the DISTINCT origin instance that reported it
    observed_at     TEXT NOT NULL DEFAULT '', -- RFC3339Nano of the observed uninstall (telemetry/LWW)
    PRIMARY KEY (instance_ulid, app_id, observer_ulid)
);

CREATE INDEX IF NOT EXISTS idx_app_uninstall_obs_key
    ON app_uninstall_observations(instance_ulid, app_id);
