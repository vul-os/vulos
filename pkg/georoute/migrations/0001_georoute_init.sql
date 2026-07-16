-- 0001_georoute_init.sql
-- CLOUD-REGION-01: per-tenant home region assignment.
--
-- One row per tenant. Home region is assigned at signup based on a hint,
-- geo-IP, or the configured default. The tenant's home region is the cloud
-- multi-region analogue of an OSS self-host "location"; cross-region
-- convergence reuses the central rendezvous (SYNC-RENDEZVOUS-01) + the
-- fabric-P2P sync (SYNC-P2P-01) — exactly like self-host multi-location.

CREATE TABLE IF NOT EXISTS tenants_regions (
    tenant_id    TEXT PRIMARY KEY,
    home_region  TEXT NOT NULL,
    assigned_at  INTEGER NOT NULL  -- Unix seconds
);

CREATE INDEX IF NOT EXISTS idx_tenants_regions_home
    ON tenants_regions (home_region);
