// Package joinsync implements the backend for joining an existing Vulos
// cluster from a brand-new device (roadmap task INIT-08).
//
// Flow:
//
//  1. The setup wizard POSTs {bucket, region, access, secret, passphrase} to
//     POST /api/setup/join.
//  2. Join validates that the S3 bucket is reachable AND that the passphrase
//     is correct, by deriving the cluster Argon2id key (via the cluster
//     package's shared-salt mechanism) and decrypting a well-known marker
//     object. A wrong passphrase fails decryption → clear error.
//  3. On success it persists the S3 credentials to ~/.vulos/db/storage.json
//     in the exact shape storageprov writes (the passphrase is NEVER written
//     to disk — it lives only in memory), ensures ~/.vulos/db/instance.json
//     exists so bootmode reports the node, and writes
//     ~/.vulos/db/sync-state.json {status:"syncing",...} so bootmode.Detect
//     enters "sync" mode.
//  4. A background goroutine performs the actual S3 pull, updating
//     sync-state.json progress, and finally flips status away from "syncing".
//
// IMPORTANT — respect main's real APIs:
//   - storageprov.storageprovState is unexported, so storage.json is written
//     directly here with a struct that emits the identical JSON shape.
//   - bootmode's sync-state types are unexported, so sync-state.json is
//     written directly with the {status,phase,progress_pct} shape that
//     bootmode.Detect reads ({"status":"syncing"} → mode "sync").
//   - instance.json is created via the identity package's public Load(), the
//     same writer the rest of the system uses.
package joinsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vulos/backend/services/cluster"
	"vulos/backend/services/identity"
)

// joinMarkerKey is the well-known S3 object every cluster carries. Its
// (SSE-C encrypted) content is joinMarkerPlaintext. A node that knows the
// correct passphrase derives the same Argon2id key (the salt is shared via
// S3 by the cluster package) and can therefore decrypt it back to the known
// plaintext. A wrong passphrase yields a decryption error.
const joinMarkerKey = "cluster/join-marker"

// joinMarkerPlaintext is the canonical sentinel stored at joinMarkerKey.
const joinMarkerPlaintext = "vulos-cluster-join-marker-v1"

// storageFileName / syncStateFileName / instanceFileName mirror the filenames
// the rest of the system uses inside ~/.vulos/db. They are duplicated here
// deliberately (the owning packages keep them unexported) — the JSON *shape*
// is what matters for cross-service compatibility, asserted by tests.
const (
	storageFileName   = "storage.json"
	syncStateFileName = "sync-state.json"
)

// Sentinel errors so the HTTP layer can map to precise status codes.
var (
	// ErrBadRequest indicates a missing/blank required field.
	ErrBadRequest = errors.New("joinsync: missing required field")
	// ErrUnreachable indicates the S3 endpoint/bucket could not be reached.
	ErrUnreachable = errors.New("joinsync: S3 bucket unreachable")
	// ErrBadPassphrase indicates the passphrase failed to decrypt the marker.
	ErrBadPassphrase = errors.New("joinsync: incorrect passphrase for this cluster")
)

// JoinRequest is the body accepted by POST /api/setup/join.
// Passphrase is intentionally NEVER persisted to disk.
type JoinRequest struct {
	Bucket     string `json:"bucket"`
	Region     string `json:"region"`
	Access     string `json:"access"`
	Secret     string `json:"secret"`
	Passphrase string `json:"passphrase"`
	// Endpoint is optional; when blank the cluster default ("localhost:9000")
	// or the VULOS_S3_ENDPOINT env var is used. Exposed for self-hosted /
	// non-AWS object stores.
	Endpoint string `json:"endpoint,omitempty"`
	// UseSSL toggles TLS for the S3 endpoint (default false for local MinIO).
	UseSSL bool `json:"use_ssl,omitempty"`
}

// JoinResult is returned to the caller on a successful validation.
type JoinResult struct {
	Status   string `json:"status"`             // always "syncing" on success
	Bucket   string `json:"bucket"`             // echoed back (no credentials)
	Region   string `json:"region,omitempty"`   // echoed back
	Endpoint string `json:"endpoint,omitempty"` // echoed back
}

