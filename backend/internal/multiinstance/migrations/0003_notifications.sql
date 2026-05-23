-- MINST-06: seen_notifications — dedup table for fan-out notifications.
-- All objects use IF NOT EXISTS so migration is idempotent.

CREATE TABLE IF NOT EXISTS seen_notifications (
    notification_id TEXT PRIMARY KEY,
    seen_at         TEXT NOT NULL DEFAULT '',
    expires_at      TEXT NOT NULL DEFAULT ''  -- 7-day TTL from seen_at
);

CREATE INDEX IF NOT EXISTS idx_seen_notif_expires ON seen_notifications(expires_at);
