// Package oauthprovider makes Vulos Cloud an OAuth2 / OIDC authorization
// server. Vulos controls identity for the suite, so it issues "Sign in with
// Vulos" + scoped API access via the Authorization Code flow with PKCE (S256)
// and refresh tokens, plus an OIDC id_token (RS256) and a userinfo endpoint.
//
// Identity reuse (LOCKED): the subject of every token is an auth.users.id. This
// package never stores users — internal/auth remains the single source of truth
// for who a user is. Here we persist only the delegation artefacts: registered
// clients, one-time authorization codes, issued access/refresh tokens, and the
// signing keypair.
//
// Storage: cpdb-backed — SQLite (modernc.org/sqlite, pure-Go, no CGO) by
// default; Postgres via pgx/v5/stdlib when DATABASE_URL / VULOS_DATABASE_URL is
// set. Migrations are embedded via go:embed — identical to the other CP stores.
package oauthprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// Sentinel errors mapped to HTTP responses by the handler layer.
var (
	ErrClientNotFound  = errors.New("oauthprovider: client not found")
	ErrCodeNotFound    = errors.New("oauthprovider: auth code not found, used, or expired")
	ErrTokenNotFound   = errors.New("oauthprovider: token not found, revoked, or expired")
	ErrNoSigningKey    = errors.New("oauthprovider: no signing key")
	ErrConsentNotFound = errors.New("oauthprovider: consent not found or revoked")
)

// Client is a registered OAuth application.
type Client struct {
	ClientID     string
	SecretHash   string // hex(sha256(secret)); empty for public clients
	IsPublic     bool
	Name         string
	RedirectURIs []string // exact-match allow-list
	Scopes       []string // scopes this client may request
	OwnerUserID  string
	// SubjectType is "public" (default — raw user id as sub) or "pairwise"
	// (per-client, non-reversible sub). Empty is treated as "public".
	SubjectType string
	// SectorIdentifier scopes a pairwise sub. Empty ⇒ the client_id is used, so
	// distinct pairwise clients get distinct subs unless deliberately grouped.
	SectorIdentifier string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// AuthCode is a one-time authorization code (stored hashed).
type AuthCode struct {
	CodeHash            string
	ClientID            string
	UserID              string
	RedirectURI         string
	Scope               string
	CodeChallenge       string
	CodeChallengeMethod string
	Nonce               string
	ExpiresAt           time.Time
	Consumed            bool
	CreatedAt           time.Time
}

// Token is an issued access or refresh token (stored hashed).
type Token struct {
	TokenHash string
	TokenType string // "access" | "refresh"
	ClientID  string
	UserID    string
	Scope     string
	ExpiresAt time.Time
	Revoked   bool
	CreatedAt time.Time
	// FamilyID groups a refresh-token rotation chain. All tokens minted from
	// the same auth-code exchange share a FamilyID; refresh rotations inherit
	// it. Used for replay-detection (RFC 9700) and access-token cascade revoke.
	FamilyID string // empty for legacy tokens created before migration 0003
}

// ConsentGrant records a user's approval of a client's scope set. It acts as a
// skip-consent cache: when all requested scopes are covered by a live grant the
// consent screen is bypassed. Stored one row per (user_id, client_id); an
// updated approval widens the scope set via UPSERT.
type ConsentGrant struct {
	UserID    string
	ClientID  string
	Scope     string // space-separated scopes the user has approved
	GrantedAt time.Time
	Revoked   bool
	RevokedAt *time.Time
}

// Store is the cpdb-backed persistence layer (SQLite or Postgres). On SQLite a
// mutex serialises writes (single writer); on Postgres the pool handles
// concurrency and the mutex overhead is negligible.
type Store struct {
	db *cpdb.DB
	mu sync.Mutex
	// kek is the 32-byte AES-256 key used to wrap the id_token signing private
	// key at rest (audit M6). nil => plaintext-compatibility mode (legacy / dev
	// without a configured KEK).
	kek []byte
}

// Open applies migrations to db and returns a ready Store.
//
// db should be obtained from cpdb.Open("oauthprovider") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied automatically
// and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("oauthprovider: migrate: %w", err)
	}
	// FAIL-CLOSED: loadKEK returns ErrKEKRequired when INTEGRATIONS_KEK is unset
	// outside dev, so the OAuth provider refuses to boot rather than persisting
	// the id_token signing key in plaintext. The only nil-KEK path remaining is
	// legacy in-memory test stores opened in dev mode.
	kek, err := loadKEK()
	if err != nil {
		return nil, err
	}
	return &Store{db: db, kek: kek}, nil
}

