-- 0001_servingpool_init.sqlite.sql — SQLite-specific schema (AUTOINCREMENT).
-- Bundle-node serving pool (CLOUD-POOL-01).
-- Tenant-affinity scheduler state: nodes, leases, autoscale signals.
-- Idempotent: CREATE TABLE IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS pool_nodes (
  node_id          TEXT PRIMARY KEY,
  region           TEXT NOT NULL DEFAULT '',
  capacity         INTEGER NOT NULL DEFAULT 1000,
  health           TEXT NOT NULL DEFAULT 'unknown',
  last_heartbeat   TEXT,
  load_score       REAL NOT NULL DEFAULT 0,
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
  generation       INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS ix_pool_leases_node ON pool_leases(node_id);
CREATE INDEX IF NOT EXISTS ix_pool_leases_expires ON pool_leases(expires_at);

CREATE TABLE IF NOT EXISTS pool_autoscale_signals (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  emitted_at   TEXT NOT NULL,
  scope        TEXT NOT NULL,
  action       TEXT NOT NULL,
  reason       TEXT NOT NULL DEFAULT '',
  load_score   REAL NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS ix_pool_autoscale_recent ON pool_autoscale_signals(emitted_at);

-- ── CLOUD-DEDICATED-01 (folded from former 0002_dedicated) ────────────────────
-- Dedicated single-tenant pinning + per-user VDI slice scheduling. tenant_pins:
-- a tenant pinned to a specific bundle node (scheduler bypasses the hash ring for
-- that tenant). vdi_slices: a scheduled per-user full-desktop VDI slice, one row
-- per user; host_kind ∈ ('pool','dedicated') picks the placement strategy.
CREATE TABLE IF NOT EXISTS tenant_pins (
  tenant_id   TEXT PRIMARY KEY,             -- tenant pinned to a dedicated node
  node_id     TEXT NOT NULL,                -- the bundle node id (matches pool_nodes.node_id)
  reason      TEXT NOT NULL DEFAULT '',     -- human-readable (e.g. "enterprise SLA", "isolation")
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_tenant_pins_node ON tenant_pins(node_id);

CREATE TABLE IF NOT EXISTS vdi_slices (
  user_id       TEXT PRIMARY KEY,
  account_id    TEXT NOT NULL,
  tenant_id     TEXT NOT NULL,
  host_kind     TEXT NOT NULL,                  -- 'pool' | 'dedicated'
  node_id       TEXT NOT NULL,                  -- the bundle node hosting the slice
  scheduled_at  TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_vdi_slices_node    ON vdi_slices(node_id);
CREATE INDEX IF NOT EXISTS ix_vdi_slices_account ON vdi_slices(account_id);
CREATE INDEX IF NOT EXISTS ix_vdi_slices_tenant  ON vdi_slices(tenant_id);
