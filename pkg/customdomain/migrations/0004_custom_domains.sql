-- 0004_custom_domains.sql
-- Custom-domain plane for paid-tier mail/os/office (OPS-AUTOSCALER-CUSTOMDOMAIN-01).
-- One row per domain; one tenant may own multiple domains.
-- Idempotent: CREATE TABLE IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS custom_domains (
  domain         TEXT PRIMARY KEY,
  tenant_id      TEXT NOT NULL,
  bundle_node    TEXT NOT NULL DEFAULT '',
  verify_token   TEXT NOT NULL,
  verify_state   TEXT NOT NULL DEFAULT 'pending',
  acme_status    TEXT NOT NULL DEFAULT '',
  -- intended_cname is the DNS validation target the customer must point their
  -- domain at (the Fly app *.fly.dev hostname or an ACME challenge target),
  -- surfaced from the Fly addCertificate response so the UI can show it.
  intended_cname TEXT NOT NULL DEFAULT '',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  attached_at    TEXT,
  verified_at    TEXT,
  last_error     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS ix_custom_domains_tenant ON custom_domains(tenant_id);
CREATE INDEX IF NOT EXISTS ix_custom_domains_state ON custom_domains(verify_state);
