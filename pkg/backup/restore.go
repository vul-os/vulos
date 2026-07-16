package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DrillStatus is the top-level outcome of a restore drill.
type DrillStatus string

const (
	StatusPass DrillStatus = "PASS"
	StatusFail DrillStatus = "FAIL"
)

// DBVerification is the per-database verification result.
type DBVerification struct {
	File           string `json:"file"`
	ChecksumOK     bool   `json:"checksum_ok"`     // restored file bytes match manifest SHA256
	SchemaOK       bool   `json:"schema_ok"`       // restored schema hash matches manifest
	RowCountsOK    bool   `json:"row_counts_ok"`   // every table's restored count matches manifest
	OpenedOK       bool   `json:"opened_ok"`       // DB opened + integrity_check passed
	ExpectedRows   int64  `json:"expected_rows"`   // sum of manifest row counts
	ActualRows     int64  `json:"actual_rows"`     // sum of restored row counts
	IntegrityCheck string `json:"integrity_check"` // sqlite "PRAGMA integrity_check" result
	Detail         string `json:"detail,omitempty"`
}

// OK reports whether all checks passed for this database.
func (v DBVerification) OK() bool {
	return v.ChecksumOK && v.SchemaOK && v.RowCountsOK && v.OpenedOK
}

// DrillReport is the structured output of a restore drill. It is what
// tools/restore-drill.sh prints and what the human DR runbook records.
type DrillReport struct {
	Status DrillStatus `json:"status"`

	SnapshotDir string    `json:"snapshot_dir"`
	StagingDir  string    `json:"staging_dir"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`

	// RTO (Recovery Time Objective): wall-clock seconds the restore + verify
	// took. This is the measured time to bring state back.
	RTOSeconds float64 `json:"rto_seconds"`
	// RPO (Recovery Point Objective): seconds of data-loss window — the age of
	// the backup at drill time (now - manifest.CreatedAt). This is how much
	// data a real restore from this artifact would lose.
	RPOSeconds float64 `json:"rpo_seconds"`

	DBs        []DBVerification `json:"dbs"`
	AccountsOK bool             `json:"accounts_ok"`
	Accounts   int              `json:"accounts_verified"`

	Errors []string `json:"errors,omitempty"`
}

// JSON renders the report as indented JSON.
func (r *DrillReport) JSON() string {
	data, _ := json.MarshalIndent(r, "", "  ")
	return string(data)
}

// RestoreDrillConfig configures a single drill run.
type RestoreDrillConfig struct {
	// SnapshotDir is the snapshot to restore. If empty, the latest under
	// BackupRoot is used.
	SnapshotDir string
	// BackupRoot is consulted only when SnapshotDir is empty.
	BackupRoot string
	// StagingDir is the ISOLATED directory the snapshot is restored into. It is
	// created fresh; never point this at a live data dir. If empty, an
	// os.MkdirTemp dir is used.
	StagingDir string
	// Logf receives progress lines; defaults to no-op.
	Logf func(format string, args ...any)
}

// RestoreDrill restores SnapshotDir into an isolated staging directory, verifies
// integrity (checksums, schema, row counts, SQLite integrity_check), measures
// RTO/RPO, and returns a structured report. It NEVER touches production data:
// all writes go to StagingDir. It is safe to run repeatedly.
//
// When DATABASE_URL / VULOS_DATABASE_URL is set (Postgres backend),
// RestoreDrill returns ErrBackupUnsupportedOnPostgres immediately. The
// file-based restore drill is not applicable on Postgres; pg_restore-based
// drill integration is a deferred item.
func RestoreDrill(ctx context.Context, cfg RestoreDrillConfig) (*DrillReport, error) {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if postgresBackendActive() {
		cfg.Logf("[restore-drill] WARNING: Postgres backend detected (DATABASE_URL / VULOS_DATABASE_URL is set) — " +
			"file-based restore drill is not applicable. pg_restore-based drill is a deferred item.")
		return nil, ErrBackupUnsupportedOnPostgres
	}
	started := time.Now()

	snapshotDir := cfg.SnapshotDir
	if snapshotDir == "" {
		if cfg.BackupRoot == "" {
			return nil, fmt.Errorf("restore: both SnapshotDir and BackupRoot are empty")
		}
		latest, err := LatestSnapshot(cfg.BackupRoot)
		if err != nil {
			return nil, fmt.Errorf("restore: find latest snapshot: %w", err)
		}
		if latest == "" {
			return nil, fmt.Errorf("restore: no snapshots under %s", cfg.BackupRoot)
		}
		snapshotDir = latest
	}

	man, err := ReadManifest(snapshotDir)
	if err != nil {
		return nil, err
	}

	stagingDir := cfg.StagingDir
	if stagingDir == "" {
		stagingDir, err = os.MkdirTemp("", "vulos-restore-drill-*")
		if err != nil {
			return nil, fmt.Errorf("restore: mkdir staging: %w", err)
		}
	} else {
		if err := os.MkdirAll(stagingDir, 0o700); err != nil {
			return nil, fmt.Errorf("restore: mkdir staging: %w", err)
		}
	}
	cfg.Logf("[restore-drill] snapshot=%s staging=%s", snapshotDir, stagingDir)

	report := &DrillReport{
		Status:      StatusPass,
		SnapshotDir: snapshotDir,
		StagingDir:  stagingDir,
		StartedAt:   started,
	}

	for _, dbe := range man.DBs {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		v := restoreAndVerifyDB(ctx, snapshotDir, stagingDir, dbe)
		report.DBs = append(report.DBs, v)
		if !v.OK() {
			report.Status = StatusFail
			report.Errors = append(report.Errors,
				fmt.Sprintf("%s: checksum=%v schema=%v rows=%v opened=%v %s",
					v.File, v.ChecksumOK, v.SchemaOK, v.RowCountsOK, v.OpenedOK, v.Detail))
		}
		cfg.Logf("[restore-drill] verified %s ok=%v", v.File, v.OK())
	}

	// Account-manifest verification: confirm the restored auth/account store
	// reproduces the same per-account fingerprints recorded at backup time.
	report.Accounts = len(man.Accounts)
	report.AccountsOK = verifyAccounts(ctx, stagingDir, man.Accounts)
	if !report.AccountsOK {
		report.Status = StatusFail
		report.Errors = append(report.Errors, "account fingerprints did not match manifest")
	}

	report.FinishedAt = time.Now()
	report.RTOSeconds = report.FinishedAt.Sub(started).Seconds()
	report.RPOSeconds = report.FinishedAt.Sub(man.CreatedAt).Seconds()
	if report.RPOSeconds < 0 {
		report.RPOSeconds = 0
	}

	cfg.Logf("[restore-drill] status=%s RTO=%.3fs RPO=%.0fs dbs=%d accounts=%d",
		report.Status, report.RTOSeconds, report.RPOSeconds, len(report.DBs), report.Accounts)
	return report, nil
}

