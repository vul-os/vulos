-- accountsecurity package schema v1 (SQLite, single box owner).
--
-- acctsec_sensitive_actions is the raw log every sensitive action hook writes
-- to (append-only, used as the anomaly-detection window).
--
-- acctsec_alerts is the small subset of those actions that tripped an anomaly
-- rule — what the owner actually sees and can act on in Settings -> Security.

CREATE TABLE IF NOT EXISTS acctsec_sensitive_actions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         TEXT    NOT NULL,
    user_id    TEXT    NOT NULL,
    action     TEXT    NOT NULL,
    client_ip  TEXT    NOT NULL DEFAULT '',
    user_agent TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_acctsec_sensitive_actions_user
    ON acctsec_sensitive_actions(user_id, ts);

CREATE TABLE IF NOT EXISTS acctsec_alerts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ts           TEXT    NOT NULL,
    user_id      TEXT    NOT NULL,
    action       TEXT    NOT NULL,
    client_ip    TEXT    NOT NULL DEFAULT '',
    reason       TEXT    NOT NULL DEFAULT '',
    notif_id     TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL DEFAULT 'pending', -- pending | dismissed | locked
    resolved_at  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_acctsec_alerts_user_status
    ON acctsec_alerts(user_id, status, ts);
