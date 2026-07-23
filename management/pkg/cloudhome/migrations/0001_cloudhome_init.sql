-- 0001_cloudhome_init.sql
-- Contract 1 — cloud-home VulaID per account (account-only document sharing).
--
-- One STANDARD self-certifying Ed25519 VulaID per account, hosted by the cell
-- on the account's behalf ("the cloud is the account-only user's box"). The
-- Ed25519 PRIVATE KEY is stored ONLY as AES-256-GCM ciphertext under the
-- cloud-home KEK (enc_priv_key); there is never plaintext key material at rest.
-- The cell now custodies many accounts' peering private keys — high-value store.
--
-- Folded initial migration — NET final schema. This chain formerly ran:
--   0001  create cloudhome_identities (+ unique index)
--   0002  ADD COLUMN kek_version (KEK-rotation version, sealed-under tag)
--   0003  create cloudhome_prekeys + cloudhome_one_time_prekeys + cloudhome_revocations
--   0004  DROP cloudhome_one_time_prekeys + cloudhome_prekeys (CONTENT-BLIND:
--         the cell must hold no content-decrypting key material)
-- The two prekey tables were created-then-dropped, so they are OMITTED here
-- entirely. kek_version is folded into cloudhome_identities in the exact append
-- form the prior SQLite ALTER produced, so a fresh :memory: schema dump is
-- byte-identical to running the full 0001..0004 chain. NO ALTER/DROP remain.

CREATE TABLE IF NOT EXISTS cloudhome_identities (
    account_id     TEXT PRIMARY KEY,              -- owning Vulos account (ULID)
    vula_id        TEXT NOT NULL UNIQUE,          -- "vula:ed25519:<base64(pubkey)>"
    public_key_b64 TEXT NOT NULL,                 -- standard-base64 Ed25519 public key (32 bytes)
    enc_priv_key   TEXT NOT NULL,                 -- base64(AES-256-GCM nonce||ct||tag) under CLOUDHOME_KEK
    server         TEXT NOT NULL,                 -- cell server base URL serving this account's peering intake
    created_at     TEXT NOT NULL                  -- RFC3339 UTC
, kek_version INTEGER NOT NULL DEFAULT 1);

CREATE UNIQUE INDEX IF NOT EXISTS ux_cloudhome_vula_id ON cloudhome_identities (vula_id);

-- Account-driven REVOCATION: the verbatim self-signed RevocationCert (base58
-- VulaIDs inside) published so any peer rejects the revoked cloud-home VulaID.
-- The identity key (signing/vouching only) and this revocation record are KEPT:
-- neither can decrypt content, so the content-blind guarantee holds.
CREATE TABLE IF NOT EXISTS cloudhome_revocations (
    vula_id    TEXT PRIMARY KEY,                    -- revoked cloud-home VulaID
    account_id TEXT NOT NULL,                       -- owning account
    cert_json  TEXT NOT NULL,                       -- verbatim RevocationCert JSON
    revoked_at TEXT NOT NULL                        -- RFC3339 UTC
);
