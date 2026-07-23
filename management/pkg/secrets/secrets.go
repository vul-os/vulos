// Package secrets provides a registry-backed secret-rotation manager for the
// vulos.cloud control plane.
//
// Named secrets (SESSION_SECRET, AIROUTER_KEY_HEX, RESTIC_PASSWORD,
// JWT_SIGNING_KEY, DB_ENCRYPT_KEY) each have a current value, a previous
// value retained for a grace window, a rotation interval, and a
// last-rotated-at timestamp.
//
// Secrets are persisted in a dedicated SQLite database (secrets.db) encrypted
// at rest using AES-256-GCM with a master key read from VULOS_SECRET_KMS_KEY
// (32-byte hex).  If the master key is missing in VULOS_ENV=prod the process
// fatals.
//
// The RotationWorker checks the registry every hour and rotates any secret
// that has exceeded its interval, emitting an audit event.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/env"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrUnknownSecret is returned when a secret name is not in the registry.
	ErrUnknownSecret = errors.New("secrets: unknown secret name")
	// ErrMissingMasterKey is returned (fatal in prod) when VULOS_SECRET_KMS_KEY
	// is not set.
	ErrMissingMasterKey = errors.New("secrets: VULOS_SECRET_KMS_KEY not set")
)

// ---------------------------------------------------------------------------
// Kind
// ---------------------------------------------------------------------------

// Kind determines how new values are generated during rotation.
type Kind string

const (
	// KindSymmetric generates 32 random bytes (used for SESSION_SECRET,
	// AIROUTER_KEY_HEX, RESTIC_PASSWORD, DB_ENCRYPT_KEY).
	KindSymmetric Kind = "symmetric"
	// KindEd25519 generates an ed25519 signing key pair; the canonical value is
	// the hex-encoded private key seed (JWT_SIGNING_KEY).
	KindEd25519 Kind = "ed25519"
)

// ---------------------------------------------------------------------------
// Registry entry
// ---------------------------------------------------------------------------

// entry holds the in-memory live state for one named secret.
type entry struct {
	kind      Kind
	current   []byte        // current secret value (plaintext in memory)
	previous  []byte        // previous value retained for grace window
	interval  time.Duration // rotation period
	rotatedAt time.Time     // last rotation timestamp
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// Manager holds the secret registry and exposes Rotate / Verify.
// All methods are safe for concurrent use.
type Manager struct {
	mu        sync.RWMutex
	registry  map[string]*entry
	db        *cpdb.DB
	masterKey []byte // 32-byte AES key; nil in dev mode
	grace     time.Duration
}

// DefaultGrace is the default window during which the previous secret value is
// still accepted by Verify (e.g. tokens minted just before rotation).
const DefaultGrace = 24 * time.Hour

// Open applies the schema migration to db, loads the master key, and returns a
// ready Manager.  If VULOS_SECRET_KMS_KEY is missing and VULOS_DEV != "1", this
// function returns ErrMissingMasterKey (caller should fatal).
//
// db should be obtained from cpdb.Open("secrets") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.  Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*Manager, error) {
	masterKey, err := loadMasterKey()
	if err != nil {
		return nil, err
	}

	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("secrets: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("secrets: migrate: %w", err)
	}

	m := &Manager{
		registry:  make(map[string]*entry),
		db:        db,
		masterKey: masterKey,
		grace:     DefaultGrace,
	}

	// Pre-populate standard names with their defaults.
	m.registerDefaults()

	// Load persisted values.
	if err := m.loadAll(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secrets: load: %w", err)
	}

	return m, nil
}

// Close closes the underlying database connection.
func (m *Manager) Close() error { return m.db.Close() }

// SetGrace overrides the default grace window.  Useful in tests.
func (m *Manager) SetGrace(d time.Duration) {
	m.mu.Lock()
	m.grace = d
	m.mu.Unlock()
}

// registerDefaults seeds the registry with the canonical secret names.
func (m *Manager) registerDefaults() {
	names := []struct {
		name     string
		kind     Kind
		interval time.Duration
	}{
		{"SESSION_SECRET", KindSymmetric, 30 * 24 * time.Hour},
		{"AIROUTER_KEY_HEX", KindSymmetric, 30 * 24 * time.Hour},
		{"RESTIC_PASSWORD", KindSymmetric, 90 * 24 * time.Hour},
		{"JWT_SIGNING_KEY", KindEd25519, 30 * 24 * time.Hour},
		{"DB_ENCRYPT_KEY", KindSymmetric, 90 * 24 * time.Hour},
	}
	for _, n := range names {
		m.registry[n.name] = &entry{
			kind:     n.kind,
			interval: n.interval,
		}
	}
}

// ---------------------------------------------------------------------------
// Rotate
// ---------------------------------------------------------------------------

