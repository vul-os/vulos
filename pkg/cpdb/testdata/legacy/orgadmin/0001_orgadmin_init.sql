-- 0001_orgadmin_init.sql — org-admin dashboard backend (OPS-ADMIN-DASHBOARD-01).
--
-- This is the FOLDED initial schema: it declares the final shape of every
-- orgadmin table directly in its CREATE, superseding the historical
-- 0002..0005 migration chain (invite display_name, orgs + membership +
-- active-org, home_region/residency_policy, product_status). The chain was
-- purely additive — CREATE … IF NOT EXISTS plus ADD COLUMN — so there is no
-- ALTER/DROP here; the added columns (org_invites.display_name,
-- orgs.home_region, orgs.residency_policy) are inlined into their CREATE TABLE.
--
-- Tables owned by this package:
--   org_backup_mode    — cloud-side record of the STORE-LOCAL-01 storage-mode
--                        toggle (central-tigris | local-minio-sync), per tenant.
--   org_invites        — pending invite tokens (standalone fallback store),
--                        carrying the invited user's display name.
--   orgs               — one row per org (ORG-MULTI-01), free root mailbox
--                        (FREE-ORG-MAIL-01), placement home_region +
--                        residency_policy (MULTI-REGION Phase 0).
--   org_membership     — (org_id, account_id) → role.
--   user_active_org    — the per-user "current" org.
--   org_product_status — per-(tenant, product) suite-product status override
--                        (PRODUCTS-ADMIN-01).
--
-- Pure-Go modernc.org/sqlite (CGO_ENABLED=0). Idempotent: CREATE … IF NOT EXISTS.
-- Portable across SQLite + Postgres (no dialect-specific types).

CREATE TABLE IF NOT EXISTS org_backup_mode (
    tenant_id            TEXT PRIMARY KEY,
    mode                 TEXT NOT NULL DEFAULT 'central', -- 'central' | 'local'
    per_location_json    TEXT NOT NULL DEFAULT '[]',      -- JSON array of overrides
    last_sync            TEXT,                            -- RFC3339, nullable
    sync_health          TEXT,                            -- 'healthy'|'degraded'|... nullable
    updated_at           TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS org_invites (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    email       TEXT NOT NULL,
    role        TEXT NOT NULL,
    state       TEXT NOT NULL DEFAULT 'pending', -- 'pending'|'revoked'|'accepted'
    created_at  TEXT NOT NULL
, display_name TEXT NOT NULL DEFAULT '');

CREATE INDEX IF NOT EXISTS idx_org_invites_tenant ON org_invites (tenant_id);

CREATE TABLE IF NOT EXISTS orgs (
    id             TEXT PRIMARY KEY,
    slug           TEXT NOT NULL UNIQUE,
    name           TEXT NOT NULL,
    owner_account  TEXT NOT NULL,
    root_mailbox   TEXT NOT NULL DEFAULT '',            -- <slug>@<mail-domain>
    mailbox_domain TEXT NOT NULL DEFAULT '',
    mailbox_state  TEXT NOT NULL DEFAULT 'none',        -- none|pending|provisioned|failed
    created_at     TEXT NOT NULL
, home_region      TEXT NOT NULL DEFAULT 'eu', residency_policy TEXT NOT NULL DEFAULT 'any');

CREATE INDEX IF NOT EXISTS idx_orgs_owner ON orgs (owner_account);

CREATE TABLE IF NOT EXISTS org_membership (
    org_id      TEXT NOT NULL,
    account_id  TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT 'member',         -- owner|admin|billing-admin|member
    joined_at   TEXT NOT NULL,
    PRIMARY KEY (org_id, account_id)
);

CREATE INDEX IF NOT EXISTS idx_org_membership_account ON org_membership (account_id);

CREATE TABLE IF NOT EXISTS user_active_org (
    account_id  TEXT PRIMARY KEY,
    org_id      TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

-- org_product_status (PRODUCTS-ADMIN-01, wave 43): per-org suite-product status.
-- A row OVERRIDES the catalogue default ('available') for one (tenant, product)
-- pair. Absence of a row ⇒ the catalogue default.
--   status ∈ 'configured' | 'available' | 'off'   (validated at the service
--   boundary, not by a CHECK, so an unknown value can never wedge a read).
CREATE TABLE IF NOT EXISTS org_product_status (
    tenant_id   TEXT NOT NULL,
    product_id  TEXT NOT NULL,
    status      TEXT NOT NULL,  -- 'configured' | 'available' | 'off'
    updated_at  TEXT NOT NULL,  -- RFC3339
    PRIMARY KEY (tenant_id, product_id)
);

CREATE INDEX IF NOT EXISTS idx_org_product_status_tenant
    ON org_product_status (tenant_id);
