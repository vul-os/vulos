-- 0001_meetalloc_init.sqlite.sql
-- Vulos Meet (MEET-CP-01) per-room state + idempotent wallet metering.
-- Idempotent: safe to run against an existing DB.
--
-- This is the FOLDED initial schema: it declares the final shape of every
-- meetalloc table directly in its CREATE, superseding the historical
-- 0002..0006 migration chain (recordings, breakouts, recording-delete,
-- waiting-room, meeting-lock). No ALTER/DROP — the ADD COLUMNs from the old
-- chain (meet_rooms.waiting_room, meet_rooms.locked, meet_recordings.deleted_at)
-- are inlined into their CREATE TABLE below.

-- meet_rooms is the cloud's view of an active meeting. The SFU itself is the
-- source of truth for the live participant roster; this table records the
-- (tenant, room_id, created_by, started_at, node_url) we need for the admin
-- surface AND the unit-of-billing for the wallet meter.
--
-- room_id is the qualified <tenant>:<roomName> — same byte sequence the
-- vulos-meet wrap.Tenant.SplitRoom parses on the SFU side.
--
-- waiting_room (MEET-WAITING-TOKEN-01): per-room CP-side flag. When 1, a
-- NON-host joiner is issued a WAITING token (CanPublish=false +
-- CanSubscribe=false). Defaults 0 (waiting off).
-- locked (MEET-MEETING-LOCK): per-room CP-side flag. When 1, Allocate refuses
-- a NEW non-host joiner. Defaults 0 (unlocked).
-- passcode (MEET-PASSCODE, cloud): the room's optional join passcode stored as
-- a SHA-256 hex digest (never plaintext). '' ⇒ no passcode gate. Set once by the
-- creator on the first Allocate; a non-host joiner must present a matching
-- passcode or the mint is refused 403 — the managed-path equivalent of the
-- self-host MEET_SELFHOST_PASSCODE gate, per-room instead of per-box.
CREATE TABLE IF NOT EXISTS meet_rooms (
    room_id       TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    account_id    TEXT NOT NULL,
    created_by    TEXT NOT NULL,
    node_url      TEXT NOT NULL,
    started_at    TEXT NOT NULL,         -- RFC3339 UTC
    ended_at      TEXT,                  -- RFC3339 UTC; NULL while live
    participants  INTEGER NOT NULL DEFAULT 1
, waiting_room INTEGER NOT NULL DEFAULT 0, locked INTEGER NOT NULL DEFAULT 0, passcode TEXT NOT NULL DEFAULT '');

CREATE INDEX IF NOT EXISTS ix_meet_rooms_tenant ON meet_rooms(tenant_id, started_at);
CREATE INDEX IF NOT EXISTS ix_meet_rooms_account ON meet_rooms(account_id, started_at);

-- meet_usage is the per-(room, period_start) idempotent metering row. The
-- wallet meter writes (room_id, period_start) as a UNIQUE key — re-emitting
-- the same slice is a no-op (ON CONFLICT DO NOTHING). Same shape and
-- idempotency contract as gpu_usage (gpusku/migrations/0001_gpusku_init.sql)
-- so the dashboard wallet section can render meet line-items uniformly with
-- the GPU ones.
--
-- charged_cents_usd = round(participants * minutes * per_minute_cents_usd)
-- charged_cents_zar = round(charged_cents_usd * rate_zar_per_usd)
CREATE TABLE IF NOT EXISTS meet_usage (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    room_id                TEXT NOT NULL,
    tenant_id              TEXT NOT NULL,
    account_id             TEXT NOT NULL,
    participants           INTEGER NOT NULL,
    minutes                INTEGER NOT NULL,
    per_minute_cents_usd   INTEGER NOT NULL,
    charged_cents_usd      INTEGER NOT NULL,
    charged_cents_zar      INTEGER NOT NULL,
    period_start           TEXT NOT NULL,    -- RFC3339 UTC
    period_end             TEXT NOT NULL,    -- RFC3339 UTC
    recorded_at            TEXT NOT NULL,
    UNIQUE (room_id, period_start)            -- idempotency key for the meter
);

CREATE INDEX IF NOT EXISTS ix_meet_usage_account ON meet_usage(account_id, recorded_at);
CREATE INDEX IF NOT EXISTS ix_meet_usage_room    ON meet_usage(room_id, recorded_at);

-- meet_recordings (MEET-RECORDING-01): per-room composite-egress recording
-- artifacts written to object storage (or BYO object store). One row per recording
-- attempt. object_path is the canonical destination key under the shared
-- bucket. deleted_at (MEET-RECORDING-DELETE-01) is set when the cloud
-- performs the actual S3 DeleteObject on retention expiry; status
-- gains a terminal 'deleted' value (CHECK is comment-level, not a constraint).
CREATE TABLE IF NOT EXISTS meet_recordings (
    id              TEXT PRIMARY KEY,         -- ULID-shaped recording id
    egress_id       TEXT,                     -- LiveKit egress id (filled on start)
    room_id         TEXT NOT NULL,
    tenant_id       TEXT NOT NULL,
    account_id      TEXT NOT NULL,
    started_at      TEXT NOT NULL,            -- RFC3339 UTC
    stopped_at      TEXT,                     -- RFC3339 UTC; NULL while active
    object_path     TEXT NOT NULL,
    size_bytes      INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL,            -- starting|active|stopped|failed
    error           TEXT
, deleted_at TEXT);

