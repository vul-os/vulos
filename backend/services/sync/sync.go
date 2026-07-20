// Package sync implements file synchronisation for Vula OS.
//
// It watches the user's data directories for changes via fsnotify, uploads
// modified files (plus .meta sidecar objects) to S3 through the cluster
// encrypted client, and pulls remote changes with conflict-copy semantics:
//
//   - If the local file is unchanged since the last sync, the remote version
//     overwrites it (simple overwrite).
//   - If both the local and the remote file diverged (both were edited), the
//     local copy is renamed to <name>.conflict-<nodeID>-<ts>.<ext> and the
//     remote version is written as the canonical file — no data is ever lost.
//
// Directories watched:
//
//	~/.vulos/data/              — user documents, downloads, app data
//	~/.vulos/db/browser-profiles/ — browser profile directories
//
// Directories ignored (never uploaded):
//
//	~/.vulos/apps/bin/          — app binaries; reinstalled from registry
package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"vulos/backend/internal/datadir"

	"github.com/fsnotify/fsnotify"
	"vulos/backend/services/cluster"
)

// S3Client is the subset of *cluster.Client used by this package.
// Keeping the dependency as an interface lets tests inject a fake.
type S3Client interface {
	PutEncrypted(ctx context.Context, key string, data []byte) error
	GetEncrypted(ctx context.Context, key string) ([]byte, error)
}

// S3Lister is the optional half of S3Client that lets the syncer discover what
// the remote holds. Without it the syncer can only push: Pull needs to be told
// which keys to fetch, and nothing else in the process knows them.
//
// It is a separate interface rather than a method on S3Client so that a client
// which genuinely cannot list (a write-only credential, a test double) stays a
// valid S3Client instead of being forced to implement a stub that lies.
// *cluster.Client satisfies it via ListPrefix.
type S3Lister interface {
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
}

// FileMeta is the JSON sidecar uploaded alongside each file as
// files/{rel_path}.meta. It carries enough information to detect divergence
// between two nodes without downloading the full file.
type FileMeta struct {
	NodeID    string    `json:"node_id"`
	Timestamp time.Time `json:"timestamp"`
	Hash      string    `json:"hash"` // hex-encoded SHA-256 of the file content
}

// Config holds the configuration for the Syncer.
type Config struct {
	// NodeID is the unique identifier for this node (e.g. "home-server").
	// Defaults to the OS hostname when empty.
	NodeID string

	// VulosRoot is the base directory used for relative-path computation in
	// S3 keys (default ~/.vulos). Override in tests to use a temp directory.
	VulosRoot string

	// DataDir is the root user data directory (default ~/.vulos/data).
	DataDir string

	// BrowserProfileDir is the browser-profiles directory
	// (default ~/.vulos/db/browser-profiles).
	BrowserProfileDir string

	// PullInterval is how often to fetch remote changes. Zero means unset and
	// New substitutes DefaultPullInterval; a negative value disables downward
	// sync, leaving the syncer push-only. Zero is deliberately not the "off"
	// switch: the production call site passes a bare Config{} and would then
	// silently keep the push-only behaviour this field exists to end.
	PullInterval time.Duration

	// IgnoreDir is the directory that must never be synced
	// (default ~/.vulos/apps/bin).
	IgnoreDir string
}

// defaultVulosRoot returns the default ~/.vulos path.
func defaultVulosRoot() string {
	return datadir.Root()
}

// defaultConfig returns a Config populated from the OS environment / home dir.
func defaultConfig() Config {
	vulos := defaultVulosRoot()
	return Config{
		VulosRoot:         vulos,
		DataDir:           filepath.Join(vulos, "data"),
		BrowserProfileDir: filepath.Join(vulos, "db", "browser-profiles"),
		IgnoreDir:         filepath.Join(vulos, "apps", "bin"),
		PullInterval:      DefaultPullInterval,
	}
}

// DefaultPullInterval is how often a box fetches remote changes when Config
// does not say. Two minutes is well under the window in which a person notices
// a file is missing on their other machine, and far above the rate at which a
// LIST costs anything.
const DefaultPullInterval = 2 * time.Minute

// Syncer watches local directories and syncs file changes to/from S3.
type Syncer struct {
	cfg     Config
	client  S3Client
	watcher *fsnotify.Watcher
	mu      sync.Mutex
	// lastHash maps local absolute path → SHA-256 hash at last sync.
	// Used to detect "unchanged since last pull" during pull.
	lastHash map[string]string
	// watchDirs lists the directories that are actively watched.
	watchDirs []string
	// lastSyncAt is the UTC timestamp of the most recent successful upload.
	// Zero value means no sync has occurred yet.
	lastSyncAt time.Time
}

