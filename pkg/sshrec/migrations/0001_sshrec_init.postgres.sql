-- 0001_sshrec_init.postgres.sql
-- SSH-recovery CA broker tables for the vulos.cloud control-plane.
-- Postgres variant. Idempotent: safe to run against an existing DB.

CREATE TABLE IF NOT EXISTS sshrec_arming (
    ulid           TEXT NOT NULL PRIMARY KEY,
    armed_until    TEXT NOT NULL,
    armed_by       TEXT NOT NULL,
    reason         TEXT
);

CREATE TABLE IF NOT EXISTS sshrec_requests (
    id                 TEXT NOT NULL PRIMARY KEY,
    ulid               TEXT NOT NULL,
    account_id         TEXT NOT NULL,
    public_key_ssh     TEXT NOT NULL,
    state              TEXT NOT NULL,
    approvals_json     TEXT NOT NULL DEFAULT '[]',
    required_approvals BIGINT NOT NULL DEFAULT 0,
    source_ip          TEXT,
    created_at         TEXT NOT NULL,
    expires_at         TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_sshrec_requests_ulid ON sshrec_requests(ulid);
CREATE INDEX IF NOT EXISTS ix_sshrec_requests_account ON sshrec_requests(account_id);

CREATE TABLE IF NOT EXISTS sshrec_certs (
    key_id          TEXT NOT NULL PRIMARY KEY,
    request_id      TEXT NOT NULL,
    ulid            TEXT NOT NULL,
    serial          BIGINT NOT NULL,
    not_before      TEXT NOT NULL,
    not_after       TEXT NOT NULL,
    principal       TEXT NOT NULL DEFAULT 'recovery',
    source_restrict TEXT NOT NULL,
    revoked         BIGINT NOT NULL DEFAULT 0,
    revoked_at      TEXT,
    revoked_reason  TEXT
);

CREATE INDEX IF NOT EXISTS ix_sshrec_certs_ulid ON sshrec_certs(ulid);
CREATE INDEX IF NOT EXISTS ix_sshrec_certs_revoked ON sshrec_certs(revoked);

CREATE TABLE IF NOT EXISTS sshrec_audit (
    id          BIGSERIAL PRIMARY KEY,
    at          TEXT NOT NULL,
    ulid        TEXT NOT NULL,
    account_id  TEXT,
    action      TEXT NOT NULL,
    key_id      TEXT,
    detail_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS ix_sshrec_audit_ulid ON sshrec_audit(ulid, at);

CREATE TABLE IF NOT EXISTS sshrec_serial (
    id     BIGINT PRIMARY KEY CHECK (id = 1),
    serial BIGINT NOT NULL DEFAULT 0
);
INSERT INTO sshrec_serial (id, serial) VALUES (1, 0) ON CONFLICT DO NOTHING;
