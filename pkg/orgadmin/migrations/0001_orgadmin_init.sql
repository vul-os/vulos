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

-- ── TEAM-INBOX-01 shared mailboxes (folded from former 0002_shared_mailboxes) ─
-- A shared mailbox is an ORDINARY mail account (e.g. support@acme.example) with a
-- MEMBERSHIP ACL layered on top: several people read ONE mailbox and assign /
-- annotate its conversations. The collaborative state (assignee, status, notes)
-- lives on the vulos-mail engine; THIS schema is the CP-side AUTHORITY for which
-- org owns the address + which connected mail account supplies the credential
-- (org_shared_mailboxes), and which CP accounts may act on it and in what role
-- (org_shared_mailbox_members — THE authorization boundary for the broker
-- headers). The CP row is the source of truth; the engine copy is downstream.
-- Colocated in the orgadmin schema so the "member must be in the owning org"
-- invariant is checkable in one database. Portable across SQLite + Postgres.
CREATE TABLE IF NOT EXISTS org_shared_mailboxes (
    id               TEXT PRIMARY KEY,          -- "shm-<hex>", CP-minted
    org_id           TEXT NOT NULL,             -- owning org (the tenant-isolation key)
    address          TEXT NOT NULL UNIQUE,      -- support@acme.example, lower-cased; globally unique
    provider         TEXT NOT NULL DEFAULT '',  -- gmail|outlook|imap (of the backing mail account)
    -- The credential to broker: mail_account_id is an integrations mail_accounts
    -- row OWNED BY owner_account_id. A member never needs their own credential for
    -- the shared address — the CP brokers the owner's, but ONLY after checking the
    -- caller's membership row below.
    owner_account_id TEXT NOT NULL,
    mail_account_id  TEXT NOT NULL,
    -- admin_of_record is the member email the CP presents to the engine as the
    -- ACTING ADMIN when mirroring a membership write (the engine's ACL requires an
    -- admin member for /internal/teaminbox/members). Set to the creator at
    -- provision time; that member cannot be removed while the mailbox exists.
    admin_of_record  TEXT NOT NULL,
    created_by       TEXT NOT NULL,             -- CP account id of the creating org admin
    created_at       TEXT NOT NULL,             -- RFC3339
    updated_at       TEXT NOT NULL              -- RFC3339
);

CREATE INDEX IF NOT EXISTS idx_org_shared_mailboxes_org ON org_shared_mailboxes (org_id);

CREATE TABLE IF NOT EXISTS org_shared_mailbox_members (
    mailbox_id TEXT NOT NULL,                   -- → org_shared_mailboxes.id
    account_id TEXT NOT NULL,                   -- CP account id (matched against the SESSION)
    -- email is the value brokered as X-Vulos-Mail-Member and mirrored into the
    -- engine ACL. Lower-cased. Server-derived at write time from the CP account —
    -- never client input at broker time (the proxy re-derives it from the session).
    email      TEXT NOT NULL,
    role       TEXT NOT NULL DEFAULT 'agent',   -- agent | admin (engine roles)
    added_at   TEXT NOT NULL,                   -- RFC3339
    PRIMARY KEY (mailbox_id, account_id)
);

CREATE INDEX IF NOT EXISTS idx_org_shared_mailbox_members_account
    ON org_shared_mailbox_members (account_id);
