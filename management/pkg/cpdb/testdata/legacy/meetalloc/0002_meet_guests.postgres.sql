-- 0002_meet_guests.postgres.sql
-- MEET-GUEST-01: host-issued, revocable guest invites for MANAGED meetings.
-- Postgres variant of 0002_meet_guests.sqlite.sql — same shape, same semantics.
-- See the SQLite file for the full rationale.
--
-- CREATE-only (no ALTER/DROP anywhere in the suite).

CREATE TABLE IF NOT EXISTS meet_invites (
    id           TEXT PRIMARY KEY,
    room_id      TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    account_id   TEXT NOT NULL,
    created_by   TEXT NOT NULL,
    code_hash    TEXT NOT NULL UNIQUE,      -- SHA-256 hex; the raw code is never stored
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    revoked_at   TEXT,
    max_uses     INTEGER NOT NULL DEFAULT 25,
    uses         INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS ix_meet_invites_room ON meet_invites(room_id, created_at);

CREATE TABLE IF NOT EXISTS meet_guest_sessions (
    id            TEXT PRIMARY KEY,
    invite_id     TEXT NOT NULL,
    room_id       TEXT NOT NULL,
    tenant_id     TEXT NOT NULL,
    display_name  TEXT NOT NULL,
    secret_hash   TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    last_seen_at  TEXT NOT NULL,
    revoked_at    TEXT
);

CREATE INDEX IF NOT EXISTS ix_meet_guest_sessions_room   ON meet_guest_sessions(room_id, last_seen_at);
CREATE INDEX IF NOT EXISTS ix_meet_guest_sessions_invite ON meet_guest_sessions(invite_id, created_at);
