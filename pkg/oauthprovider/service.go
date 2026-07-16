package oauthprovider

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Subject types the provider supports (OIDC subject_types_supported).
const (
	// SubjectTypePublic returns the raw auth.users.id as `sub` — the same value
	// to every RP. This is the DEFAULT and preserves existing SSO mappings.
	SubjectTypePublic = "public"
	// SubjectTypePairwise returns a per-client, non-reversible `sub`.
	SubjectTypePairwise = "pairwise"
)

// Default lifetimes. Auth codes are intentionally very short-lived (they are
// single-use and only need to survive a redirect round-trip); access tokens are
// short; refresh tokens are long and rotated on every use.
const (
	DefaultAuthCodeTTL = 5 * time.Minute
	DefaultAccessTTL   = time.Hour
	DefaultRefreshTTL  = 30 * 24 * time.Hour
)

// UserResolver resolves a user ULID to the claims the provider can assert.
// internal/auth.Store satisfies this (ProfileByID). The provider NEVER stores
// users itself — this is the only seam into the identity source of truth.
type UserResolver interface {
	ProfileByID(ctx context.Context, userID string) (email string, emailVerified bool, err error)
}

// Service is the OAuth2/OIDC authorization-server engine.
type Service struct {
	store *Store
	users UserResolver
	// keyMu guards signKey so RotateSigningKey can swap the active signer while
	// token signing reads it concurrently.
	keyMu   sync.RWMutex
	signKey *SigningKey
	issuer  string
	codeTTL time.Duration
	accTTL  time.Duration
	refTTL  time.Duration
	// rotationOverlap is how long a retired signing key stays published in JWKS
	// after RotateSigningKey (see store.DefaultRotationOverlap).
	rotationOverlap time.Duration
	// pairwiseSalt is a stable server secret mixed into pairwise subject
	// identifiers (loaded/created once at construction).
	pairwiseSalt string
	now          func() time.Time
}

// NewService builds the engine, loading (or generating) the RS256 signing key.
// issuer is the public base URL of this provider (e.g. https://vulos.cloud),
// used as the id_token `iss` and in the discovery document.
func NewService(ctx context.Context, store *Store, users UserResolver, issuer string) (*Service, error) {
	key, err := store.LoadOrCreateSigningKey(ctx)
	if err != nil {
		return nil, err
	}
	salt, err := store.LoadOrCreatePairwiseSalt(ctx)
	if err != nil {
		return nil, err
	}
	return &Service{
		store:           store,
		users:           users,
		signKey:         key,
		issuer:          strings.TrimRight(issuer, "/"),
		codeTTL:         DefaultAuthCodeTTL,
		accTTL:          DefaultAccessTTL,
		refTTL:          DefaultRefreshTTL,
		rotationOverlap: DefaultRotationOverlap,
		pairwiseSalt:    salt,
		now:             func() time.Time { return time.Now().UTC() },
	}, nil
}

