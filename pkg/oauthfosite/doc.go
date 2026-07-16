// Package oauthfosite is the STEP-1 parallel foundation for migrating Vulos
// Cloud's hand-rolled OAuth2/OIDC provider (internal/oauthprovider) onto the
// ory/fosite protocol engine.
//
// SCOPE (deliberately narrow): this package builds a fosite OAuth2Provider and
// a cpdb-backed storage adapter, configured for exactly the flows that "Sign in
// with Vulos" uses — authorization-code + PKCE (S256 enforced for public
// clients) + refresh (with rotation & reuse-detection family revocation) +
// OpenID Connect id_token (RS256). It is UNWIRED: no live /authorize or /token
// handler routes through it. internal/oauthprovider remains the production path.
//
// SINGLE SOURCE OF TRUTH for storage:
//   - Registered clients are read from the EXISTING audited oauth_clients table
//     via internal/oauthprovider (exact-redirect allow-list preserved).
//   - The RS256 id_token signing key is the EXISTING KEK-wrapped key loaded via
//     internal/oauthprovider (LoadOrCreateSigningKey) — never a second key.
//   - fosite's own session artefacts (authorize codes, PKCE requests, OIDC
//     sessions, access & refresh tokens) live in NEW dedicated cpdb tables
//     (oauthfosite_*) keyed by fosite's token SIGNATURE. A signature is a keyed
//     HMAC of the token's random half; the plaintext token can never be derived
//     from it, so the "hashed-at-rest" invariant is preserved. Auth codes are
//     single-use (invalidated in place), and refresh rotation marks the rotated
//     token inactive rather than deleting it so replay is detected and the whole
//     token family is revoked (RFC 6819 §5.2.2.3 / RFC 9700).
package oauthfosite

import (
	// Anchor the ory/fosite dependency so `go mod tidy` retains it while the
	// rest of the foundation is assembled in subsequent commits.
	_ "github.com/ory/fosite"
)
