-- CRDT-QUORUM-01 hardening + observation-set GC.
--
-- Two changes, both idempotent (IF NOT EXISTS / additive columns guarded below):
--
-- 1. Observation GC (epoch/generation). The app_uninstall_observations set was
--    never garbage-collected, so a re-install followed by a later uninstall
--    could reach quorum off STALE observations gathered before the re-install.
--    We tag every observation with the install GENERATION (epoch) it was made
--    against, and only count observations whose epoch matches the app's CURRENT
--    generation. A (re)install bumps the generation, instantly invalidating all
--    prior observations without a destructive bulk delete (the set stays a
--    monotonic, convergent OR-set; the epoch is the watermark).
--
--    app_install_generation holds the current generation per (instance_ulid,
--    app_id). It is bumped (max-merge, so it converges) whenever an install is
--    applied with a strictly-newer timestamp than the generation's stamp.
--
-- 2. Signed-observation provenance. recordUninstallObservation now stores the
--    rostered Ed25519 public key the observation was VERIFIED against, so the
--    set is auditable and a later roster lookup is not required to re-prove an
--    already-counted observation. Unverified observations are never written.
--
-- modernc.org/sqlite has no "ADD COLUMN IF NOT EXISTS"; the columns are added
-- with a guarded approach in Go (migrate() tolerates the "duplicate column"
-- error) — but for a fresh DB the CREATE below carries them, and the ALTERs are
-- no-ops-on-conflict applied by ensureObservationEpochColumns() in Go.

CREATE TABLE IF NOT EXISTS app_install_generation (
    instance_ulid   TEXT NOT NULL,
    app_id          TEXT NOT NULL,
    generation      INTEGER NOT NULL DEFAULT 0, -- current install generation/epoch
    generation_at   TEXT NOT NULL DEFAULT '',   -- RFC3339Nano of the install that set it (LWW)
    PRIMARY KEY (instance_ulid, app_id)
);

-- NOTE: the epoch / observer_pubkey columns on app_uninstall_observations (and
-- the idx_app_uninstall_obs_epoch index) are added by ensureObservationEpochColumns()
-- in Go *after* this file runs, so they apply uniformly whether the observations
-- table was created by 0005 (pre-0006, no epoch column) or freshly. Doing it in
-- SQL here would race the 0005 CREATE: on an already-migrated DB the table exists
-- without the column, so a CREATE INDEX referencing `epoch` would fail.