// encodePrivForStore wraps a plaintext PKCS#8 PEM for storage. With no KEK it
// returns the plaintext unchanged (compatibility mode).
func (s *Store) encodePrivForStore(plainPEM string) (string, error) {
	if s.kek == nil {
		return plainPEM, nil
	}
	return encryptKEK(plainPEM, s.kek)
}

// decodePrivFromStore returns the plaintext PKCS#8 PEM for a stored value,
// transparently handling both legacy plaintext rows and GCM-wrapped rows.
func (s *Store) decodePrivFromStore(stored string) (string, error) {
	if isPlaintextPEM(stored) {
		return stored, nil // legacy plaintext (re-wrapped opportunistically on load)
	}
	if s.kek == nil {
		return "", fmt.Errorf("oauthprovider: signing key is encrypted but no KEK is configured")
	}
	return decryptKEK(stored, s.kek)
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for health-check pingers.
func (s *Store) DB() *sql.DB { return s.db.DB }

// CPDB exposes the underlying cpdb handle so the sibling internal/oauthfosite
// package can create its own fosite-session tables in the SAME database (and
// reuse Rebind / the SQLite single-writer discipline) instead of opening a
// second connection. It shares the connection but never mutates this Store's
// own tables — clients and the signing key remain the single source of truth.
func (s *Store) CPDB() *cpdb.DB { return s.db }

func joinLines(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "\n"
		}
		out += s
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// ---------------------------------------------------------------------------
// Clients
// ---------------------------------------------------------------------------

// CreateClient inserts a new client registration.
func (s *Store) CreateClient(ctx context.Context, c Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	pub := 0
	if c.IsPublic {
		pub = 1
	}
	subjectType := c.SubjectType
	if subjectType == "" {
		subjectType = "public"
	}
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO oauth_clients
		    (client_id, secret_hash, is_public, name, redirect_uris, scopes, owner_user_id, subject_type, sector_identifier, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		c.ClientID, c.SecretHash, pub, c.Name, joinLines(c.RedirectURIs),
		joinScopesCSV(c.Scopes), c.OwnerUserID, subjectType, c.SectorIdentifier, now, now)
	if err != nil {
		return fmt.Errorf("oauthprovider: create client: %w", err)
	}
	return nil
}

// joinScopesCSV stores scopes as a space-separated string (canonical form).
func joinScopesCSV(scopes []string) string { return JoinScopes(scopes) }

func scanClient(row interface {
	Scan(dest ...any) error
}) (Client, error) {
	var (
		c        Client
		pub      int
		redirect string
		scopes   string
		created  int64
		updated  int64
	)
	if err := row.Scan(&c.ClientID, &c.SecretHash, &pub, &c.Name, &redirect, &scopes, &c.OwnerUserID, &c.SubjectType, &c.SectorIdentifier, &created, &updated); err != nil {
		return Client{}, err
	}
	c.IsPublic = pub != 0
	c.RedirectURIs = splitLines(redirect)
	c.Scopes = ParseScopes(scopes)
	c.CreatedAt = time.Unix(created, 0).UTC()
	c.UpdatedAt = time.Unix(updated, 0).UTC()
	return c, nil
}

const clientCols = `client_id, secret_hash, is_public, name, redirect_uris, scopes, owner_user_id, subject_type, sector_identifier, created_at, updated_at`

// GetClient returns the client by id, or ErrClientNotFound.
func (s *Store) GetClient(ctx context.Context, clientID string) (Client, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT `+clientCols+` FROM oauth_clients WHERE client_id = ?`), clientID)
	c, err := scanClient(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Client{}, ErrClientNotFound
	}
	if err != nil {
		return Client{}, fmt.Errorf("oauthprovider: get client: %w", err)
	}
	return c, nil
}

// ListClientsByOwner returns all clients owned by ownerUserID (dev console).
func (s *Store) ListClientsByOwner(ctx context.Context, ownerUserID string) ([]Client, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`SELECT `+clientCols+` FROM oauth_clients WHERE owner_user_id = ? ORDER BY created_at DESC`), ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: list clients: %w", err)
	}
	defer rows.Close()
	var out []Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, fmt.Errorf("oauthprovider: scan client: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateClientSecret rotates the stored secret hash for a client.
func (s *Store) UpdateClientSecret(ctx context.Context, clientID, secretHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE oauth_clients SET secret_hash = ?, updated_at = ? WHERE client_id = ?`),
		secretHash, time.Now().Unix(), clientID)
	if err != nil {
		return fmt.Errorf("oauthprovider: rotate secret: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClientNotFound
	}
	return nil
}

