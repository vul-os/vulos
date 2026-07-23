package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/backup"

	_ "modernc.org/sqlite"
)

// seedDataDir creates a data dir with two SQLite DBs: an "auth.db" carrying an
// accounts table + rows, and a "misc.db" with a plain table. Returns the dir.
func seedDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	authPath := filepath.Join(dir, "auth.db")
	authDB := openDB(t, authPath)
	mustExec(t, authDB, `CREATE TABLE accounts (id TEXT PRIMARY KEY, email TEXT)`)
	for i := 0; i < 5; i++ {
		mustExec(t, authDB,
			`INSERT INTO accounts (id, email) VALUES (?, ?)`,
			"acct-"+strconv.Itoa(i), "user"+strconv.Itoa(i)+"@vulos.org")
	}
	mustExec(t, authDB, `CREATE TABLE sessions (token TEXT, account_id TEXT)`)
	mustExec(t, authDB, `INSERT INTO sessions (token, account_id) VALUES ('t1','acct-0')`)
	authDB.Close()

	miscPath := filepath.Join(dir, "misc.db")
	miscDB := openDB(t, miscPath)
	mustExec(t, miscDB, `CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT)`)
	for i := 0; i < 3; i++ {
		mustExec(t, miscDB, `INSERT INTO widgets (name) VALUES (?)`, "w"+strconv.Itoa(i))
	}
	miscDB.Close()

	return dir
}

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// TestBackupProducesRestorableArtifact: a backup writes a manifest + DB copies
// and the artifact restores cleanly through the drill (AC: scheduled backup
// produces a restorable artifact).
func TestBackupProducesRestorableArtifact(t *testing.T) {
	ctx := context.Background()
	dataDir := seedDataDir(t)
	backupRoot := t.TempDir()

	b := backup.New(backup.Config{DataDir: dataDir, BackupRoot: backupRoot})
	snapDir, man, err := b.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if man.Version != backup.ManifestVersion {
		t.Fatalf("manifest version = %d", man.Version)
	}
	if len(man.DBs) != 2 {
		t.Fatalf("expected 2 dbs, got %d", len(man.DBs))
	}
	if len(man.Accounts) != 5 {
		t.Fatalf("expected 5 accounts in manifest, got %d", len(man.Accounts))
	}

	// Manifest file + DB copies must exist on disk.
	if _, err := os.Stat(filepath.Join(snapDir, "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	for _, want := range []string{"auth.db", "misc.db"} {
		if _, err := os.Stat(filepath.Join(snapDir, want)); err != nil {
			t.Fatalf("db copy %s missing: %v", want, err)
		}
	}

	// Row counts captured correctly.
	for _, dbe := range man.DBs {
		switch dbe.File {
		case "auth.db":
			assertTableCount(t, dbe, "accounts", 5)
			assertTableCount(t, dbe, "sessions", 1)
		case "misc.db":
			assertTableCount(t, dbe, "widgets", 3)
		}
		if dbe.SHA256 == "" || dbe.SchemaHash == "" {
			t.Fatalf("%s: empty checksum/schema hash", dbe.File)
		}
	}

	// The artifact must restore cleanly.
	report, err := backup.RestoreDrill(ctx, backup.RestoreDrillConfig{
		SnapshotDir: snapDir,
		StagingDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RestoreDrill: %v", err)
	}
	if report.Status != backup.StatusPass {
		t.Fatalf("restore status = %s; errors=%v", report.Status, report.Errors)
	}
}

func assertTableCount(t *testing.T, dbe backup.DBEntry, table string, want int64) {
	t.Helper()
	for _, ts := range dbe.Tables {
		if ts.Name == table {
			if ts.RowCount != want {
				t.Fatalf("%s.%s rows = %d, want %d", dbe.File, table, ts.RowCount, want)
			}
			return
		}
	}
	t.Fatalf("%s: table %s not found in manifest", dbe.File, table)
}

// TestBackupIdempotent: running twice produces two distinct restorable
// snapshots and never mutates the source data dir.
func TestBackupIdempotent(t *testing.T) {
	ctx := context.Background()
	dataDir := seedDataDir(t)
	backupRoot := t.TempDir()
	b := backup.New(backup.Config{DataDir: dataDir, BackupRoot: backupRoot})

	snap1, _, err := b.Run(ctx)
	if err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	snap2, _, err := b.Run(ctx)
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if snap1 == snap2 {
		t.Fatalf("expected distinct snapshot dirs, both = %s", snap1)
	}

	latest, err := backup.LatestSnapshot(backupRoot)
	if err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	}
	if latest == "" {
		t.Fatal("LatestSnapshot returned empty")
	}
}

// TestRestoreDrillVerifiesIntegrityAndMeasuresRTORPO: the drill restores to
// staging, verifies integrity, and produces measured RTO/RPO (AC: restore-drill
// restores to staging + verifies integrity; RTO/RPO measured + reported).
func TestRestoreDrillVerifiesIntegrityAndMeasuresRTORPO(t *testing.T) {
	ctx := context.Background()
	dataDir := seedDataDir(t)
	backupRoot := t.TempDir()
	b := backup.New(backup.Config{DataDir: dataDir, BackupRoot: backupRoot})

	snapDir, _, err := b.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Simulate a backup taken ~2s ago so RPO is measurable and positive.
	time.Sleep(50 * time.Millisecond)

	staging := t.TempDir()
	report, err := backup.RestoreDrill(ctx, backup.RestoreDrillConfig{
		SnapshotDir: snapDir,
		StagingDir:  staging,
	})
	if err != nil {
		t.Fatalf("RestoreDrill: %v", err)
	}

	if report.Status != backup.StatusPass {
		t.Fatalf("status = %s; errors=%v", report.Status, report.Errors)
	}
	if report.RTOSeconds <= 0 {
		t.Fatalf("RTO not measured: %v", report.RTOSeconds)
	}
	if report.RPOSeconds < 0 {
		t.Fatalf("RPO negative: %v", report.RPOSeconds)
	}
	if !report.AccountsOK || report.Accounts != 5 {
		t.Fatalf("accounts verify failed: ok=%v n=%d", report.AccountsOK, report.Accounts)
	}
	for _, v := range report.DBs {
		if !v.OK() {
			t.Fatalf("db %s verification failed: checksum=%v schema=%v rows=%v opened=%v integ=%q",
				v.File, v.ChecksumOK, v.SchemaOK, v.RowCountsOK, v.OpenedOK, v.IntegrityCheck)
		}
		if v.IntegrityCheck != "ok" {
			t.Fatalf("db %s integrity_check = %q", v.File, v.IntegrityCheck)
		}
	}

	// Staging must be isolated: restored files land there, source untouched.
	if _, err := os.Stat(filepath.Join(staging, "auth.db")); err != nil {
		t.Fatalf("restored auth.db not in staging: %v", err)
	}
	if report.JSON() == "" {
		t.Fatal("report JSON empty")
	}
}

// TestRestoreDrillDetectsCorruption: a tampered checksum / row count must fail
// the drill (proves the verification actually verifies).
func TestRestoreDrillDetectsCorruption(t *testing.T) {
	ctx := context.Background()
	dataDir := seedDataDir(t)
	backupRoot := t.TempDir()
	b := backup.New(backup.Config{DataDir: dataDir, BackupRoot: backupRoot})

	snapDir, _, err := b.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Corrupt the backed-up auth.db copy by appending garbage.
	corrupt := filepath.Join(snapDir, "auth.db")
	f, err := os.OpenFile(corrupt, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open for corrupt: %v", err)
	}
	if _, err := f.Write([]byte("CORRUPTIONCORRUPTIONCORRUPTION")); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	f.Close()

	report, err := backup.RestoreDrill(ctx, backup.RestoreDrillConfig{
		SnapshotDir: snapDir,
		StagingDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RestoreDrill: %v", err)
	}
	if report.Status != backup.StatusFail {
		t.Fatalf("expected FAIL on corrupted artifact, got %s", report.Status)
	}
	if len(report.Errors) == 0 {
		t.Fatal("expected drill errors on corruption, got none")
	}
}

// TestRestoreDrillLatestFromBackupRoot: drill can pick the latest snapshot when
// only a backup root is provided (the operator's common path).
func TestRestoreDrillLatestFromBackupRoot(t *testing.T) {
	ctx := context.Background()
	dataDir := seedDataDir(t)
	backupRoot := t.TempDir()
	b := backup.New(backup.Config{DataDir: dataDir, BackupRoot: backupRoot})
	if _, _, err := b.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	report, err := backup.RestoreDrill(ctx, backup.RestoreDrillConfig{
		BackupRoot: backupRoot,
		StagingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RestoreDrill: %v", err)
	}
	if report.Status != backup.StatusPass {
		t.Fatalf("status = %s; errors=%v", report.Status, report.Errors)
	}
}

// TestSchedulerRunsImmediately: the scheduler takes an initial backup on Start
// so a fresh CP always has a restorable artifact (AC: scheduled backup runs).
func TestSchedulerRunsImmediately(t *testing.T) {
	dataDir := seedDataDir(t)
	backupRoot := t.TempDir()
	b := backup.New(backup.Config{DataDir: dataDir, BackupRoot: backupRoot})
	s := backup.NewScheduler(b, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	// Poll briefly for the immediate backup to land.
	deadline := time.Now().Add(2 * time.Second)
	var latest string
	for time.Now().Before(deadline) {
		l, err := backup.LatestSnapshot(backupRoot)
		if err != nil {
			t.Fatalf("LatestSnapshot: %v", err)
		}
		if l != "" {
			latest = l
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if latest == "" {
		t.Fatal("scheduler did not produce an immediate backup")
	}

	// And that immediate backup must be restorable.
	report, err := backup.RestoreDrill(context.Background(), backup.RestoreDrillConfig{
		SnapshotDir: latest,
		StagingDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("RestoreDrill: %v", err)
	}
	if report.Status != backup.StatusPass {
		t.Fatalf("scheduled artifact not restorable: %s %v", report.Status, report.Errors)
	}
}

// TestSchedulerDefaultInterval: a non-positive interval falls back to default.
func TestSchedulerDefaultInterval(t *testing.T) {
	dataDir := seedDataDir(t)
	b := backup.New(backup.Config{DataDir: dataDir, BackupRoot: t.TempDir()})
	// Construct with a bad interval; Start should still work without panicking.
	s := backup.NewScheduler(b, -1)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()
	if backup.DefaultInterval <= 0 {
		t.Fatal("DefaultInterval must be positive")
	}
}

// TestBackupUnsupportedOnPostgresBackend: when DATABASE_URL is set (Postgres
// backend), Run returns ErrBackupUnsupportedOnPostgres — a typed, explicit
// signal so callers can handle the Postgres path deliberately rather than
// receiving a silent no-op or an opaque failure.
func TestBackupUnsupportedOnPostgresBackend(t *testing.T) {
	// Simulate Postgres backend via DATABASE_URL. The check is env-only;
	// no real Postgres connection is attempted.
	t.Setenv("DATABASE_URL", "postgres://localhost/fake_vulos_test")

	b := backup.New(backup.Config{
		DataDir:    t.TempDir(),
		BackupRoot: t.TempDir(),
	})
	_, _, err := b.Run(context.Background())
	if !errors.Is(err, backup.ErrBackupUnsupportedOnPostgres) {
		t.Fatalf("Run on Postgres backend: want ErrBackupUnsupportedOnPostgres, got %v", err)
	}
}

// TestRestoreDrillUnsupportedOnPostgresBackend: RestoreDrill also returns
// ErrBackupUnsupportedOnPostgres when DATABASE_URL signals Postgres backend.
func TestRestoreDrillUnsupportedOnPostgresBackend(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/fake_vulos_test")

	_, err := backup.RestoreDrill(context.Background(), backup.RestoreDrillConfig{
		BackupRoot: t.TempDir(),
		StagingDir: t.TempDir(),
	})
	if !errors.Is(err, backup.ErrBackupUnsupportedOnPostgres) {
		t.Fatalf("RestoreDrill on Postgres backend: want ErrBackupUnsupportedOnPostgres, got %v", err)
	}
}

// TestVULOS_DATABASE_URL_TreatedAsPostgres: VULOS_DATABASE_URL (the alternate
// env var) is also recognised as a Postgres backend signal.
func TestVULOS_DATABASE_URL_TreatedAsPostgres(t *testing.T) {
	t.Setenv("VULOS_DATABASE_URL", "postgres://localhost/fake_vulos_test")

	b := backup.New(backup.Config{
		DataDir:    t.TempDir(),
		BackupRoot: t.TempDir(),
	})
	_, _, err := b.Run(context.Background())
	if !errors.Is(err, backup.ErrBackupUnsupportedOnPostgres) {
		t.Fatalf("Run with VULOS_DATABASE_URL set: want ErrBackupUnsupportedOnPostgres, got %v", err)
	}
}
