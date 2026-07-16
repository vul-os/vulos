package appsplatform

import (
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// AppsKEKEnv is the environment variable from which NewStandaloneRegistry
// auto-seeds the AES-256-GCM signing-secret encryptor. Its value must be a
// 64-character lowercase hex string (32 bytes). If the variable is absent the
// caller must provide WithSigningSecretEncryptor; persisting a non-empty
// signing secret without any encryptor configured is refused (see persist).
const AppsKEKEnv = "VULOS_APPS_KEK"

// StandaloneRegistry is the in-repo default Registry. It keeps fast in-memory
// indexes as the source of truth for lookups and WRITES THROUGH to a pure-Go
// modernc SQLite database for durability. On open it rebuilds the indexes from
// the DB.
//
// With a nil db (NewMemoryRegistry) it is purely in-memory — used by tests and
// as a fallback when the durable DB cannot be opened.
type StandaloneRegistry struct {
	mu        sync.RWMutex
	db        *sql.DB // nil = in-memory only
	all       map[string]*App
	scopeSet  ScopeSet
	encryptor SigningSecretEncryptor // nil = store signing secret as plaintext
}

var _ Registry = (*StandaloneRegistry)(nil)

// Option configures a StandaloneRegistry.
type Option func(*StandaloneRegistry)

// WithScopeSet sets the scope set the registry validates against. When unset,
// DefaultScopeSet is used.
func WithScopeSet(s ScopeSet) Option {
	return func(r *StandaloneRegistry) { r.scopeSet = s }
}

// WithSigningSecretEncryptor configures envelope encryption for the vas_ signing
// secret at rest. The encryptor is applied on every write and reversed on every
// read, so the in-memory App always carries the plaintext. Legacy plaintext rows
// (no enc: prefix) are decrypted transparently and re-encrypted on next write.
func WithSigningSecretEncryptor(e SigningSecretEncryptor) Option {
	return func(r *StandaloneRegistry) { r.encryptor = e }
}

// NewMemoryRegistry builds an in-memory-only registry (no persistence).
func NewMemoryRegistry(opts ...Option) *StandaloneRegistry {
	r := &StandaloneRegistry{all: make(map[string]*App), scopeSet: DefaultScopeSet()}
	for _, o := range opts {
		o(r)
	}
	return r
}

// NewStandaloneRegistry opens (or creates) the SQLite database at dsn, ensures
// the schema, and loads existing apps into memory. Use a file path for
// durability or ":memory:" for an ephemeral DB.
//
// Signing-secret encryption is automatically enabled when the VULOS_APPS_KEK
// environment variable is set to a 64-hex-char AES-256 key. Callers may also
// pass WithSigningSecretEncryptor to supply a KEK programmatically (takes
// precedence over the env var). Persisting an app whose signing secret is
// non-empty without any encryptor configured is refused; set VULOS_APPS_KEK or
// call WithSigningSecretEncryptor to satisfy this requirement.
func NewStandaloneRegistry(dsn string, opts ...Option) (*StandaloneRegistry, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("appsplatform: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	r := &StandaloneRegistry{db: db, all: make(map[string]*App), scopeSet: DefaultScopeSet()}
	// Auto-configure signing-secret encryption from VULOS_APPS_KEK if set.
	// Opts are applied afterwards so WithSigningSecretEncryptor overrides this.
	if kekHex := strings.TrimSpace(os.Getenv(AppsKEKEnv)); kekHex != "" {
		kek, err := hex.DecodeString(kekHex)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("appsplatform: %s is not valid hex: %w", AppsKEKEnv, err)
		}
		enc, err := NewAESGCMEncryptor(kek)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("appsplatform: %s: %w", AppsKEKEnv, err)
		}
		r.encryptor = enc
	}
	for _, o := range opts {
		o(r)
	}
	if err := r.init(); err != nil {
		db.Close()
		return nil, err
	}
	if err := r.load(); err != nil {
		db.Close()
		return nil, err
	}
	return r, nil
}

// Close releases the underlying database handle.
func (r *StandaloneRegistry) Close() error {
	if r.db == nil {
		return nil
	}
	return r.db.Close()
}

