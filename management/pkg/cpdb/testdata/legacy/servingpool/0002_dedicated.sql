-- 0002_dedicated.sql
-- Dedicated single-tenant pinning + per-user VDI slice scheduling
-- (CLOUD-DEDICATED-01). Extends the servingpool pool tables; the existing
-- pooled/multi-tenant behaviour is unchanged when no rows are present here.
-- Idempotent: CREATE TABLE IF NOT EXISTS.

-- tenant_pins: a tenant pinned to a specific bundle node. When a pin exists
-- the scheduler bypasses the consistent-hash ring for that tenant and uses
-- the pinned node. Single row per tenant (PK on tenant_id).
CREATE TABLE IF NOT EXISTS tenant_pins (
  tenant_id   TEXT PRIMARY KEY,             -- tenant pinned to a dedicated node
  node_id     TEXT NOT NULL,                -- the bundle node id (matches pool_nodes.node_id)
  reason      TEXT NOT NULL DEFAULT '',     -- human-readable (e.g. "enterprise SLA", "isolation")
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_tenant_pins_node ON tenant_pins(node_id);

-- vdi_slices: a scheduled per-user full-desktop (cage/labwc) VDI slice. One
-- attachment row per (user_id) — re-scheduling overwrites the previous slice.
-- host_kind ∈ ('pool', 'dedicated') picks the placement strategy:
--   - 'pool'      → scheduler picks any healthy bundle node
--   - 'dedicated' → requires an existing pin on the user's tenant
-- account_id is captured for billing meter attribution; tenant_id is the
-- tenant the user belongs to (used for the dedicated-pin lookup).
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