// subjectFor computes the `sub` claim for a client + user. Public clients (the
// default) get the RAW user id — UNCHANGED from before this feature, so existing
// SSO mappings are preserved. Pairwise clients get a stable, non-reversible,
// per-sector identifier: base64url(sha256(sector || user_id || salt)). The
// sector defaults to the client_id, so distinct pairwise clients get distinct
// subs for the same user unless an explicit shared sector_identifier groups them.
func (s *Service) subjectFor(c Client, userID string) string {
	if c.SubjectType != SubjectTypePairwise {
		return userID
	}
	sector := c.SectorIdentifier
	if sector == "" {
		sector = c.ClientID
	}
	// NUL separators prevent boundary ambiguity between the three inputs.
	sum := sha256.Sum256([]byte(sector + "\x00" + userID + "\x00" + s.pairwiseSalt))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// SubjectFor computes the OIDC `sub` claim for a client + user. It is the
// exported form of subjectFor so the fosite-backed authorize handler can seed
// the id_token / access-token session with the SAME pairwise-or-raw subject the
// hand-rolled path asserted (public clients get the raw user id; pairwise
// clients get their per-sector, non-reversible identifier).
func (s *Service) SubjectFor(c Client, userID string) string { return s.subjectFor(c, userID) }

// RotateSigningKey rotates the id_token signing key: the store mints + KEK-wraps
// + persists a fresh RSA-2048 keypair as the new active key and retires the
// prior key (kept in JWKS for the overlap window). The new key becomes the
// signer for subsequent id_tokens. Safe to call concurrently with token signing.
//
// Rotation is OFF by default: it only runs when explicitly invoked (manually or
// by the scheduler job, which is registered only when a rotation interval is
// configured), so existing deployments are never forced to rotate.
func (s *Service) RotateSigningKey(ctx context.Context) error {
	k, err := s.store.RotateSigningKey(ctx, s.rotationOverlap)
	if err != nil {
		return err
	}
	s.keyMu.Lock()
	s.signKey = k
	s.keyMu.Unlock()
	log.Printf("[oauthprovider] signing key rotated: new active kid=%s (prior key retained in JWKS for %s)", k.KID, s.rotationOverlap)
	return nil
}

// SetRotationOverlap overrides the JWKS overlap window for retired keys (tests).
func (s *Service) SetRotationOverlap(d time.Duration) { s.rotationOverlap = d }

// Issuer returns the configured issuer base URL.
func (s *Service) Issuer() string { return s.issuer }

// Store exposes the underlying store (client CRUD, JWKS).
func (s *Service) Store() *Store { return s.store }

// SigningKey returns the current active signing key (JWKS construction / tests).
func (s *Service) SigningKey() *SigningKey {
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	return s.signKey
}

// ---------------------------------------------------------------------------
// Client registration
// ---------------------------------------------------------------------------

// RegisterClient creates a new OAuth app for owner. For confidential clients it
// returns the plaintext secret ONCE — it is hashed before storage and can never
// be retrieved again (only rotated). For public clients the returned secret is
// empty and PKCE is mandatory at the token endpoint.
func (s *Service) RegisterClient(ctx context.Context, owner, name string, redirectURIs, scopes []string, isPublic bool) (Client, string, error) {
	// Default subject type is "public" (raw id sub) — unchanged behaviour.
	return s.RegisterClientTyped(ctx, owner, name, redirectURIs, scopes, isPublic, SubjectTypePublic, "")
}

// RegisterClientTyped is RegisterClient plus an explicit subject type. Pass
// subjectType="pairwise" (optionally with a sectorIdentifier) to give the client
// a per-client, non-reversible `sub`; "" or "public" behaves exactly like
// RegisterClient (raw-id sub). Any other subjectType is rejected.
func (s *Service) RegisterClientTyped(ctx context.Context, owner, name string, redirectURIs, scopes []string, isPublic bool, subjectType, sectorIdentifier string) (Client, string, error) {
	switch subjectType {
	case "", SubjectTypePublic:
		subjectType = SubjectTypePublic
	case SubjectTypePairwise:
		// ok
	default:
		return Client{}, "", errors.New("oauthprovider: subject_type must be \"public\" or \"pairwise\"")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Client{}, "", errors.New("oauthprovider: client name required")
	}
	if owner == "" {
		return Client{}, "", errors.New("oauthprovider: owner required")
	}
	cleanURIs, err := sanitizeRedirectURIs(redirectURIs)
	if err != nil {
		return Client{}, "", err
	}
	if len(cleanURIs) == 0 {
		return Client{}, "", errors.New("oauthprovider: at least one redirect_uri required")
	}
	cleanScopes, hadUnknown := FilterKnown(ParseScopes(JoinScopes(scopes)))
	if hadUnknown {
		return Client{}, "", errors.New("oauthprovider: unknown scope requested")
	}
	// Grantability gate: refuse to register a client for a scope that cannot be
	// granted today (e.g. mail.* while hosted mail is dormant). The scope stays
	// KNOWN, so this is a pure config gate — when hosted mail is enabled the same
	// registration succeeds with no schema change.
	if _, hadUngrantable := FilterGrantable(cleanScopes); hadUngrantable {
		return Client{}, "", ErrScopeNotGrantable
	}
	if len(cleanScopes) == 0 {
		cleanScopes = []string{ScopeOpenID}
	}

	clientID, err := randomToken(16)
	if err != nil {
		return Client{}, "", err
	}
	clientID = "vcid_" + clientID

	c := Client{
		ClientID:         clientID,
		IsPublic:         isPublic,
		Name:             name,
		RedirectURIs:     cleanURIs,
		Scopes:           cleanScopes,
		OwnerUserID:      owner,
		SubjectType:      subjectType,
		SectorIdentifier: strings.TrimSpace(sectorIdentifier),
	}
	var plaintext string
	if !isPublic {
		sec, err := randomToken(32)
		if err != nil {
			return Client{}, "", err
		}
		plaintext = "vcsk_" + sec
		c.SecretHash = hashToken(plaintext)
	}
	if err := s.store.CreateClient(ctx, c); err != nil {
		return Client{}, "", err
	}
	return c, plaintext, nil
}

// RotateSecret mints a new secret for a confidential client and returns the
// plaintext once. Public clients have no secret to rotate.
func (s *Service) RotateSecret(ctx context.Context, clientID string) (string, error) {
	c, err := s.store.GetClient(ctx, clientID)
	if err != nil {
		return "", err
	}
	if c.IsPublic {
		return "", errors.New("oauthprovider: public clients have no secret")
	}
	sec, err := randomToken(32)
	if err != nil {
		return "", err
	}
	plaintext := "vcsk_" + sec
	if err := s.store.UpdateClientSecret(ctx, clientID, hashToken(plaintext)); err != nil {
		return "", err
	}
	return plaintext, nil
}

// verifyClientSecret reports whether secret matches the client's stored hash,
// using a constant-time comparison over the (fixed-length) hex digests.
func verifyClientSecret(c Client, secret string) bool {
	if c.SecretHash == "" {
		return false
	}
	got := hashToken(secret)
	return subtle.ConstantTimeCompare([]byte(got), []byte(c.SecretHash)) == 1
}

// VerifyClientSecretExported is the exported version of verifyClientSecret for
// use by the revoke handler (which must authenticate clients outside the normal
// authenticateClient code path because it also handles public clients).
func VerifyClientSecretExported(c Client, secret string) bool {
	return verifyClientSecret(c, secret)
}

// authenticateClient resolves + authenticates the token-endpoint caller.
//   - confidential client: a correct secret is REQUIRED (constant-time compare).
//   - public client: no secret; identity is proven later by PKCE.
func (s *Service) authenticateClient(ctx context.Context, clientID, secret string) (Client, error) {
	c, err := s.store.GetClient(ctx, clientID)
	if err != nil {
		return Client{}, ErrInvalidClient
	}
	if c.IsPublic {
		// A public client must NOT present a secret; if one is sent, reject.
		if secret != "" {
			return Client{}, ErrInvalidClient
		}
		return c, nil
	}
	if !verifyClientSecret(c, secret) {
		return Client{}, ErrInvalidClient
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// Authorize
// ---------------------------------------------------------------------------

// AuthorizeRequest is the parsed /oauth/authorize query.
type AuthorizeRequest struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	Scope               string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	Nonce               string
}

// ValidatedAuthorize is a request that passed all checks; ready for consent.
type ValidatedAuthorize struct {
	Client        Client
	RedirectURI   string
	Scopes        []string
	State         string
	CodeChallenge string
	Nonce         string
}

// AuthzError carries an OAuth error code + description. RedirectOK reports
// whether the error may be safely redirected back to the client (true only once
// client_id AND redirect_uri have been validated). When false the caller must
// render an error page instead of redirecting (open-redirect / phishing guard).
type AuthzError struct {
	Code        string
	Description string
	RedirectOK  bool
}

func (e *AuthzError) Error() string { return e.Code + ": " + e.Description }

// ValidateAuthorize runs every authorization-request check in security order.
func (s *Service) ValidateAuthorize(ctx context.Context, req AuthorizeRequest) (ValidatedAuthorize, *AuthzError) {
	// 1. Client must exist. Cannot redirect — we don't trust any URI yet.
	c, err := s.store.GetClient(ctx, req.ClientID)
	if err != nil {
		return ValidatedAuthorize{}, &AuthzError{Code: "invalid_client", Description: "unknown client_id", RedirectOK: false}
	}
	// 2. redirect_uri must EXACTLY match a registered URI. Still cannot redirect
	//    until this passes (that is what makes the redirect target trusted).
	if req.RedirectURI == "" || !exactMatch(req.RedirectURI, c.RedirectURIs) {
		return ValidatedAuthorize{}, &AuthzError{Code: "invalid_request", Description: "redirect_uri mismatch", RedirectOK: false}
	}
	// From here the redirect target is trusted: errors may be redirected back.
	if req.ResponseType != "code" {
		return ValidatedAuthorize{}, &AuthzError{Code: "unsupported_response_type", Description: "only response_type=code is supported", RedirectOK: true}
	}
	// 3. PKCE is mandatory and must be S256.
	if req.CodeChallengeMethod != PKCEMethodS256 {
		return ValidatedAuthorize{}, &AuthzError{Code: "invalid_request", Description: "code_challenge_method must be S256", RedirectOK: true}
	}
	if !ValidChallenge(req.CodeChallenge) {
		return ValidatedAuthorize{}, &AuthzError{Code: "invalid_request", Description: "invalid code_challenge", RedirectOK: true}
	}
	// 4. Scopes: known + a subset of what the client was registered for.
	scopes := ParseScopes(req.Scope)
	if len(scopes) == 0 {
		return ValidatedAuthorize{}, &AuthzError{Code: "invalid_scope", Description: "at least one scope required", RedirectOK: true}
	}
	if _, hadUnknown := FilterKnown(scopes); hadUnknown {
		return ValidatedAuthorize{}, &AuthzError{Code: "invalid_scope", Description: "unknown scope", RedirectOK: true}
	}
	// Grantability gate: a request that includes a KNOWN-but-currently-ungrantable
	// scope (mail.* while hosted mail is dormant) is rejected — an app must not be
	// able to obtain a scope that resolves to nothing. This runs BEFORE the
	// per-client subset check so the error is the same ("temporarily unavailable")
	// whether or not the client was registered for the scope, and never leaks the
	// client's registered-scope list.
	if _, hadUngrantable := FilterGrantable(scopes); hadUngrantable {
		return ValidatedAuthorize{}, &AuthzError{Code: "invalid_scope", Description: "requested scope is temporarily unavailable", RedirectOK: true}
	}
	if !SubsetOf(scopes, c.Scopes) {
		return ValidatedAuthorize{}, &AuthzError{Code: "invalid_scope", Description: "scope not allowed for this client", RedirectOK: true}
	}
	return ValidatedAuthorize{
		Client:        c,
		RedirectURI:   req.RedirectURI,
		Scopes:        scopes,
		State:         req.State,
		CodeChallenge: req.CodeChallenge,
		Nonce:         req.Nonce,
	}, nil
}

// IssueCode mints + stores a single-use authorization code for the consenting
// user and returns the raw code (only returned here, stored hashed).
func (s *Service) IssueCode(ctx context.Context, v ValidatedAuthorize, userID string) (string, error) {
	raw, err := randomToken(32)
	if err != nil {
		return "", err
	}
	raw = "vcac_" + raw
	ac := AuthCode{
		CodeHash:            hashToken(raw),
		ClientID:            v.Client.ClientID,
		UserID:              userID,
		RedirectURI:         v.RedirectURI,
		Scope:               JoinScopes(v.Scopes),
		CodeChallenge:       v.CodeChallenge,
		CodeChallengeMethod: PKCEMethodS256,
		Nonce:               v.Nonce,
		ExpiresAt:           s.now().Add(s.codeTTL),
	}
	if err := s.store.SaveAuthCode(ctx, ac); err != nil {
		return "", err
	}
	return raw, nil
}

// ---------------------------------------------------------------------------
// Token endpoint
// ---------------------------------------------------------------------------

// OAuth token-endpoint error sentinels (mapped to the spec error codes).
var (
	ErrInvalidClient = errors.New("invalid_client")
	ErrInvalidGrant  = errors.New("invalid_grant")
	ErrInvalidScope  = errors.New("invalid_scope")
	// ErrScopeNotGrantable is returned by RegisterClientTyped when a requested
	// scope is KNOWN but not currently grantable (e.g. mail.* while hosted mail is
	// dormant). It is distinct from "unknown scope" so the dev console can explain
	// the scope exists but is temporarily unavailable.
	ErrScopeNotGrantable = errors.New("oauthprovider: scope not currently grantable")
)

// TokenResponse is the JSON body returned from /oauth/token.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token,omitempty"`
}

// ExchangeCodeParams carries the authorization_code grant inputs.
type ExchangeCodeParams struct {
	Code         string
	RedirectURI  string
	ClientID     string
	ClientSecret string
	CodeVerifier string
}

// ExchangeCode performs the authorization_code → tokens exchange with PKCE.
func (s *Service) ExchangeCode(ctx context.Context, p ExchangeCodeParams) (*TokenResponse, error) {
	client, err := s.authenticateClient(ctx, p.ClientID, p.ClientSecret)
	if err != nil {
		return nil, err
	}
	codeHash := hashToken(p.Code)
	// Validate all bindings on a read-only copy first, so a benign client mistake
	// (wrong verifier) does not burn the single-use code.
	ac, err := s.store.GetAuthCode(ctx, codeHash)
	if err != nil {
		return nil, ErrInvalidGrant
	}
	// Bind the code to the authenticated client + the original redirect_uri.
	if ac.ClientID != client.ClientID {
		return nil, ErrInvalidGrant
	}
	if p.RedirectURI == "" || p.RedirectURI != ac.RedirectURI {
		return nil, ErrInvalidGrant
	}
	// PKCE proof-of-possession.
	if p.CodeVerifier == "" || !VerifyS256(p.CodeVerifier, ac.CodeChallenge) {
		return nil, ErrInvalidGrant
	}
	// All checks passed — now atomically claim the code (single-use). A racing or
	// replayed exchange loses here and gets ErrInvalidGrant.
	ac, err = s.store.ConsumeAuthCode(ctx, codeHash)
	if err != nil {
		return nil, ErrInvalidGrant
	}
	return s.issueTokens(ctx, client, ac.UserID, ac.Scope, ac.Nonce, "") // "" → new family (initial exchange)
}

// RefreshParams carries the refresh_token grant inputs.
type RefreshParams struct {
	RefreshToken string
	ClientID     string
	ClientSecret string
	Scope        string // optional down-scope; must be a subset of the original
}

// Refresh performs the refresh_token grant with refresh-token rotation: the
// presented refresh token is revoked and a fresh one is issued, so a stolen
// (and already-used) refresh token is detectably dead.
//
// M4 (audit): implements RFC 9700 §4.14 token-family replay detection.
// If a REVOKED refresh token is presented (i.e. a token that existed but was
// already rotated out), the entire token family is immediately revoked. This
// limits the damage from a stolen refresh token: whoever has the newer token
// after the legitimate owner has refreshed will trigger the full revocation the
// next time they try to refresh.
func (s *Service) Refresh(ctx context.Context, p RefreshParams) (*TokenResponse, error) {
	client, err := s.authenticateClient(ctx, p.ClientID, p.ClientSecret)
	if err != nil {
		return nil, err
	}
	tokenHash := hashToken(p.RefreshToken)
	tok, err := s.store.GetToken(ctx, tokenHash, "refresh")
	if err != nil {
		// GetToken returns ErrTokenNotFound for revoked or expired tokens.
		// Distinguish "token exists but is revoked" from "token unknown":
		// a revoked refresh token indicates replay of a rotated-out token.
		if errors.Is(err, ErrTokenNotFound) {
			if existing, lookupErr := s.store.GetTokenByHashAny(ctx, tokenHash, "refresh"); lookupErr == nil && existing.Revoked {
				// Replay detected: revoke the whole family to protect the legitimate owner.
				// WAVE34-5: a swallowed error here is a partial token-revocation hole —
				// the attacker's family stays live. Surface it so it can be retried /
				// alerted rather than silently discarded.
				if revErr := s.store.RevokeFamilyByID(ctx, existing.FamilyID); revErr != nil {
					log.Printf("[oauthprovider] WARNING (revocation-hole): family revoke FAILED on refresh-token replay for family=%s — stolen family may remain live: %v", existing.FamilyID, revErr)
				}
			}
		}
		return nil, ErrInvalidGrant
	}
	if tok.ClientID != client.ClientID {
		return nil, ErrInvalidGrant
	}
	scope := tok.Scope
	if p.Scope != "" {
		req := ParseScopes(p.Scope)
		if !SubsetOf(req, ParseScopes(tok.Scope)) {
			return nil, ErrInvalidScope
		}
		scope = JoinScopes(req)
	}
	// Rotate: kill the presented refresh token (and cascade to access tokens)
	// before issuing replacements. Inherit the family_id so the chain stays linked.
	if err := s.store.RevokeTokenByHash(ctx, tok.TokenHash); err != nil {
		return nil, err
	}
	return s.issueTokens(ctx, client, tok.UserID, scope, "", tok.FamilyID)
}

// issueTokens mints + persists an access token (+ refresh token) and, when the
// openid scope is present, a signed id_token.
//
// M4 (audit): familyID groups the rotation chain. Pass "" to generate a new
// family (initial auth-code exchange); pass the existing family from the
// presented refresh token to inherit the chain (rotation).
func (s *Service) issueTokens(ctx context.Context, client Client, userID, scope, nonce, familyID string) (*TokenResponse, error) {
	now := s.now()

	// M4: generate a fresh family_id for new auth-code exchanges; inherit for rotations.
	if familyID == "" {
		fam, ferr := randomToken(16)
		if ferr != nil {
			return nil, ferr
		}
		familyID = "fam_" + fam
	}

	accessRaw, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	accessRaw = "vcat_" + accessRaw
	accExp := now.Add(s.accTTL)
	if err := s.store.SaveToken(ctx, Token{
		TokenHash: hashToken(accessRaw), TokenType: "access", ClientID: client.ClientID,
		UserID: userID, Scope: scope, ExpiresAt: accExp, FamilyID: familyID,
	}); err != nil {
		return nil, err
	}

	refreshRaw, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	refreshRaw = "vcrt_" + refreshRaw
	if err := s.store.SaveToken(ctx, Token{
		TokenHash: hashToken(refreshRaw), TokenType: "refresh", ClientID: client.ClientID,
		UserID: userID, Scope: scope, ExpiresAt: now.Add(s.refTTL), FamilyID: familyID,
	}); err != nil {
		return nil, err
	}

	resp := &TokenResponse{
		AccessToken:  accessRaw,
		TokenType:    "Bearer",
		ExpiresIn:    int(s.accTTL.Seconds()),
		RefreshToken: refreshRaw,
		Scope:        scope,
	}

	if HasScope(scope, ScopeOpenID) {
		idtok, err := s.buildIDToken(ctx, client, userID, scope, nonce, now)
		if err != nil {
			return nil, err
		}
		resp.IDToken = idtok
	}
	return resp, nil
}

// buildIDToken constructs + signs the OIDC id_token. Email claims are included
// only when the email scope was granted.
func (s *Service) buildIDToken(ctx context.Context, client Client, userID, scope string, nonce string, now time.Time) (string, error) {
	claims := IDTokenClaims{
		Issuer:   s.issuer,
		Subject:  s.subjectFor(client, userID), // pairwise for pairwise clients; raw id otherwise
		Audience: client.ClientID,
		Expiry:   now.Add(s.accTTL).Unix(),
		IssuedAt: now.Unix(),
		AuthTime: now.Unix(),
		Nonce:    nonce,
	}
	if HasScope(scope, ScopeEmail) {
		email, verified, err := s.users.ProfileByID(ctx, userID)
		if err != nil {
			return "", fmt.Errorf("oauthprovider: resolve user for id_token: %w", err)
		}
		claims.Email = email
		v := verified
		claims.EmailVerified = &v
	}
	// Sign with the CURRENT active key under the read lock so a concurrent
	// rotation cannot swap the key mid-sign.
	s.keyMu.RLock()
	k := s.signKey
	s.keyMu.RUnlock()
	return k.SignIDToken(claims)
}

// ---------------------------------------------------------------------------
// userinfo
// ---------------------------------------------------------------------------

// UserInfo is the resolved profile returned by the userinfo endpoint.
type UserInfo struct {
	Subject       string
	Email         string
	EmailVerified bool
	Scope         string
}

// IntrospectAccessToken validates a Bearer access token and returns the
// associated subject + granted scope. Returns ErrTokenNotFound for any invalid,
// expired, or revoked token.
func (s *Service) IntrospectAccessToken(ctx context.Context, raw string) (Token, error) {
	return s.store.GetToken(ctx, hashToken(raw), "access")
}

// UserInfoForToken validates the access token and builds the userinfo response,
// honouring the granted scopes (email claims only when email scope present).
func (s *Service) UserInfoForToken(ctx context.Context, raw string) (UserInfo, error) {
	tok, err := s.IntrospectAccessToken(ctx, raw)
	if err != nil {
		return UserInfo{}, err
	}
	ui := UserInfo{Subject: tok.UserID, Scope: tok.Scope}
	// Match the id_token `sub`: pairwise clients get their per-client subject.
	// The token cannot outlive its client (DeleteClient cascades tokens), so a
	// lookup miss is not expected; fall back to the raw id (public behaviour).
	if c, cerr := s.store.GetClient(ctx, tok.ClientID); cerr == nil {
		ui.Subject = s.subjectFor(c, tok.UserID)
	}
	if HasScope(tok.Scope, ScopeEmail) {
		email, verified, err := s.users.ProfileByID(ctx, tok.UserID)
		if err != nil {
			return UserInfo{}, err
		}
		ui.Email = email
		ui.EmailVerified = verified
	}
	return ui, nil
}

// SetClock overrides the clock (tests only).
func (s *Service) SetClock(f func() time.Time) { s.now = f }

// SetTTLs overrides the default lifetimes (tests only).
func (s *Service) SetTTLs(code, access, refresh time.Duration) {
	s.codeTTL, s.accTTL, s.refTTL = code, access, refresh
}

// ---------------------------------------------------------------------------
// Consent management
// ---------------------------------------------------------------------------

// HasConsent reports whether userID has an existing live consent that covers
// all of requestedScopes for the given clientID. When true the authorize flow
// may skip the interactive consent screen.
func (s *Service) HasConsent(ctx context.Context, userID, clientID string, requestedScopes []string) bool {
	grant, err := s.store.GetConsent(ctx, userID, clientID)
	if err != nil {
		return false
	}
	// The stored grant must cover every requested scope.
	return SubsetOf(requestedScopes, ParseScopes(grant.Scope))
}

// GrantConsent persists a consent approval for userID for the given validated
// authorize request. Called after the user clicks "Allow" on the consent screen.
func (s *Service) GrantConsent(ctx context.Context, userID string, v ValidatedAuthorize) error {
	return s.store.UpsertConsent(ctx, userID, v.Client.ClientID, JoinScopes(v.Scopes))
}

// RevokeConsent revokes the stored consent for userID+clientID AND immediately
// revokes all outstanding tokens for the (user, client) pair so the revocation
// takes effect before tokens naturally expire.
// SEC-H1 (audit): token revocation was previously missing — consent revocation
// only removed the grant row; tokens lived until their TTL.
func (s *Service) RevokeConsent(ctx context.Context, userID, clientID string) error {
	// Revoke the consent grant first.
	if err := s.store.RevokeConsent(ctx, userID, clientID); err != nil {
		return err
	}
	// Kill all outstanding access + refresh tokens for this (user, client) pair.
	// Non-fatal if the token table update fails (consent is already revoked; the
	// tokens will expire naturally and cannot be refreshed without consent).
	if err := s.store.RevokeUserClientTokens(ctx, userID, clientID); err != nil {
		return err
	}
	return nil
}

// RevokeUserClientTokens revokes all outstanding tokens (access + refresh)
// issued to clientID on behalf of userID. Exposed separately so callers that
// only want to kill tokens (e.g. admin forced-logout) can do so without
// touching the consent record.
func (s *Service) RevokeUserClientTokens(ctx context.Context, userID, clientID string) error {
	return s.store.RevokeUserClientTokens(ctx, userID, clientID)
}

// ListUserConsents returns all live consent grants for userID, enriched with
// the client's human-readable name (the client row may be missing if the app
// was deleted; in that case Name is empty and the grant is still returned).
func (s *Service) ListUserConsents(ctx context.Context, userID string) ([]ConsentInfo, error) {
	grants, err := s.store.ListUserConsents(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]ConsentInfo, 0, len(grants))
	for _, g := range grants {
		ci := ConsentInfo{
			ClientID:  g.ClientID,
			Scope:     g.Scope,
			GrantedAt: g.GrantedAt,
		}
		if c, cerr := s.store.GetClient(ctx, g.ClientID); cerr == nil {
			ci.ClientName = c.Name
		}
		out = append(out, ci)
	}
	return out, nil
}

