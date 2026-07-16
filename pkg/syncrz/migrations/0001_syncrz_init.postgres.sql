-- syncrz: central Tigris rendezvous coordinator for CRDT delta + blob
-- manifest exchange between an org's boxes (SYNC-RENDEZVOUS-01).
--
-- Postgres variant: BIGSERIAL for auto-increment primary key.
-- Storage posture: this DB holds ONLY routing metadata (cursors, manifest
-- indices, content-hash listings). It NEVER holds plaintext payloads.

CREATE TABLE IF NOT EXISTS syncrz_deltas (
    id              BIGSERIAL PRIMARY KEY,
    account_id      TEXT NOT NULL,
    sync_key        TEXT NOT NULL,
    box_id          TEXT NOT NULL,
    codec_version   BIGINT NOT NULL,
    payload_bytes   BIGINT NOT NULL,
    payload_sha256  TEXT NOT NULL,
    tigris_key      TEXT NOT NULL,
    pushed_at       BIGINT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_syncrz_deltas_dedup
    ON syncrz_deltas (account_id, sync_key, payload_sha256);

CREATE INDEX IF NOT EXISTS idx_syncrz_deltas_pull
    ON syncrz_deltas (account_id, sync_key, id);

CREATE TABLE IF NOT EXISTS syncrz_cursors (
    account_id      TEXT NOT NULL,
    sync_key        TEXT NOT NULL,
    box_id          TEXT NOT NULL,
    last_seen_id    BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL,
    PRIMARY KEY (account_id, sync_key, box_id)
);

CREATE TABLE IF NOT EXISTS syncrz_blobs (
    account_id      TEXT NOT NULL,
    content_hash    TEXT NOT NULL,
    size_bytes      BIGINT NOT NULL,
    tigris_key      TEXT NOT NULL,
    first_seen_at   BIGINT NOT NULL,
    PRIMARY KEY (account_id, content_hash)
);

CREATE TABLE IF NOT EXISTS syncrz_manifest_refs (
    delta_id        BIGINT NOT NULL,
    content_hash    TEXT NOT NULL,
    PRIMARY KEY (delta_id, content_hash)
);

CREATE INDEX IF NOT EXISTS idx_syncrz_manifest_refs_hash
    ON syncrz_manifest_refs (content_hash);
