-- OS Files control-plane schema — clean initial schema.
--
-- Folded from the incremental 0001–0003 migration chain (foundation index/ACLs/
-- shares/versions/audit/external-mounts + Phase-2B peer-share + the import
-- engine). Every migration was a pure additive CREATE TABLE / CREATE INDEX with
-- no column alterations, so this file reproduces the identical final shape. All
-- objects use IF NOT EXISTS so applying the schema repeatedly is a no-op.


-- nodes: files and folders. A node's bytes (files only) live at bucket/object_key
-- in the OWNER's per-user bucket (Drive area "<ownerID>/drive/..."). Folders are
-- metadata-only (object_key empty). path is the Drive-relative path, denormalized
-- for listing/key derivation; parent_id gives the tree for ACL inheritance.
CREATE TABLE IF NOT EXISTS files_nodes (
    id                 TEXT PRIMARY KEY,
    owner_id           TEXT NOT NULL,
    parent_id          TEXT NOT NULL DEFAULT '',  -- '' == drive root
    name               TEXT NOT NULL,
    is_dir             INTEGER NOT NULL DEFAULT 0,
    bucket             TEXT NOT NULL DEFAULT '',
    object_key         TEXT NOT NULL DEFAULT '',
    path               TEXT NOT NULL DEFAULT '',   -- drive-relative, e.g. "docs/report.txt"
    size               INTEGER NOT NULL DEFAULT 0,
    content_type       TEXT NOT NULL DEFAULT '',
    current_version_id TEXT NOT NULL DEFAULT '',
    deleted            INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_files_nodes_owner  ON files_nodes(owner_id);
CREATE INDEX IF NOT EXISTS idx_files_nodes_parent ON files_nodes(parent_id);

-- acls: explicit (principal, role) grants on a node. The owner's role is implicit
-- (owner_id on the node) and is NOT stored here. Inheritance: a principal's role
-- on a node is the max over the node and all ancestors. UNIQUE(node,principal).
CREATE TABLE IF NOT EXISTS files_acls (
    id           TEXT PRIMARY KEY,
    node_id      TEXT NOT NULL,
    principal_id TEXT NOT NULL,
    role         TEXT NOT NULL,             -- owner | editor | viewer
    created_by   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    UNIQUE(node_id, principal_id)
);
CREATE INDEX IF NOT EXISTS idx_files_acls_node      ON files_acls(node_id);
CREATE INDEX IF NOT EXISTS idx_files_acls_principal ON files_acls(principal_id);

-- share_links: expiring, revocable capability tokens granting a role on a node
-- to whoever redeems the token. The token id is a high-entropy ULID.
CREATE TABLE IF NOT EXISTS files_share_links (
    token      TEXT PRIMARY KEY,
    node_id    TEXT NOT NULL,
    role       TEXT NOT NULL,               -- editor | viewer (never owner)
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_files_share_links_node ON files_share_links(node_id);

-- versions: immutable history rows for a file node. A new row is appended on each
-- committed upload; the node's current_version_id points at the latest.
CREATE TABLE IF NOT EXISTS files_versions (
    id           TEXT PRIMARY KEY,
    node_id      TEXT NOT NULL,
    version_key  TEXT NOT NULL,             -- object key for this version's bytes
    size         INTEGER NOT NULL DEFAULT 0,
    content_type TEXT NOT NULL DEFAULT '',
    etag         TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL,
    created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_files_versions_node ON files_versions(node_id);

-- audit: append-only record of security-sensitive actions (shares, grants, link
-- create/revoke/redeem, deletes). Mirrors to the process log as well.
CREATE TABLE IF NOT EXISTS files_audit (
    id       TEXT PRIMARY KEY,
    ts       TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    action   TEXT NOT NULL,
    node_id  TEXT NOT NULL DEFAULT '',
    detail   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_files_audit_node ON files_audit(node_id);

-- external_mounts: SEAM (Phase 4) for CP-brokered external store connections
-- (Google Drive / GCS / Dropbox / S3) mounted as extra drives. Defined now so
-- the index has a stable home; not populated in the foundation.
CREATE TABLE IF NOT EXISTS files_external_mounts (
    id         TEXT PRIMARY KEY,
    owner_id   TEXT NOT NULL,
    provider   TEXT NOT NULL,
    name       TEXT NOT NULL,
    config     TEXT NOT NULL DEFAULT '',    -- opaque JSON; secrets stay in CP
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_files_external_mounts_owner ON files_external_mounts(owner_id);


-- peer_shares: capabilities issued by THIS box (owner side). The signed
-- capability token handed to the recipient is self-contained, but the owner
-- keeps a row per capability so it can be REVOKED (revoked=1 → fetch denied)
-- and audited. recipient is the bound peer's Vula ID, or '' for an
-- anyone-with-the-link capability. access is the granted role (viewer|editor).
CREATE TABLE IF NOT EXISTS files_peer_shares (
    id          TEXT PRIMARY KEY,            -- capability id (also signed into the token)
    node_id     TEXT NOT NULL,               -- target node in files_nodes
    owner_id    TEXT NOT NULL,               -- OS user that issued it
    access      TEXT NOT NULL,               -- editor | viewer
    recipient   TEXT NOT NULL DEFAULT '',    -- bound recipient Vula ID, or '' = anyone-with-link
    created_by  TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    revoked     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_files_peer_shares_node  ON files_peer_shares(node_id);
CREATE INDEX IF NOT EXISTS idx_files_peer_shares_owner ON files_peer_shares(owner_id);

-- peer_received: items THIS box has redeemed from another box (recipient side).
-- The fetched bytes are staged at staging_path on local disk; saved_node_id is
-- set once the recipient promotes the item into their own Drive (the B→A
-- bridge). Folders are staged as a tar archive (is_dir=1).
CREATE TABLE IF NOT EXISTS files_received (
    id            TEXT PRIMARY KEY,
    recipient_id  TEXT NOT NULL,             -- OS user that redeemed
    cap_id        TEXT NOT NULL,             -- source capability id
    name          TEXT NOT NULL,
    is_dir        INTEGER NOT NULL DEFAULT 0,
    size          INTEGER NOT NULL DEFAULT 0,
    content_type  TEXT NOT NULL DEFAULT '',
    owner_vulos_id TEXT NOT NULL DEFAULT '',  -- issuing box identity
    staging_path  TEXT NOT NULL DEFAULT '',  -- local staged bytes (tar for folders)
    saved_node_id TEXT NOT NULL DEFAULT '',  -- set when promoted into recipient's Drive
    received_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_files_received_recipient ON files_received(recipient_id);


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
