-- 0001_streamsession_init.sql
-- Streaming session orchestration tables for vulos.cloud (STREAM-SIGNAL-01).
--
-- This single initial migration declares the final schema directly in the
-- CREATE TABLE / CREATE INDEX statements (no ALTER); it supersedes the former
-- 0002_gaming.sql (gaming column + ix_stream_sessions_gaming), now folded in.
--
-- A session is one client→host streaming negotiation. The cloud (this package)
-- is ONLY the SDP/ICE signaling broker — media never touches the control
-- plane. Media flows P2P over WebRTC between the client and the host; if P2P
-- fails the relay opens a TURN slot (vulos-relay's streamsignal.go) and media
-- flows host↔relay↔client there. The CP only sees session state + opaque
-- signaling payload bytes that it FORWARDS to the relay.
--
-- Correlation: when host_kind = 'cloud_gpu' the host_ulid matches a row in
-- gpusku.gpu_attachments and the per-GPU-hour meter is active for the
-- session's lifetime. When host_kind = 'byo_gpu' the host_ulid identifies a
-- registered BYO GPU box (no cloud GPU charge — the box is on the user's
-- hardware).
--
-- GAME-SESSION-01: a gaming session is an ordinary stream_sessions row with
-- gaming=1. It is subject to two extra service-layer policies: one ACTIVE
-- gaming session per account, and the per-tier concurrent GPU-session cap
-- (gpusku.SessionCapForTier). Billing is unchanged — a gaming session bills
-- exactly like any other cloud_gpu session, once, through the watermark.

CREATE TABLE IF NOT EXISTS stream_sessions (
    id              TEXT PRIMARY KEY,                  -- opaque session id (random)
    account_id      TEXT NOT NULL,
    host_kind       TEXT NOT NULL,                     -- 'cloud_gpu' | 'byo_gpu'
    host_ulid       TEXT NOT NULL,                     -- GPU attach ulid (cloud) or BYO box ulid
    gpu_type        TEXT NOT NULL DEFAULT '',          -- catalogue key (cloud_gpu only; '' for byo)
    relay_endpoint  TEXT NOT NULL,                     -- https URL of the configured relay's signaling ingress
    state           TEXT NOT NULL,                     -- 'allocating' | 'active' | 'torn_down' | 'failed'
    created_at      TEXT NOT NULL,                     -- RFC3339 UTC
    updated_at      TEXT NOT NULL,
    closed_at       TEXT
, gaming INTEGER NOT NULL DEFAULT 0);

CREATE INDEX IF NOT EXISTS ix_stream_sessions_acct ON stream_sessions(account_id, created_at);
CREATE INDEX IF NOT EXISTS ix_stream_sessions_ulid ON stream_sessions(host_ulid);
CREATE INDEX IF NOT EXISTS ix_stream_sessions_state ON stream_sessions(state);
CREATE INDEX IF NOT EXISTS ix_stream_sessions_gaming
    ON stream_sessions(account_id, gaming, state);
