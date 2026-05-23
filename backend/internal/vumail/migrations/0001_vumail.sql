-- VUMAIL-02: vumail identity, mailbox, and outbox schema.
-- All statements use IF NOT EXISTS so the migration is idempotent.

CREATE TABLE IF NOT EXISTS vumail_identity (
    id              TEXT PRIMARY KEY DEFAULT 'default',
    address         TEXT NOT NULL,           -- user@vumail.org
    public_key_b64  TEXT NOT NULL,           -- base64-encoded Ed25519 public key (32 bytes)
    private_key_enc TEXT NOT NULL,           -- base64-encoded encrypted Ed25519 private key
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS vumail_mailbox (
    id              TEXT PRIMARY KEY,        -- ULID
    from_address    TEXT NOT NULL,
    subject         TEXT NOT NULL,
    body_encrypted  BLOB NOT NULL,           -- XChaCha20-Poly1305 ciphertext
    received_at     TEXT NOT NULL,
    read            INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_mailbox_received_at ON vumail_mailbox(received_at DESC);
CREATE INDEX IF NOT EXISTS idx_mailbox_read        ON vumail_mailbox(read);

CREATE TABLE IF NOT EXISTS vumail_outbox (
    id              TEXT PRIMARY KEY,        -- ULID
    to_address      TEXT NOT NULL,
    subject         TEXT NOT NULL,
    body_encrypted  BLOB NOT NULL,           -- XChaCha20-Poly1305 ciphertext (sender copy)
    queued_at       TEXT NOT NULL,
    sent_at         TEXT,                    -- NULL until delivered
    status          TEXT NOT NULL DEFAULT 'queued'  -- queued | sent | failed
);

CREATE INDEX IF NOT EXISTS idx_outbox_status ON vumail_outbox(status);