// LastSyncTime returns the UTC time of the most recent successful file upload.
// Returns the zero time if no sync has occurred since the Syncer started.
func (s *Syncer) LastSyncTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSyncAt
}

// New creates a Syncer. Call Start(ctx) to begin watching.
// cfg may be the zero value — defaults are applied automatically.
// If cfg.NodeID is empty, os.Hostname() is used.
func New(cfg Config, client S3Client) (*Syncer, error) {
	// Apply defaults for unset fields.
	def := defaultConfig()
	if cfg.DataDir == "" {
		cfg.DataDir = def.DataDir
	}
	if cfg.BrowserProfileDir == "" {
		cfg.BrowserProfileDir = def.BrowserProfileDir
	}
	if cfg.IgnoreDir == "" {
		cfg.IgnoreDir = def.IgnoreDir
	}
	// A zero PullInterval means "unset" here, not "disabled": the production
	// call site passes sync.Config{} and relies on New to fill the blanks, so
	// treating zero as disabled would leave the shipped box push-only -- exactly
	// the bug this loop exists to fix. A caller that genuinely wants upload-only
	// sets it negative.
	if cfg.PullInterval == 0 {
		cfg.PullInterval = def.PullInterval
	}
	if cfg.PullInterval < 0 {
		cfg.PullInterval = 0
	}
	if cfg.VulosRoot == "" {
		cfg.VulosRoot = def.VulosRoot
	}
	if cfg.NodeID == "" {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "unknown"
		}
		cfg.NodeID = h
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("sync: create watcher: %w", err)
	}

	return &Syncer{
		cfg:      cfg,
		client:   client,
		watcher:  watcher,
		lastHash: make(map[string]string),
	}, nil
}

// NewFromCluster creates a Syncer using the cluster package's S3 client directly.
// This is the production constructor. Use New() with a mock for tests.
func NewFromCluster(cfg Config, cc *cluster.Client) (*Syncer, error) {
	return New(cfg, cc)
}

// Start begins watching directories and processing events.
// It blocks until ctx is cancelled.
func (s *Syncer) Start(ctx context.Context) error {
	// Ensure watched dirs exist.
	for _, dir := range []string{s.cfg.DataDir, s.cfg.BrowserProfileDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("sync: mkdir %s: %w", dir, err)
		}
	}

	// Walk and add every sub-directory (fsnotify does not recurse).
	for _, dir := range []string{s.cfg.DataDir, s.cfg.BrowserProfileDir} {
		if err := s.addWatchRecursive(dir); err != nil {
			return fmt.Errorf("sync: watch %s: %w", dir, err)
		}
	}

	log.Printf("[sync] watching %s and %s (ignoring %s)",
		s.cfg.DataDir, s.cfg.BrowserProfileDir, s.cfg.IgnoreDir)

	defer s.watcher.Close()

	// Downward sync (SYNC-PULL-01). The upload path is driven by fsnotify, but
	// nothing ever called Pull, so this service has been push-only in practice:
	// a change made on one box reached S3 and stopped there, and the docs
	// described a bidirectional sync that did not exist.
	//
	// Pull already had the careful part -- it skips keys this node uploaded,
	// compares hashes, and writes a .conflict-<node>-<ts> copy before letting a
	// remote version win, so a local edit is never silently destroyed. What it
	// lacked was a caller. It runs once at boot (the state a box has been away
	// from is exactly the state it needs on waking) and then on a timer.
	//
	// A pull failure is logged, never fatal: losing downward sync must not take
	// the upload watcher down with it.
	if s.cfg.PullInterval > 0 {
		go s.pullLoop(ctx)
	} else {
		log.Printf("[sync] downward sync disabled (PullInterval is 0) -- this box uploads but never fetches")
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-s.watcher.Events:
			if !ok {
				return nil
			}
			s.handleEvent(ctx, event)
		case err, ok := <-s.watcher.Errors:
			if !ok {
				return nil
			}
			log.Printf("[sync] watcher error: %v", err)
		}
	}
}

// pullLoop fetches remote changes at boot and then every PullInterval until ctx
// is cancelled.
func (s *Syncer) pullLoop(ctx context.Context) {
	pull := func() {
		if err := s.PullAll(ctx); err != nil {
			if errors.Is(err, ErrNoLister) {
				// Structural, not transient: log once and stop trying.
				log.Printf("[sync] downward sync unavailable: %v", err)
				return
			}
			log.Printf("[sync] pull: %v", err)
		}
	}
	pull()

	t := time.NewTicker(s.cfg.PullInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pull()
		}
	}
}

