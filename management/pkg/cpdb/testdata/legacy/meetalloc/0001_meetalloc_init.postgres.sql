-- 0001_meetalloc_init.postgres.sql
-- Vulos Meet (MEET-CP-01) per-room state + idempotent wallet metering.
-- Postgres variant: uses BIGSERIAL instead of INTEGER PRIMARY KEY AUTOINCREMENT.
--
-- This is the FOLDED initial schema: it declares the final shape of every
-- meetalloc table directly in its CREATE, superseding the historical
-- 0002..0006 migration chain (recordings, breakouts, recording-delete,
-- waiting-room, meeting-lock). No ALTER/DROP — the ADD COLUMNs from the old
-- chain (meet_rooms.waiting_room, meet_rooms.locked, meet_rooms.passcode,
-- meet_recordings.deleted_at) are inlined into their CREATE TABLE below.

CREATE TABLE IF NOT EXISTS meet_rooms (
    room_id       TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    account_id    TEXT NOT NULL,
    created_by    TEXT NOT NULL,
    node_url      TEXT NOT NULL,
    started_at    TEXT NOT NULL,
    ended_at      TEXT,
    participants  INTEGER NOT NULL DEFAULT 1,
    waiting_room  INTEGER NOT NULL DEFAULT 0,
    locked        INTEGER NOT NULL DEFAULT 0,
    -- MEET-PASSCODE (cloud): the room's optional join passcode, stored as a
    -- SHA-256 hex digest (never plaintext). '' ⇒ no passcode gate. Set once by
    -- the room creator on the first Allocate; a non-host joiner must present a
    -- matching passcode or the token mint is refused 403. The self-host minter
    -- enforces the same gate from MEET_SELFHOST_PASSCODE; this is the managed-
    -- path equivalent, per-room instead of per-box.
    passcode      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS ix_meet_rooms_tenant ON meet_rooms(tenant_id, started_at);
CREATE INDEX IF NOT EXISTS ix_meet_rooms_account ON meet_rooms(account_id, started_at);

CREATE TABLE IF NOT EXISTS meet_usage (
    id                     BIGSERIAL PRIMARY KEY,
    room_id                TEXT NOT NULL,
    tenant_id              TEXT NOT NULL,
    account_id             TEXT NOT NULL,
    participants           INTEGER NOT NULL,
    minutes                INTEGER NOT NULL,
    per_minute_cents_usd   INTEGER NOT NULL,
    charged_cents_usd      INTEGER NOT NULL,
    charged_cents_zar      INTEGER NOT NULL,
    period_start           TEXT NOT NULL,
    period_end             TEXT NOT NULL,
    recorded_at            TEXT NOT NULL,
    UNIQUE (room_id, period_start)
);

CREATE INDEX IF NOT EXISTS ix_meet_usage_account ON meet_usage(account_id, recorded_at);
CREATE INDEX IF NOT EXISTS ix_meet_usage_room    ON meet_usage(room_id, recorded_at);

-- meet_recordings (MEET-RECORDING-01) + deleted_at (MEET-RECORDING-DELETE-01).
CREATE TABLE IF NOT EXISTS meet_recordings (
    id              TEXT PRIMARY KEY,
    egress_id       TEXT,
    room_id         TEXT NOT NULL,
    tenant_id       TEXT NOT NULL,
    account_id      TEXT NOT NULL,
    started_at      TEXT NOT NULL,
    stopped_at      TEXT,
    object_path     TEXT NOT NULL,
    size_bytes      INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL,
    error           TEXT,
    deleted_at      TEXT
);

CREATE INDEX IF NOT EXISTS ix_meet_recordings_tenant  ON meet_recordings(tenant_id, started_at);
CREATE INDEX IF NOT EXISTS ix_meet_recordings_account ON meet_recordings(account_id, started_at);
CREATE INDEX IF NOT EXISTS ix_meet_recordings_room    ON meet_recordings(room_id, started_at);

CREATE UNIQUE INDEX IF NOT EXISTS ux_meet_recordings_active
    ON meet_recordings(room_id)
    WHERE status IN ('starting', 'active');

-- meet_breakouts + meet_breakout_assignments (FIX-BREAKOUTS-BACKEND-01).
CREATE TABLE IF NOT EXISTS meet_breakouts (
    id              TEXT PRIMARY KEY,
    room_id         TEXT NOT NULL,
    parent_room_id  TEXT NOT NULL,
    tenant_id       TEXT NOT NULL,
    name            TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    ended_at        TEXT
);

CREATE INDEX IF NOT EXISTS ix_meet_breakouts_parent ON meet_breakouts(parent_room_id, created_at);
CREATE INDEX IF NOT EXISTS ix_meet_breakouts_tenant ON meet_breakouts(tenant_id, created_at);

CREATE TABLE IF NOT EXISTS meet_breakout_assignments (
    id              BIGSERIAL PRIMARY KEY,
    breakout_id     TEXT NOT NULL,
    parent_room_id  TEXT NOT NULL,
    room_id         TEXT NOT NULL,
    tenant_id       TEXT NOT NULL,
    user_id         TEXT NOT NULL,
    joined_at       TEXT NOT NULL,
    recalled_at     TEXT
);

CREATE INDEX IF NOT EXISTS ix_meet_breakout_assignments_breakout ON meet_breakout_assignments(breakout_id, joined_at);
CREATE INDEX IF NOT EXISTS ix_meet_breakout_assignments_user     ON meet_breakout_assignments(user_id, joined_at);
CREATE INDEX IF NOT EXISTS ix_meet_breakout_assignments_parent   ON meet_breakout_assignments(parent_room_id, joined_at);

CREATE UNIQUE INDEX IF NOT EXISTS ux_meet_breakout_assignments_active
    ON meet_breakout_assignments(parent_room_id, user_id)
    WHERE recalled_at IS NULL;

-- meet_admissions (MEET-WAITING-TOKEN-01): host-issued admit ledger.
CREATE TABLE IF NOT EXISTS meet_admissions (
    id            BIGSERIAL PRIMARY KEY,
    room_id       TEXT NOT NULL,
    tenant_id     TEXT NOT NULL,
    user_id       TEXT NOT NULL,
    admitted_by   TEXT NOT NULL,
    admitted_at   TEXT NOT NULL,
    revoked_at    TEXT
);

CREATE INDEX IF NOT EXISTS ix_meet_admissions_room ON meet_admissions(room_id, admitted_at);

CREATE UNIQUE INDEX IF NOT EXISTS ux_meet_admissions_active
    ON meet_admissions(room_id, user_id)
    WHERE revoked_at IS NULL;
