-- 0002_shared_mailboxes.sql — SHARED TEAM INBOX (TEAM-INBOX-01).
--
-- A shared mailbox is an ORDINARY mail account (e.g. support@acme.example) with
-- a MEMBERSHIP ACL layered on top: several people read ONE mailbox and assign /
-- annotate its conversations. It is NOT a distribution list.
--
-- The collaborative state itself (assignee, status, notes, canned replies) lives
-- on the vulos-mail engine (its internal/teaminbox store). THIS schema is the
-- CP-side AUTHORITY for two things the engine cannot know:
--
--   org_shared_mailboxes        — which org owns the shared address, and WHICH
--                                 connected mail account (integrations
--                                 mail_accounts row, owned by owner_account_id)
--                                 supplies the IMAP/SMTP credential the CP
--                                 brokers when a member opens the mailbox.
--   org_shared_mailbox_members  — which CP accounts may act on it, and in what
--                                 role. This is THE authorization boundary for
--                                 emitting the X-Vulos-Mail-Teaminbox-Url /
--                                 X-Vulos-Mail-Member broker headers: a row here
--                                 (and only a row here) turns lilmail's /v1/team
--                                 surface on for that caller.
--
-- The engine keeps its own mirror of the roster (it authorizes the `member` value
-- lilmail forwards); the CP mirrors every membership write to it. The CP row is
-- the source of truth — the engine copy is downstream.
--
-- Reuses the orgadmin subsystem (same cpdb schema) deliberately: a shared-mailbox
-- member MUST be a member of the owning org (org_membership), and only an org
-- owner/admin may administer one (rbac.go ActionSharedMailboxManage) — colocating
-- the tables keeps that invariant checkable in one database.
--
-- CREATE-only (suite rule): no ALTER, no DROP. Idempotent: CREATE … IF NOT EXISTS.
-- Portable across SQLite + Postgres (no dialect-specific types).

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