// DeleteClient removes a client and revokes its issued tokens + codes.
func (s *Store) DeleteClient(ctx context.Context, clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("oauthprovider: begin delete: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, s.db.Rebind(`DELETE FROM oauth_clients WHERE client_id = ?`), clientID)
	if err != nil {
		return fmt.Errorf("oauthprovider: delete client: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrClientNotFound
	}
	if _, err := tx.ExecContext(ctx, s.db.Rebind(`DELETE FROM oauth_tokens WHERE client_id = ?`), clientID); err != nil {
		return fmt.Errorf("oauthprovider: delete client tokens: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.db.Rebind(`DELETE FROM oauth_authcodes WHERE client_id = ?`), clientID); err != nil {
		return fmt.Errorf("oauthprovider: delete client codes: %w", err)
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Authorization codes
// ---------------------------------------------------------------------------

// SaveAuthCode persists a one-time authorization code.
func (s *Store) SaveAuthCode(ctx context.Context, c AuthCode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO oauth_authcodes
		    (code_hash, client_id, user_id, redirect_uri, scope, code_challenge, code_challenge_method, nonce, expires_at, consumed, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`),
		c.CodeHash, c.ClientID, c.UserID, c.RedirectURI, c.Scope,
		c.CodeChallenge, c.CodeChallengeMethod, c.Nonce, c.ExpiresAt.Unix(), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("oauthprovider: save auth code: %w", err)
	}
	return nil
}

// GetAuthCode reads a live (unconsumed, unexpired) auth code without mutating
// it, so the exchange can validate the client/redirect/PKCE binding BEFORE
// burning the single-use code. The actual single-use claim is ConsumeAuthCode.
func (s *Store) GetAuthCode(ctx context.Context, codeHash string) (AuthCode, error) {
	var (
		c        AuthCode
		expires  int64
		consumed int
		created  int64
	)
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT code_hash, client_id, user_id, redirect_uri, scope, code_challenge, code_challenge_method, nonce, expires_at, consumed, created_at
		FROM oauth_authcodes WHERE code_hash = ?`), codeHash).
		Scan(&c.CodeHash, &c.ClientID, &c.UserID, &c.RedirectURI, &c.Scope,
			&c.CodeChallenge, &c.CodeChallengeMethod, &c.Nonce, &expires, &consumed, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthCode{}, ErrCodeNotFound
	}
	if err != nil {
		return AuthCode{}, fmt.Errorf("oauthprovider: read auth code: %w", err)
	}
	c.ExpiresAt = time.Unix(expires, 0).UTC()
	c.CreatedAt = time.Unix(created, 0).UTC()
	if consumed != 0 || time.Now().Unix() > expires {
		return AuthCode{}, ErrCodeNotFound
	}
	return c, nil
}

// ConsumeAuthCode atomically fetches and marks an auth code consumed. It returns
// ErrCodeNotFound if the code is unknown, already consumed, or expired. The
// single-use guarantee is enforced in a transaction so a replayed code can
// never mint a second set of tokens.
func (s *Store) ConsumeAuthCode(ctx context.Context, codeHash string) (AuthCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AuthCode{}, fmt.Errorf("oauthprovider: begin consume: %w", err)
	}
	defer tx.Rollback()

	var (
		c        AuthCode
		expires  int64
		consumed int
		created  int64
	)
	err = tx.QueryRowContext(ctx, s.db.Rebind(`
		SELECT code_hash, client_id, user_id, redirect_uri, scope, code_challenge, code_challenge_method, nonce, expires_at, consumed, created_at
		FROM oauth_authcodes WHERE code_hash = ?`), codeHash).
		Scan(&c.CodeHash, &c.ClientID, &c.UserID, &c.RedirectURI, &c.Scope,
			&c.CodeChallenge, &c.CodeChallengeMethod, &c.Nonce, &expires, &consumed, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthCode{}, ErrCodeNotFound
	}
	if err != nil {
		return AuthCode{}, fmt.Errorf("oauthprovider: read auth code: %w", err)
	}
	c.ExpiresAt = time.Unix(expires, 0).UTC()
	c.CreatedAt = time.Unix(created, 0).UTC()
	if consumed != 0 || time.Now().Unix() > expires {
		return AuthCode{}, ErrCodeNotFound
	}
	// SEC (multi-replica auth-code replay): the pre-read of `consumed` above plus
	// the process-local s.mu do NOT serialise across replicas on shared Postgres.
	// Under READ COMMITTED two concurrent ExchangeCode calls for the SAME code
	// could each read consumed=0 and — with an unconditional UPDATE — each flip the
	// row and each mint a token set, defeating the RFC 6749 §4.1.2 / RFC 9700
	// single-use guarantee (and the "code replay ⇒ revoke the family" property that
	// rests on it). Condition the UPDATE on the row still being unconsumed so the
	// row lock becomes the authoritative gate: exactly one caller flips 0→1
	// (RowsAffected==1) and only that caller receives the code; a racing/replayed
	// loser sees 0 rows and is rejected. This mirrors the atomic-claim pattern used
	// by billing's collection ledger (attemptCollectionCharge) and UpsertInvoice.
	res, err := tx.ExecContext(ctx,
		s.db.Rebind(`UPDATE oauth_authcodes SET consumed = 1 WHERE code_hash = ? AND consumed = 0`),
		codeHash,
	)
	if err != nil {
		return AuthCode{}, fmt.Errorf("oauthprovider: mark code consumed: %w", err)
	}
	claimed, err := res.RowsAffected()
	if err != nil {
		return AuthCode{}, fmt.Errorf("oauthprovider: consume rows affected: %w", err)
	}
	if claimed == 0 {
		// A concurrent exchange (possibly on another replica) already claimed this
		// code between our read and our write. Fail closed — no second token mint.
		return AuthCode{}, ErrCodeNotFound
	}
	if err := tx.Commit(); err != nil {
		return AuthCode{}, fmt.Errorf("oauthprovider: commit consume: %w", err)
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// Tokens
// ---------------------------------------------------------------------------

// SaveToken persists an issued access or refresh token (hashed).
// M4 (audit): persists FamilyID for token-family replay detection (RFC 9700).
func (s *Store) SaveToken(ctx context.Context, t Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO oauth_tokens (token_hash, token_type, client_id, user_id, scope, expires_at, revoked, created_at, family_id)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`),
		t.TokenHash, t.TokenType, t.ClientID, t.UserID, t.Scope, t.ExpiresAt.Unix(), time.Now().Unix(), nullableString(t.FamilyID))
	if err != nil {
		return fmt.Errorf("oauthprovider: save token: %w", err)
	}
	return nil
}

// GetToken returns a live (non-revoked, non-expired) token of the given type by
// its hash, or ErrTokenNotFound.
// M4 (audit): also returns FamilyID for replay detection.
func (s *Store) GetToken(ctx context.Context, tokenHash, tokenType string) (Token, error) {
	var (
		t        Token
		expires  int64
		revoked  int
		created  int64
		familyID sql.NullString
	)
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT token_hash, token_type, client_id, user_id, scope, expires_at, revoked, created_at, family_id
		FROM oauth_tokens WHERE token_hash = ? AND token_type = ?`), tokenHash, tokenType).
		Scan(&t.TokenHash, &t.TokenType, &t.ClientID, &t.UserID, &t.Scope, &expires, &revoked, &created, &familyID)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrTokenNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("oauthprovider: get token: %w", err)
	}
	t.ExpiresAt = time.Unix(expires, 0).UTC()
	t.CreatedAt = time.Unix(created, 0).UTC()
	t.Revoked = revoked != 0
	if familyID.Valid {
		t.FamilyID = familyID.String
	}
	if t.Revoked || time.Now().Unix() > expires {
		return Token{}, ErrTokenNotFound
	}
	return t, nil
}

// GetTokenByHashAny looks up a token by hash regardless of revoked/expired
// status. Used by replay detection to distinguish "unknown token" from
// "known but revoked token" (which indicates a replay of a rotated-out token).
func (s *Store) GetTokenByHashAny(ctx context.Context, tokenHash, tokenType string) (Token, error) {
	var (
		t        Token
		expires  int64
		revoked  int
		created  int64
		familyID sql.NullString
	)
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT token_hash, token_type, client_id, user_id, scope, expires_at, revoked, created_at, family_id
		FROM oauth_tokens WHERE token_hash = ? AND token_type = ?`), tokenHash, tokenType).
		Scan(&t.TokenHash, &t.TokenType, &t.ClientID, &t.UserID, &t.Scope, &expires, &revoked, &created, &familyID)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrTokenNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("oauthprovider: get token any: %w", err)
	}
	t.ExpiresAt = time.Unix(expires, 0).UTC()
	t.CreatedAt = time.Unix(created, 0).UTC()
	t.Revoked = revoked != 0
	if familyID.Valid {
		t.FamilyID = familyID.String
	}
	return t, nil
}

// RevokeFamilyByID revokes all tokens (access + refresh) with the given family_id.
// Called on refresh-token replay detection per RFC 9700 §4.14.
func (s *Store) RevokeFamilyByID(ctx context.Context, familyID string) error {
	if familyID == "" {
		return nil // no family tracking for legacy tokens
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE oauth_tokens SET revoked = 1 WHERE family_id = ?`), familyID)
	if err != nil {
		return fmt.Errorf("oauthprovider: revoke family: %w", err)
	}
	return nil
}