// addWatchRecursive adds dir and every sub-directory to the fsnotify watcher,
// skipping ignored paths.
func (s *Syncer) addWatchRecursive(dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable paths
		}
		if !info.IsDir() {
			return nil
		}
		if s.isIgnored(path) {
			return filepath.SkipDir
		}
		if watchErr := s.watcher.Add(path); watchErr != nil {
			log.Printf("[sync] cannot watch %s: %v", path, watchErr)
		}
		s.watchDirs = append(s.watchDirs, path)
		return nil
	})
}

// handleEvent processes a single fsnotify event.
func (s *Syncer) handleEvent(ctx context.Context, event fsnotify.Event) {
	path := event.Name
	if s.isIgnored(path) {
		return
	}
	// Skip .meta sidecars we wrote ourselves.
	if strings.HasSuffix(path, ".meta") {
		return
	}

	switch {
	case event.Has(fsnotify.Create) || event.Has(fsnotify.Write):
		// Stat to check it's a regular file (not a directory).
		info, err := os.Stat(path)
		if err != nil {
			return
		}
		if info.IsDir() {
			// A new directory — watch it too.
			if !s.isIgnored(path) {
				_ = s.watcher.Add(path)
			}
			return
		}
		if err := s.upload(ctx, path); err != nil {
			log.Printf("[sync] upload %s: %v", path, err)
		}

	case event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename):
		// File removed locally — nothing to upload; remote retains its copy.
		// Updating lastHash is not strictly necessary but keeps it tidy.
		s.mu.Lock()
		delete(s.lastHash, path)
		s.mu.Unlock()
	}
}

// isIgnored returns true when path falls under the configured ignore directory.
func (s *Syncer) isIgnored(path string) bool {
	if s.cfg.IgnoreDir == "" {
		return false
	}
	ignoreAbs := s.cfg.IgnoreDir
	if !strings.HasSuffix(ignoreAbs, string(os.PathSeparator)) {
		ignoreAbs += string(os.PathSeparator)
	}
	return path == s.cfg.IgnoreDir || strings.HasPrefix(path, ignoreAbs)
}

