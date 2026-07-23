-- 0002_oauth_identities.postgres.sql
--
-- Social-login (Vulos as OAuth CLIENT) linked-identity table. Postgres variant;
-- identical shape to the SQLite variant.
--
-- Account model (LOCKED, 2026-07): email+password is the CENTRE of every
-- account — mandatory, independent of any IdP. Social login (Google / Microsoft
-- / …) is a CONVENIENCE login layered on top and is recorded here as a LINK from
-- an external IdP subject to a Vulos account. A social sign-up still creates a
-- normal users row and MUST set a Vulos password before the account is usable
-- (the row carries the LockedPasswordHash sentinel until it does — see
-- internal/auth/oauth_sunset.go). This table never holds a credential; it only
-- maps (provider, subject) -> users.id.
CREATE TABLE IF NOT EXISTS oauth_identities (
    provider       TEXT NOT NULL,                       -- 'google' | 'microsoft' | …
    subject        TEXT NOT NULL,                        -- IdP stable subject ('sub' claim)
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email          TEXT,                                 -- verified provider email at link time
    email_verified INTEGER NOT NULL DEFAULT 0,           -- 0/1: provider asserted email_verified
    created_at     TEXT NOT NULL,                        -- RFC3339 UTC
    PRIMARY KEY (provider, subject)
);

CREATE INDEX IF NOT EXISTS ix_oauth_identities_user ON oauth_identities (user_id);
