-- 0001_edge_init.sqlite.sql
-- WEBAPP-04: Vulos edge cache — control-plane configuration tables.
-- Idempotent: safe to run against an existing DB (CREATE TABLE IF NOT EXISTS).

-- edge_nodes stores the set of configured edge PoPs (pull-through cache nodes).
-- Each node has a region, an origin-pull URL, and drain state.
CREATE TABLE IF NOT EXISTS edge_nodes (
  id         TEXT PRIMARY KEY,          -- opaque ID, e.g. "iad1", "fra1", "jnb1"
  region     TEXT NOT NULL,             -- human label, e.g. "us-east", "eu-west"
  origin_url TEXT NOT NULL,             -- base URL used by edge to pull from origin
  drained    INTEGER NOT NULL DEFAULT 0,-- 1 = no new traffic routed to this node
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- edge_domain_configs stores per-domain cache + rate-limit settings.
-- Each row is keyed by the customer's ULID (instance) + public domain.
CREATE TABLE IF NOT EXISTS edge_domain_configs (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  ulid           TEXT NOT NULL,   -- customer instance ULID
  domain         TEXT NOT NULL,   -- e.g. "myapp.example.com"
  cache_ttl_sec  INTEGER NOT NULL DEFAULT 300,   -- default 5 min TTL
  rate_limit_rps INTEGER NOT NULL DEFAULT 100,   -- req/s before 429 (0 = disabled)
  enabled        INTEGER NOT NULL DEFAULT 1,     -- 0 = edge bypass (BYO CDN mode)
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  UNIQUE(ulid, domain)
);

CREATE INDEX IF NOT EXISTS ix_edge_dom_ulid ON edge_domain_configs(ulid);

-- edge_bandwidth_events is an append-only metered record of bytes proxied
-- through the edge per domain per period.  Used for wallet overage billing.
CREATE TABLE IF NOT EXISTS edge_bandwidth_events (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id   TEXT NOT NULL,
  ulid         TEXT NOT NULL,    -- customer instance ULID
  domain       TEXT NOT NULL,
  bytes_in     INTEGER NOT NULL DEFAULT 0,
  bytes_out    INTEGER NOT NULL DEFAULT 0,
  period_start TEXT NOT NULL,    -- RFC3339
  period_end   TEXT NOT NULL,    -- RFC3339
  recorded_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_edge_bw_acct    ON edge_bandwidth_events(account_id, recorded_at DESC);
CREATE INDEX IF NOT EXISTS ix_edge_bw_ulid    ON edge_bandwidth_events(ulid, recorded_at DESC);