// upload reads a local file, hashes it, and uploads both the file data and a
// .meta sidecar to S3. It stores the hash in lastHash so Pull can later
// detect whether the file changed locally since the last pull.
func (s *Syncer) upload(ctx context.Context, absPath string) error {
	relPath, err := s.relPath(absPath)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	hash := sha256Hex(data)

	// Upload the file content.
	s3Key := "files/" + relPath
	if err := s.client.PutEncrypted(ctx, s3Key, data); err != nil {
		return fmt.Errorf("put file: %w", err)
	}

	// Upload the .meta sidecar.
	meta := FileMeta{
		NodeID:    s.cfg.NodeID,
		Timestamp: time.Now().UTC(),
		Hash:      hash,
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	if err := s.client.PutEncrypted(ctx, s3Key+".meta", metaJSON); err != nil {
		return fmt.Errorf("put meta: %w", err)
	}

	// Record hash as "local known state" for conflict detection on pull,
	// and update the last-sync timestamp for the health endpoint.
	s.mu.Lock()
	s.lastHash[absPath] = hash
	s.lastSyncAt = time.Now().UTC()
	s.mu.Unlock()

	log.Printf("[sync] uploaded %s (%s)", relPath, hash[:8])
	return nil
}

// Pull fetches all files under files/ from S3 and applies them locally.
//
// For each remote file:
//   - If the local file does not exist, or is unchanged since the last sync
//     (lastHash matches the current on-disk content), the remote version is
//     written directly.
//   - If the local file has been edited since the last pull (local hash differs
//     from lastHash AND from the remote hash), a conflict copy is created at
//     <base>.conflict-<nodeID>-<ts>.<ext> and the remote version is written as
//     the canonical file.
//
// Pull is typically called on boot and then on a timer; the continuous
// upload path is driven by fsnotify.
func (s *Syncer) Pull(ctx context.Context, remoteKeys []string) error {
	for _, key := range remoteKeys {
		if err := s.pullOne(ctx, key); err != nil {
			log.Printf("[sync] pull %s: %v", key, err)
		}
	}
	return nil
}

// PullAll lists the remote files/ prefix and applies everything it finds.
//
// Pull takes an explicit key list because the S3Client contract cannot list.
// PullAll is the loop's entry point: it needs a client that also implements
// S3Lister, and reports ErrNoLister when handed one that does not, so a
// deployment that cannot pull says so rather than appearing to sync.
func (s *Syncer) PullAll(ctx context.Context) error {
	lister, ok := s.client.(S3Lister)
	if !ok {
		return ErrNoLister
	}
	keys, err := lister.ListPrefix(ctx, "files/")
	if err != nil {
		return fmt.Errorf("sync: list files/: %w", err)
	}
	return s.Pull(ctx, keys)
}

// ErrNoLister is returned by PullAll when the configured S3Client cannot
// enumerate remote keys, which makes downward sync impossible.
var ErrNoLister = errors.New("sync: S3Client does not implement S3Lister; downward sync unavailable")

// pullOne applies a single remote S3 key (e.g. "files/docs/notes.txt") to the
// local filesystem.
func (s *Syncer) pullOne(ctx context.Context, s3Key string) error {
	// We only handle file keys under files/.
	if !strings.HasPrefix(s3Key, "files/") {
		return nil
	}
	// Skip .meta sidecars — we process them alongside the main file.
	if strings.HasSuffix(s3Key, ".meta") {
		return nil
	}

	// Fetch the remote .meta to learn the remote hash and source node.
	metaData, err := s.client.GetEncrypted(ctx, s3Key+".meta")
	if err != nil {
		return fmt.Errorf("get meta: %w", err)
	}
	var remoteMeta FileMeta
	if err := json.Unmarshal(metaData, &remoteMeta); err != nil {
		return fmt.Errorf("parse meta: %w", err)
	}

	// Skip files that originated from this node (we uploaded them).
	if remoteMeta.NodeID == s.cfg.NodeID {
		return nil
	}

	// Fetch the remote file content.
	remoteData, err := s.client.GetEncrypted(ctx, s3Key)
	if err != nil {
		return fmt.Errorf("get file: %w", err)
	}

	// Determine local path.
	relPath := strings.TrimPrefix(s3Key, "files/")
	absPath := s.absPath(relPath)

	// Compute local hash if the file exists on disk.
	localData, err := os.ReadFile(absPath)
	localExists := err == nil
	var localHash string
	if localExists {
		localHash = sha256Hex(localData)
	}

	s.mu.Lock()
	knownHash := s.lastHash[absPath]
	s.mu.Unlock()

	// Decision: does the local file need a conflict copy?
	needsConflict := localExists &&
		localHash != remoteMeta.Hash && // remote is different from local
		localHash != knownHash // local was edited since last sync

	if needsConflict {
		conflictPath := conflictCopyPath(absPath, remoteMeta.NodeID, remoteMeta.Timestamp)
		if err := writeFile(conflictPath, localData); err != nil {
			return fmt.Errorf("write conflict copy: %w", err)
		}
		log.Printf("[sync] conflict: local %s saved as %s", relPath, filepath.Base(conflictPath))
	}

	// Write the remote (canonical) version.
	if err := writeFile(absPath, remoteData); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	// Update lastHash so future pulls know this is the synced baseline.
	s.mu.Lock()
	s.lastHash[absPath] = remoteMeta.Hash
	s.mu.Unlock()

	log.Printf("[sync] pulled %s from node %s", relPath, remoteMeta.NodeID)
	return nil
}

// relPath returns the path of absPath relative to the Vulos root directory,
// preserving a stable prefix so all files can be stored under files/ in S3
// regardless of which watch directory they came from.
//
// <vulosRoot>/data/docs/notes.txt         → data/docs/notes.txt
// <vulosRoot>/db/browser-profiles/chrome  → db/browser-profiles/chrome
func (s *Syncer) relPath(absPath string) (string, error) {
	rel, err := filepath.Rel(s.cfg.VulosRoot, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		// Not under VulosRoot — use basename as fallback.
		return filepath.Base(absPath), nil
	}
	return filepath.ToSlash(rel), nil
}

// absPath is the inverse of relPath: reconstructs the absolute path from a
// relative path stored in S3 (under files/).
func (s *Syncer) absPath(relPath string) string {
	return filepath.Join(s.cfg.VulosRoot, filepath.FromSlash(relPath))
}

// conflictCopyPath returns the path for a conflict copy of absPath.
// The format is: <dir>/<base>.conflict-<nodeID>-<yyyymmdd-hhmmss>.<ext>
//
// Example: /home/alice/.vulos/data/notes.txt edited on "office" node at
// 2026-05-16 14:30:22 → .../notes.conflict-office-20260516-143022.txt
func conflictCopyPath(absPath, nodeID string, ts time.Time) string {
	dir := filepath.Dir(absPath)
	base := filepath.Base(absPath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	timestamp := ts.UTC().Format("20060102-150405")
	return filepath.Join(dir, fmt.Sprintf("%s.conflict-%s-%s%s", stem, nodeID, timestamp, ext))
}

// writeFile writes data to path, creating any missing parent directories.
func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// sha256Hex returns the lowercase hex-encoded SHA-256 digest of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
