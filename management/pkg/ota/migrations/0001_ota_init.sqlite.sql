CREATE TABLE IF NOT EXISTS ota_releases (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  version         TEXT NOT NULL,            -- semver
  channel         TEXT NOT NULL,            -- 'stable' | 'beta' | 'pinned'
  artifact_url    TEXT NOT NULL,
  sha256          TEXT NOT NULL,
  min_from        TEXT NOT NULL,            -- semver
  security        INTEGER NOT NULL DEFAULT 0,
  rollout_pct     INTEGER NOT NULL DEFAULT 100,
  signature_b64   TEXT NOT NULL,            -- ed25519(manifest_json)
  defer_max_sec   INTEGER NOT NULL DEFAULT 604800, -- 7d for security forcing
  created_at      TEXT NOT NULL,
  halted          INTEGER NOT NULL DEFAULT 0,
  UNIQUE(version, channel)
);

CREATE TABLE IF NOT EXISTS ota_device_policy (
  ulid            TEXT PRIMARY KEY,          -- cross-ref routing.bindings.ulid
  channel         TEXT NOT NULL DEFAULT 'stable',
  pin_version     TEXT,                      -- nullable
  defer_until     TEXT,                      -- RFC3339 nullable
  opt_out_features INTEGER NOT NULL DEFAULT 0,
  updated_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ota_device_reports (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  ulid            TEXT NOT NULL,
  version         TEXT NOT NULL,
  result          TEXT NOT NULL,             -- 'applied' | 'failed' | 'rolled-back'
  received_at     TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS ix_ota_reports_ulid ON ota_device_reports(ulid);