// Rotate generates a new value for the named secret, stores the current as
// previous, and persists both to the database.
func (m *Manager) Rotate(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.registry[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSecret, name)
	}

	newVal, err := generate(e.kind)
	if err != nil {
		return fmt.Errorf("secrets: generate %q: %w", name, err)
	}

	e.previous = e.current
	e.current = newVal
	e.rotatedAt = time.Now().UTC()

	return m.persist(ctx, name, e)
}

// ---------------------------------------------------------------------------
// Verify
// ---------------------------------------------------------------------------

// Verify checks candidate against the named secret's current value, and if
// that fails, against previous (within the grace window).  For KindEd25519 it
// does an ed25519 signature verification; for KindSymmetric it does a
// constant-time byte comparison.
//
// candidate for KindSymmetric is the raw hex-encoded secret value.
// candidate for KindEd25519 is "msg:sig" where sig is a hex-encoded ed25519
// signature — but for simple equality-test usage it just compares the raw seed.
func (m *Manager) Verify(name string, candidate []byte) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	e, ok := m.registry[name]
	if !ok {
		return false
	}

	if verifyValue(e.kind, e.current, candidate) {
		return true
	}

	// Check previous within grace window.
	if len(e.previous) > 0 && time.Since(e.rotatedAt) <= m.grace {
		return verifyValue(e.kind, e.previous, candidate)
	}
	return false
}

// Current returns the current plaintext value for the named secret.
// Returns nil if the name is unknown or the secret has not been generated yet.
func (m *Manager) Current(name string) []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.registry[name]
	if !ok {
		return nil
	}
	out := make([]byte, len(e.current))
	copy(out, e.current)
	return out
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func verifyValue(k Kind, stored, candidate []byte) bool {
	if len(stored) == 0 {
		return false
	}
	switch k {
	case KindEd25519:
		// For ed25519 we compare the raw private key seed (hex-encoded) for
		// token migration purposes; a full verify(msg, sig) path would be
		// added in production token validation.
		return constEq(stored, candidate)
	default:
		return constEq(stored, candidate)
	}
}

// constEq is a constant-time byte comparison.
func constEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

// generate produces a new secret value for the given kind.
func generate(k Kind) ([]byte, error) {
	switch k {
	case KindEd25519:
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		_ = pub
		// Store the private key as hex.
		return []byte(hex.EncodeToString(priv.Seed())), nil
	default: // KindSymmetric
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		return []byte(hex.EncodeToString(raw)), nil
	}
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

func (m *Manager) persist(ctx context.Context, name string, e *entry) error {
	encCurrent, err := m.encrypt(e.current)
	if err != nil {
		return fmt.Errorf("secrets: encrypt current: %w", err)
	}
	var encPrevious []byte
	if len(e.previous) > 0 {
		encPrevious, err = m.encrypt(e.previous)
		if err != nil {
			return fmt.Errorf("secrets: encrypt previous: %w", err)
		}
	}

	_, err = m.db.ExecContext(ctx, m.db.Rebind(`
		INSERT INTO secrets (name, kind, current_enc, previous_enc, interval_ns, rotated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			current_enc  = excluded.current_enc,
			previous_enc = excluded.previous_enc,
			interval_ns  = excluded.interval_ns,
			rotated_at   = excluded.rotated_at`),
		name, string(e.kind), encCurrent, encPrevious,
		int64(e.interval), e.rotatedAt.Format(time.RFC3339Nano),
	)
	return err
}

func (m *Manager) loadAll() error {
	rows, err := m.db.Query(m.db.Rebind(
		`SELECT name, kind, current_enc, previous_enc, interval_ns, rotated_at FROM secrets`))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			name       string
			kind       string
			curEnc     []byte
			prevEnc    []byte
			intervalNS int64
			rotatedAt  string
		)
		if err := rows.Scan(&name, &kind, &curEnc, &prevEnc, &intervalNS, &rotatedAt); err != nil {
			return err
		}
		e, ok := m.registry[name]
		if !ok {
			// Unknown name — skip (forward compatibility).
			continue
		}
		if len(curEnc) > 0 {
			plain, err := m.decrypt(curEnc)
			if err != nil {
				return fmt.Errorf("secrets: decrypt current for %q: %w", name, err)
			}
			e.current = plain
		}
		if len(prevEnc) > 0 {
			plain, err := m.decrypt(prevEnc)
			if err != nil {
				return fmt.Errorf("secrets: decrypt previous for %q: %w", name, err)
			}
			e.previous = plain
		}
		e.kind = Kind(kind)
		e.interval = time.Duration(intervalNS)
		if rotatedAt != "" {
			t, _ := time.Parse(time.RFC3339Nano, rotatedAt)
			e.rotatedAt = t
		}
	}
	return rows.Err()
}

// ---------------------------------------------------------------------------
// Encryption (AES-256-GCM envelope)
// ---------------------------------------------------------------------------

