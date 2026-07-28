// Package storagemode persists the OS-side storage-mode selector for the
// Vulos bundle (mail + office + OS) and renders the env-var contract that
// co-located services read at startup.
//
// Three modes are recognised:
//
//   - "local-fs"           — DEFAULT. The box stores its own bytes on its own
//     filesystem, under the local-FS root of internal/storage (the resolver's
//     LocalRoot, $VULOS_STORAGE_LOCAL_ROOT or ~/.vulos/storage). No object
//     store, no credentials, no third-party service, no network call. This is
//     the mode a fresh install comes up in, and it is the one the data plane
//     in internal/storage/grant.go exercises when Resolution.Configured() is
//     false.
//   - "local-minio-sync"   — opt-in. The bundle reads/writes a co-located
//     MinIO source-of-truth and the CRDT/fabric sync layer (provided by
//     STORE-SYNC-01, OFFICE-SYNC-01, SYNC-P2P-01) replicates changes between
//     nodes. MinIO endpoint + region + bucket + credentials-ref are stored
//     here and surfaced as environment variables consumed by the co-located
//     mail and office services.
//   - "central-tigris"     — opt-in, HOSTED. The bundle reads/writes the
//     hosted Tigris bucket directly (the historical behaviour from
//     /etc/vulos/storage.yaml: backend=tigris). No sync layer is engaged.
//     This was the default before D-STORE-LOCAL-DEFAULT; it is now something
//     an operator has to ask for.
//
// # Why the default moved (D-STORE-LOCAL-DEFAULT)
//
// The suite's stated posture is self-hosted, with no hosted third-party
// service as a default and no default network calls. An OS whose default
// storage selector names a hosted S3 vendor contradicts that at the most
// visible possible point. The default is therefore local-fs, and every hosted
// backend is an explicit selection.
//
// # Existing installs are NOT migrated
//
// Changing DefaultMode must not move anyone's data or silently repoint a box
// that has been running on Tigris. Open() therefore resolves the default ONCE,
// from evidence, and records it:
//
//	evidence                                        → mode pinned
//	------------------------------------------------------------------
//	an explicit row already exists                   → that row (untouched)
//	storagemode.db pre-dates this change, no row      → central-tigris
//	a legacy storage.yaml naming a hosted backend     → central-tigris
//	anything else (a genuinely new box)               → local-fs
//
// The pin is written back as a normal row, so the decision is made once, is
// visible in the API and on disk, and never silently re-evaluates. Nothing
// else on the box is rewritten: /etc/vulos/storage.yaml is only ever READ
// here, and no data is copied between backends by this package or by
// scripts/install-vulos.sh.
//
// # What this selector does and does NOT do today (verified, not assumed)
//
// It records and reports the box's storage mode, and renders the env contract
// in EnvFor/EnvSlice. It does NOT itself route any byte:
//
//   - Nothing in this repository reads VULOS_STORAGE_MODE, and nothing calls
//     EnvFor/EnvSlice outside this package's tests. The co-located lilmail and
//     diwan services do not read /etc/vulos/storage.yaml either.
//   - What actually selects the OS's backend is internal/storage's resolver:
//     VULOS_STORAGE_ENDPOINT / VULOS_S3_ENDPOINT. With neither set — which is
//     the case for every install-vulos.sh install, since the installer writes
//     no storage environment into the units — Resolution.Configured() is false
//     and the grant broker in internal/storage/grant.go serves bytes off local
//     disk under LocalRoot.
//
// So a default box has ALWAYS been storing bytes locally; what was hosted was
// the DECLARED default. This change makes the declaration match the behaviour
// instead of promising a hosted backend nothing was actually wired to. The
// local data path is covered end to end by internal/storage/grant_local_test.go.
//
// The store is backed by pure-Go modernc.org/sqlite (D23 — never CGo) and
// lives at $HOME/.vulos/db/storagemode.db so it can be opened by every Vulos
// process without coordination. A single 1-row "config" table is enough.
package storagemode

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"vulos/backend/internal/datadir"

	_ "modernc.org/sqlite" // pure-Go SQLite driver — never CGo (D23)
)

// Mode is the bundle-wide storage selector.
type Mode string

const (
	// ModeLocalFS is the default — the box keeps its bytes on its own disk.
	// No object store, no credentials, no hosted service, no network call.
	ModeLocalFS Mode = "local-fs"

	// ModeLocalMinIOSync writes to a co-located MinIO source-of-truth and
	// enables the CRDT sync layer to replicate between nodes.
	ModeLocalMinIOSync Mode = "local-minio-sync"

	// ModeCentralTigris talks directly to the hosted Tigris S3-compatible
	// bucket. Opt-in only: it sends the box's data to a third party.
	ModeCentralTigris Mode = "central-tigris"
)

// DefaultMode is the mode a NEW Vulos install starts in: local, self-contained,
// no hosted dependency. See the package doc for what happens to installs that
// pre-date this default.
const DefaultMode = ModeLocalFS

