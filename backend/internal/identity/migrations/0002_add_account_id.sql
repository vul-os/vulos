-- IDENTITY-SEC-02: add account_id, archived, deleted to vumail_mailbox.
-- ALTER TABLE ... ADD COLUMN errors (duplicate column) are silently ignored by
-- the Go migration runner so this file is safe to apply on an already-migrated
-- database.

ALTER TABLE vumail_mailbox ADD COLUMN account_id TEXT;
ALTER TABLE vumail_mailbox ADD COLUMN archived   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vumail_mailbox ADD COLUMN deleted    INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_mailbox_account_id ON vumail_mailbox(account_id);
