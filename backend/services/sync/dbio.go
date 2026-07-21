// Package sync — DB snapshot/rehydrate glue between the Compactor/Restorer
// library layer and a real on-disk modernc.org/sqlite database.
//
// This file provides the two caller-supplied callbacks the snapshot library
// declares but does not implement:
//
//   - SnapshotDB  → a DBSnapshot that captures a consistent point-in-time image
//     of the live SQLite database file using `VACUUM INTO` (pure-Go, no CGO),
//     reads the resulting compact image as opaque bytes, and reports the DB
//     version (PRAGMA user_version, with a content-hash fallback so the
//     Compactor's "only write if newer" guard still makes monotonic progress).
//   - RehydrateDB → a DBRehydrate that takes a decrypted snapshot image and
//     atomically swaps it in as the live database file. Restore is DESTRUCTIVE:
//     the previous database file is replaced. The new image is validated
//     (opens + integrity_check) before the swap so a corrupt blob can never
//     clobber a good DB.
//
// Both helpers use modernc.org/sqlite exclusively (CGO_ENABLED=0). They never
// see or persist the SSE-C passphrase — encryption is owned by cluster.Client.
package sync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"vulos/backend/services/cluster"
	"vulos/backend/services/lease"
)

// sqliteDSN builds a modernc.org/sqlite DSN with the same pragmas the auth
// store uses (busy_timeout + WAL), so a snapshot/rehydrate connection behaves
// identically to the production connection.
func sqliteDSN(path string) string {
	return path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
}

// SnapshotDB returns a DBSnapshot that captures dbPath via `VACUUM INTO`.
//
// VACUUM INTO writes a fully consistent, compacted copy of the database to a
// new file while holding a read transaction, so the produced image is a valid
// standalone SQLite database even while writers are active. modernc.org/sqlite
// supports it natively with no CGO.
//
// The reported version is PRAGMA user_version when it is > 0; otherwise it
// falls back to a deterministic 63-bit value derived from the SHA-256 of the
// image bytes. The fallback is monotone-friendly enough for the Compactor's
// "skip if not newer than existing" guard: identical content yields the same
// version (idempotent re-runs are skipped), changed content yields a different
// version (a new snapshot is written). Operators who want strict monotonic
// versions can bump user_version on each migration.
func SnapshotDB(dbPath string) DBSnapshot {
	return func(ctx context.Context) ([]byte, int64, error) {
		if dbPath == "" {
			return nil, 0, errors.New("sync: SnapshotDB: empty db path")
		}
		if _, err := os.Stat(dbPath); err != nil {
			// No DB yet — nothing to back up. Empty data makes the Compactor skip.
			if errors.Is(err, os.ErrNotExist) {
				return nil, 0, nil
			}
			return nil, 0, fmt.Errorf("sync: SnapshotDB: stat %s: %w", dbPath, err)
		}

		db, err := sql.Open("sqlite", sqliteDSN(dbPath))
		if err != nil {
			return nil, 0, fmt.Errorf("sync: SnapshotDB: open %s: %w", dbPath, err)
		}
		db.SetMaxOpenConns(1)
		defer db.Close()
		if err := db.PingContext(ctx); err != nil {
			return nil, 0, fmt.Errorf("sync: SnapshotDB: ping %s: %w", dbPath, err)
		}

		// VACUUM INTO needs a destination path that does not yet exist.
		tmp, err := os.CreateTemp(filepath.Dir(dbPath), ".vulos-snapshot-*.db")
		if err != nil {
			return nil, 0, fmt.Errorf("sync: SnapshotDB: temp file: %w", err)
		}
		tmpPath := tmp.Name()
		tmp.Close()
		// VACUUM INTO refuses to overwrite an existing file.
		os.Remove(tmpPath)
		defer os.Remove(tmpPath)

		if _, err := db.ExecContext(ctx, "VACUUM INTO ?", tmpPath); err != nil {
			return nil, 0, fmt.Errorf("sync: SnapshotDB: VACUUM INTO: %w", err)
		}

		var userVersion int64
		if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
			// Non-fatal — fall back to the content-hash version below.
			userVersion = 0
		}

		data, err := os.ReadFile(tmpPath)
		if err != nil {
			return nil, 0, fmt.Errorf("sync: SnapshotDB: read image: %w", err)
		}
		if len(data) == 0 {
			return nil, 0, errors.New("sync: SnapshotDB: produced empty image")
		}

		version := userVersion
		if version <= 0 {
			version = contentVersion(data)
		}
		return data, version, nil
	}
}

// contentVersion derives a stable, positive 63-bit version from the first 8
// bytes of the SHA-256 of the snapshot image. Same bytes → same version (so
// idempotent backups are skipped); different bytes → (almost certainly) a
// different version (so a changed DB produces a fresh snapshot).
func contentVersion(data []byte) int64 {
	sum := sha256.Sum256(data)
	v := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
	if v == 0 {
		v = 1
	}
	return v
}

