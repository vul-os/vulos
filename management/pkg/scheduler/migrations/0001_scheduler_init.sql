CREATE TABLE IF NOT EXISTS scheduler_jobs (
    name        TEXT PRIMARY KEY,
    locked_by   TEXT,            -- instance ID holding the lock
    locked_until TEXT,           -- RFC3339; lock expires at this time
    last_run_at  TEXT,           -- RFC3339; when job last ran
    run_count    INTEGER NOT NULL DEFAULT 0
);
