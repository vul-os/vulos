-- 0001_webapp_init.sql
-- WEBAPP-03: Custom domain publishing pipeline — control-plane tables.
-- Idempotent: safe to run against an existing DB (CREATE TABLE IF NOT EXISTS).

-- webapp_domains stores the domain-connection + NS delegation state for each
-- customer-published domain. One row per (account_id, ulid, domain) triple.
CREATE TABLE IF NOT EXISTS webapp_domains (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id      TEXT    NOT NULL,
  ulid            TEXT    NOT NULL,   -- customer instance ULID
  domain          TEXT    NOT NULL,   -- e.g. "myapp.example.com"
  -- NS delegation: has the customer delegated NS to ns1.vulos.cloud?
  ns_delegated    INTEGER NOT NULL DEFAULT 0,   -- 1 = delegation confirmed
  ns_verified_at  TEXT,                          -- RFC3339 when last confirmed
  -- TLS: has a cert been provisioned for this domain?
  tls_provisioned INTEGER NOT NULL DEFAULT 0,   -- 1 = cert exists
  tls_issued_at   TEXT,                          -- RFC3339
  -- Publishing toggle (runtime): manifest declares publishable; dashboard flips.
  published       INTEGER NOT NULL DEFAULT 0,   -- 1 = traffic flowing
  -- App kind: "native" or "oci"
  app_kind        TEXT    NOT NULL DEFAULT 'native', -- native|oci
  -- Edge config (cache + rate limit mirrors edge.DomainConfig defaults).
  cache_ttl_sec   INTEGER NOT NULL DEFAULT 300,
  rate_limit_rps  INTEGER NOT NULL DEFAULT 100,
  created_at      TEXT    NOT NULL,
  updated_at      TEXT    NOT NULL,
  UNIQUE(ulid, domain)
);

CREATE INDEX IF NOT EXISTS ix_webapp_acct   ON webapp_domains(account_id);
CREATE INDEX IF NOT EXISTS ix_webapp_ulid   ON webapp_domains(ulid);
CREATE INDEX IF NOT EXISTS ix_webapp_domain ON webapp_domains(domain);