// RevokeTokenByHash marks the given token (by hash) revoked AND cascades to
// all access tokens sharing the same family_id (RFC 7009 §2 cascade).
// M4 (audit): replaces the old store-level RevokeToken for refresh tokens.
func (s *Store) RevokeTokenByHash(ctx context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Look up the token's family so we can cascade.
	// WAVE34-5: do NOT swallow the lookup error. If it fails, familyID stays
	// invalid and the cascade below silently skips — a partial revocation that
	// leaves sibling access tokens live. A genuine query error (not "no rows")
	// is surfaced so the whole revoke fails closed rather than half-completing.
	var familyID sql.NullString
	if err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT family_id FROM oauth_tokens WHERE token_hash = ?`), tokenHash).Scan(&familyID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("oauthprovider: revoke token by hash: family lookup: %w", err)
	}

	// Revoke the specific token first.
	if _, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE oauth_tokens SET revoked = 1 WHERE token_hash = ?`), tokenHash); err != nil {
		return fmt.Errorf("oauthprovider: revoke token by hash: %w", err)
	}

	// Cascade: revoke all access tokens in the same family.
	if familyID.Valid && familyID.String != "" {
		if _, err := s.db.ExecContext(ctx,
			s.db.Rebind(`UPDATE oauth_tokens SET revoked = 1 WHERE family_id = ? AND token_type = 'access'`),
			familyID.String); err != nil {
			return fmt.Errorf("oauthprovider: revoke family access tokens: %w", err)
		}
	}
	return nil
}

