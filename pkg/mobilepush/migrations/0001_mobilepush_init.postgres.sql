-- mobilepush schema: device token registry.

CREATE TABLE IF NOT EXISTS device_tokens (
    id          BIGSERIAL PRIMARY KEY,
    account_id  TEXT    NOT NULL,
    token       TEXT    NOT NULL,
    platform    TEXT    NOT NULL CHECK(platform IN ('apns', 'fcm')),
    created_at  TEXT    NOT NULL,   -- RFC3339 UTC
    updated_at  TEXT    NOT NULL,   -- RFC3339 UTC

    UNIQUE(account_id, token, platform)
);

CREATE INDEX IF NOT EXISTS idx_device_tokens_account
    ON device_tokens (account_id);