// LegacyDefaultMode is the mode installs created BEFORE D-STORE-LOCAL-DEFAULT
// ran in implicitly. It is pinned (not silently changed) for such boxes.
const LegacyDefaultMode = ModeCentralTigris

// Valid reports whether m is one of the three recognised modes.
func (m Mode) Valid() bool {
	switch m {
	case ModeLocalFS, ModeLocalMinIOSync, ModeCentralTigris:
		return true
	default:
		return false
	}
}

// Hosted reports whether m sends the box's data to a third-party service the
// operator does not run. Callers use it to require an explicit opt-in and to
// label the mode honestly in UI and installer output.
func (m Mode) Hosted() bool { return m == ModeCentralTigris }

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

// Defaults returns a Config in the default (local-fs) mode.
func Defaults() Config {
	return Config{Mode: DefaultMode}
}

// Store is the sqlite-backed mode store. It is safe for concurrent use.
type Store struct {
	mu sync.RWMutex
	db *sql.DB

	// fallback is the mode Get() reports when — against expectation — no row
	// exists (Open normally pins one). fallbackWhy records the evidence.
	fallback    Mode
	fallbackWhy string
}

// DefaultDBPath returns $HOME/.vulos/db/storagemode.db.
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("storagemode: cannot resolve home dir: %w", err)
	}
	return datadir.Join("db", "storagemode.db"), nil
}

// LegacyStorageConfigPath is the bundle's pre-existing storage selector,
// written by scripts/install-vulos.sh. This package only ever READS it, and
// only to tell a pre-existing (possibly hosted) install apart from a new box.
const LegacyStorageConfigPath = "/etc/vulos/storage.yaml"

// Options configures Open. The zero value is the production configuration.
type Options struct {
	// DBPath is the sqlite file. Empty ⇒ DefaultDBPath().
	DBPath string
	// LegacyStorageConfig is the path consulted to detect a pre-existing
	// install. Empty ⇒ LegacyStorageConfigPath. Tests point this at a path
	// under t.TempDir() so the result never depends on the host's /etc.
	LegacyStorageConfig string
	// Quiet suppresses the one-line "default resolved to X because Y" log.
	Quiet bool
}

// Open opens (or creates) the storagemode database at dbPath and runs the
// (idempotent) schema setup. When dbPath is empty, DefaultDBPath is used.
func Open(dbPath string) (*Store, error) {
	return OpenWithOptions(Options{DBPath: dbPath})
}

// OpenWithOptions is Open with the legacy-detection inputs made explicit.
//
// On a store that holds no explicit selection it PINS one (see the package
// doc): the pin preserves what the box is already doing, so upgrading a
// Tigris box to this build leaves it on Tigris, while a new box comes up
// local. The pin is the only write Open performs, it happens at most once per
// box, and it never touches anything outside the sqlite file.
func OpenWithOptions(opt Options) (*Store, error) {
	dbPath := opt.DBPath
	if dbPath == "" {
		var err error
		dbPath, err = DefaultDBPath()
		if err != nil {
			return nil, err
		}
	}
	legacyPath := opt.LegacyStorageConfig
	if legacyPath == "" {
		legacyPath = LegacyStorageConfigPath
	}
	// Sampled BEFORE the file is created: an existing db means this box was
	// installed before local-fs became the default.
	dbPreExisted := fileExists(dbPath)

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

	// Resolve the default from evidence, then pin it so it is decided once.
	mode, why := EffectiveDefault(legacyPath)
	if dbPreExisted && mode == DefaultMode {
		mode = LegacyDefaultMode
		why = fmt.Sprintf("%s pre-dates the local-first default and holds no explicit selection — "+
			"preserving %q so an existing install is not repointed", dbPath, LegacyDefaultMode)
	}

	s := &Store{db: db, fallback: mode, fallbackWhy: why}
	pinned, err := s.pinDefault(mode)
	if err != nil {
		db.Close()
		return nil, err
	}
	if pinned && !opt.Quiet {
		log.Printf("[storagemode] no stored selection — pinned %q (hosted=%v): %s",
			mode, mode.Hosted(), why)
	}
	return s, nil
}

