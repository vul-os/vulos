package oauthfosite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sync"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/openid"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/oauthprovider"
)

// Store is the cpdb-backed fosite storage adapter. It implements the fosite
// storage interfaces needed for the authorization-code + PKCE + refresh + OIDC
// flows:
//
//	fosite.ClientManager
//	oauth2.AuthorizeCodeStorage
//	oauth2.TokenRevocationStorage (access + refresh + revoke)
//	oauth2.RefreshTokenStorage.RotateRefreshToken
//	pkce.PKCERequestStorage
//	openid.OpenIDConnectRequestStorage
//
// Clients are read from the audited oauth_clients table (via *oauthprovider.Store)
// — the single source of truth. fosite's own artefacts live in the dedicated
// oauthfosite_* tables (see migrations/0001).
type Store struct {
	db      *cpdb.DB
	clients *oauthprovider.Store
	// mu serialises writes on SQLite (single-writer), matching the pattern used
	// by the oauthprovider Store. On Postgres the pool handles concurrency and
	// the overhead is negligible.
	mu sync.Mutex
}

// Open applies the oauthfosite migrations to db and returns a ready Store.
// clients supplies audited client lookups (and, elsewhere, the shared signing
// key). db should come from cpdb.Open("oauthfosite") in production or
// cpdb.OpenSQLiteDSN(":memory:") in tests.
func Open(db *cpdb.DB, clients *oauthprovider.Store) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("oauthfosite: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("oauthfosite: migrate: %w", err)
	}
	return &Store{db: db, clients: clients}, nil
}

// ---------------------------------------------------------------------------
// Requester (de)serialization
// ---------------------------------------------------------------------------

// persistedRequest is the on-disk shape of a fosite.Requester. It never holds a
// plaintext token — only the request metadata and the OIDC session (which may
// carry the id_token subject/nonce claims).
type persistedRequest struct {
	ID                string          `json:"id"`
	RequestedAt       time.Time       `json:"requested_at"`
	ClientID          string          `json:"client_id"`
	RequestedScopes   []string        `json:"requested_scopes"`
	GrantedScopes     []string        `json:"granted_scopes"`
	RequestedAudience []string        `json:"requested_audience"`
	GrantedAudience   []string        `json:"granted_audience"`
	Form              url.Values      `json:"form"`
	Session           json.RawMessage `json:"session"`
}

// marshalRequester serialises r for storage, returning the JSON blob plus the
// request id and client id (indexed columns).
func marshalRequester(r fosite.Requester) (blob, requestID, clientID string, err error) {
	sessJSON, err := json.Marshal(r.GetSession())
	if err != nil {
		return "", "", "", fmt.Errorf("oauthfosite: marshal session: %w", err)
	}
	cid := ""
	if r.GetClient() != nil {
		cid = r.GetClient().GetID()
	}
	pr := persistedRequest{
		ID:                r.GetID(),
		RequestedAt:       r.GetRequestedAt(),
		ClientID:          cid,
		RequestedScopes:   r.GetRequestedScopes(),
		GrantedScopes:     r.GetGrantedScopes(),
		RequestedAudience: r.GetRequestedAudience(),
		GrantedAudience:   r.GetGrantedAudience(),
		Form:              r.GetRequestForm(),
		Session:           sessJSON,
	}
	b, err := json.Marshal(pr)
	if err != nil {
		return "", "", "", fmt.Errorf("oauthfosite: marshal request: %w", err)
	}
	return string(b), pr.ID, cid, nil
}

// unmarshalRequester rebuilds a fosite.Requester from a stored blob. The session
// is hydrated into sess when non-nil (fosite passes the caller's session to be
// filled); otherwise a fresh openid.DefaultSession is used. The client is loaded
// from the audited oauth_clients table so the returned requester carries the
// authoritative client definition.
func (s *Store) unmarshalRequester(ctx context.Context, blob string, sess fosite.Session) (fosite.Requester, error) {
	var pr persistedRequest
	if err := json.Unmarshal([]byte(blob), &pr); err != nil {
		return nil, fmt.Errorf("oauthfosite: unmarshal request: %w", err)
	}
	if sess == nil {
		sess = openid.NewDefaultSession()
	}
	if len(pr.Session) > 0 {
		if err := json.Unmarshal(pr.Session, sess); err != nil {
			return nil, fmt.Errorf("oauthfosite: unmarshal session: %w", err)
		}
	}
	cl, err := s.GetClient(ctx, pr.ClientID)
	if err != nil {
		return nil, err
	}
	form := pr.Form
	if form == nil {
		form = url.Values{}
	}
	req := &fosite.Request{
		ID:                pr.ID,
		RequestedAt:       pr.RequestedAt,
		Client:            cl,
		RequestedScope:    fosite.Arguments(pr.RequestedScopes),
		GrantedScope:      fosite.Arguments(pr.GrantedScopes),
		RequestedAudience: fosite.Arguments(pr.RequestedAudience),
		GrantedAudience:   fosite.Arguments(pr.GrantedAudience),
		Form:              form,
		Session:           sess,
	}
	return req, nil
}

