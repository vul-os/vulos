package selfhost

import (
	"context"
	"fmt"
	"time"
)

// accessTokenSkew refreshes a cached access token slightly before real expiry.
const accessTokenSkew = 60 * time.Second

// Service ties the Store (persistence + encryption-at-rest) to the per-provider
// Exchangers (the provider OAuth calls). Route handlers depend only on Service.
//
// The KEK is held in process memory only; refresh tokens are decrypted for the
// duration of a single refresh call and never returned to a caller.
type Service struct {
	store      *Store
	exchangers map[string]Exchanger
	kek        []byte
	now        func() time.Time
}

// NewService builds a Service. exchangers maps provider → Exchanger; kek must be
// 32 bytes (see LoadKEK). Production callers pass real exchangers via
// DefaultExchangers(); tests inject stubs.
func NewService(store *Store, exchangers map[string]Exchanger, kek []byte) *Service {
	return &Service{
		store:      store,
		exchangers: exchangers,
		kek:        kek,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// DefaultExchangers returns the production Exchanger for every known provider.
func DefaultExchangers() map[string]Exchanger {
	return map[string]Exchanger{
		ProviderGoogle:    NewExchanger(ProviderGoogle),
		ProviderMicrosoft: NewExchanger(ProviderMicrosoft),
		ProviderDropbox:   NewExchanger(ProviderDropbox),
	}
}

func (s *Service) exchangerFor(provider string) (Exchanger, error) {
	ex, ok := s.exchangers[provider]
	if !ok || ex == nil {
		return nil, ErrUnknownProvider
	}
	return ex, nil
}

// AuthCodeURL returns the consent URL for (provider, state, pkceChallenge).
func (s *Service) AuthCodeURL(provider, state, pkceChallenge string) (string, error) {
	ex, err := s.exchangerFor(provider)
	if err != nil {
		return "", err
	}
	return ex.AuthCodeURL(state, pkceChallenge)
}

// Connect exchanges an auth code (+ PKCE verifier) and stores the ENCRYPTED
// refresh token (+ cached access token) under (userID, provider). Replaces any
// existing connection for that pair.
func (s *Service) Connect(ctx context.Context, userID, provider, code, pkceVerifier string) (Connection, error) {
	ex, err := s.exchangerFor(provider)
	if err != nil {
		return Connection{}, err
	}
	tok, err := ex.Exchange(ctx, code, pkceVerifier)
	if err != nil {
		return Connection{}, err
	}
	rtEnc, err := encrypt(tok.RefreshToken, s.kek)
	if err != nil {
		return Connection{}, err
	}
	atEnc, err := encrypt(tok.AccessToken, s.kek)
	if err != nil {
		return Connection{}, err
	}
	c := Connection{
		UserID:          userID,
		Provider:        provider,
		RefreshTokenEnc: rtEnc,
		AccessTokenEnc:  atEnc,
		AccessExpiry:    tok.Expiry,
		Scopes:          tok.Scopes,
		AccountEmail:    tok.Email,
	}
	if err := s.store.Upsert(ctx, c); err != nil {
		return Connection{}, err
	}
	return s.store.Get(ctx, userID, provider)
}

// MintedToken is the short-lived credential handed to a consumer. It NEVER
// contains the refresh token.
type MintedToken struct {
	AccessToken string
	Expiry      time.Time
	Scopes      string
}

// MintAccessToken returns a valid short-lived access token for (userID,
// provider): it serves the cached token if still fresh, otherwise refreshes via
// the provider (locally, using the encrypted refresh token), persists the new
// cached token, and returns it. The refresh token never leaves the box.
func (s *Service) MintAccessToken(ctx context.Context, userID, provider string) (MintedToken, error) {
	c, err := s.store.Get(ctx, userID, provider)
	if err == ErrNotFound {
		return MintedToken{}, ErrNotConnected
	}
	if err != nil {
		return MintedToken{}, err
	}

	// Serve the cached access token if it is still comfortably valid.
	if c.AccessTokenEnc != "" && !c.AccessExpiry.IsZero() &&
		s.now().Add(accessTokenSkew).Before(c.AccessExpiry) {
		if at, derr := decrypt(c.AccessTokenEnc, s.kek); derr == nil && at != "" {
			return MintedToken{AccessToken: at, Expiry: c.AccessExpiry, Scopes: c.Scopes}, nil
		}
		// fall through to refresh on decrypt failure
	}

	refresh, err := decrypt(c.RefreshTokenEnc, s.kek)
	if err != nil {
		return MintedToken{}, fmt.Errorf("selfhost: decrypt refresh token: %w", err)
	}
	ex, err := s.exchangerFor(provider)
	if err != nil {
		return MintedToken{}, err
	}
	tok, err := ex.Refresh(ctx, refresh)
	if err != nil {
		return MintedToken{}, err
	}

	// Persist the freshly-minted access token (encrypted) for reuse.
	atEnc, err := encrypt(tok.AccessToken, s.kek)
	if err != nil {
		return MintedToken{}, err
	}
	if err := s.store.SetAccessToken(ctx, userID, provider, atEnc, tok.Expiry); err != nil {
		return MintedToken{}, err
	}
	// Some providers rotate the refresh token; persist it if returned + changed.
	if tok.RefreshToken != "" && tok.RefreshToken != refresh {
		if rtEnc, eerr := encrypt(tok.RefreshToken, s.kek); eerr == nil {
			_ = s.store.SetRefreshToken(ctx, userID, provider, rtEnc)
		}
	}

	scopes := tok.Scopes
	if scopes == "" {
		scopes = c.Scopes
	}
	return MintedToken{AccessToken: tok.AccessToken, Expiry: tok.Expiry, Scopes: scopes}, nil
}

// Status reports whether a user has a connection for provider + display metadata.
func (s *Service) Status(ctx context.Context, userID, provider string) (connected bool, conn Connection, err error) {
	c, err := s.store.Get(ctx, userID, provider)
	if err == ErrNotFound {
		return false, Connection{}, nil
	}
	if err != nil {
		return false, Connection{}, err
	}
	return true, c, nil
}

// List returns all of a user's connections.
func (s *Service) List(ctx context.Context, userID string) ([]Connection, error) {
	return s.store.List(ctx, userID)
}

// Disconnect removes a user's connection for a provider. The local credential is
// always removed (no dependency on provider-side revocation being reachable).
func (s *Service) Disconnect(ctx context.Context, userID, provider string) error {
	return s.store.Delete(ctx, userID, provider)
}
