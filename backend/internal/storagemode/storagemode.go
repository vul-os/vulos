// Package storagemode persists the OS-side storage-mode selector for the
// Vulos bundle (mail + office + OS) and renders the env-var contract that
// co-located services read at startup.
//
// Two modes are recognised:
//
//   - "central-tigris"     — default. The bundle reads/writes the hosted
//     Tigris bucket directly (the existing behaviour from
//     /etc/vulos/storage.yaml: backend=tigris). No sync layer is engaged.
//   - "local-minio-sync"   — opt-in. The bundle reads/writes a co-located
//     MinIO source-of-truth and the CRDT/fabric sync layer (provided by
//     STORE-SYNC-01, OFFICE-SYNC-01, SYNC-P2P-01) replicates changes between
//     nodes. MinIO endpoint + region + bucket + credentials-ref are stored
//     here and surfaced as environment variables consumed by the co-located
//     mail and office services.
//
// The store is backed by pure-Go modernc.org/sqlite (D23 — never CGo) and
// lives at $HOME/.vulos/db/storagemode.db so it can be opened by every Vulos
// process without coordination. A single 1-row "config" table is enough.
//
// The default mode is "central-tigris"; an empty / missing database returns
// the default with no error. Switching modes is a single UPDATE.
package storagemode

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // pure-Go SQLite driver — never CGo (D23)
)

// Mode is the bundle-wide storage selector.
type Mode string

const (
	// ModeCentralTigris is the default — the bundle talks directly to the
	// hosted Tigris S3-compatible bucket. No local MinIO, no sync layer.
	ModeCentralTigris Mode = "central-tigris"

	// ModeLocalMinIOSync writes to a co-located MinIO source-of-truth and
	// enables the CRDT sync layer to replicate between nodes.
	ModeLocalMinIOSync Mode = "local-minio-sync"
)

// DefaultMode is the mode every Vulos install starts in.
const DefaultMode = ModeCentralTigris

// Valid reports whether m is one of the two recognised modes.
func (m Mode) Valid() bool {
	return m == ModeCentralTigris || m == ModeLocalMinIOSync
}

// Config is the persisted storage-mode configuration. The MinIO* fields are
// only meaningful when Mode == ModeLocalMinIOSync; they are ignored (but kept)
// when the mode is central-tigris so flipping back and forth doesn't drop
// user-supplied values.
type Config struct {
	Mode          Mode   `json:"mode"`
	MinIOEndpoint string `json:"minio_endpoint,omitempty"`
	MinIORegion   string `json:"minio_region,omitempty"`
	MinIOBucket   string `json:"minio_bucket,omitempty"`
	// MinIOCredsRef is a reference (file path or secret-store key) to the
	// MinIO credentials. The credentials themselves are NEVER stored here —
	// install-vulos.sh writes them to ${DATA_DIR}/minio/.minio_secret and we
	// only persist the reference so logs / dashboards never leak secrets.
	MinIOCredsRef string `json:"minio_creds_ref,omitempty"`
}

// Defaults returns a Config in the default (central-tigris) mode.
func Defaults() Config {
	return Config{Mode: DefaultMode}
}

// Store is the sqlite-backed mode store. It is safe for concurrent use.
type Store struct {
	mu sync.RWMutex
	db *sql.DB
}

// DefaultDBPath returns $HOME/.vulos/db/storagemode.db.
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("storagemode: cannot resolve home dir: %w", err)
	}
	return filepath.Join(home, ".vulos", "db", "storagemode.db"), nil
}