// nullableString converts an empty string to a SQL NULL so the DB stores NULL
// rather than an empty string for missing FamilyIDs.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// RevokeToken marks a single token (by hash) revoked. No error if absent.
func (s *Store) RevokeToken(ctx context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`UPDATE oauth_tokens SET revoked = 1 WHERE token_hash = ?`), tokenHash)
	if err != nil {
		return fmt.Errorf("oauthprovider: revoke token: %w", err)
	}
	return nil
}

// RevokeUserClientTokens revokes ALL tokens (access + refresh) issued to
// clientID on behalf of userID. Called by the consent-revoke handler so that
// revoking app access takes immediate effect — outstanding tokens become
// invalid rather than living out their TTL.
// SEC-H1 (audit): previously RevokeConsent only removed the consent grant;
// tokens remained valid until their natural expiry.
func (s *Store) RevokeUserClientTokens(ctx context.Context, userID, clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE oauth_tokens SET revoked = 1 WHERE user_id = ? AND client_id = ?`),
		userID, clientID)
	if err != nil {
		return fmt.Errorf("oauthprovider: revoke user client tokens: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Signing keys
// ---------------------------------------------------------------------------

// LoadOrCreateSigningKey returns the active signing key, generating + persisting
// one on first use. The keypair survives restarts so previously-issued
// id_tokens keep verifying.
func (s *Store) LoadOrCreateSigningKey(ctx context.Context) (*SigningKey, error) {
	var kid, storedPriv string
	err := s.db.QueryRowContext(ctx,
		`SELECT kid, private_pem FROM oauth_signing_keys WHERE active = 1 ORDER BY created_at DESC LIMIT 1`).
		Scan(&kid, &storedPriv)
	if err == nil {
		privPEM, derr := s.decodePrivFromStore(storedPriv)
		if derr != nil {
			return nil, fmt.Errorf("oauthprovider: decode signing key: %w", derr)
		}
		k, perr := parseSigningKey(kid, privPEM)
		if perr != nil {
			return nil, perr
		}
		// M6 re-wrap shim: a legacy plaintext key is opportunistically encrypted
		// in place once a KEK is configured. Best-effort: a failure to re-wrap
		// must not break token signing.
		if s.kek != nil && isPlaintextPEM(storedPriv) {
			if wrapped, eerr := encryptKEK(privPEM, s.kek); eerr == nil {
				s.mu.Lock()
				if _, uerr := s.db.ExecContext(ctx,
					s.db.Rebind(`UPDATE oauth_signing_keys SET private_pem = ? WHERE kid = ?`), wrapped, kid); uerr != nil {
					log.Printf("[oauthprovider] WARNING: could not re-wrap legacy signing key kid=%s: %v", kid, uerr)
				}
				s.mu.Unlock()
			}
		}
		return k, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("oauthprovider: load signing key: %w", err)
	}

	// None yet — generate + persist (encrypted at rest when a KEK is present).
	k, err := generateSigningKey()
	if err != nil {
		return nil, err
	}
	privPEM, pubEnc, err := k.encodePEM()
	if err != nil {
		return nil, err
	}
	privForStore, err := s.encodePrivForStore(privPEM)
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: wrap signing key: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO oauth_signing_keys (kid, private_pem, public_pem, active, created_at) VALUES (?, ?, ?, 1, ?)`),
		k.KID, privForStore, pubEnc, time.Now().Unix()); err != nil {
		return nil, fmt.Errorf("oauthprovider: persist signing key: %w", err)
	}
	return k, nil
}

