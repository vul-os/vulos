package appsplatform

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// Postgres seam for cloud consolidation.
//
// When DATABASE_URL (or VULOS_DATABASE_URL) is set, the apps platform stores its
// registry in a Postgres database (one Neon database shared across Vulos
// products) under a DEDICATED SCHEMA "apps", so product tables never collide.
// When neither is set, the in-repo SQLite StandaloneRegistry (store_standalone.go)
// is used unchanged — open-core / self-host stays fully intact, and the OS, which
// embeds apps with its own store, keeps calling NewStandaloneRegistry directly.
//
// The security posture is identical to the SQLite path:
//   - vat_ app tokens are stored as a sha256 HASH only (never the plaintext);
//   - vas_ signing secrets are envelope-encrypted at rest with AES-256-GCM
//     (AESGCMEncryptor seeded from VULOS_APPS_KEK), and persisting a non-empty
//     signing secret with NO encryptor configured is refused (fail-loud);
//   - token TTL / issued-at / expires-at timing fields are persisted.

// Environment variables selecting the Postgres backend. DATABASE_URL takes
// precedence; VULOS_DATABASE_URL is the namespaced fallback.
const (
	DatabaseURLEnv    = "DATABASE_URL"
	DatabaseURLEnvAlt = "VULOS_DATABASE_URL"
)

// PGSchema is the dedicated Postgres schema the apps registry owns. It lets a
// single Neon database host several Vulos products without table collisions.
const PGSchema = "apps"

// pgCols is the canonical column ordering shared by SELECT, INSERT and scanApp.
const pgCols = `id, name, icon, description, owner_id, org_id, scopes_json, ` +
	`products_json, events_json, slash_json, webhook_url, incoming_webhook_id, ` +
	`incoming_webhook_enabled, default_target, token_hash, signing_secret, created_at, ` +
	`token_issued_at, token_expires_at, token_ttl_ns`

// PostgresRegistry is a Postgres-backed Registry for cloud deployments. Unlike
// StandaloneRegistry it holds no in-memory index — every lookup hits the shared
// database so multiple product instances see a single source of truth.
type PostgresRegistry struct {
	db        *sql.DB
	scopeSet  ScopeSet
	encryptor SigningSecretEncryptor // nil = no encryptor (cleartext persist refused)
}

var _ Registry = (*PostgresRegistry)(nil)

// NewPostgresRegistry opens a Postgres connection (pgx stdlib driver), ensures
// the "apps" schema + tables exist, and returns a Registry.
//
// Signing-secret encryption is auto-enabled from VULOS_APPS_KEK (a 64-hex-char
// AES-256 key); WithSigningSecretEncryptor overrides it. Persisting a non-empty
// signing secret without any encryptor is refused (see persist).
func NewPostgresRegistry(dsn string, opts ...Option) (*PostgresRegistry, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("appsplatform: open postgres: %w", err)
	}
	// Resolve scope set + encryptor through the shared Option type. Option mutates
	// a *StandaloneRegistry, so we use a throw-away one purely as a config carrier
	// (only its scopeSet/encryptor fields are read back). Env-seeded encryption is
	// applied first so WithSigningSecretEncryptor can override it.
	probe := &StandaloneRegistry{scopeSet: DefaultScopeSet()}
	enc, err := encryptorFromEnv()
	if err != nil {
		db.Close()
		return nil, err
	}
	probe.encryptor = enc
	for _, o := range opts {
		o(probe)
	}
	r := &PostgresRegistry{db: db, scopeSet: probe.scopeSet, encryptor: probe.encryptor}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("appsplatform: ping postgres: %w", err)
	}
	if err := r.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return r, nil
}

// Close releases the underlying database handle.
func (r *PostgresRegistry) Close() error {
	if r.db == nil {
		return nil
	}
	return r.db.Close()
}