// storageState is the on-disk shape of ~/.vulos/db/storage.json.
//
// It is a SUPERSET that satisfies both readers that exist on main:
//   - storageprov (POST /api/setup/storage): enabled, access_key, secret_key,
//     ssec_key, bucket_name, provisioned_at.
//   - routes_storage.go CLUSTER-06 (GET /api/storage/status): endpoint,
//     bucket, region, access_key, secret_key.
//
// We never touch ssec_key from the join path (SSE-C is keyed off the
// passphrase-derived key by the cluster package, not this file). The
// passphrase itself is never a field here — by design.
type storageState struct {
	// storageprov fields
	Enabled       bool      `json:"enabled"`
	AccessKey     string    `json:"access_key"`
	SecretKey     string    `json:"secret_key"`
	SSECKey       string    `json:"ssec_key"`
	BucketName    string    `json:"bucket_name"`
	ProvisionedAt time.Time `json:"provisioned_at"`
	// CLUSTER-06 status-reader fields (harmless to storageprov, which ignores
	// unknown keys when it overwrites the file).
	Endpoint string  `json:"endpoint"`
	Bucket   string  `json:"bucket"`
	Region   string  `json:"region"`
	SizeGB   float64 `json:"size_gb"`
	UsedGB   float64 `json:"used_gb"`
}

// syncState is the on-disk shape of ~/.vulos/db/sync-state.json.
// bootmode.Detect only inspects "status"; "phase"/"progress_pct" are extra
// telemetry the setup UI can poll. status=="syncing" → bootmode mode "sync".
type syncState struct {
	Status      string `json:"status"` // "syncing" | "complete" | "error"
	Phase       string `json:"phase"`
	ProgressPct int    `json:"progress_pct"`
	Message     string `json:"message,omitempty"`
	UpdatedAt   string `json:"updated_at"`
}

// clusterBackend abstracts the S3 round-trips so tests can inject a mock
// (no real AWS / no minio dialing). The production implementation wraps the
// cluster package.
type clusterBackend interface {
	// validate connects to S3 with cfg, derives the cluster key from
	// passphrase, and confirms the passphrase by decrypting (or, on a fresh
	// bucket, by writing) the join marker.
	//   - ErrUnreachable  → endpoint/bucket not reachable / bad S3 creds.
	//   - ErrBadPassphrase → marker present but won't decrypt.
	validate(ctx context.Context, cfg cluster.S3Config, passphrase string) error
	// pull performs the post-join data pull. progress is called with
	// (phase, pct) updates; it must return nil on success.
	pull(ctx context.Context, cfg cluster.S3Config, passphrase string, progress func(phase string, pct int)) error
}

// backend is the active clusterBackend. Tests swap it via SetBackendForTest.
var backend clusterBackend = realBackend{}

// progress getter state -------------------------------------------------------

var (
	progMu      sync.RWMutex
	lastProg    syncState
	progLoaded  bool
	progHomeDir string
)

// Validate / Join -------------------------------------------------------------

func sanitize(req *JoinRequest) error {
	req.Bucket = strings.TrimSpace(req.Bucket)
	req.Region = strings.TrimSpace(req.Region)
	req.Access = strings.TrimSpace(req.Access)
	req.Secret = strings.TrimSpace(req.Secret)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	// Passphrase is NOT trimmed of internal content but leading/trailing
	// whitespace is almost always an input error; trim for usability.
	req.Passphrase = strings.TrimSpace(req.Passphrase)

	switch {
	case req.Bucket == "":
		return fmt.Errorf("%w: bucket", ErrBadRequest)
	case req.Access == "":
		return fmt.Errorf("%w: access", ErrBadRequest)
	case req.Secret == "":
		return fmt.Errorf("%w: secret", ErrBadRequest)
	case req.Passphrase == "":
		return fmt.Errorf("%w: passphrase", ErrBadRequest)
	}
	return nil
}

func s3ConfigFor(req JoinRequest) cluster.S3Config {
	endpoint := req.Endpoint
	if endpoint == "" {
		// Fall back to the cluster package's env-driven default.
		endpoint = cluster.LoadS3Config().Endpoint
	}
	return cluster.S3Config{
		Endpoint:  endpoint,
		Bucket:    req.Bucket,
		AccessKey: req.Access,
		SecretKey: req.Secret,
		UseSSL:    req.UseSSL,
	}
}