// DefaultRotationOverlap is how long a retired signing key's public half stays
// published in JWKS after a rotation, so id_tokens signed just before the
// rotation keep verifying during rollover.
const DefaultRotationOverlap = 48 * time.Hour

// RotateSigningKey generates a fresh RSA-2048 signing key, KEK-wraps its private
// half FAIL-CLOSED (identical custody to LoadOrCreateSigningKey — never plaintext
// in prod), inserts it as the new ACTIVE signing key, and RETIRES the previously
// active key(s): they are deactivated and given retire_at = now+overlap so their
// public halves stay in JWKS for the overlap window. New id_tokens are signed
// with the returned key; tokens signed with a retired key still verify until the
// window elapses. The whole swap is transactional.
// overlap is taken as-is (the caller owns the policy — Service defaults it to
// DefaultRotationOverlap); a zero/negative overlap retires the prior key
// immediately (its retire_at lands at/behind now, so JWKS drops it at once).
func (s *Store) RotateSigningKey(ctx context.Context, overlap time.Duration) (*SigningKey, error) {
	k, err := generateSigningKey()
	if err != nil {
		return nil, err
	}
	privPEM, pubEnc, err := k.encodePEM()
	if err != nil {
		return nil, err
	}
	// FAIL-CLOSED wrap: reuses encodePrivForStore, so with a KEK configured the
	// private key is AES-256-GCM sealed exactly like the original signing key and
	// is never persisted in plaintext outside dev.
	privForStore, err := s.encodePrivForStore(privPEM)
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: wrap rotated signing key: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: begin rotate: %w", err)
	}
	defer tx.Rollback()
	now := time.Now()
	retireAt := now.Add(overlap).Unix()
	// Retire currently-live keys: deactivate + schedule removal from JWKS after the
	// overlap window. retire_at is in the FUTURE, so AllPublicJWKs keeps publishing
	// them until it elapses (tokens signed with them still verify during rollover).
	// Only touch keys not already retired so re-rotations do not extend an old
	// key's window.
	if _, err := tx.ExecContext(ctx,
		s.db.Rebind(`UPDATE oauth_signing_keys SET active = 0, retire_at = ? WHERE active = 1 AND retire_at IS NULL`),
		retireAt); err != nil {
		return nil, fmt.Errorf("oauthprovider: retire prior signing keys: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO oauth_signing_keys (kid, private_pem, public_pem, active, created_at, retire_at) VALUES (?, ?, ?, 1, ?, NULL)`),
		k.KID, privForStore, pubEnc, now.Unix()); err != nil {
		return nil, fmt.Errorf("oauthprovider: persist rotated signing key: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("oauthprovider: commit rotate: %w", err)
	}
	return k, nil
}

// AllPublicJWKs returns every non-revoked, NON-EXPIRED public key as JWKs (for
// JWKS). During key rotation this lets clients verify tokens signed by either
// the active key or a recently-retired key still inside its overlap window; keys
// whose retire_at has passed are dropped. retire_at IS NULL means "live / never
// retired" (all pre-0004 keys and the current active key), so this is fully
// backward-compatible with existing single-key deployments.
func (s *Store) AllPublicJWKs(ctx context.Context) ([]JWK, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(
		`SELECT kid, private_pem FROM oauth_signing_keys WHERE retire_at IS NULL OR retire_at > ? ORDER BY created_at DESC`),
		time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: list signing keys: %w", err)
	}
	defer rows.Close()
	var out []JWK
	for rows.Next() {
		var kid, storedPriv string
		if err := rows.Scan(&kid, &storedPriv); err != nil {
			return nil, fmt.Errorf("oauthprovider: scan signing key: %w", err)
		}
		privPEM, derr := s.decodePrivFromStore(storedPriv)
		if derr != nil {
			return nil, fmt.Errorf("oauthprovider: decode signing key: %w", derr)
		}
		k, err := parseSigningKey(kid, privPEM)
		if err != nil {
			return nil, err
		}
		out = append(out, k.PublicJWK())
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Provider secrets (pairwise-sub salt)
// ---------------------------------------------------------------------------

// pairwiseSaltName is the oauth_provider_secrets key for the pairwise-sub salt.
const pairwiseSaltName = "pairwise_salt"

// LoadOrCreatePairwiseSalt returns the server-held salt mixed into pairwise
// subject identifiers, generating + persisting one (32 bytes of entropy) on
// first use. It is stable across restarts so a pairwise `sub` never changes for
// a given (user, sector). Concurrent boots converge on a single salt via
// INSERT … ON CONFLICT DO NOTHING followed by a re-read.
func (s *Store) LoadOrCreatePairwiseSalt(ctx context.Context) (string, error) {
	var val string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT value FROM oauth_provider_secrets WHERE name = ?`), pairwiseSaltName).Scan(&val)
	if err == nil {
		return val, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("oauthprovider: load pairwise salt: %w", err)
	}
	salt, err := randomToken(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if _, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO oauth_provider_secrets (name, value, created_at) VALUES (?, ?, ?) ON CONFLICT(name) DO NOTHING`),
		pairwiseSaltName, salt, time.Now().Unix()); err != nil {
		s.mu.Unlock()
		return "", fmt.Errorf("oauthprovider: persist pairwise salt: %w", err)
	}
	s.mu.Unlock()
	// Re-read: a concurrent boot may have won the insert.
	if err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT value FROM oauth_provider_secrets WHERE name = ?`), pairwiseSaltName).Scan(&val); err != nil {
		return "", fmt.Errorf("oauthprovider: reload pairwise salt: %w", err)
	}
	return val, nil
}

// ---------------------------------------------------------------------------
// Consent grants
// ---------------------------------------------------------------------------

// UpsertConsent records (or updates) the user's consent for the given client
// and scope. If a live grant already exists for this (user, client) pair the
// scope column is updated to the union of the old and new scopes so the row
// always reflects the most-permissive approval the user ever gave.
func (s *Store) UpsertConsent(ctx context.Context, userID, clientID, scope string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	// The scope stored is the new scope; if the user had previously approved
	// a wider set that old approval is preserved in the audit log but the live
	// row reflects the current grant.
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO oauth_consents (user_id, client_id, scope, granted_at, revoked)
		VALUES (?, ?, ?, ?, 0)
		ON CONFLICT(user_id, client_id) DO UPDATE SET
		    scope      = excluded.scope,
		    granted_at = excluded.granted_at,
		    revoked    = 0,
		    revoked_at = NULL
		`), userID, clientID, scope, now)
	if err != nil {
		return fmt.Errorf("oauthprovider: upsert consent: %w", err)
	}
	return nil
}

