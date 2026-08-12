-- FILES PHASE 2B: OS Peer-Share (Mechanism B — bucket-less, box-to-box).
-- Tables for signed, scoped, expiring, REVOCABLE capabilities the owner box
-- issues over an existing node, and for items a recipient box has redeemed.
-- All objects use IF NOT EXISTS so migration is idempotent. Bytes are NEVER
-- stored here — capabilities reference a node in files_nodes; redeemed bytes
-- are staged on the recipient's local disk until promoted into their Drive.

-- peer_shares: capabilities issued by THIS box (owner side). The signed
-- capability token handed to the recipient is self-contained, but the owner
-- keeps a row per capability so it can be REVOKED (revoked=1 → fetch denied)
-- and audited. recipient is the bound peer's Vulos ID, or '' for an
-- anyone-with-the-link capability. access is the granted role (viewer|editor).
CREATE TABLE IF NOT EXISTS files_peer_shares (
    id          TEXT PRIMARY KEY,            -- capability id (also signed into the token)
    node_id     TEXT NOT NULL,               -- target node in files_nodes
    owner_id    TEXT NOT NULL,               -- OS user that issued it
    access      TEXT NOT NULL,               -- editor | viewer
    recipient   TEXT NOT NULL DEFAULT '',    -- bound recipient Vulos ID, or '' = anyone-with-link
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
