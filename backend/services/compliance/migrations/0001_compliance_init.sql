-- POPIA / GDPR data-subject request intake — clean baseline.
-- One row per recorded data-rights request; fulfilment is operator-driven (the
-- box owner, manually) so there is no automated state machine here — status is
-- "received" on intake and stays that way. All DDL is idempotent.
CREATE TABLE IF NOT EXISTS compliance_requests (
  id         TEXT PRIMARY KEY,  -- "dsr_<hex>" request reference shown to the user
  account_id TEXT NOT NULL,     -- the requesting user's X-User-ID (session-derived)
  kind       TEXT NOT NULL,     -- "export" | "erasure"
  status     TEXT NOT NULL,     -- "received"
  note       TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL   -- unix seconds, UTC
);

CREATE INDEX IF NOT EXISTS idx_compliance_requests_account
  ON compliance_requests (account_id, created_at DESC);
