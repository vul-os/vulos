-- FILES-FOUNDATION: OS Files metadata / control-plane schema.
-- Source of truth for the Drive index, ACLs/shares, share links, versions, and
-- (seam) external-store connections. Bytes live in per-user buckets; truth here.
-- All objects use IF NOT EXISTS so migration is idempotent.

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