// RehydrateDB returns a DBRehydrate that atomically replaces the database file
// at dbPath with the supplied snapshot image.
//
// Restore is DESTRUCTIVE. To avoid replacing a good DB with a corrupt blob, the
// image is first written to a temp file, opened, and integrity-checked; only
// then is it renamed over dbPath. WAL/SHM sidecars from the previous DB are
// removed so the swapped-in image is read cleanly. The caller is responsible
// for ensuring no other process holds the DB open during a restore (the
// `vulos restore` CLI runs offline; the HTTP endpoint is guarded + admin-only).
func RehydrateDB(dbPath string) DBRehydrate {
	return func(ctx context.Context, data []byte, version int64) error {
		if dbPath == "" {
			return errors.New("sync: RehydrateDB: empty db path")
		}
		if len(data) == 0 {
			return errors.New("sync: RehydrateDB: empty snapshot image")
		}

		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return fmt.Errorf("sync: RehydrateDB: mkdir: %w", err)
		}

		// Write the candidate image to a temp file in the same directory so the
		// final rename is atomic (same filesystem).
		tmp, err := os.CreateTemp(filepath.Dir(dbPath), ".vulos-restore-*.db")
		if err != nil {
			return fmt.Errorf("sync: RehydrateDB: temp file: %w", err)
		}
		tmpPath := tmp.Name()
		cleanup := true
		defer func() {
			if cleanup {
				os.Remove(tmpPath)
			}
		}()
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			return fmt.Errorf("sync: RehydrateDB: write image: %w", err)
		}
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return fmt.Errorf("sync: RehydrateDB: fsync image: %w", err)
		}
		if err := tmp.Close(); err != nil {
			return fmt.Errorf("sync: RehydrateDB: close image: %w", err)
		}

		// Validate the candidate before we destroy the live DB.
		if err := validateSQLite(ctx, tmpPath); err != nil {
			return fmt.Errorf("sync: RehydrateDB: candidate failed validation (version=%d): %w", version, err)
		}

		// Remove stale WAL/SHM sidecars from the previous DB so a fresh image is
		// read without leftover journal state.
		_ = os.Remove(dbPath + "-wal")
		_ = os.Remove(dbPath + "-shm")

		// Atomic swap.
		if err := os.Rename(tmpPath, dbPath); err != nil {
			return fmt.Errorf("sync: RehydrateDB: swap into place: %w", err)
		}
		cleanup = false
		return nil
	}
}

// validateSQLite opens path as a SQLite DB and runs a quick integrity check.
// Returns an error if the file is not a usable SQLite database.
func validateSQLite(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check returned %q", result)
	}
	return nil
}

// ── Production builders: wire Compactor/Restorer to the live cluster client ──

// BackupConfig holds everything needed to build a production Compactor.
type BackupConfig struct {
	// NodeID is the lease holder ID (defaults to the OS hostname when empty).
	NodeID string
	// DBPath is the SQLite database file to snapshot.
	DBPath string
	// LeaseTTL is how long the snapshot lease is held (default 5m via Compactor).
	LeaseTTL time.Duration
}

// BuildCompactor constructs a production Compactor wired to:
//   - the encrypted cluster S3 client (SSE-C owned by cluster.Client),
//   - a snapshot-lease facade backed by a real lease.Manager (single-writer +
//     fencing — never regressed),
//   - a VACUUM-INTO DBSnapshot over cfg.DBPath.
//
// leaseCfg must point at the same bucket/credentials as the cluster client.
//
// passphrase is the cluster passphrase (VULOS_CLUSTER_PASSPHRASE) — the same
// one used to build the cluster.Client's SSE-C key. It is forwarded into
// CompactorConfig.Passphrase so the anti-rollback check authenticates
// latest.json before trusting its Version (see snapshot.go's package doc
// comment). Pass "" only when no passphrase is available; the Compactor then
// falls back to trusting Version unconditionally.
func BuildCompactor(cfg BackupConfig, s3 *cluster.Client, leaseCfg lease.S3Config, passphrase string) (*Compactor, error) {
	if s3 == nil {
		return nil, errors.New("sync: BuildCompactor: nil cluster client")
	}
	if cfg.DBPath == "" {
		return nil, errors.New("sync: BuildCompactor: empty DB path")
	}
	mgr, err := lease.New(leaseCfg)
	if err != nil {
		return nil, fmt.Errorf("sync: BuildCompactor: lease manager: %w", err)
	}
	facade := NewLeaseManagerFacade(mgr)
	return NewCompactor(
		CompactorConfig{NodeID: cfg.NodeID, LeaseTTL: cfg.LeaseTTL, Passphrase: passphrase},
		facade,
		s3,
		SnapshotDB(cfg.DBPath),
	), nil
}

// BuildRestorer constructs a production Restorer wired to the encrypted cluster
// S3 client and a RehydrateDB that atomically swaps the latest snapshot into
// dbPath. Restore is lease-free by design (read of an immutable versioned blob
// + local apply).
//
// passphrase is the cluster passphrase; when non-empty it is used to derive
// the latest.json authenticity MAC key (WithLatestMACKey) so Restore refuses
// (ErrSnapshotTampered) rather than applies a snapshot doc that anyone with
// bucket write access — but not the passphrase — could have forged. Pass ""
// only when no passphrase is available.
func BuildRestorer(s3 *cluster.Client, dbPath string, passphrase string) (*Restorer, error) {
	if s3 == nil {
		return nil, errors.New("sync: BuildRestorer: nil cluster client")
	}
	if dbPath == "" {
		return nil, errors.New("sync: BuildRestorer: empty DB path")
	}
	var opts []RestorerOption
	if passphrase != "" {
		opts = append(opts, WithLatestMACKey(deriveLatestMACKey(passphrase)))
	}
	return NewRestorer(s3, RehydrateDB(dbPath), opts...), nil
}
