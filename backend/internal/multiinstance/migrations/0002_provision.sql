-- MINST-05: provision_requests — tracks Koyeb cloud instance provisioning jobs.
-- All objects use IF NOT EXISTS so migration is idempotent.

CREATE TABLE IF NOT EXISTS provision_requests (
    id          TEXT PRIMARY KEY,          -- request ULID (client-generated)
    region      TEXT NOT NULL DEFAULT '',  -- Koyeb region slug (e.g. "fra", "was")
    plan        TEXT NOT NULL DEFAULT '',  -- Koyeb instance type (e.g. "nano", "small")
    status      TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'provisioning', 'ready', 'failed')),
    instance_ulid TEXT NOT NULL DEFAULT '', -- filled once cloud returns the new ULID
    endpoint_url  TEXT NOT NULL DEFAULT '', -- filled once the instance is ready
    error_message TEXT NOT NULL DEFAULT '',
    requested_at  TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_provision_status ON provision_requests(status);
