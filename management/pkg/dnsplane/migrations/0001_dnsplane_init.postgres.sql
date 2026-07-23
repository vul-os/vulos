-- 0001_dnsplane_init.postgres.sql
-- Zone-record cache for the vulos.cloud dnsplane subsystem.
-- Idempotent: safe to run against an existing DB (CREATE TABLE IF NOT EXISTS).

CREATE TABLE IF NOT EXISTS dnsplane_records (
  id            BIGSERIAL PRIMARY KEY,
  fqdn          TEXT NOT NULL,                 -- e.g. *.01HX….vulos.org
  type          TEXT NOT NULL,                 -- 'A'|'CNAME'|'TXT'
  value         TEXT NOT NULL,
  ttl           BIGINT NOT NULL DEFAULT 60,
  provider_id   TEXT,                          -- Cloudflare record id; nullable until applied
  ulid          TEXT,                          -- back-ref; nullable for non-ULID records
  applied_at    TEXT,                          -- RFC3339 UTC; nullable until applied
  state         TEXT NOT NULL DEFAULT 'desired', -- 'desired'|'applied'|'failed'
  last_error    TEXT,
  updated_at    TEXT NOT NULL,                 -- RFC3339 UTC
  UNIQUE(fqdn, type, value)
);

CREATE INDEX IF NOT EXISTS ix_dns_ulid ON dnsplane_records(ulid);
CREATE INDEX IF NOT EXISTS ix_dns_state ON dnsplane_records(state);