// pinDefault writes mode as the stored selection IF AND ONLY IF no row exists.
// It reports whether it wrote. An existing selection is never overwritten —
// that is the whole point (requirement: never rewrite an operator's config).
func (s *Store) pinDefault(mode Mode) (bool, error) {
	res, err := s.db.Exec(
		`INSERT INTO storagemode (id, mode) VALUES (1, ?) ON CONFLICT(id) DO NOTHING`,
		string(mode),
	)
	if err != nil {
		return false, fmt.Errorf("storagemode: pin default: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, nil // driver without RowsAffected support: pin still applied
	}
	return n > 0, nil
}

// DefaultOrigin returns the mode Open resolved as this box's default and the
// evidence it used. Callers surface it so the choice is legible rather than
// magical. It describes the DEFAULT resolution only — if an operator has since
// selected a mode explicitly, Get() is authoritative.
func (s *Store) DefaultOrigin() (Mode, string) {
	if s == nil {
		return DefaultMode, "store not initialised"
	}
	return s.fallback, s.fallbackWhy
}

// EffectiveDefault reports the mode a box with NO stored selection should run
// in, plus the evidence for that choice. legacyConfigPath is the bundle's
// storage.yaml (empty ⇒ LegacyStorageConfigPath); it is only read.
//
// A legacy config naming a hosted backend keeps the box on the hosted mode:
// that operator is already running there and this build must not move them.
// A legacy config naming a local backend, or no legacy config at all, gets the
// local default.
func EffectiveDefault(legacyConfigPath string) (Mode, string) {
	if legacyConfigPath == "" {
		legacyConfigPath = LegacyStorageConfigPath
	}
	backend, found := legacyStorageBackend(legacyConfigPath)
	switch {
	case !found:
		return DefaultMode, fmt.Sprintf("no prior storage selection and no %s on this box — "+
			"new install, defaulting to local, self-contained storage", legacyConfigPath)
	case isLocalBackend(backend):
		return DefaultMode, fmt.Sprintf("%s declares backend=%q, which is already local — "+
			"defaulting to local, self-contained storage", legacyConfigPath, backend)
	default:
		return LegacyDefaultMode, fmt.Sprintf("%s declares hosted backend=%q — keeping this "+
			"existing install on %q; its data is NOT migrated", legacyConfigPath, backend, LegacyDefaultMode)
	}
}

// localBackends are the storage.yaml "backend:" values that mean "this box's
// own disk or its own co-located object store". Everything else is treated as
// a hosted/remote backend, which is the conservative direction: an unknown
// value keeps a pre-existing install where it is instead of relocating it.
var localBackends = map[string]bool{
	"local":      true,
	"local-fs":   true,
	"localfs":    true,
	"filesystem": true,
	"fs":         true,
	"minio":      true,
}

func isLocalBackend(b string) bool { return localBackends[strings.ToLower(b)] }

// legacyStorageBackend extracts the top-level `backend:` value from a bundle
// storage.yaml. It is deliberately a line scan rather than a YAML dependency:
// the only thing needed is one scalar at column 0, and a parse failure must
// degrade to "not found" rather than to an error path that changes a mode.
func legacyStorageBackend(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	// The bundle's storage.yaml is a few hundred bytes; cap the read so a
	// hostile/huge file cannot be pulled into memory here.
	buf := make([]byte, 64<<10)
	n, err := f.Read(buf)
	if n == 0 || (err != nil && n == 0) {
		return "", false
	}
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		// Top-level key only — no leading whitespace, so a nested
		// "backend:" under some other section cannot be mistaken for it.
		if !strings.HasPrefix(line, "backend:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "backend:"))
		if i := strings.Index(v, "#"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		v = strings.Trim(v, `"'`)
		if v == "" {
			return "", false
		}
		return v, true
	}
	return "", false
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular() && st.Size() > 0
}

// Close releases the underlying connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Get returns the persisted Config. Open pins a row on first use, so the
// missing-row branch below is a belt-and-braces path; when it is taken it
// returns the SAME evidence-resolved default Open computed (not a blanket
// local-fs), so a pre-existing hosted install never reads back as local.
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
		return s.defaultConfig(), nil
	}
	if err != nil {
		return s.defaultConfig(), fmt.Errorf("storagemode: get: %w", err)
	}
	if !cfg.Mode.Valid() {
		// Corrupt row: fall back to default rather than crashing.
		return s.defaultConfig(), nil
	}
	return cfg, nil
}

// defaultConfig is the evidence-resolved default for THIS box (see
// DefaultOrigin), falling back to the package default for a zero Store.
func (s *Store) defaultConfig() Config {
	if s == nil || !s.fallback.Valid() {
		return Defaults()
	}
	return Config{Mode: s.fallback}
}

// Set persists cfg, validating the mode and, for local-minio-sync, the
// minimum endpoint+bucket pair. Endpoints are trimmed; region defaults to
// "auto" when empty and the mode is local-minio-sync.
func (s *Store) Set(cfg Config) error {
	if s == nil || s.db == nil {
		return errors.New("storagemode: store not initialised")
	}
	if !cfg.Mode.Valid() {
		return fmt.Errorf("storagemode: invalid mode %q (want %q, %q or %q)",
			cfg.Mode, ModeLocalFS, ModeLocalMinIOSync, ModeCentralTigris)
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
// (lilmail, diwan) read at startup to learn the bundle storage
// mode and, when applicable, the local MinIO endpoint.
//
// For ModeLocalFS (the default) and ModeCentralTigris only VULOS_STORAGE_MODE
// is exported. local-fs needs nothing else: with no VULOS_STORAGE_ENDPOINT in
// the environment, internal/storage resolves to its local-FS root and the
// grant broker serves bytes off local disk. central-tigris likewise exports
// nothing extra — the legacy storage.yaml continues to provide the bucket and
// credentials, which is what the FROZEN invariant "default mode path
// unaffected" guaranteed for installs that were on it.
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
