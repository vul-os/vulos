package oauthfosite

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"time"

	jose "github.com/go-jose/go-jose/v3"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/openid"
	"github.com/ory/fosite/token/jwt"
)

// Default token lifespans for the foundation. These mirror sensible "Sign in
// with Vulos" defaults and can be tuned at cutover; they are not a live policy
// change since no endpoint routes through this provider yet.
const (
	defaultAccessTokenLifespan   = time.Hour
	defaultRefreshTokenLifespan  = 30 * 24 * time.Hour
	defaultAuthorizeCodeLifespan = 10 * time.Minute
	defaultIDTokenLifespan       = time.Hour
)

// Compile-time proof that *Store satisfies every fosite storage interface the
// composed provider requires. If a fosite upgrade changes a signature this
// fails to build rather than panicking at runtime inside compose.
var (
	_ fosite.ClientManager               = (*Store)(nil)
	_ oauth2.CoreStorage                 = (*Store)(nil)
	_ oauth2.TokenRevocationStorage      = (*Store)(nil)
	_ openid.OpenIDConnectRequestStorage = (*Store)(nil)
)

// New builds a wired fosite OAuth2Provider for the "Sign in with Vulos" flows:
// authorization-code + PKCE (S256 enforced for public clients) + refresh
// (rotation with reuse-detection family revocation) + OpenID Connect id_token
// (RS256) + token introspection + RFC 7009 revocation.
//
// The RS256 id_token signing key is the EXISTING KEK-wrapped key loaded via the
// oauthprovider store (LoadOrCreateSigningKey) — never a second key. The key is
// loaded FRESH on every id_token signature (keyGetter), so a signing-key
// rotation performed through oauthprovider.Service.RotateSigningKey is picked up
// immediately and id_tokens are always signed with the key currently published
// in the JWKS (matching the hand-rolled provider's rotation semantics).
//
// The HMAC GlobalSecret used for the opaque access/refresh/code token signatures
// is derived from the provider's STABLE pairwise salt (never the rotating signing
// key), so rotating the id_token signing key never invalidates outstanding access
// or refresh tokens — again matching the hand-rolled provider, whose opaque tokens
// are DB-hashed and independent of the signing key.
//
// Behaviour parity with the audited hand-rolled provider is pinned in the config:
//   - RefreshTokenScopes = []string{} → a refresh token is issued on EVERY
//     authorization_code exchange (the hand-rolled provider always issued one; it
//     never required an `offline` scope, which is not even a registrable scope).
//   - ClientSecretsHasher = sha256HexHasher → confidential-client secrets are
//     verified against the stored hex(sha256(secret)) digest, the format the
//     oauthprovider store persists (fosite's default BCrypt would never match).
//   - MinParameterEntropy = -1 → no `nonce` minimum-entropy rule. The OpenID
//     id_token strategy reads the RAW config value (config.GetMinParameterEntropy,
//     which returns -1 verbatim), so a short nonce like "nonce-1" is accepted,
//     matching the hand-rolled provider, which never constrained the nonce.
//
// The separate `state` minimum-entropy rule fosite enforces in NewAuthorizeRequest
// reads the CLAMPED wrapper (Fosite.GetMinParameterEntropy forces any value <= 0
// back up to the default 8), so it cannot be disabled by config. The hand-rolled
// provider left `state` optional and unchecked, so code issuance instead goes
// through IssueCode (see authorize.go), which never calls NewAuthorizeRequest.
//
// issuer is the OIDC issuer URL placed in id_token `iss`.
func New(ctx context.Context, store *Store, issuer string) (fosite.OAuth2Provider, error) {
	if store == nil || store.clients == nil {
		return nil, fmt.Errorf("oauthfosite: nil store")
	}
	// Prove the signing key can be loaded up-front (fail closed at boot rather
	// than on the first id_token) — the same idempotent load the keyGetter uses.
	if _, err := store.clients.LoadOrCreateSigningKey(ctx); err != nil {
		return nil, fmt.Errorf("oauthfosite: load signing key: %w", err)
	}
	secret, err := deriveGlobalSecret(ctx, store)
	if err != nil {
		return nil, err
	}

	config := &fosite.Config{
		GlobalSecret:                   secret,
		IDTokenIssuer:                  issuer,
		AccessTokenLifespan:            defaultAccessTokenLifespan,
		RefreshTokenLifespan:           defaultRefreshTokenLifespan,
		AuthorizeCodeLifespan:          defaultAuthorizeCodeLifespan,
		IDTokenLifespan:                defaultIDTokenLifespan,
		ScopeStrategy:                  fosite.ExactScopeStrategy,
		MinParameterEntropy:            -1,                // parity: no `nonce` minimum-entropy rule (see note below)
		RefreshTokenScopes:             []string{},        // parity: always issue a refresh token
		ClientSecretsHasher:            sha256HexHasher{}, // parity: hex(sha256(secret)) store format
		EnforcePKCEForPublicClients:    true,              // public clients MUST use PKCE …
		EnablePKCEPlainChallengeMethod: false,             // … and only S256 is accepted.
	}

	// keyGetter loads the CURRENT active signing key on every call and pins RS256
	// + its kid, so every id_token is signed RS256 with the kid published in the
	// oauthprovider JWKS — including after a rotation.
	keyGetter := func(ctx context.Context) (interface{}, error) {
		sk, err := store.clients.LoadOrCreateSigningKey(ctx)
		if err != nil {
			return nil, fmt.Errorf("oauthfosite: load signing key: %w", err)
		}
		return &jose.JSONWebKey{
			Key:       sk.Private,
			KeyID:     sk.KID,
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}, nil
	}

	strategy := &compose.CommonStrategy{
		CoreStrategy:               compose.NewOAuth2HMACStrategy(config),
		OpenIDConnectTokenStrategy: compose.NewOpenIDConnectStrategy(keyGetter, config),
		Signer:                     &jwt.DefaultSigner{GetPrivateKey: keyGetter},
	}

	provider := compose.Compose(
		config,
		store,
		strategy,
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2PKCEFactory,
		compose.OpenIDConnectExplicitFactory,
		compose.OAuth2TokenIntrospectionFactory,
		compose.OAuth2TokenRevocationFactory,
	)
	return provider, nil
}

