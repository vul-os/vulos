-- 0001_servingpool_init.postgres.sql — Postgres-specific schema (BIGSERIAL).
-- Bundle-node serving pool (CLOUD-POOL-01).
-- Tenant-affinity scheduler state: nodes, leases, autoscale signals.
-- Idempotent: CREATE TABLE IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS pool_nodes (
  node_id          TEXT PRIMARY KEY,
  region           TEXT NOT NULL DEFAULT '',
  capacity         BIGINT NOT NULL DEFAULT 1000,
  health           TEXT NOT NULL DEFAULT 'unknown',
  last_heartbeat   TEXT,
  load_score       DOUBLE PRECISION NOT NULL DEFAULT 0,
  registered_at    TEXT NOT NULL,
  updated_at       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_pool_nodes_health ON pool_nodes(health);

CREATE TABLE IF NOT EXISTS pool_leases (
  tenant_id        TEXT PRIMARY KEY,
  node_id          TEXT NOT NULL,
  acquired_at      TEXT NOT NULL,
  renewed_at       TEXT NOT NULL,
  expires_at       TEXT NOT NULL,
  generation       BIGINT NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS ix_pool_leases_node ON pool_leases(node_id);
CREATE INDEX IF NOT EXISTS ix_pool_leases_expires ON pool_leases(expires_at);

CREATE TABLE IF NOT EXISTS pool_autoscale_signals (
  id           BIGSERIAL PRIMARY KEY,
  emitted_at   TEXT NOT NULL,
  scope        TEXT NOT NULL,
  action       TEXT NOT NULL,
  reason       TEXT NOT NULL DEFAULT '',
  load_score   DOUBLE PRECISION NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS ix_pool_autoscale_recent ON pool_autoscale_signals(emitted_at);