// Open opens (or creates) the storagemode database at dbPath and runs the
// (idempotent) schema setup. When dbPath is empty, DefaultDBPath is used.
func Open(dbPath string) (*Store, error) {
	if dbPath == "" {
		var err error
		dbPath, err = DefaultDBPath()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return nil, fmt.Errorf("storagemode: mkdir: %w", err)
	}

	dsn := "file:" + dbPath +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storagemode: open: %w", err)
	}
	// modernc/sqlite is not safe for unbounded concurrent writers on one file.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("storagemode: ping: %w", err)
	}

	const schema = `
CREATE TABLE IF NOT EXISTS storagemode (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    mode            TEXT NOT NULL,
    minio_endpoint  TEXT NOT NULL DEFAULT '',
    minio_region    TEXT NOT NULL DEFAULT '',
    minio_bucket    TEXT NOT NULL DEFAULT '',
    minio_creds_ref TEXT NOT NULL DEFAULT '',
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);
`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("storagemode: schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Get returns the persisted Config. If no row has ever been written, the
// default (central-tigris) is returned with no error so first-boot callers
// don't have to special-case the missing-row condition.
func (s *Store) Get() (Config, error) {
	if s == nil || s.db == nil {
		return Defaults(), errors.New("storagemode: store not initialised")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var cfg Config
	err := s.db.QueryRow(
		`SELECT mode, minio_endpoint, minio_region, minio_bucket, minio_creds_ref
           FROM storagemode WHERE id = 1`,
	).Scan(&cfg.Mode, &cfg.MinIOEndpoint, &cfg.MinIORegion, &cfg.MinIOBucket, &cfg.MinIOCredsRef)
	if errors.Is(err, sql.ErrNoRows) {
		return Defaults(), nil
	}
	if err != nil {
		return Defaults(), fmt.Errorf("storagemode: get: %w", err)
	}
	if !cfg.Mode.Valid() {
		// Corrupt row: fall back to default rather than crashing.
		return Defaults(), nil
	}
	return cfg, nil
}

// Set persists cfg, validating the mode and, for local-minio-sync, the
// minimum endpoint+bucket pair. Endpoints are trimmed; region defaults to
// "auto" when empty and the mode is local-minio-sync.
func (s *Store) Set(cfg Config) error {
	if s == nil || s.db == nil {
		return errors.New("storagemode: store not initialised")
	}
	if !cfg.Mode.Valid() {
		return fmt.Errorf("storagemode: invalid mode %q (want %q or %q)",
			cfg.Mode, ModeCentralTigris, ModeLocalMinIOSync)
	}

	cfg.MinIOEndpoint = strings.TrimSpace(cfg.MinIOEndpoint)
	cfg.MinIORegion = strings.TrimSpace(cfg.MinIORegion)
	cfg.MinIOBucket = strings.TrimSpace(cfg.MinIOBucket)
	cfg.MinIOCredsRef = strings.TrimSpace(cfg.MinIOCredsRef)

	if cfg.Mode == ModeLocalMinIOSync {
		if cfg.MinIOEndpoint == "" {
			return errors.New("storagemode: local-minio-sync requires minio_endpoint")
		}
		if cfg.MinIOBucket == "" {
			return errors.New("storagemode: local-minio-sync requires minio_bucket")
		}
		if cfg.MinIORegion == "" {
			cfg.MinIORegion = "auto"
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
INSERT INTO storagemode (id, mode, minio_endpoint, minio_region, minio_bucket, minio_creds_ref, updated_at)
VALUES (1, ?, ?, ?, ?, ?, datetime('now'))
ON CONFLICT(id) DO UPDATE SET
    mode            = excluded.mode,
    minio_endpoint  = excluded.minio_endpoint,
    minio_region    = excluded.minio_region,
    minio_bucket    = excluded.minio_bucket,
    minio_creds_ref = excluded.minio_creds_ref,
    updated_at      = datetime('now')`,
		string(cfg.Mode), cfg.MinIOEndpoint, cfg.MinIORegion, cfg.MinIOBucket, cfg.MinIOCredsRef,
	)
	if err != nil {
		return fmt.Errorf("storagemode: set: %w", err)
	}
	return nil
}

// EnvFor returns the environment-variable map co-located services
// (vulos-mail, vulos-office) read at startup to learn the bundle storage
// mode and, when applicable, the local MinIO endpoint.
//
// For ModeCentralTigris only VULOS_STORAGE_MODE is exported (the legacy
// storage.yaml continues to provide the bucket and credentials). This is
// the default path and is what the FROZEN invariant "default mode path
// unaffected" guarantees.
//
// For ModeLocalMinIOSync the four MinIO endpoint vars and the creds-ref
// pointer are exported alongside the mode. The credentials themselves are
// NOT exported here — services read them via the creds-ref (typically the
// file path written by scripts/install-vulos.sh).
func EnvFor(cfg Config) map[string]string {
	out := map[string]string{"VULOS_STORAGE_MODE": string(cfg.Mode)}
	if cfg.Mode != ModeLocalMinIOSync {
		return out
	}
	if cfg.MinIOEndpoint != "" {
		out["VULOS_MINIO_ENDPOINT"] = cfg.MinIOEndpoint
	}
	if cfg.MinIORegion != "" {
		out["VULOS_MINIO_REGION"] = cfg.MinIORegion
	}
	if cfg.MinIOBucket != "" {
		out["VULOS_MINIO_BUCKET"] = cfg.MinIOBucket
	}
	if cfg.MinIOCredsRef != "" {
		out["VULOS_MINIO_CREDS_REF"] = cfg.MinIOCredsRef
	}
	return out
}

// EnvSlice returns EnvFor formatted as a deterministic "KEY=VALUE" slice,
// suitable for handing straight to exec.Cmd.Env or writing to an
// EnvironmentFile= drop-in. Order is sorted by key for reproducibility.
func EnvSlice(cfg Config) []string {
	env := EnvFor(cfg)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(env))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
