package integrations

import (
	"context"
	"fmt"
	"time"
)

// accessTokenSkew is subtracted from a cached access token's expiry when
// deciding whether it is still usable, so we refresh slightly early.
const accessTokenSkew = 60 * time.Second

// Service is the broker's orchestration layer: it ties together the Store
// (persistence), crypto (KEK encryption of tokens at rest), and per-provider
// Exchangers (the provider OAuth calls). Route handlers depend only on Service.
type Service struct {
	store      Store
	ex         Exchanger            // default exchanger (Google); also the fallback
	exchangers map[string]Exchanger // provider → exchanger (e.g. dropbox)
	kek        []byte
	now        func() time.Time // injectable clock for tests
}

// NewService builds a Service whose default/primary provider exchanger is ex
// (Google). Additional providers are registered with RegisterExchanger. kek must
// be 32 bytes (see LoadKEK).
func NewService(store Store, ex Exchanger, kek []byte) *Service {
	return &Service{
		store:      store,
		ex:         ex,
		exchangers: map[string]Exchanger{ProviderGoogle: ex},
		kek:        kek,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// RegisterExchanger registers (or replaces) the Exchanger for a provider. Call at
// wiring time, before serving. Not safe for concurrent use with live requests.
func (s *Service) RegisterExchanger(provider string, ex Exchanger) {
	if s.exchangers == nil {
		s.exchangers = make(map[string]Exchanger)
	}
	s.exchangers[provider] = ex
}

// exchangerFor returns the Exchanger for provider, falling back to the default
// (Google) exchanger for an unregistered provider.
func (s *Service) exchangerFor(provider string) Exchanger {
	if ex, ok := s.exchangers[provider]; ok && ex != nil {
		return ex
	}
	return s.ex
}

// AuthCodeURL returns the Google consent URL for a CSRF state (back-compat).
func (s *Service) AuthCodeURL(state string) (string, error) { return s.ex.AuthCodeURL(state) }

// AuthCodeURLFor returns the consent URL for a provider + CSRF state.
func (s *Service) AuthCodeURLFor(provider, state string) (string, error) {
	return s.exchangerFor(provider).AuthCodeURL(state)
}

// Connect exchanges an authorization code and stores the encrypted refresh
// token (+ cached access token) under (accountID, provider). Replaces any
// existing connection for that pair.
func (s *Service) Connect(ctx context.Context, accountID, provider, code string) (Connection, error) {
	tok, err := s.exchangerFor(provider).Exchange(ctx, code)
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
		AccountID:       accountID,
		Provider:        provider,
		RefreshTokenEnc: rtEnc,
		AccessTokenEnc:  atEnc,
		AccessExpiry:    tok.Expiry,
		Scopes:          tok.Scopes,
		AccountEmail:    tok.Email,
		AccountSub:      tok.Sub,
	}
	if err := s.store.Upsert(ctx, c); err != nil {
		return Connection{}, err
	}
	return s.store.Get(ctx, accountID, provider)
}

// MintedToken is the short-lived credential handed to a box. It NEVER contains
// the refresh token.
type MintedToken struct {
	AccessToken string
	Expiry      time.Time
	Scopes      string
}

// MintAccessToken returns a valid short-lived access token for (accountID,
// provider): it serves the cached token if still fresh, otherwise refreshes via
// the provider, persists the new cached token, and returns it. The refresh
// token never leaves the CP.
func (s *Service) MintAccessToken(ctx context.Context, accountID, provider string) (MintedToken, error) {
	c, err := s.store.Get(ctx, accountID, provider)
	if err != nil {
		return MintedToken{}, err
	}

	// Serve the cached access token if it is still comfortably valid.
	if c.AccessTokenEnc != "" && !c.AccessExpiry.IsZero() &&
		s.now().Add(accessTokenSkew).Before(c.AccessExpiry) {
		at, derr := decrypt(c.AccessTokenEnc, s.kek)
		if derr == nil && at != "" {
			return MintedToken{AccessToken: at, Expiry: c.AccessExpiry, Scopes: c.Scopes}, nil
		}
		// fall through to refresh on decrypt failure
	}

	refresh, err := decrypt(c.RefreshTokenEnc, s.kek)
	if err != nil {
		return MintedToken{}, fmt.Errorf("integrations: decrypt refresh token: %w", err)
	}
	tok, err := s.exchangerFor(provider).Refresh(ctx, refresh)
	if err != nil {
		return MintedToken{}, err
	}

	// Persist the freshly-minted access token (encrypted) for reuse.
	atEnc, err := encrypt(tok.AccessToken, s.kek)
	if err != nil {
		return MintedToken{}, err
	}
	if err := s.store.SetAccessToken(ctx, accountID, provider, atEnc, tok.Expiry); err != nil {
		return MintedToken{}, err
	}
	// Google occasionally rotates the refresh token; persist it if returned.
	if tok.RefreshToken != "" && tok.RefreshToken != refresh {
		if rtEnc, eerr := encrypt(tok.RefreshToken, s.kek); eerr == nil {
			_ = s.store.SetRefreshToken(ctx, accountID, provider, rtEnc)
		}
	}

	scopes := tok.Scopes
	if scopes == "" {
		scopes = c.Scopes
	}
	return MintedToken{AccessToken: tok.AccessToken, Expiry: tok.Expiry, Scopes: scopes}, nil
}

// Status reports whether an account has a connection and its display metadata.
func (s *Service) Status(ctx context.Context, accountID, provider string) (connected bool, conn Connection, err error) {
	c, err := s.store.Get(ctx, accountID, provider)
	if err == ErrNotConnected {
		return false, Connection{}, nil
	}
	if err != nil {
		return false, Connection{}, err
	}
	return true, c, nil
}

// List returns all of an account's connections (connected-accounts UI).
func (s *Service) List(ctx context.Context, accountID string) ([]Connection, error) {
	return s.store.List(ctx, accountID)
}

// Disconnect revokes the refresh token at the provider (best-effort) and then
// deletes the stored connection. Revocation failure does not block deletion —
// the local credential is always removed. Returns the revoke error (if any) for
// logging; the connection is gone regardless.
func (s *Service) Disconnect(ctx context.Context, accountID, provider string) error {
	c, err := s.store.Get(ctx, accountID, provider)
	if err == ErrNotConnected {
		return nil // already gone
	}
	if err != nil {
		return err
	}
	var revokeErr error
	if refresh, derr := decrypt(c.RefreshTokenEnc, s.kek); derr == nil && refresh != "" {
		revokeErr = s.exchangerFor(provider).Revoke(ctx, refresh)
	}
	if delErr := s.store.Delete(ctx, accountID, provider); delErr != nil {
		return delErr
	}
	return revokeErr
}
