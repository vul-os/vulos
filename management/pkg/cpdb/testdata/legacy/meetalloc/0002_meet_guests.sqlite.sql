-- 0002_meet_guests.sqlite.sql
-- MEET-GUEST-01: host-issued, revocable guest invites for MANAGED meetings.
--
-- Before this, every CP meet route was session-gated and the CP is the sole
-- issuer of vulos-meet join tokens — so a person without a Vulos account could
-- not join a managed meeting at all. These two tables are the capability that
-- fixes it WITHOUT opening a hole: a guest never names a room, an invite does.
--
-- CREATE-only (no ALTER/DROP anywhere in the suite).

-- meet_invites: one host-issued capability to join ONE room as a guest.
--
-- code_hash is the SHA-256 hex of the 256-bit random invite code. The RAW CODE IS
-- NEVER STORED — it is returned exactly once, at creation. A database read (or a
-- leaked backup) therefore yields no replayable meeting access. The UNIQUE index
-- makes the guest-join lookup an O(1) point read on the hash, so there is no scan
-- and no enumeration surface.
--
-- Bounds (an unauthenticated surface must be bounded in every dimension):
--   expires_at — every invite dies on its own (12h default, 7d ceiling).
--   max_uses   — ceiling on DISTINCT guests (25 default, 200 ceiling). One use is
--                one guest, NOT one token mint: a guest refreshing their 15-minute
--                token does not burn a use.
--   revoked_at — the host can kill the link; re-checked on EVERY guest mint, so a
--                revoked invite stops working within one token TTL.
CREATE TABLE IF NOT EXISTS meet_invites (
    id           TEXT PRIMARY KEY,          -- inv_<millis>_<rand>
    room_id      TEXT NOT NULL,             -- qualified <tenant>:<roomName>
    tenant_id    TEXT NOT NULL,             -- copied from the room row (never client input)
    account_id   TEXT NOT NULL,             -- the room owner's billing account
    created_by   TEXT NOT NULL,             -- the room's host (meet_rooms.created_by)
    code_hash    TEXT NOT NULL UNIQUE,      -- SHA-256 hex of the raw code
    created_at   TEXT NOT NULL,             -- RFC3339 UTC
    expires_at   TEXT NOT NULL,             -- RFC3339 UTC
    revoked_at   TEXT,                      -- RFC3339 UTC; NULL while live
    max_uses     INTEGER NOT NULL DEFAULT 25,
    uses         INTEGER NOT NULL DEFAULT 0 -- distinct guests admitted so far
);

CREATE INDEX IF NOT EXISTS ix_meet_invites_room ON meet_invites(room_id, created_at);

-- meet_guest_sessions: one row per DISTINCT guest behind an invite.
--
-- id IS the guest's identity on the SFU ("guest_<slug>_<rand>") — the name the
-- host sees in the lobby and admits (meet_admissions.user_id). secret_hash makes
-- that identity unforgeable across the token refreshes a 15-minute TTL forces:
-- without it, anyone holding the invite code could re-mint under ANOTHER guest's
-- identity, including one the host had already admitted past the waiting room.
--
-- last_seen_at is the liveness signal. It backs BOTH the participant cap and the
-- billing meter (see Service.meteredParticipants), so the figure we gate on and
-- the figure we bill are the same figure by construction — a guest is a
-- participant, metered on the ROOM OWNER's account, and there is no unmetered
-- free-participant path.
CREATE TABLE IF NOT EXISTS meet_guest_sessions (
    id            TEXT PRIMARY KEY,   -- the guest's SFU identity
    invite_id     TEXT NOT NULL,      -- the invite this guest came through
    room_id       TEXT NOT NULL,      -- denormalised from the invite (hot-path lookup)
    tenant_id     TEXT NOT NULL,
    display_name  TEXT NOT NULL,      -- sanitized; display only, never an authz input
    secret_hash   TEXT NOT NULL,      -- SHA-256 hex of the one-time guest secret
    created_at    TEXT NOT NULL,      -- RFC3339 UTC
    last_seen_at  TEXT NOT NULL,      -- RFC3339 UTC; refreshed on every mint
    revoked_at    TEXT                -- RFC3339 UTC; set when the invite is revoked
);

CREATE INDEX IF NOT EXISTS ix_meet_guest_sessions_room   ON meet_guest_sessions(room_id, last_seen_at);
CREATE INDEX IF NOT EXISTS ix_meet_guest_sessions_invite ON meet_guest_sessions(invite_id, created_at);