// ConsentInfo is the user-facing representation of a stored consent grant.
type ConsentInfo struct {
	ClientID   string
	ClientName string
	Scope      string
	GrantedAt  time.Time
}

// ---------------------------------------------------------------------------
// Token revocation (RFC 7009)
// ---------------------------------------------------------------------------

// ErrUnsupportedTokenType is returned when the hint names an unsupported type.
var ErrUnsupportedTokenType = errors.New("oauthprovider: unsupported_token_type")

// RevokeRaw looks up raw (which may be an access or refresh token) by hash and
// marks it revoked. Per RFC 7009 §2.2 an unknown token is a no-op (returns nil)
// so the endpoint always returns 200 to the caller (prevents oracle).
//
// clientID must match the token's bound client — a client cannot revoke another
// client's tokens.
func (s *Service) RevokeRaw(ctx context.Context, clientID, raw, tokenTypeHint string) error {
	tokenHash := hashToken(raw)
	// Try the hinted type first (fast path), then the other type.
	types := []string{"access", "refresh"}
	if tokenTypeHint == "refresh_token" {
		types = []string{"refresh", "access"}
	} else if tokenTypeHint == "access_token" {
		types = []string{"access", "refresh"}
	}
	for _, tt := range types {
		tok, err := s.store.GetToken(ctx, tokenHash, tt)
		if err != nil {
			continue // not found under this type; try next
		}
		if tok.ClientID != clientID {
			// A client may not revoke tokens it did not issue.
			return ErrInvalidClient
		}
		return s.store.RevokeTokenByHash(ctx, tokenHash)
	}
	// Unknown token — no-op per RFC 7009 §2.2.
	return nil
}
