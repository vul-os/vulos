-- Migration: 0001_enroll_init
-- Creates the enroll_grants table for the RFC 8628 Device Authorization Grant
-- enrollment flow (see ../../BOOT_API.md).

CREATE TABLE IF NOT EXISTS enroll_grants (
    -- device_code is the high-entropy secret known only to the device.
    -- It is the bearer token for POST /enroll/poll.
    device_code   TEXT        NOT NULL PRIMARY KEY,

    -- user_code is the short human-typable code (e.g. "WXYZ-1234") displayed
    -- to the user for approval at https://vulos.org/activate.
    user_code     TEXT        NOT NULL UNIQUE,

    -- state is one of: pending, approved, denied, consumed.
    state         TEXT        NOT NULL DEFAULT 'pending',

    -- account_id is set when a logged-in user approves the grant.
    -- NULL until approved.
    account_id    TEXT,

    -- device_pubkey is the raw ed25519 public key (32 bytes) sent by the device
    -- in POST /enroll/start. Stored as a BLOB.
    device_pubkey BLOB        NOT NULL,

    -- device_cert is the signed management-scope cert returned in EnrollResult.
    -- NULL until approved and signed.
    device_cert   BLOB,

    -- ulid is the Crockford base32 ULID assigned to the device on approval.
    -- NULL until approved.
    ulid          TEXT,

    -- expires_at is the absolute UTC timestamp after which the grant is invalid.
    expires_at    DATETIME    NOT NULL,

    -- approved_at is set when the grant is approved by the user.
    -- NULL until approved.
    approved_at   DATETIME,

    -- created_at is set at insert time.
    created_at    DATETIME    NOT NULL DEFAULT (STRFTIME('%Y-%m-%dT%H:%M:%fZ', 'NOW'))
);

-- Index for the approve/deny path (user_code lookup) — already covered by the
-- UNIQUE constraint above; the explicit index is a belt-and-suspenders hint for
-- the query planner on the SELECT ... WHERE user_code = ? path.
CREATE INDEX IF NOT EXISTS idx_enroll_grants_user_code ON enroll_grants (user_code);

-- Index for expiry sweeps.
CREATE INDEX IF NOT EXISTS idx_enroll_grants_expires_at ON enroll_grants (expires_at);
