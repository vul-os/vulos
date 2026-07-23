-- 0001_multiloc_init.sql
-- Multi-location schema: locations + routing table.
-- Central-bucket model: all locations in an org share ONE bucket.
-- CRDT + bucket-lease coordinator handle concurrent writes.

CREATE TABLE IF NOT EXISTS locations (
    org_id       TEXT NOT NULL,
    location_id  TEXT NOT NULL,
    name         TEXT NOT NULL DEFAULT '',
    endpoint     TEXT NOT NULL DEFAULT '',   -- base URL of the box, e.g. https://box.example.com
    last_seen_at INTEGER NOT NULL DEFAULT 0, -- Unix seconds; 0 = never seen
    healthy      INTEGER NOT NULL DEFAULT 0, -- 1 = healthy, 0 = unhealthy
    enrolled_at  INTEGER NOT NULL,           -- Unix seconds
    PRIMARY KEY (org_id, location_id)
);

CREATE INDEX IF NOT EXISTS idx_locations_org
    ON locations (org_id);

CREATE INDEX IF NOT EXISTS idx_locations_org_healthy
    ON locations (org_id, healthy);