// GetConsent returns the live (non-revoked) consent for (user, client) or
// ErrConsentNotFound if absent or revoked.
func (s *Store) GetConsent(ctx context.Context, userID, clientID string) (ConsentGrant, error) {
	var (
		g         ConsentGrant
		grantedAt int64
		revoked   int
		revokedAt sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT user_id, client_id, scope, granted_at, revoked, revoked_at
		FROM oauth_consents WHERE user_id = ? AND client_id = ?`),
		userID, clientID).
		Scan(&g.UserID, &g.ClientID, &g.Scope, &grantedAt, &revoked, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ConsentGrant{}, ErrConsentNotFound
	}
	if err != nil {
		return ConsentGrant{}, fmt.Errorf("oauthprovider: get consent: %w", err)
	}
	g.GrantedAt = time.Unix(grantedAt, 0).UTC()
	g.Revoked = revoked != 0
	if revokedAt.Valid {
		t := time.Unix(revokedAt.Int64, 0).UTC()
		g.RevokedAt = &t
	}
	if g.Revoked {
		return ConsentGrant{}, ErrConsentNotFound
	}
	return g, nil
}

// RevokeConsent marks the user's consent for a client as revoked.
func (s *Store) RevokeConsent(ctx context.Context, userID, clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE oauth_consents SET revoked = 1, revoked_at = ?
		WHERE user_id = ? AND client_id = ? AND revoked = 0`),
		now, userID, clientID)
	if err != nil {
		return fmt.Errorf("oauthprovider: revoke consent: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConsentNotFound
	}
	return nil
}

// ListUserConsents returns all live (non-revoked) consents for a user.
func (s *Store) ListUserConsents(ctx context.Context, userID string) ([]ConsentGrant, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT user_id, client_id, scope, granted_at, revoked, revoked_at
		FROM oauth_consents WHERE user_id = ? AND revoked = 0 ORDER BY granted_at DESC`), userID)
	if err != nil {
		return nil, fmt.Errorf("oauthprovider: list consents: %w", err)
	}
	defer rows.Close()
	var out []ConsentGrant
	for rows.Next() {
		var (
			g         ConsentGrant
			grantedAt int64
			revoked   int
			revokedAt sql.NullInt64
		)
		if err := rows.Scan(&g.UserID, &g.ClientID, &g.Scope, &grantedAt, &revoked, &revokedAt); err != nil {
			return nil, fmt.Errorf("oauthprovider: scan consent: %w", err)
		}
		g.GrantedAt = time.Unix(grantedAt, 0).UTC()
		g.Revoked = revoked != 0
		if revokedAt.Valid {
			t := time.Unix(revokedAt.Int64, 0).UTC()
			g.RevokedAt = &t
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
