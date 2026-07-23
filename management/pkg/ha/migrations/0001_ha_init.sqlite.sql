-- 0001_ha_init.sqlite.sql
-- Control-plane HA leader election state (RISK-SPOF-01) — SQLite dialect.
-- A single row per lease_key holds the current leader, lease deadline, and a
-- monotonic generation counter that bumps on every leadership handoff.
-- Idempotent: CREATE TABLE IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS ha_leases (
  lease_key      TEXT PRIMARY KEY,             -- logical lease name (e.g. "cp-leader")
  holder_id      TEXT NOT NULL,                -- the peer that owns the lease
  acquired_at    TEXT NOT NULL,                -- when this generation started
  renewed_at     TEXT NOT NULL,                -- last heartbeat by holder
  expires_at     TEXT NOT NULL,                -- deadline; another peer may steal after this
  generation     INTEGER NOT NULL DEFAULT 1    -- monotonic, bumps on every handoff
);

CREATE INDEX IF NOT EXISTS ix_ha_leases_expires ON ha_leases(expires_at);

-- Peer heartbeats: every peer publishes its liveness here so the leader-of-the-
-- moment + observers can compute "who is alive". One row per peer.
CREATE TABLE IF NOT EXISTS ha_peers (
  peer_id          TEXT PRIMARY KEY,
  address          TEXT NOT NULL DEFAULT '',   -- optional, e.g. "https://cp-b.example:8081"
  last_heartbeat   TEXT NOT NULL,
  role             TEXT NOT NULL DEFAULT 'standby', -- 'leader'|'standby'|'unknown'
  generation_seen  INTEGER NOT NULL DEFAULT 0  -- highest lease generation this peer has observed
);

CREATE INDEX IF NOT EXISTS ix_ha_peers_heartbeat ON ha_peers(last_heartbeat);

-- Leader markers: append-only log of leader-written facts; used by tests to
-- prove that state written by a leader survives a failover.
CREATE TABLE IF NOT EXISTS ha_markers (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  lease_key    TEXT NOT NULL,
  generation   INTEGER NOT NULL,
  holder_id    TEXT NOT NULL,
  marker       TEXT NOT NULL,
  written_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_ha_markers_lease ON ha_markers(lease_key, id);
