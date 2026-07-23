-- Data-residency per-org region setting.
-- org_id is the same as account_id (auth.users.id) — the org owner's account.
CREATE TABLE IF NOT EXISTS org_residency (
  org_id     TEXT PRIMARY KEY,  -- cross-ref auth.users.id
  region     TEXT NOT NULL,     -- e.g. "af-south-1", "eu-west-1", "us-east-1"
  updated_at TEXT NOT NULL
);
