-- 0001_files_init.sql
-- Drive index for the vulos.cloud control plane (cloud Files, phase 1).
--
-- The tree is the source of truth: object_key is DERIVED from `path`
-- ("<owner_id>/drive/<path>"), never parsed back out of it. A move/rename
-- therefore re-keys the node and every descendant file, and the bytes are
-- relocated to match.
--
-- Folders are metadata-only rows: bucket and object_key stay '' and no object
-- (not even a zero-byte marker) is ever written for them.
--
-- Dialect-neutral: applied on BOTH SQLite (self-host) and Postgres (cloud), so
-- no PRAGMA / AUTOINCREMENT / SQLite-only syntax. Timestamps are RFC3339Nano
-- TEXT, matching every other cpdb subsystem. Sizes are BIGINT — a Postgres
-- INTEGER caps at 2 GiB, which a single uploaded file can exceed.

CREATE TABLE IF NOT EXISTS files_nodes (
    id                 TEXT PRIMARY KEY,            -- ULID: opaque, unguessable, never an encoded path
    owner_id           TEXT NOT NULL,
    parent_id          TEXT NOT NULL DEFAULT '',    -- '' = the owner's Drive root
    name               TEXT NOT NULL,
    is_dir             INTEGER NOT NULL DEFAULT 0,
    bucket             TEXT NOT NULL DEFAULT '',    -- '' for folders
    object_key         TEXT NOT NULL DEFAULT '',    -- '' for folders
    path               TEXT NOT NULL,               -- Drive-relative, e.g. "docs/report.txt"
    size               BIGINT NOT NULL DEFAULT 0,
    content_type       TEXT NOT NULL DEFAULT '',
    current_version_id TEXT NOT NULL DEFAULT '',
    deleted            INTEGER NOT NULL DEFAULT 0,  -- 1 = tombstoned; bytes freed later by the purge sweep
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_files_nodes_parent ON files_nodes (parent_id, deleted);
CREATE INDEX IF NOT EXISTS ix_files_nodes_owner  ON files_nodes (owner_id, deleted);

-- The sibling-name rule is a DB INVARIANT, not merely an application check: two
-- concurrent creates of the same name race the find-or-create read, and only the
-- index stops the loser from landing a duplicate row (which would give two nodes
-- the same derived object key). The partial predicate keeps tombstoned rows out
-- of the way so a deleted name can be reused.
CREATE UNIQUE INDEX IF NOT EXISTS ux_files_nodes_sibling
    ON files_nodes (owner_id, parent_id, name) WHERE deleted = 0;

CREATE TABLE IF NOT EXISTS files_versions (
    id           TEXT PRIMARY KEY,
    node_id      TEXT NOT NULL,
    version_key  TEXT NOT NULL,               -- == the node's object_key; every version shares one key
    size         BIGINT NOT NULL DEFAULT 0,
    content_type TEXT NOT NULL DEFAULT '',
    etag         TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL,
    created_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_files_versions_node ON files_versions (node_id, created_at);
