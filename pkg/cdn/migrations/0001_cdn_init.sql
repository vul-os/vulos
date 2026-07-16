-- 0001_cdn_init.sql
-- BYO-CDN configuration + CDN IP-range cache for vulos.cloud WEBAPP-05.
-- Idempotent: safe to run against an existing DB.

-- Per-account BYO CDN configuration.
CREATE TABLE IF NOT EXISTS cdn_configs (
  account_id   TEXT NOT NULL PRIMARY KEY,
  provider     TEXT NOT NULL,              -- 'cloudflare'|'fastly'|'bunny'
  origin_host  TEXT NOT NULL,              -- origin-{cust}.vulos.cloud
  mtls_enabled INTEGER NOT NULL DEFAULT 1, -- 1 = authenticated-origin-pulls on
  host_header  TEXT NOT NULL DEFAULT '',   -- Host header value to enforce
  enabled      INTEGER NOT NULL DEFAULT 1, -- 0 = BYO CDN disabled
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);

-- Cached CDN IP ranges (one row per CIDR, keyed by provider).
CREATE TABLE IF NOT EXISTS cdn_ip_ranges (
  provider     TEXT NOT NULL,
  cidr         TEXT NOT NULL,
  fetched_at   TEXT NOT NULL,
  PRIMARY KEY (provider, cidr)
);

CREATE INDEX IF NOT EXISTS ix_cdn_ip_ranges_provider ON cdn_ip_ranges(provider);