func (m *Manager) encrypt(plain []byte) ([]byte, error) {
	if len(m.masterKey) == 0 {
		// Dev mode: store plaintext (prefixed to distinguish from encrypted blobs).
		out := make([]byte, 1+len(plain))
		out[0] = 0x00 // plaintext tag
		copy(out[1:], plain)
		return out, nil
	}
	block, err := aes.NewCipher(m.masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nonce, nonce, plain, nil)
	// Prefix 0x01 = encrypted tag.
	out := make([]byte, 1+len(ct))
	out[0] = 0x01
	copy(out[1:], ct)
	return out, nil
}

func (m *Manager) decrypt(blob []byte) ([]byte, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	tag := blob[0]
	data := blob[1:]

	if tag == 0x00 {
		// Plaintext (dev mode).
		out := make([]byte, len(data))
		copy(out, data)
		return out, nil
	}

	if len(m.masterKey) == 0 {
		return nil, fmt.Errorf("secrets: encrypted blob but no master key")
	}
	block, err := aes.NewCipher(m.masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, fmt.Errorf("secrets: ciphertext too short")
	}
	nonce := data[:gcm.NonceSize()]
	ct := data[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

// ---------------------------------------------------------------------------
// Master key loading
// ---------------------------------------------------------------------------

func loadMasterKey() ([]byte, error) {
	raw := os.Getenv("VULOS_SECRET_KMS_KEY")
	if raw == "" {
		// SEC-M6 (audit): fail-closed by default. Plaintext storage is only
		// permitted when the operator explicitly opts in via VULOS_DEV.
		// WAVE34: route through env.DevKEKFallbackAllowed so (a) the VULOS_DEV
		// truthy parse is normalised in one place (this used to require exactly
		// "1" while every other site accepted "true"), and (b) a stray VULOS_DEV
		// on a prod box can never re-enable plaintext storage — !IsProd() is now
		// cross-checked.
		if !env.DevKEKFallbackAllowed() {
			return nil, ErrMissingMasterKey
		}
		log.Println("[secrets] WARNING: VULOS_SECRET_KMS_KEY not set — dev mode (plaintext storage, VULOS_DEV + env!=prod)")
		return nil, nil
	}
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("secrets: VULOS_SECRET_KMS_KEY must be 64 hex chars (32 bytes): %w", err)
	}
	return key, nil
}

// ---------------------------------------------------------------------------
// RotationWorker
// ---------------------------------------------------------------------------

// AuditEmitter is the narrow interface the RotationWorker uses to emit audit
// events.  audit.Log satisfies this.
type AuditEmitter interface {
	RecordRotation(ctx context.Context, name string) error
}

// noopAudit is a no-op emitter used when no audit log is wired.
type noopAudit struct{}

func (noopAudit) RecordRotation(_ context.Context, _ string) error { return nil }

// RotationWorker runs a periodic sweep that rotates any secret past its
// interval.
type RotationWorker struct {
	mgr    *Manager
	audit  AuditEmitter
	ticker *time.Ticker
	done   chan struct{}
	cancel context.CancelFunc
}

// NewRotationWorker creates a RotationWorker. audit may be nil (no-op emitter
// is used).
func NewRotationWorker(mgr *Manager, audit AuditEmitter) *RotationWorker {
	if audit == nil {
		audit = noopAudit{}
	}
	return &RotationWorker{mgr: mgr, audit: audit, done: make(chan struct{})}
}

// Run starts the background rotation loop.  Call Stop to shut it down.
func (w *RotationWorker) Run(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	w.ticker = time.NewTicker(time.Hour)
	go func() {
		defer close(w.done)
		for {
			select {
			case <-ctx.Done():
				w.ticker.Stop()
				return
			case <-w.ticker.C:
				w.sweep(ctx)
			}
		}
	}()
}

// Stop signals the worker to stop and waits for it to exit.
func (w *RotationWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}

// Sweep is exported for testing so callers can trigger an immediate sweep.
func (w *RotationWorker) Sweep(ctx context.Context) { w.sweep(ctx) }

func (w *RotationWorker) sweep(ctx context.Context) {
	w.mgr.mu.RLock()
	names := make([]string, 0, len(w.mgr.registry))
	for name, e := range w.mgr.registry {
		if e.interval > 0 && time.Since(e.rotatedAt) >= e.interval {
			names = append(names, name)
		}
	}
	w.mgr.mu.RUnlock()

	for _, name := range names {
		if err := w.mgr.Rotate(ctx, name); err != nil {
			log.Printf("[secrets] rotation failed for %q: %v", name, err)
			continue
		}
		if err := w.audit.RecordRotation(ctx, name); err != nil {
			log.Printf("[secrets] audit emit failed for %q: %v", name, err)
		}
	}
}