// migrate creates the dedicated schema and the apps table. All statements are
// idempotent (IF NOT EXISTS) so it doubles as the upgrade path.
func (r *PostgresRegistry) migrate() error {
	stmts := []string{
		`CREATE SCHEMA IF NOT EXISTS apps`,
		`CREATE TABLE IF NOT EXISTS apps.apps (
			id                       TEXT PRIMARY KEY,
			name                     TEXT    NOT NULL DEFAULT '',
			icon                     TEXT    NOT NULL DEFAULT '',
			description              TEXT    NOT NULL DEFAULT '',
			owner_id                 TEXT    NOT NULL DEFAULT '',
			org_id                   TEXT    NOT NULL DEFAULT '',
			scopes_json              TEXT    NOT NULL DEFAULT '[]',
			products_json            TEXT    NOT NULL DEFAULT '[]',
			events_json              TEXT    NOT NULL DEFAULT '[]',
			slash_json               TEXT    NOT NULL DEFAULT '[]',
			webhook_url              TEXT    NOT NULL DEFAULT '',
			incoming_webhook_id      TEXT    NOT NULL DEFAULT '',
			incoming_webhook_enabled BOOLEAN NOT NULL DEFAULT FALSE,
			default_target           TEXT    NOT NULL DEFAULT '',
			token_hash               TEXT    NOT NULL DEFAULT '',
			signing_secret           TEXT    NOT NULL DEFAULT '',
			created_at               BIGINT  NOT NULL DEFAULT 0,
			token_issued_at          BIGINT  NOT NULL DEFAULT 0,
			token_expires_at         BIGINT  NOT NULL DEFAULT 0,
			token_ttl_ns             BIGINT  NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_apps_token_hash ON apps.apps(token_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_apps_webhook    ON apps.apps(incoming_webhook_id)`,
		`CREATE INDEX IF NOT EXISTS idx_apps_owner      ON apps.apps(owner_id)`,
	}
	for _, s := range stmts {
		if _, err := r.db.Exec(s); err != nil {
			return fmt.Errorf("appsplatform: postgres migrate: %w", err)
		}
	}
	return nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// execer is satisfied by both *sql.DB and *sql.Tx so persist works inside or
// outside a transaction.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// scanApp materializes an *App from a row, decrypting the signing secret.
func (r *PostgresRegistry) scanApp(s rowScanner) (*App, error) {
	a := &App{}
	var scopesJSON, productsJSON, eventsJSON, slashJSON, signingSecret string
	var incEnabled bool
	var created, tokenIssuedNs, tokenExpiresNs, tokenTTLNs int64
	if err := s.Scan(&a.ID, &a.Name, &a.Icon, &a.Description, &a.OwnerID, &a.OrgID, &scopesJSON,
		&productsJSON, &eventsJSON, &slashJSON, &a.WebhookURL, &a.Incoming.ID,
		&incEnabled, &a.DefaultTarget, &a.TokenHash, &signingSecret, &created,
		&tokenIssuedNs, &tokenExpiresNs, &tokenTTLNs); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(scopesJSON), &a.Scopes)
	_ = json.Unmarshal([]byte(productsJSON), &a.Products)
	_ = json.Unmarshal([]byte(eventsJSON), &a.Events)
	_ = json.Unmarshal([]byte(slashJSON), &a.SlashCommands)
	a.Incoming.Enabled = incEnabled
	a.CreatedAt = time.Unix(0, created)
	if tokenIssuedNs != 0 {
		a.TokenIssuedAt = time.Unix(0, tokenIssuedNs)
	}
	if tokenExpiresNs != 0 {
		a.TokenExpiresAt = time.Unix(0, tokenExpiresNs)
	}
	a.TokenTTL = time.Duration(tokenTTLNs)
	plain, err := decodeSigningSecret(r.encryptor, signingSecret)
	if err != nil {
		return nil, fmt.Errorf("appsplatform: decrypt signing secret for app %s: %w", a.ID, err)
	}
	a.SigningSecret = plain
	return a, nil
}

