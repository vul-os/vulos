-- secrets: named secret registry with at-rest AES-256-GCM encryption.
CREATE TABLE IF NOT EXISTS secrets (
  name          TEXT    NOT NULL PRIMARY KEY,
  kind          TEXT    NOT NULL DEFAULT 'symmetric',
  current_enc   BLOB    NOT NULL DEFAULT '',  -- encrypted current value
  previous_enc  BLOB,                         -- encrypted previous value (may be null)
  interval_ns   INTEGER NOT NULL DEFAULT 0,   -- rotation interval in nanoseconds
  rotated_at    TEXT    NOT NULL DEFAULT ''   -- RFC3339Nano UTC; empty = never
);
