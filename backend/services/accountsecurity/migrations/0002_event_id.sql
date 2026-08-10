-- 0002_event_id.sql — give each audit entry a globally-unique identity.
--
-- acctsec_sensitive_actions keys on `id INTEGER PRIMARY KEY AUTOINCREMENT`,
-- which is allocated INDEPENDENTLY ON EVERY BOX. Two machines therefore both
-- assign id=1 to two DIFFERENT events. That is harmless while the log is
-- purely local, and wrong the moment it replicates: a CRDT keyed on `id` sees
-- one key with two conflicting values and resolves it, so one box's real audit
-- event is silently discarded. An audit log that looks complete and is missing
-- entries is worse than one that never claimed to replicate.
--
-- It also makes the grow-only residual maximally exploitable. Grow-only keeps
-- the FIRST writer, so a hostile peer can suppress an entry it can PREDICT by
-- writing that key first — and 1, 2, 3 is as predictable as a key gets. With a
-- random event_id there is nothing to predict.
--
-- event_id is TEXT and unique. Existing rows are backfilled with a value
-- derived from this box's own row — deterministic, so re-running is a no-op,
-- and prefixed so two boxes' pre-migration histories cannot collide with each
-- other. hex(randomblob(16)) supplies the per-box entropy; SQLite has it
-- built in, so no application code has to run for the backfill to be correct.
ALTER TABLE acctsec_sensitive_actions ADD COLUMN event_id TEXT NOT NULL DEFAULT '';

UPDATE acctsec_sensitive_actions
   SET event_id = 'legacy-' || hex(randomblob(8)) || '-' || CAST(id AS TEXT)
 WHERE event_id = '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_acctsec_sensitive_actions_event_id
    ON acctsec_sensitive_actions(event_id);