// persist upserts an app row. The signing secret is envelope-encrypted before it
// touches durable storage; cleartext persistence is refused (fail-loud).
func (r *PostgresRegistry) persist(ex execer, a *App) error {
	scopesJSON, _ := json.Marshal(a.Scopes)
	productsJSON, _ := json.Marshal(a.Products)
	eventsJSON, _ := json.Marshal(a.Events)
	slashJSON, _ := json.Marshal(a.SlashCommands)
	signingSecret, err := encodeSigningSecret(r.encryptor, a.SigningSecret)
	if err != nil {
		return err
	}
	tokenIssuedNs := int64(0)
	if !a.TokenIssuedAt.IsZero() {
		tokenIssuedNs = a.TokenIssuedAt.UnixNano()
	}
	tokenExpiresNs := int64(0)
	if !a.TokenExpiresAt.IsZero() {
		tokenExpiresNs = a.TokenExpiresAt.UnixNano()
	}
	_, err = ex.Exec(
		`INSERT INTO apps.apps (`+pgCols+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
		 ON CONFLICT (id) DO UPDATE SET name=excluded.name, icon=excluded.icon,
			description=excluded.description, owner_id=excluded.owner_id, org_id=excluded.org_id,
			scopes_json=excluded.scopes_json, products_json=excluded.products_json,
			events_json=excluded.events_json, slash_json=excluded.slash_json,
			webhook_url=excluded.webhook_url, incoming_webhook_id=excluded.incoming_webhook_id,
			incoming_webhook_enabled=excluded.incoming_webhook_enabled,
			default_target=excluded.default_target, token_hash=excluded.token_hash,
			signing_secret=excluded.signing_secret, token_issued_at=excluded.token_issued_at,
			token_expires_at=excluded.token_expires_at, token_ttl_ns=excluded.token_ttl_ns`,
		a.ID, a.Name, a.Icon, a.Description, a.OwnerID, a.OrgID, string(scopesJSON),
		string(productsJSON), string(eventsJSON), string(slashJSON), a.WebhookURL, a.Incoming.ID,
		a.Incoming.Enabled, a.DefaultTarget, a.TokenHash, signingSecret, a.CreatedAt.UnixNano(),
		tokenIssuedNs, tokenExpiresNs, int64(a.TokenTTL))
	return err
}

// Create implements Registry.
func (r *PostgresRegistry) Create(p CreateParams) (*Created, error) {
	scopes, err := r.scopeSet.Normalize(p.Scopes)
	if err != nil {
		return nil, err
	}
	products, err := NormalizeProducts(p.Products)
	if err != nil {
		return nil, err
	}
	webhookURL := strings.TrimSpace(p.WebhookURL)
	if err := ValidateWebhookURL(webhookURL); err != nil {
		return nil, err
	}
	token := GenerateToken()
	secret := GenerateSecret()
	now := time.Now()
	a := &App{
		ID:            GenerateAppID(),
		Name:          strings.TrimSpace(p.Name),
		Icon:          strings.TrimSpace(p.Icon),
		Description:   strings.TrimSpace(p.Description),
		OwnerID:       p.OwnerID,
		OrgID:         p.OrgID,
		Scopes:        scopes,
		Products:      products,
		Events:        NormalizeEvents(p.Events),
		SlashCommands: NormalizeSlashCommands(p.SlashCommands),
		WebhookURL:    webhookURL,
		Incoming: IncomingWebhook{
			ID:      GenerateWebhookID(),
			Enabled: p.IncomingEnabled,
		},
		DefaultTarget: strings.TrimSpace(p.DefaultTarget),
		CreatedAt:     now,
		TokenHash:     HashToken(token),
		SigningSecret: secret,
		TokenIssuedAt: now,
		TokenTTL:      p.TokenTTL,
	}
	if p.TokenTTL > 0 {
		a.TokenExpiresAt = now.Add(p.TokenTTL)
	}
	if err := r.persist(r.db, a); err != nil {
		return nil, err
	}
	return &Created{App: a, Token: token, SigningSecret: secret}, nil
}

// Get implements Registry.
func (r *PostgresRegistry) Get(id string) (*App, error) {
	row := r.db.QueryRow(`SELECT `+pgCols+` FROM apps.apps WHERE id = $1`, id)
	a, err := r.scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// GetByTokenHash implements Registry. token_hash is a sha256 hash (not the
// secret itself), so an indexed equality lookup is safe — the plaintext token is
// never stored or compared.
func (r *PostgresRegistry) GetByTokenHash(tokenHash string) (*App, error) {
	if tokenHash == "" {
		return nil, ErrNotFound
	}
	row := r.db.QueryRow(`SELECT `+pgCols+` FROM apps.apps WHERE token_hash = $1`, tokenHash)
	a, err := r.scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// GetByIncomingWebhookID implements Registry.
func (r *PostgresRegistry) GetByIncomingWebhookID(webhookID string) (*App, error) {
	if webhookID == "" {
		return nil, ErrNotFound
	}
	row := r.db.QueryRow(`SELECT `+pgCols+` FROM apps.apps WHERE incoming_webhook_id = $1`, webhookID)
	a, err := r.scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// List implements Registry.
func (r *PostgresRegistry) List(owner string, isAdmin bool) ([]*App, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if isAdmin {
		rows, err = r.db.Query(`SELECT ` + pgCols + ` FROM apps.apps ORDER BY created_at`)
	} else {
		rows, err = r.db.Query(`SELECT `+pgCols+` FROM apps.apps WHERE owner_id = $1 ORDER BY created_at`, owner)
	}
	if err != nil {
		return nil, fmt.Errorf("appsplatform: list: %w", err)
	}
	defer rows.Close()
	out := make([]*App, 0)
	for rows.Next() {
		a, err := r.scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// mutate locks a row, applies fn, and persists it inside one transaction. fn
// receives the live *App to mutate; returning an error aborts the change.
func (r *PostgresRegistry) mutate(id string, fn func(a *App) error) (*App, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck — rollback after commit is a no-op
	row := tx.QueryRow(`SELECT `+pgCols+` FROM apps.apps WHERE id = $1 FOR UPDATE`, id)
	a, err := r.scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := fn(a); err != nil {
		return nil, err
	}
	if err := r.persist(tx, a); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return a, nil
}

// Update implements Registry.
func (r *PostgresRegistry) Update(id string, p UpdateParams) (*App, error) {
	return r.mutate(id, func(a *App) error {
		if p.Name != nil {
			a.Name = strings.TrimSpace(*p.Name)
		}
		if p.Icon != nil {
			a.Icon = strings.TrimSpace(*p.Icon)
		}
		if p.Description != nil {
			a.Description = strings.TrimSpace(*p.Description)
		}
		if p.Scopes != nil {
			scopes, err := r.scopeSet.Normalize(*p.Scopes)
			if err != nil {
				return err
			}
			a.Scopes = scopes
		}
		if p.Products != nil {
			products, err := NormalizeProducts(*p.Products)
			if err != nil {
				return err
			}
			a.Products = products
		}
		if p.Events != nil {
			a.Events = NormalizeEvents(*p.Events)
		}
		if p.SlashCommands != nil {
			a.SlashCommands = NormalizeSlashCommands(*p.SlashCommands)
		}
		if p.WebhookURL != nil {
			webhookURL := strings.TrimSpace(*p.WebhookURL)
			if err := ValidateWebhookURL(webhookURL); err != nil {
				return err
			}
			a.WebhookURL = webhookURL
		}
		if p.DefaultTarget != nil {
			a.DefaultTarget = strings.TrimSpace(*p.DefaultTarget)
		}
		if p.IncomingEnabled != nil {
			a.Incoming.Enabled = *p.IncomingEnabled
		}
		if p.TokenTTL != nil {
			// Retroactive age-revocation: the absolute TokenExpiresAt is left as-is
			// (it reflects the current token's issuance); the middleware's age check
			// enforces the new TTL against TokenIssuedAt.
			a.TokenTTL = *p.TokenTTL
		}
		return nil
	})
}

// Delete implements Registry.
func (r *PostgresRegistry) Delete(id string) error {
	res, err := r.db.Exec(`DELETE FROM apps.apps WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateToken implements Registry.
func (r *PostgresRegistry) RotateToken(id string) (string, error) {
	var token string
	_, err := r.mutate(id, func(a *App) error {
		token = GenerateToken()
		now := time.Now()
		a.TokenHash = HashToken(token)
		a.TokenIssuedAt = now
		if a.TokenTTL > 0 {
			a.TokenExpiresAt = now.Add(a.TokenTTL)
		} else {
			a.TokenExpiresAt = time.Time{} // clear any previous absolute expiry
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// RotateSecret implements Registry.
func (r *PostgresRegistry) RotateSecret(id string) (string, error) {
	var secret string
	_, err := r.mutate(id, func(a *App) error {
		secret = GenerateSecret()
		a.SigningSecret = secret
		return nil
	})
	if err != nil {
		return "", err
	}
	return secret, nil
}

// ResolveSlashCommand implements Registry.
func (r *PostgresRegistry) ResolveSlashCommand(product, name string) (*App, *SlashCommand, bool) {
	name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "/")))
	if name == "" {
		return nil, nil, false
	}
	apps, err := r.List("", true)
	if err != nil {
		return nil, nil, false
	}
	for _, a := range apps {
		if product != "" && !a.TargetsProduct(product) {
			continue
		}
		for i := range a.SlashCommands {
			if a.SlashCommands[i].Name == name {
				cmd := a.SlashCommands[i]
				return a, &cmd, true
			}
		}
	}
	return nil, nil, false
}

// AllSlashCommands implements Registry.
func (r *PostgresRegistry) AllSlashCommands(product string) []RegisteredCommand {
	out := make([]RegisteredCommand, 0)
	apps, err := r.List("", true)
	if err != nil {
		return out
	}
	for _, a := range apps {
		if product != "" && !a.TargetsProduct(product) {
			continue
		}
		for _, cmd := range a.SlashCommands {
			out = append(out, RegisteredCommand{Name: cmd.Name, Description: cmd.Description, AppID: a.ID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---- Backend selection + shared signing-secret helpers ----------------------

// DatabaseURLFromEnv returns the Postgres connection string from DATABASE_URL,
// falling back to VULOS_DATABASE_URL. It is empty when neither is set.
func DatabaseURLFromEnv() string {
	if v := strings.TrimSpace(os.Getenv(DatabaseURLEnv)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(DatabaseURLEnvAlt))
}

// OpenRegistry selects the apps registry backend from the environment. When
// DATABASE_URL (or VULOS_DATABASE_URL) is set it returns a Postgres-backed
// registry on the dedicated "apps" schema (cloud consolidation: one Neon DB
// shared across products). Otherwise it returns the SQLite StandaloneRegistry at
// sqliteDSN (open-core / self-host default). The OS, which embeds apps with its
// own store, keeps calling NewStandaloneRegistry directly and is unaffected.
func OpenRegistry(sqliteDSN string, opts ...Option) (Registry, error) {
	if pg := DatabaseURLFromEnv(); pg != "" {
		return NewPostgresRegistry(pg, opts...)
	}
	return NewStandaloneRegistry(sqliteDSN, opts...)
}

// encryptorFromEnv builds an AES-256-GCM signing-secret encryptor from
// VULOS_APPS_KEK. It returns (nil, nil) when the variable is unset.
func encryptorFromEnv() (SigningSecretEncryptor, error) {
	kekHex := strings.TrimSpace(os.Getenv(AppsKEKEnv))
	if kekHex == "" {
		return nil, nil
	}
	kek, err := hex.DecodeString(kekHex)
	if err != nil {
		return nil, fmt.Errorf("appsplatform: %s is not valid hex: %w", AppsKEKEnv, err)
	}
	return NewAESGCMEncryptor(kek)
}

// encodeSigningSecret returns the value to store for a signing secret, enforcing
// the fail-loud cleartext rule: a non-empty secret with no encryptor is refused.
func encodeSigningSecret(enc SigningSecretEncryptor, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if enc == nil {
		return "", fmt.Errorf("appsplatform: refusing to persist signing secret in cleartext: "+
			"set the %s environment variable to a 64-hex-char AES-256 key, "+
			"or pass WithSigningSecretEncryptor", AppsKEKEnv)
	}
	return enc.Encrypt(plaintext)
}

// decodeSigningSecret reverses encodeSigningSecret. With no encryptor the stored
// value is returned as-is (it can only be cleartext).
func decodeSigningSecret(enc SigningSecretEncryptor, stored string) (string, error) {
	if enc == nil {
		return stored, nil
	}
	return enc.Decrypt(stored)
}
