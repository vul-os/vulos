-- FILES IMPORT ENGINE: copy files/folders from a connected provider (Google
-- Drive/Docs, Microsoft OneDrive/Office) into the user's canonical Drive area
-- ("<owner>/drive/..."). IMPORT is DISTINCT from external mounts: a mount is a
-- live read-through view that disappears with the provider; an import writes a
-- Vulos-OWNED COPY that PERSISTS after the integration is disconnected or the
-- upstream file is deleted. All objects use IF NOT EXISTS so migration is
-- idempotent. Provider secrets are NEVER stored here — the box mints a
-- short-lived access token per call from the CP integration broker.

-- import_jobs: one row per import the user has started. A job pulls from a
-- provider (source folder id, or '' = everything) into the owner's Drive.
-- mode=once is a single copy; mode=sync supports an idempotent, ADDITIVE-ONLY
-- re-pull (provider→Vulos only; an upstream delete NEVER removes the Vulos
-- copy). Counts/status/last_sync_at drive the UI's progress + history view.
CREATE TABLE IF NOT EXISTS files_import_jobs (
    id             TEXT PRIMARY KEY,
    owner_id       TEXT NOT NULL,                 -- OS user that owns the job + the copies
    provider       TEXT NOT NULL,                 -- import source kind, e.g. "gdrive" | "onedrive"
    kind           TEXT NOT NULL DEFAULT 'files',  -- import kind (files; contacts/cal are future)
    source         TEXT NOT NULL DEFAULT '',      -- provider folder id, or '' = everything
    mode           TEXT NOT NULL DEFAULT 'once',   -- once | sync
    status         TEXT NOT NULL DEFAULT 'pending',-- pending | running | done | error
    imported       INTEGER NOT NULL DEFAULT 0,    -- files copied in the last run
    skipped        INTEGER NOT NULL DEFAULT 0,    -- files already present (dedup) in the last run
    errors         INTEGER NOT NULL DEFAULT 0,    -- per-item failures in the last run
    error          TEXT NOT NULL DEFAULT '',      -- fatal error message, if status=error
    created_at     TEXT NOT NULL,
    last_sync_at   TEXT NOT NULL DEFAULT ''       -- last successful run completion
);
CREATE INDEX IF NOT EXISTS idx_files_import_jobs_owner ON files_import_jobs(owner_id);

-- import_items: the durable map of an upstream provider file/folder id to the
-- Vulos node it was copied into, scoped to (owner, provider). This makes a
-- mode=sync re-pull RESUMABLE + DEDUPED: a file already imported is not copied
-- again, and a previously-imported folder is reused (not duplicated). Keyed by
-- the provider's opaque external id so it is stable across runs and jobs.
CREATE TABLE IF NOT EXISTS files_import_items (
    owner_id     TEXT NOT NULL,
    provider     TEXT NOT NULL,
    external_id  TEXT NOT NULL,                   -- provider's opaque file/folder id
    node_id      TEXT NOT NULL,                   -- the Vulos node it maps to
    is_dir       INTEGER NOT NULL DEFAULT 0,
    job_id       TEXT NOT NULL DEFAULT '',        -- the job that last imported it
    imported_at  TEXT NOT NULL,
    PRIMARY KEY (owner_id, provider, external_id)
);
CREATE INDEX IF NOT EXISTS idx_files_import_items_job ON files_import_items(job_id);