CREATE INDEX IF NOT EXISTS ix_meet_recordings_tenant  ON meet_recordings(tenant_id, started_at);
CREATE INDEX IF NOT EXISTS ix_meet_recordings_account ON meet_recordings(account_id, started_at);
CREATE INDEX IF NOT EXISTS ix_meet_recordings_room    ON meet_recordings(room_id, started_at);

-- Partial unique index: at most one "in-flight" (starting OR active) recording
-- per room. A second StartRecording while one is live SHOULD return the
-- existing row idempotently; this constraint is the storage-level seatbelt
-- against the race.
CREATE UNIQUE INDEX IF NOT EXISTS ux_meet_recordings_active
    ON meet_recordings(room_id)
    WHERE status IN ('starting', 'active');

-- meet_breakouts (FIX-BREAKOUTS-BACKEND-01) is one row per breakout child
-- room. The parent (main) room is referenced by its qualified room_id
-- (<tenant>:<roomName>) — same byte sequence meet_rooms.room_id uses.
CREATE TABLE IF NOT EXISTS meet_breakouts (
    id              TEXT PRIMARY KEY,         -- breakout id (returned to clients)
    room_id         TEXT NOT NULL,            -- the breakout's own qualified room_id
    parent_room_id  TEXT NOT NULL,            -- the main room this breakout is a child of
    tenant_id       TEXT NOT NULL,
    name            TEXT NOT NULL,            -- human-friendly label ("Group A")
    created_at      TEXT NOT NULL,            -- RFC3339 UTC
    ended_at        TEXT                      -- RFC3339 UTC; NULL while live
);

CREATE INDEX IF NOT EXISTS ix_meet_breakouts_parent ON meet_breakouts(parent_room_id, created_at);
CREATE INDEX IF NOT EXISTS ix_meet_breakouts_tenant ON meet_breakouts(tenant_id, created_at);

-- meet_breakout_assignments tracks which user is currently in which
-- breakout. A user can be in at most one breakout per parent room — the
-- UNIQUE (parent_room_id, user_id) WHERE recalled_at IS NULL partial index
-- enforces that at the storage layer.
CREATE TABLE IF NOT EXISTS meet_breakout_assignments (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    breakout_id     TEXT NOT NULL,
    parent_room_id  TEXT NOT NULL,
    room_id         TEXT NOT NULL,            -- denorm of meet_breakouts.room_id (cheap lookup on assign-token mint)
    tenant_id       TEXT NOT NULL,
    user_id         TEXT NOT NULL,
    joined_at       TEXT NOT NULL,            -- RFC3339 UTC
    recalled_at     TEXT                      -- RFC3339 UTC; NULL while live
);

CREATE INDEX IF NOT EXISTS ix_meet_breakout_assignments_breakout ON meet_breakout_assignments(breakout_id, joined_at);
CREATE INDEX IF NOT EXISTS ix_meet_breakout_assignments_user     ON meet_breakout_assignments(user_id, joined_at);
CREATE INDEX IF NOT EXISTS ix_meet_breakout_assignments_parent   ON meet_breakout_assignments(parent_room_id, joined_at);

CREATE UNIQUE INDEX IF NOT EXISTS ux_meet_breakout_assignments_active
    ON meet_breakout_assignments(parent_room_id, user_id)
    WHERE recalled_at IS NULL;

-- meet_admissions (MEET-WAITING-TOKEN-01): the host-issued admit ledger. One
-- row per (room, user) the host has admitted from the lobby. It is the
-- ANTI-SELF-UPGRADE proof: the token-upgrade endpoint refuses to re-mint a
-- full-media token unless a matching admit row exists, and ONLY a RoomAdmin
-- (the room's created_by) can write one.
CREATE TABLE IF NOT EXISTS meet_admissions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    room_id       TEXT NOT NULL,           -- qualified <tenant>:<roomName>
    tenant_id     TEXT NOT NULL,
    user_id       TEXT NOT NULL,           -- the admitted joiner's identity
    admitted_by   TEXT NOT NULL,           -- the host (room's created_by) that admitted
    admitted_at   TEXT NOT NULL,           -- RFC3339 UTC
    revoked_at    TEXT                     -- RFC3339 UTC; NULL while the admission is live
);

CREATE INDEX IF NOT EXISTS ix_meet_admissions_room ON meet_admissions(room_id, admitted_at);

-- At most one LIVE admission per (room, user) — a re-admit of the same user is
-- idempotent, and the upgrade check is an existence lookup on this key.
CREATE UNIQUE INDEX IF NOT EXISTS ux_meet_admissions_active
    ON meet_admissions(room_id, user_id)
    WHERE revoked_at IS NULL;