// restoreAndVerifyDB copies one snapshot DB into staging and verifies it.
func restoreAndVerifyDB(ctx context.Context, snapshotDir, stagingDir string, dbe DBEntry) DBVerification {
	v := DBVerification{File: dbe.File}
	src := filepath.Join(snapshotDir, dbe.File)
	dst := filepath.Join(stagingDir, dbe.File)

	if err := copyFile(src, dst); err != nil {
		v.Detail = "copy: " + err.Error()
		return v
	}

	// Checksum: restored bytes must equal what the manifest recorded.
	sum, _, err := fileSHA256(dst)
	if err != nil {
		v.Detail = "checksum: " + err.Error()
		return v
	}
	v.ChecksumOK = sum == dbe.SHA256

	// Open the restored copy and run integrity_check + re-inspect schema. The
	// VACUUM INTO output is a standalone (non-WAL) DB and staging is an
	// isolated throwaway dir, so we open it read-write in rollback-journal mode
	// — modernc's integrity_check needs a writable journal even for reads.
	dsn := "file:" + dst
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		v.Detail = "open: " + err.Error()
		return v
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var integ string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integ); err != nil {
		v.Detail = "integrity_check: " + err.Error()
		return v
	}
	v.IntegrityCheck = integ
	v.OpenedOK = integ == "ok"

	tables, schemaHash, err := inspectSchema(ctx, db)
	if err != nil {
		v.Detail = "inspect: " + err.Error()
		return v
	}
	v.SchemaOK = schemaHash == dbe.SchemaHash

	// Row counts: build a name->count map from the manifest and compare.
	want := make(map[string]int64, len(dbe.Tables))
	for _, t := range dbe.Tables {
		want[t.Name] = t.RowCount
		v.ExpectedRows += t.RowCount
	}
	rowsOK := len(tables) == len(dbe.Tables)
	for _, t := range tables {
		v.ActualRows += t.RowCount
		if w, ok := want[t.Name]; !ok || w != t.RowCount {
			rowsOK = false
		}
	}
	v.RowCountsOK = rowsOK
	return v
}

// verifyAccounts re-derives per-account fingerprints from whichever restored DB
// carries an accounts table and confirms they match the manifest. Returns true
// when there are no accounts to verify (vacuously OK) or all match.
func verifyAccounts(ctx context.Context, stagingDir string, want []AccountManifest) bool {
	if len(want) == 0 {
		return true
	}
	wantFP := make(map[string]string, len(want))
	for _, a := range want {
		wantFP[a.AccountID] = a.Fingerprint
	}

	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		return false
	}
	matched := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".db" {
			continue
		}
		dsn := "file:" + filepath.Join(stagingDir, e.Name())
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			continue
		}
		accts, _ := extractAccounts(ctx, db)
		db.Close()
		for _, a := range accts {
			if fp, ok := wantFP[a.AccountID]; ok && fp == a.Fingerprint {
				matched++
			}
		}
	}
	return matched == len(want)
}

// copyFile copies src to dst (creating parent dirs), with restrictive perms.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