// Join performs the SYNCHRONOUS validation (S3 reachability + passphrase),
// persists S3 creds + writes sync-state.json, then spawns the async pull.
// The passphrase is held only in memory for the lifetime of the pull
// goroutine and is never written anywhere.
func Join(req JoinRequest, home string) (*JoinResult, error) {
	if err := sanitize(&req); err != nil {
		return nil, err
	}

	cfg := s3ConfigFor(req)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := backend.validate(ctx, cfg, req.Passphrase); err != nil {
		return nil, err
	}

	// Persist S3 credentials (NO passphrase) in the storageprov shape.
	if err := writeStorageState(home, req, cfg); err != nil {
		return nil, fmt.Errorf("joinsync: persist storage.json: %w", err)
	}

	// Ensure instance.json exists so bootmode.Detect can leave "setup" and,
	// with sync-state syncing, report "sync".
	if _, err := identity.Load(home); err != nil {
		return nil, fmt.Errorf("joinsync: ensure instance.json: %w", err)
	}

	// Mark syncing so bootmode.Detect → "sync".
	if err := writeSyncState(home, syncState{
		Status:      "syncing",
		Phase:       "starting",
		ProgressPct: 0,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return nil, fmt.Errorf("joinsync: write sync-state.json: %w", err)
	}

	// Spawn the actual S3 pull. The passphrase stays only in this closure.
	passphrase := req.Passphrase
	go runPull(home, cfg, passphrase)

	return &JoinResult{
		Status:   "syncing",
		Bucket:   req.Bucket,
		Region:   req.Region,
		Endpoint: cfg.Endpoint,
	}, nil
}

// runPull executes the background pull and keeps sync-state.json current.
func runPull(home string, cfg cluster.S3Config, passphrase string) {
	// pullTimeout is generous — an initial cluster pull can be large.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	progress := func(phase string, pct int) {
		_ = writeSyncState(home, syncState{
			Status:      "syncing",
			Phase:       phase,
			ProgressPct: pct,
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
		})
	}

	if err := backend.pull(ctx, cfg, passphrase, progress); err != nil {
		_ = writeSyncState(home, syncState{
			Status:      "error",
			Phase:       "pull",
			ProgressPct: 0,
			Message:     err.Error(),
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	_ = writeSyncState(home, syncState{
		Status:      "complete",
		Phase:       "done",
		ProgressPct: 100,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	})
}

// Status getter ---------------------------------------------------------------

// Progress returns the current sync state for GET /api/setup/join/status.
// It reads ~/.vulos/db/sync-state.json fresh each call so it reflects the
// background goroutine's latest write even across process restarts.
func Progress(home string) syncState {
	st, err := readSyncState(home)
	if err != nil {
		// No sync-state yet (never joined, or file removed) → idle.
		return syncState{Status: "idle", Phase: "", ProgressPct: 0}
	}
	progMu.Lock()
	lastProg = st
	progLoaded = true
	progHomeDir = home
	progMu.Unlock()
	return st
}

// on-disk helpers -------------------------------------------------------------

func dbDir(home string) string { return filepath.Join(home, ".vulos", "db") }

func writeStorageState(home string, req JoinRequest, cfg cluster.S3Config) error {
	dir := dbDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st := storageState{
		Enabled:       true,
		AccessKey:     req.Access,
		SecretKey:     req.Secret,
		SSECKey:       "", // SSE-C key is derived from the passphrase at runtime.
		BucketName:    req.Bucket,
		ProvisionedAt: time.Now().UTC(),
		Endpoint:      cfg.Endpoint,
		Bucket:        req.Bucket,
		Region:        req.Region,
	}
	return atomicWriteJSON(filepath.Join(dir, storageFileName), st)
}

func writeSyncState(home string, st syncState) error {
	dir := dbDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return atomicWriteJSON(filepath.Join(dir, syncStateFileName), st)
}

func readSyncState(home string) (syncState, error) {
	var st syncState
	data, err := os.ReadFile(filepath.Join(dbDir(home), syncStateFileName))
	if err != nil {
		return st, err
	}
	if err := jsonUnmarshal(data, &st); err != nil {
		return st, err
	}
	return st, nil
}
