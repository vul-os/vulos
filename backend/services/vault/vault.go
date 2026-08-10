package vault

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
	"vulos/backend/internal/datadir"
	vulenv "vulos/backend/services/env"

	"vulos/backend/internal/storage"
)

// Vault wraps Restic for silent, continuous backups to S3-compatible storage.
type Vault struct {
	s3       *storage.S3Config
	password string
	dataDir  string // directory to back up (e.g., /home or user data dir)
	mu       sync.Mutex
	status   Status
}

type Status struct {
	Initialized bool      `json:"initialized"`
	LastBackup  time.Time `json:"last_backup,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	Snapshots   int       `json:"snapshots"`
	Running     bool      `json:"running"`
}

type Snapshot struct {
	ID       string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Paths    []string  `json:"paths"`
}

// defaultResticPassword is the well-known dev fallback. Using this in
// production silently encrypts every backup with a value an attacker can guess
// from the source tree, so prod paths refuse to run with it (see usingDefaultPassword).
const defaultResticPassword = "vulos-default-key"

func New(s3 *storage.S3Config, dataDir string) *Vault {
	// VULOS_RESTIC_PASSWORD takes precedence (namespaced convention);
	// RESTIC_PASSWORD is accepted as a fallback for compatibility with
	// standard Restic tooling. Dev-only default if neither is set.
	password := os.Getenv("VULOS_RESTIC_PASSWORD")
	if password == "" {
		password = getenv("RESTIC_PASSWORD", defaultResticPassword)
	}
	return &Vault{
		s3:       s3,
		password: password,
		dataDir:  dataDir,
	}
}

// SetPassword updates the vault encryption passphrase at runtime and
// re-initializes the repository. Called by POST /init-passphrase when a
// managed VM boots and the passphrase is injected by the orchestrator.
func (v *Vault) SetPassword(ctx context.Context, passphrase string) error {
	if passphrase == "" {
		return fmt.Errorf("vault: passphrase must not be empty")
	}
	v.mu.Lock()
	v.password = passphrase
	v.mu.Unlock()
	return v.Init(ctx)
}

// usingDefaultPassword reports whether the vault is configured with the
// well-known dev fallback password. The fail-closed checks in Init / Backup
// consult this so RESTIC_PASSWORD can be left blank in dev without crashing.
func (v *Vault) usingDefaultPassword() bool {
	return v.password == defaultResticPassword
}

// errDefaultPasswordInProd is returned by Init/Backup when the box is running
// in production (--env=prod or VULOS_ENV=prod, whichever main() resolved) and
// the vault would otherwise encrypt backups with the dev fallback key.
var errDefaultPasswordInProd = fmt.Errorf("vault: RESTIC_PASSWORD is unset in a production environment — refusing to encrypt backups with the default dev key")

// Init initializes the Restic repository if not already done.
func (v *Vault) Init(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.s3.Configured() {
		return fmt.Errorf("s3 storage not configured")
	}

	if v.usingDefaultPassword() {
		if vulenv.IsProdActive() {
			return errDefaultPasswordInProd
		}
		log.Printf("[vault] WARNING: RESTIC_PASSWORD unset — encrypting backups with the deterministic dev key (NEVER use in production; start with --env=prod to enforce)")
	}

	// Check if repo exists
	cmd := v.resticCmd(ctx, "snapshots", "--json")
	if err := cmd.Run(); err == nil {
		v.status.Initialized = true
		return nil
	}

	// Initialize new repo
	cmd = v.resticCmd(ctx, "init")
	out, err := cmd.CombinedOutput()
	if err != nil {
		v.status.LastError = string(out)
		return fmt.Errorf("restic init failed: %s", out)
	}

	v.status.Initialized = true
	log.Printf("[vault] repository initialized")
	return nil
}

// Backup performs a silent snapshot of the data directory.
func (v *Vault) Backup(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.status.Initialized {
		return fmt.Errorf("vault not initialized")
	}

	// Defence in depth: even if Init() succeeded in a non-prod environment, a
	// later switch to VULOS_ENV=prod (or a process restart in prod that never
	// hits Init because the repo already exists) must not silently encrypt new
	// snapshots with the dev key.
	if v.usingDefaultPassword() && vulenv.IsProdActive() {
		return errDefaultPasswordInProd
	}

	v.status.Running = true
	defer func() { v.status.Running = false }()

	cmd := v.resticCmd(ctx, "backup", v.dataDir, "--json", "--exclude-caches")
	out, err := cmd.CombinedOutput()
	if err != nil {
		v.status.LastError = string(out)
		return fmt.Errorf("backup failed: %s", out)
	}

	v.status.LastBackup = time.Now()
	v.status.LastError = ""
	log.Printf("[vault] backup complete")
	return nil
}

// Snapshots lists all available snapshots.
func (v *Vault) Snapshots(ctx context.Context) ([]Snapshot, error) {
	cmd := v.resticCmd(ctx, "snapshots", "--json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}

	var snaps []Snapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, fmt.Errorf("failed to parse snapshots: %w", err)
	}

	v.mu.Lock()
	v.status.Snapshots = len(snaps)
	v.mu.Unlock()

	return snaps, nil
}

// Restore restores a snapshot to a target directory.
func (v *Vault) Restore(ctx context.Context, snapshotID, targetDir string) error {
	cmd := v.resticCmd(ctx, "restore", snapshotID, "--target", targetDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restore failed: %s", out)
	}
	log.Printf("[vault] restored snapshot %s to %s", snapshotID, targetDir)
	return nil
}

// Prune removes old snapshots keeping the last N.
func (v *Vault) Prune(ctx context.Context, keepLast int) error {
	cmd := v.resticCmd(ctx, "forget", "--keep-last", fmt.Sprintf("%d", keepLast), "--prune")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("prune failed: %s", out)
	}
	log.Printf("[vault] pruned, keeping last %d snapshots", keepLast)
	return nil
}

// Status returns the current vault status.
func (v *Vault) Status() Status {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.status
}

// StartSchedule runs automatic backups at the given interval.
func (v *Vault) StartSchedule(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := v.Backup(ctx); err != nil {
					log.Printf("[vault] scheduled backup error: %v", err)
				}
			}
		}
	}()
	log.Printf("[vault] scheduled backups every %s", interval)
}

func (v *Vault) resticCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "restic", args...)
	cmd.Env = append(os.Environ(), v.s3.ResticEnv()...)
	cmd.Env = append(cmd.Env, "RESTIC_PASSWORD="+v.password)
	return cmd
}

// FindRestic checks if restic is installed.
func FindRestic() bool {
	_, err := exec.LookPath("restic")
	return err == nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// DataDir returns the default data directory to back up.
func DataDir() string {
	d := datadir.Join("data")
	os.MkdirAll(d, 0755)
	return d
}
