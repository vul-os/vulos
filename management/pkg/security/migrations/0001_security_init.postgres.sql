-- security package schema v1 (Postgres)
CREATE TABLE IF NOT EXISTS security_waf_events (
    id         BIGSERIAL PRIMARY KEY,
    ts         TEXT    NOT NULL,
    rule_id    TEXT    NOT NULL,
    pattern    TEXT    NOT NULL DEFAULT '',
    client_ip  TEXT    NOT NULL DEFAULT '',
    path       TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS security_bot_events (
    id        BIGSERIAL PRIMARY KEY,
    ts        TEXT    NOT NULL,
    client_ip TEXT    NOT NULL DEFAULT '',
    score     REAL    NOT NULL DEFAULT 0,
    reason    TEXT    NOT NULL DEFAULT '',
    ua        TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS security_stepup_events (
    id         BIGSERIAL PRIMARY KEY,
    ts         TEXT    NOT NULL,
    user_id    TEXT    NOT NULL DEFAULT '',
    client_ip  TEXT    NOT NULL DEFAULT '',
    risk_score REAL    NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS security_ato_events (
    id        BIGSERIAL PRIMARY KEY,
    ts        TEXT    NOT NULL,
    user_id   TEXT    NOT NULL DEFAULT '',
    action    TEXT    NOT NULL DEFAULT '',
    client_ip TEXT    NOT NULL DEFAULT '',
    dismissed INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS security_ct_certs (
    id          BIGSERIAL PRIMARY KEY,
    ts          TEXT    NOT NULL,
    common_name TEXT    NOT NULL,
    issuer      TEXT    NOT NULL DEFAULT '',
    not_before  TEXT    NOT NULL DEFAULT '',
    not_after   TEXT    NOT NULL DEFAULT '',
    recognized  INTEGER NOT NULL DEFAULT 0,
    UNIQUE(common_name, not_before)
);

CREATE TABLE IF NOT EXISTS security_honeypot_hits (
    id        BIGSERIAL PRIMARY KEY,
    ts        TEXT    NOT NULL,
    email     TEXT    NOT NULL DEFAULT '',
    client_ip TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS security_honeypot_accounts (
    email      TEXT    PRIMARY KEY,
    created_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS security_egress_samples (
    id      BIGSERIAL PRIMARY KEY,
    ts      TEXT    NOT NULL,
    user_id TEXT    NOT NULL,
    bytes   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_egress_samples_user ON security_egress_samples(user_id,ts);

CREATE TABLE IF NOT EXISTS security_egress_anomalies (
    id       BIGSERIAL PRIMARY KEY,
    ts       TEXT    NOT NULL,
    user_id  TEXT    NOT NULL,
    bytes_hr INTEGER NOT NULL DEFAULT 0,
    baseline REAL    NOT NULL DEFAULT 0,
    sigma    REAL    NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS security_ip_blocks (
    ip         TEXT    PRIMARY KEY,
    reason     TEXT    NOT NULL DEFAULT '',
    expires_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS security_sensitive_actions (
    id        BIGSERIAL PRIMARY KEY,
    ts        TEXT    NOT NULL,
    user_id   TEXT    NOT NULL,
    action    TEXT    NOT NULL,
    client_ip TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sensitive_actions_user ON security_sensitive_actions(user_id,ts);
