-- PUBWEB-04: cgroup v2 resource governance tables.
-- All statements use IF NOT EXISTS so migration is idempotent.

CREATE TABLE IF NOT EXISTS cgroup_slices (
    id          TEXT PRIMARY KEY,           -- ulid
    app_id      TEXT NOT NULL DEFAULT '',   -- '' for system slice
    slice_type  TEXT NOT NULL,              -- 'system' | 'app'
    visibility  TEXT NOT NULL DEFAULT 'private', -- 'private' | 'public'
    cpu_weight  INTEGER NOT NULL DEFAULT 10,
    mem_min     INTEGER NOT NULL DEFAULT 0,   -- bytes; system reservation
    mem_high    INTEGER NOT NULL DEFAULT 0,   -- bytes; soft limit
    mem_max     INTEGER NOT NULL DEFAULT 0,   -- bytes; hard OOM limit
    cgroup_path TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS cgroup_events (
    id          TEXT PRIMARY KEY,           -- ulid
    slice_id    TEXT NOT NULL,
    event_type  TEXT NOT NULL,              -- 'oom' | 'throttle' | 'restore' | 'alert'
    detail      TEXT NOT NULL DEFAULT '',
    occurred_at TEXT NOT NULL DEFAULT ''
);
