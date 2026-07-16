-- syncrz: central Tigris rendezvous coordinator for CRDT delta + blob
-- manifest exchange between an org's boxes (SYNC-RENDEZVOUS-01).
--
-- Storage posture: this DB holds ONLY routing metadata (cursors, manifest
-- indices, content-hash listings). It NEVER holds plaintext payloads —
-- delta bytes and blob bytes live in Tigris under the per-account prefix.
-- For BYO/local-MinIO accounts, the box encrypts before push, so even the
-- Tigris bytes are opaque ciphertext from the coordinator's perspective.
--
-- Pure-Go SQLite (modernc.org/sqlite). NEVER CGO.

-- Per-(account,key) push log. Each row is one opaque delta blob a box has
-- pushed; the cursor a peer sends with /pull is a row-id high-water mark.
-- The actual delta bytes live in Tigris under the account prefix, addressed
-- by (cursor, codec_version). The coordinator stores only the metadata.
CREATE TABLE IF NOT EXISTS syncrz_deltas (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id      TEXT NOT NULL,
    sync_key        TEXT NOT NULL,    -- e.g. "mailbox/45", "office/session/abc"
    box_id          TEXT NOT NULL,    -- pusher (kept so we can skip echo on pull)
    codec_version   INTEGER NOT NULL, -- opaque to coordinator, just round-tripped
    payload_bytes   INTEGER NOT NULL, -- size of the opaque payload in Tigris (audit only)
    payload_sha256  TEXT NOT NULL,    -- content hash of the OPAQUE payload (dedup key)
    tigris_key      TEXT NOT NULL,    -- s3 key under the account prefix
    pushed_at       INTEGER NOT NULL  -- unix seconds
);

-- Idempotent re-push: a (account, sync_key, payload_sha256) tuple appears
-- at most once. A duplicate push is a no-op (rowid stable).
CREATE UNIQUE INDEX IF NOT EXISTS idx_syncrz_deltas_dedup
    ON syncrz_deltas (account_id, sync_key, payload_sha256);

-- Pull range: "deltas with id > since for (account, sync_key)".
CREATE INDEX IF NOT EXISTS idx_syncrz_deltas_pull
    ON syncrz_deltas (account_id, sync_key, id);

-- Per-box cursor (last id observed for a (box, sync_key)). Strictly an
-- optimization / audit aid — the canonical cursor a box sends on /pull is
-- its own value. The coordinator persists the last-seen so /pull-since-last
-- works without the box needing to remember.
CREATE TABLE IF NOT EXISTS syncrz_cursors (
    account_id      TEXT NOT NULL,
    sync_key        TEXT NOT NULL,
    box_id          TEXT NOT NULL,
    last_seen_id    INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (account_id, sync_key, box_id)
);

-- Content-addressed blob manifest index. The blob bytes live in Tigris
-- under the account prefix; this table records "hash X exists for account Y"
-- so /pull and /blob can quickly resolve a hash to a Tigris key without a
-- LIST.
CREATE TABLE IF NOT EXISTS syncrz_blobs (
    account_id      TEXT NOT NULL,
    content_hash    TEXT NOT NULL,    -- lowercase hex sha256 (caller-asserted; verified)
    size_bytes      INTEGER NOT NULL,
    tigris_key      TEXT NOT NULL,
    first_seen_at   INTEGER NOT NULL,
    PRIMARY KEY (account_id, content_hash)
);

-- Per-delta manifest references: which blob hashes a delta references.
-- Lets a peer fetch "the blobs this delta references" without parsing the
-- opaque delta payload (which the coordinator must NEVER do).
CREATE TABLE IF NOT EXISTS syncrz_manifest_refs (
    delta_id        INTEGER NOT NULL,
    content_hash    TEXT NOT NULL,
    PRIMARY KEY (delta_id, content_hash)
);

CREATE INDEX IF NOT EXISTS idx_syncrz_manifest_refs_hash
    ON syncrz_manifest_refs (content_hash);