func (r *StandaloneRegistry) init() error {
	_, err := r.db.Exec(`
		CREATE TABLE IF NOT EXISTS apps (
			id                       TEXT PRIMARY KEY,
			name                     TEXT NOT NULL DEFAULT '',
			icon                     TEXT NOT NULL DEFAULT '',
			description              TEXT NOT NULL DEFAULT '',
			owner_id                 TEXT NOT NULL DEFAULT '',
			org_id                   TEXT NOT NULL DEFAULT '',
			scopes_json              TEXT NOT NULL DEFAULT '[]',
			products_json            TEXT NOT NULL DEFAULT '[]',
			events_json              TEXT NOT NULL DEFAULT '[]',
			slash_json               TEXT NOT NULL DEFAULT '[]',
			webhook_url              TEXT NOT NULL DEFAULT '',
			incoming_webhook_id      TEXT NOT NULL DEFAULT '',
			incoming_webhook_enabled INTEGER NOT NULL DEFAULT 0,
			default_target           TEXT NOT NULL DEFAULT '',
			token_hash               TEXT NOT NULL DEFAULT '',
			signing_secret           TEXT NOT NULL DEFAULT '',
			created_at               INTEGER NOT NULL DEFAULT 0,
			token_issued_at          INTEGER NOT NULL DEFAULT 0,
			token_expires_at         INTEGER NOT NULL DEFAULT 0,
			token_ttl_ns             INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_apps_token_hash ON apps(token_hash);
		CREATE INDEX IF NOT EXISTS idx_apps_webhook ON apps(incoming_webhook_id);
		CREATE INDEX IF NOT EXISTS idx_apps_owner ON apps(owner_id);
	`)
	if err != nil {
		return fmt.Errorf("appsplatform: init schema: %w", err)
	}
	return nil
}

