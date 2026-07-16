-- POPIA / GDPR data-subject request intake.
-- One row per recorded data-rights request; fulfilment is operator-driven so
-- there is no automated state machine here — status is "received" on intake.
CREATE TABLE IF NOT EXISTS compliance_requests (
  id         TEXT PRIMARY KEY,  -- "dsr_<hex>" request reference shown to the user
  account_id TEXT NOT NULL,     -- cross-ref auth.users.id (derived from session)
  kind       TEXT NOT NULL,     -- "export" | "erasure"
  status     TEXT NOT NULL,     -- "received"
  note       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_compliance_requests_account
  ON compliance_requests (account_id, created_at DESC);