// ---------------------------------------------------------------------------
// fosite.ClientManager
// ---------------------------------------------------------------------------

// GetClient loads a client from the audited oauth_clients table and adapts it
// for fosite.
func (s *Store) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	c, err := s.clients.GetClient(ctx, id)
	if errors.Is(err, oauthprovider.ErrClientNotFound) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return newClientAdapter(c), nil
}

// ClientAssertionJWTValid / SetClientAssertionJWT satisfy fosite.ClientManager.
// This foundation does not use the private_key_jwt client-authentication method,
// so JTIs are always considered fresh and are not persisted.
func (s *Store) ClientAssertionJWTValid(_ context.Context, _ string) error { return nil }

func (s *Store) SetClientAssertionJWT(_ context.Context, _ string, _ time.Time) error { return nil }

// ---------------------------------------------------------------------------
// oauth2.AuthorizeCodeStorage
// ---------------------------------------------------------------------------

// CreateAuthorizeCodeSession stores the authorize request under the code
// signature.
func (s *Store) CreateAuthorizeCodeSession(ctx context.Context, signature string, req fosite.Requester) error {
	blob, reqID, clientID, err := marshalRequester(req)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO oauthfosite_authcodes (signature, request_id, client_id, active, requested, created_at)
		VALUES (?, ?, ?, 1, ?, ?)`),
		signature, reqID, clientID, blob, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("oauthfosite: create authorize code: %w", err)
	}
	return nil
}

// GetAuthorizeCodeSession hydrates the stored authorize request. If the code has
// been invalidated it returns the requester together with
// fosite.ErrInvalidatedAuthorizeCode (as the interface requires) so the handler
// can revoke the issued tokens.
func (s *Store) GetAuthorizeCodeSession(ctx context.Context, signature string, sess fosite.Session) (fosite.Requester, error) {
	var (
		blob   string
		active int
	)
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT requested, active FROM oauthfosite_authcodes WHERE signature = ?`), signature).
		Scan(&blob, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("oauthfosite: get authorize code: %w", err)
	}
	req, err := s.unmarshalRequester(ctx, blob, sess)
	if err != nil {
		return nil, err
	}
	if active == 0 {
		return req, fosite.ErrInvalidatedAuthorizeCode
	}
	return req, nil
}

// InvalidateAuthorizeCodeSession marks the code used (single-use). The UPDATE is
// conditioned on the row still being active so exactly one caller can claim it,
// mirroring the atomic single-use guarantee of the hand-rolled provider.
func (s *Store) InvalidateAuthorizeCodeSession(ctx context.Context, signature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE oauthfosite_authcodes SET active = 0 WHERE signature = ? AND active = 1`), signature)
	if err != nil {
		return fmt.Errorf("oauthfosite: invalidate authorize code: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either unknown or already invalidated. fosite treats a missing/invalid
		// code via GetAuthorizeCodeSession; returning nil here is safe because the
		// row is already inactive.
		return nil
	}
	return nil
}

// ---------------------------------------------------------------------------
// pkce.PKCERequestStorage
// ---------------------------------------------------------------------------

func (s *Store) CreatePKCERequestSession(ctx context.Context, signature string, req fosite.Requester) error {
	blob, reqID, clientID, err := marshalRequester(req)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO oauthfosite_pkce (signature, request_id, client_id, requested, created_at)
		VALUES (?, ?, ?, ?, ?)`),
		signature, reqID, clientID, blob, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("oauthfosite: create pkce session: %w", err)
	}
	return nil
}

func (s *Store) GetPKCERequestSession(ctx context.Context, signature string, sess fosite.Session) (fosite.Requester, error) {
	var blob string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT requested FROM oauthfosite_pkce WHERE signature = ?`), signature).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("oauthfosite: get pkce session: %w", err)
	}
	return s.unmarshalRequester(ctx, blob, sess)
}

func (s *Store) DeletePKCERequestSession(ctx context.Context, signature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM oauthfosite_pkce WHERE signature = ?`), signature)
	if err != nil {
		return fmt.Errorf("oauthfosite: delete pkce session: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// openid.OpenIDConnectRequestStorage
// ---------------------------------------------------------------------------

func (s *Store) CreateOpenIDConnectSession(ctx context.Context, authorizeCode string, req fosite.Requester) error {
	blob, reqID, clientID, err := marshalRequester(req)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO oauthfosite_oidc (signature, request_id, client_id, requested, created_at)
		VALUES (?, ?, ?, ?, ?)`),
		authorizeCode, reqID, clientID, blob, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("oauthfosite: create oidc session: %w", err)
	}
	return nil
}

func (s *Store) GetOpenIDConnectSession(ctx context.Context, authorizeCode string, requester fosite.Requester) (fosite.Requester, error) {
	var blob string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT requested FROM oauthfosite_oidc WHERE signature = ?`), authorizeCode).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, openid.ErrNoSessionFound
	}
	if err != nil {
		return nil, fmt.Errorf("oauthfosite: get oidc session: %w", err)
	}
	// Hydrate into the caller's session so the reconstructed requester carries
	// the stored id_token claims (subject/nonce).
	var sess fosite.Session
	if requester != nil {
		sess = requester.GetSession()
	}
	return s.unmarshalRequester(ctx, blob, sess)
}

