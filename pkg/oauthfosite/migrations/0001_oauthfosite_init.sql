-- 0001_oauthfosite_init.sql — storage tables for the ory/fosite protocol engine
-- (STEP-1 parallel foundation, internal/oauthfosite).
--
-- These tables hold ONLY fosite's own session artefacts. They are separate from
-- the hand-rolled internal/oauthprovider tables (oauth_authcodes / oauth_tokens)
-- so the two providers never share mutable rows while the fosite path is unwired.
-- Registered clients and the RS256 signing key remain the single source of truth
-- in the oauthprovider tables and are NOT duplicated here.
--
-- Security invariants preserved from the audited hand-rolled provider:
--   - Every row is keyed by fosite's token SIGNATURE, a keyed HMAC of the token's
--     random half. The plaintext token cannot be derived from the signature, so
--     tokens/codes are effectively HASHED-AT-REST.
--   - Authorization codes are SINGLE-USE: `active` is flipped to 0 on use
--     (InvalidateAuthorizeCodeSession) and a replayed code returns the
--     invalidated-code error rather than minting a second token set.
--   - Refresh tokens are ROTATED, not silently dropped: a rotated/revoked refresh
--     token keeps its row with `active=0` so a replay is DETECTED and the whole
--     token family (all rows sharing request_id) is revoked (RFC 6819 §5.2.2.3 /
--     RFC 9700). request_id is the family key — every rotation inherits it.
--   - `requested` is the serialized fosite.Requester (client id, scopes, form and
--     the OIDC session incl. subject/nonce). It never contains a plaintext token.

-- Authorization codes (keyed by authorize-code signature; single-use).
CREATE TABLE IF NOT EXISTS oauthfosite_authcodes (
    signature   TEXT PRIMARY KEY,               -- fosite authorize-code signature
    request_id  TEXT NOT NULL,                  -- fosite request id (family key)
    client_id   TEXT NOT NULL,
    active      INTEGER NOT NULL DEFAULT 1,     -- 0 once the code has been used
    requested   TEXT NOT NULL,                  -- serialized fosite.Requester (JSON)
    created_at  BIGINT NOT NULL                 -- unix seconds
);
CREATE INDEX IF NOT EXISTS idx_oauthfosite_authcodes_reqid ON oauthfosite_authcodes(request_id);

-- PKCE request sessions (keyed by authorize-code signature).
CREATE TABLE IF NOT EXISTS oauthfosite_pkce (
    signature   TEXT PRIMARY KEY,
    request_id  TEXT NOT NULL,
    client_id   TEXT NOT NULL,
    requested   TEXT NOT NULL,
    created_at  BIGINT NOT NULL
);

-- OpenID Connect sessions (keyed by authorize-code signature).
CREATE TABLE IF NOT EXISTS oauthfosite_oidc (
    signature   TEXT PRIMARY KEY,
    request_id  TEXT NOT NULL,
    client_id   TEXT NOT NULL,
    requested   TEXT NOT NULL,
    created_at  BIGINT NOT NULL
);

-- Access tokens (keyed by access-token signature).
CREATE TABLE IF NOT EXISTS oauthfosite_access_tokens (
    signature   TEXT PRIMARY KEY,
    request_id  TEXT NOT NULL,                  -- family key; RevokeAccessToken deletes by this
    client_id   TEXT NOT NULL,
    requested   TEXT NOT NULL,
    created_at  BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oauthfosite_access_reqid ON oauthfosite_access_tokens(request_id);

-- Refresh tokens (keyed by refresh-token signature; rotated in place).
CREATE TABLE IF NOT EXISTS oauthfosite_refresh_tokens (
    signature        TEXT PRIMARY KEY,
    request_id       TEXT NOT NULL,             -- family key; RevokeRefreshToken deactivates by this
    access_signature TEXT NOT NULL DEFAULT '',  -- access token minted alongside this refresh token
    client_id        TEXT NOT NULL,
    active           INTEGER NOT NULL DEFAULT 1,-- 0 once rotated/revoked (kept for replay detection)
    requested        TEXT NOT NULL,
    created_at       BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oauthfosite_refresh_reqid ON oauthfosite_refresh_tokens(request_id);
