-- secrets: named secret registry with at-rest AES-256-GCM encryption.
CREATE TABLE IF NOT EXISTS secrets (
  name          TEXT    NOT NULL PRIMARY KEY,
  kind          TEXT    NOT NULL DEFAULT 'symmetric',
  current_enc   BYTEA   NOT NULL DEFAULT ''::bytea,  -- encrypted current value
  previous_enc  BYTEA,                        -- encrypted previous value (may be null)
  interval_ns   BIGINT  NOT NULL DEFAULT 0,   -- rotation interval in nanoseconds
  rotated_at    TEXT    NOT NULL DEFAULT ''   -- RFC3339Nano UTC; empty = never
);