func (s *Store) DeleteOpenIDConnectSession(ctx context.Context, authorizeCode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM oauthfosite_oidc WHERE signature = ?`), authorizeCode)
	if err != nil {
		return fmt.Errorf("oauthfosite: delete oidc session: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// oauth2.AccessTokenStorage
// ---------------------------------------------------------------------------

func (s *Store) CreateAccessTokenSession(ctx context.Context, signature string, req fosite.Requester) error {
	blob, reqID, clientID, err := marshalRequester(req)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO oauthfosite_access_tokens (signature, request_id, client_id, requested, created_at)
		VALUES (?, ?, ?, ?, ?)`),
		signature, reqID, clientID, blob, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("oauthfosite: create access token: %w", err)
	}
	return nil
}

func (s *Store) GetAccessTokenSession(ctx context.Context, signature string, sess fosite.Session) (fosite.Requester, error) {
	var blob string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT requested FROM oauthfosite_access_tokens WHERE signature = ?`), signature).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("oauthfosite: get access token: %w", err)
	}
	return s.unmarshalRequester(ctx, blob, sess)
}

func (s *Store) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM oauthfosite_access_tokens WHERE signature = ?`), signature)
	if err != nil {
		return fmt.Errorf("oauthfosite: delete access token: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// oauth2.RefreshTokenStorage
// ---------------------------------------------------------------------------

func (s *Store) CreateRefreshTokenSession(ctx context.Context, signature, accessSignature string, req fosite.Requester) error {
	blob, reqID, clientID, err := marshalRequester(req)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO oauthfosite_refresh_tokens (signature, request_id, access_signature, client_id, active, requested, created_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)`),
		signature, reqID, accessSignature, clientID, blob, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("oauthfosite: create refresh token: %w", err)
	}
	return nil
}

// GetRefreshTokenSession returns the stored request. A refresh token that has
// been rotated/revoked (active=0) is returned WITH fosite.ErrInactiveToken so
// the handler detects reuse and revokes the whole family.
func (s *Store) GetRefreshTokenSession(ctx context.Context, signature string, sess fosite.Session) (fosite.Requester, error) {
	var (
		blob   string
		active int
	)
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT requested, active FROM oauthfosite_refresh_tokens WHERE signature = ?`), signature).
		Scan(&blob, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fosite.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("oauthfosite: get refresh token: %w", err)
	}
	req, err := s.unmarshalRequester(ctx, blob, sess)
	if err != nil {
		return nil, err
	}
	if active == 0 {
		return req, fosite.ErrInactiveToken
	}
	return req, nil
}

func (s *Store) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM oauthfosite_refresh_tokens WHERE signature = ?`), signature)
	if err != nil {
		return fmt.Errorf("oauthfosite: delete refresh token: %w", err)
	}
	return nil
}

// RotateRefreshToken is called on a successful refresh, BEFORE the new tokens
// are minted. It deactivates the old refresh token(s) of the family (keeping
// their rows for replay detection) and deletes the old access token(s). Because
// every rotation shares the family's request_id, deactivating/deleting by
// request_id revokes the prior generation without touching the not-yet-created
// new tokens. This mirrors the reference fosite implementation and preserves the
// hand-rolled provider's "rotate with reuse-detection + family revoke" property.
func (s *Store) RotateRefreshToken(ctx context.Context, requestID string, refreshTokenSignature string) error {
	if err := s.RevokeRefreshToken(ctx, requestID); err != nil {
		return err
	}
	return s.RevokeAccessToken(ctx, requestID)
}

// ---------------------------------------------------------------------------
// oauth2.TokenRevocationStorage
// ---------------------------------------------------------------------------

// RevokeRefreshToken deactivates every refresh token in the family (request_id).
// Rows are kept (active=0) so a replayed refresh token is DETECTED rather than
// merely "not found".
func (s *Store) RevokeRefreshToken(ctx context.Context, requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE oauthfosite_refresh_tokens SET active = 0 WHERE request_id = ?`), requestID)
	if err != nil {
		return fmt.Errorf("oauthfosite: revoke refresh token family: %w", err)
	}
	return nil
}

// RevokeAccessToken deletes every access token in the family (request_id).
func (s *Store) RevokeAccessToken(ctx context.Context, requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		DELETE FROM oauthfosite_access_tokens WHERE request_id = ?`), requestID)
	if err != nil {
		return fmt.Errorf("oauthfosite: revoke access token family: %w", err)
	}
	return nil
}