// deriveGlobalSecret produces a stable 32-byte HMAC secret for the opaque
// access/refresh/authorize-code token signatures. It is derived (domain-
// separated) from the provider's persistent pairwise salt — a stable server
// secret that, unlike the id_token signing key, is NEVER rotated. Anchoring the
// GlobalSecret here means rotating the signing key can never invalidate live
// opaque tokens, and the secret is identical across process restarts, so tokens
// survive a restart exactly as the hand-rolled provider's DB-hashed tokens do.
func deriveGlobalSecret(ctx context.Context, store *Store) ([]byte, error) {
	salt, err := store.clients.LoadOrCreatePairwiseSalt(ctx)
	if err != nil {
		return nil, fmt.Errorf("oauthfosite: load pairwise salt: %w", err)
	}
	h := sha256.New()
	h.Write([]byte("vulos-oauthfosite-global-secret\x00"))
	h.Write([]byte(salt))
	return h.Sum(nil), nil
}

// sha256HexHasher verifies confidential-client secrets against the store's
// persisted hex(sha256(secret)) digest (the format oauthprovider writes via its
// hashToken). It satisfies fosite.Hasher; fosite's default BCrypt hasher would
// never match those digests. Comparison is constant-time over the fixed-length
// hex digests. It is high-entropy-secret hashing (SHA-256, not a slow KDF), which
// is appropriate because every client secret is itself >=128 bits of randomness —
// the SAME rationale and construction the hand-rolled provider used.
type sha256HexHasher struct{}

func (sha256HexHasher) hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Compare reports nil iff hex(sha256(data)) equals the stored digest.
func (h sha256HexHasher) Compare(_ context.Context, hash, data []byte) error {
	got := h.hashHex(data)
	if subtle.ConstantTimeCompare([]byte(got), hash) == 1 {
		return nil
	}
	return fmt.Errorf("oauthfosite: secret mismatch")
}

// Hash returns hex(sha256(data)). fosite only calls this when it mints a secret
// itself; Vulos never registers clients through fosite (clients come from the
// oauthprovider store), so this exists to satisfy the interface and stays
// consistent with the store's format.
func (h sha256HexHasher) Hash(_ context.Context, data []byte) ([]byte, error) {
	return []byte(h.hashHex(data)), nil
}
