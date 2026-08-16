-- SYNC-APPS-01: split the installed app set into DESIRE and REALISATION.
--
-- app_registry is keyed (instance_ulid, app_id): a PER-INSTANCE INVENTORY. That
-- shape answers "what does box B happen to have", which is a DESCRIPTION. It
-- cannot express "I installed Steam, put it everywhere", which is an INTENT, and
-- the standing directive ("each instance is almost a direct clone of the next")
-- makes the intent the default.
--
-- So there are two sets, and they are deliberately different tables with
-- different keys:
--
--   app_desired    one row per app_id, FLEET-LEVEL.  What the user asked for.
--   app_registry   one row per (instance, app), PER-BOX.  What actually happened.
--
-- The split is the whole point, not bookkeeping. It is what lets a box that
-- CANNOT install an app report WHY instead of quietly not having it, and it is
-- what stops one broken box from uninstalling the fleet: nothing in the
-- realisation path writes app_desired. That is enforced structurally — a
-- separate table with a separate merge function — rather than by review.

-- ── the desired set ──────────────────────────────────────────────────────────
--
-- `desired` is a two-state flag, NOT the presence or absence of a row:
--
--     desired = 1   the user wants this app on every instance
--     desired = 0   the user REMOVED it — an explicit tombstone
--
-- A removal must never be spelled "the row is gone". A desired set in which
-- removal is indistinguishable from "not yet installed" resurrects apps the user
-- deleted, on every sync, forever: box A deletes the row, box B still holds its
-- copy and re-sends it, box A merges it back as new. The tombstone carries a
-- timestamp, so a stale re-send is simply an older write and loses LWW.
-- Tombstones are therefore never vacuumed.
--
-- actor_ulid is the instance that expressed the intent. It is the LWW tie-break
-- (deterministic, symmetric across peers) and the audit record of who removed
-- what. It is NOT an authorisation input — that is the changeset signature.
CREATE TABLE IF NOT EXISTS app_desired (
    app_id          TEXT NOT NULL PRIMARY KEY,
    desired_version TEXT NOT NULL DEFAULT '',    -- '' = whatever the registry calls latest
    desired         INTEGER NOT NULL DEFAULT 1,  -- 1 = wanted fleet-wide, 0 = removed (tombstone)
    actor_ulid      TEXT NOT NULL DEFAULT '',    -- instance that expressed the intent; LWW tie-break
    updated_at      TEXT NOT NULL DEFAULT ''     -- RFC3339Nano; LWW ordering
);

CREATE INDEX IF NOT EXISTS idx_app_desired_state ON app_desired(desired);

-- ── the realised set gains a REASON ──────────────────────────────────────────
--
-- Under the directive an app the user wants that an instance cannot run is an
-- instance REPORTING WHY, not an app quietly missing. Without somewhere to put
-- the reason, a failed realisation is indistinguishable from one that has not
-- been attempted, and the fleet has no way to show the user the difference.
--
-- realise_state is one of:
--
--     ''          legacy / never attempted (the default every existing row reads back as)
--     'realised'  installed here and working
--     'removed'   uninstalled here
--     'failed'    attempted and could not be done; realise_detail says why
--
-- ARCHITECTURE IS NOT A SPECIAL CASE. An arm64 box that cannot install an
-- amd64-only app records state='failed' and the reason InstallFromRegistry
-- already produces ("requires amd64; this box is arm64" —
-- services/appnet/arch.go ArchUnavailableReason). It travels the same column as
-- a failed download. There is no arch-specific branch anywhere in the merge, and
-- there should not be: the moment arch gets its own path, every other reason an
-- install can fail becomes invisible again.
--
-- Both columns replicate with the row, because a box reporting a failure only to
-- itself is the same silence the split exists to remove.
ALTER TABLE app_registry ADD COLUMN realise_state  TEXT NOT NULL DEFAULT '';
ALTER TABLE app_registry ADD COLUMN realise_detail TEXT NOT NULL DEFAULT '';