func (r *StandaloneRegistry) load() error {
	rows, err := r.db.Query(`SELECT id, name, icon, description, owner_id, org_id, scopes_json,
		products_json, events_json, slash_json, webhook_url, incoming_webhook_id,
		incoming_webhook_enabled, default_target, token_hash, signing_secret, created_at,
		token_issued_at, token_expires_at, token_ttl_ns FROM apps`)
	if err != nil {
		return fmt.Errorf("appsplatform: load: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		a := &App{}
		var scopesJSON, productsJSON, eventsJSON, slashJSON string
		var incEnabled int
		var created, tokenIssuedNs, tokenExpiresNs, tokenTTLNs int64
		if err := rows.Scan(&a.ID, &a.Name, &a.Icon, &a.Description, &a.OwnerID, &a.OrgID, &scopesJSON,
			&productsJSON, &eventsJSON, &slashJSON, &a.WebhookURL, &a.Incoming.ID,
			&incEnabled, &a.DefaultTarget, &a.TokenHash, &a.SigningSecret, &created,
			&tokenIssuedNs, &tokenExpiresNs, &tokenTTLNs); err != nil {
			return fmt.Errorf("appsplatform: scan: %w", err)
		}
		_ = json.Unmarshal([]byte(scopesJSON), &a.Scopes)
		_ = json.Unmarshal([]byte(productsJSON), &a.Products)
		_ = json.Unmarshal([]byte(eventsJSON), &a.Events)
		_ = json.Unmarshal([]byte(slashJSON), &a.SlashCommands)
		a.Incoming.Enabled = incEnabled != 0
		a.CreatedAt = time.Unix(0, created)
		if tokenIssuedNs != 0 {
			a.TokenIssuedAt = time.Unix(0, tokenIssuedNs)
		}
		if tokenExpiresNs != 0 {
			a.TokenExpiresAt = time.Unix(0, tokenExpiresNs)
		}
		a.TokenTTL = time.Duration(tokenTTLNs)
		// Decrypt signing secret if encryptor is configured.
		if r.encryptor != nil {
			plain, err := r.encryptor.Decrypt(a.SigningSecret)
			if err != nil {
				return fmt.Errorf("appsplatform: decrypt signing secret for app %s: %w", a.ID, err)
			}
			a.SigningSecret = plain
		}
		r.all[a.ID] = a
	}
	return rows.Err()
}

// persist writes an app row (insert-or-replace). No-op when db is nil.
func (r *StandaloneRegistry) persist(a *App) error {
	if r.db == nil {
		return nil
	}
	scopesJSON, _ := json.Marshal(a.Scopes)
	productsJSON, _ := json.Marshal(a.Products)
	eventsJSON, _ := json.Marshal(a.Events)
	slashJSON, _ := json.Marshal(a.SlashCommands)
	inc := 0
	if a.Incoming.Enabled {
		inc = 1
	}
	// Envelope-encrypt the signing secret before writing to durable storage.
	// Persisting a non-empty signing secret without an encryptor is refused:
	// writing it in cleartext would silently create a plaintext secret in the
	// database. Set VULOS_APPS_KEK (or call WithSigningSecretEncryptor) to
	// satisfy this requirement.
	signingSecret := a.SigningSecret
	if r.encryptor != nil && a.SigningSecret != "" {
		enc, err := r.encryptor.Encrypt(a.SigningSecret)
		if err != nil {
			return fmt.Errorf("appsplatform: encrypt signing secret: %w", err)
		}
		signingSecret = enc
	} else if r.encryptor == nil && a.SigningSecret != "" {
		return fmt.Errorf("appsplatform: refusing to persist signing secret in cleartext: "+
			"set the %s environment variable to a 64-hex-char AES-256 key, "+
			"or pass WithSigningSecretEncryptor", AppsKEKEnv)
	}
	tokenIssuedNs := a.TokenIssuedAt.UnixNano()
	if a.TokenIssuedAt.IsZero() {
		tokenIssuedNs = 0
	}
	tokenExpiresNs := a.TokenExpiresAt.UnixNano()
	if a.TokenExpiresAt.IsZero() {
		tokenExpiresNs = 0
	}
	_, err := r.db.Exec(
		`INSERT INTO apps (id, name, icon, description, owner_id, org_id, scopes_json,
			products_json, events_json, slash_json, webhook_url, incoming_webhook_id,
			incoming_webhook_enabled, default_target, token_hash, signing_secret, created_at,
			token_issued_at, token_expires_at, token_ttl_ns)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name=excluded.name, icon=excluded.icon,
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
		inc, a.DefaultTarget, a.TokenHash, signingSecret, a.CreatedAt.UnixNano(),
		tokenIssuedNs, tokenExpiresNs, int64(a.TokenTTL))
	return err
}

// clone returns a deep-ish copy so callers can't mutate registry-internal state
// through the returned pointer.
func clone(a *App) *App {
	if a == nil {
		return nil
	}
	cp := *a
	cp.Scopes = append([]string(nil), a.Scopes...)
	cp.Products = append([]string(nil), a.Products...)
	cp.Events = append([]string(nil), a.Events...)
	cp.SlashCommands = append([]SlashCommand(nil), a.SlashCommands...)
	return &cp
}

// Create implements Registry.
func (r *StandaloneRegistry) Create(p CreateParams) (*Created, error) {
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.persist(a); err != nil {
		return nil, err
	}
	r.all[a.ID] = a
	return &Created{App: clone(a), Token: token, SigningSecret: secret}, nil
}

// Get implements Registry.
func (r *StandaloneRegistry) Get(id string) (*App, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if a, ok := r.all[id]; ok {
		return clone(a), nil
	}
	return nil, ErrNotFound
}

// GetByTokenHash implements Registry. The hash comparison is constant-time so a
// valid-but-wrong token cannot be distinguished by timing.
func (r *StandaloneRegistry) GetByTokenHash(tokenHash string) (*App, error) {
	if tokenHash == "" {
		return nil, ErrNotFound
	}
	want := []byte(tokenHash)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var match *App
	for _, a := range r.all {
		if a.TokenHash == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(a.TokenHash), want) == 1 {
			match = a
		}
	}
	if match == nil {
		return nil, ErrNotFound
	}
	return clone(match), nil
}

// GetByIncomingWebhookID implements Registry. Like GetByTokenHash, the id
// comparison is constant-time and does not short-circuit, so a valid-but-wrong
// webhook id cannot be distinguished by timing.
func (r *StandaloneRegistry) GetByIncomingWebhookID(webhookID string) (*App, error) {
	if webhookID == "" {
		return nil, ErrNotFound
	}
	want := []byte(webhookID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var match *App
	for _, a := range r.all {
		if a.Incoming.ID == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(a.Incoming.ID), want) == 1 {
			match = a
		}
	}
	if match == nil {
		return nil, ErrNotFound
	}
	return clone(match), nil
}

// List implements Registry.
func (r *StandaloneRegistry) List(owner string, isAdmin bool) ([]*App, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*App, 0, len(r.all))
	for _, a := range r.all {
		if isAdmin || a.OwnerID == owner {
			out = append(out, clone(a))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Update implements Registry.
func (r *StandaloneRegistry) Update(id string, p UpdateParams) (*App, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.all[id]
	if !ok {
		return nil, ErrNotFound
	}
	updated := clone(a)
	if p.Name != nil {
		updated.Name = strings.TrimSpace(*p.Name)
	}
	if p.Icon != nil {
		updated.Icon = strings.TrimSpace(*p.Icon)
	}
	if p.Description != nil {
		updated.Description = strings.TrimSpace(*p.Description)
	}
	if p.Scopes != nil {
		scopes, err := r.scopeSet.Normalize(*p.Scopes)
		if err != nil {
			return nil, err
		}
		updated.Scopes = scopes
	}
	if p.Products != nil {
		products, err := NormalizeProducts(*p.Products)
		if err != nil {
			return nil, err
		}
		updated.Products = products
	}
	if p.Events != nil {
		updated.Events = NormalizeEvents(*p.Events)
	}
	if p.SlashCommands != nil {
		updated.SlashCommands = NormalizeSlashCommands(*p.SlashCommands)
	}
	if p.WebhookURL != nil {
		webhookURL := strings.TrimSpace(*p.WebhookURL)
		if err := ValidateWebhookURL(webhookURL); err != nil {
			return nil, err
		}
		updated.WebhookURL = webhookURL
	}
	if p.DefaultTarget != nil {
		updated.DefaultTarget = strings.TrimSpace(*p.DefaultTarget)
	}
	if p.IncomingEnabled != nil {
		updated.Incoming.Enabled = *p.IncomingEnabled
	}
	if p.TokenTTL != nil {
		updated.TokenTTL = *p.TokenTTL
		// Retroactive age-revocation: if a TTL is set, existing tokens older
		// than TTL are already expired by the middleware's age check. The
		// absolute TokenExpiresAt is NOT changed here — it reflects when the
		// current token was issued (unchanged until the next rotation).
	}
	if err := r.persist(updated); err != nil {
		return nil, err
	}
	r.all[id] = updated
	return clone(updated), nil
}

// Delete implements Registry.
func (r *StandaloneRegistry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.all[id]; !ok {
		return ErrNotFound
	}
	if r.db != nil {
		if _, err := r.db.Exec(`DELETE FROM apps WHERE id = ?`, id); err != nil {
			return err
		}
	}
	delete(r.all, id)
	return nil
}

// RotateToken implements Registry. It mints a new token, resets TokenIssuedAt,
// and re-applies the app's TokenTTL policy (if set) to compute a new TokenExpiresAt.
func (r *StandaloneRegistry) RotateToken(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.all[id]
	if !ok {
		return "", ErrNotFound
	}
	token := GenerateToken()
	now := time.Now()
	updated := clone(a)
	updated.TokenHash = HashToken(token)
	updated.TokenIssuedAt = now
	if updated.TokenTTL > 0 {
		updated.TokenExpiresAt = now.Add(updated.TokenTTL)
	} else {
		updated.TokenExpiresAt = time.Time{} // clear any previous absolute expiry
	}
	if err := r.persist(updated); err != nil {
		return "", err
	}
	r.all[id] = updated
	return token, nil
}

// RotateSecret implements Registry.
func (r *StandaloneRegistry) RotateSecret(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.all[id]
	if !ok {
		return "", ErrNotFound
	}
	secret := GenerateSecret()
	updated := clone(a)
	updated.SigningSecret = secret
	if err := r.persist(updated); err != nil {
		return "", err
	}
	r.all[id] = updated
	return secret, nil
}

// ResolveSlashCommand implements Registry.
func (r *StandaloneRegistry) ResolveSlashCommand(product, name string) (*App, *SlashCommand, bool) {
	name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "/")))
	if name == "" {
		return nil, nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, a := range r.all {
		if product != "" && !a.TargetsProduct(product) {
			continue
		}
		for i := range a.SlashCommands {
			if a.SlashCommands[i].Name == name {
				cmd := a.SlashCommands[i]
				return clone(a), &cmd, true
			}
		}
	}
	return nil, nil, false
}

// AllSlashCommands implements Registry.
func (r *StandaloneRegistry) AllSlashCommands(product string) []RegisteredCommand {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RegisteredCommand, 0)
	for _, a := range r.all {
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
