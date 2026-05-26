package sync

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// makeDB creates a small sqlite DB at path with a table and the given rows.
func makeDB(t *testing.T, path string, userVersion int, rows []string) {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO notes (body) VALUES (?)`, r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = " + itoa(userVersion)); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

// readNotes reads all note bodies from the DB at path.
func readNotes(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	rows, err := db.Query(`SELECT body FROM notes ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	return out
}

// TestSnapshotDBProducesValidImage verifies SnapshotDB captures a usable image
// and reports user_version when set.
func TestSnapshotDBProducesValidImage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")
	makeDB(t, dbPath, 7, []string{"alpha", "beta"})

	data, version, err := SnapshotDB(dbPath)(ctx)
	if err != nil {
		t.Fatalf("SnapshotDB: %v", err)
	}
	if version != 7 {
		t.Errorf("version: got %d want 7 (PRAGMA user_version)", version)
	}
	if len(data) == 0 {
		t.Fatal("snapshot image is empty")
	}

	// The image must itself be a valid sqlite DB with the same rows.
	imgPath := filepath.Join(dir, "image.db")
	if err := os.WriteFile(imgPath, data, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if got := readNotes(t, imgPath); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("image rows: got %v want [alpha beta]", got)
	}
}

// TestSnapshotDBContentVersionFallback verifies that when user_version is 0 the
// version is derived from content (stable for identical bytes).
func TestSnapshotDBContentVersionFallback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "app.db")
	makeDB(t, dbPath, 0, []string{"x"})

	_, v1, err := SnapshotDB(dbPath)(ctx)
	if err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}
	if v1 <= 0 {
		t.Fatalf("content version must be positive, got %d", v1)
	}
}

// TestSnapshotDBMissingFileIsEmpty verifies a non-existent DB yields empty data
// (which makes the Compactor skip) rather than an error.
func TestSnapshotDBMissingFileIsEmpty(t *testing.T) {
	ctx := context.Background()
	data, version, err := SnapshotDB(filepath.Join(t.TempDir(), "absent.db"))(ctx)
	if err != nil {
		t.Fatalf("expected nil error for missing DB, got %v", err)
	}
	if len(data) != 0 || version != 0 {
		t.Errorf("missing DB should yield empty snapshot, got len=%d version=%d", len(data), version)
	}
}

// TestRehydrateDBSwapsImageIn verifies RehydrateDB replaces the live DB file.
func TestRehydrateDBSwapsImageIn(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Source DB → snapshot bytes.
	srcPath := filepath.Join(dir, "src.db")
	makeDB(t, srcPath, 3, []string{"one", "two", "three"})
	data, _, err := SnapshotDB(srcPath)(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Destination DB with different content.
	dstPath := filepath.Join(dir, "dst.db")
	makeDB(t, dstPath, 1, []string{"OLD"})
	// Leave stale WAL/SHM sidecars to confirm they get removed.
	os.WriteFile(dstPath+"-wal", []byte("garbage"), 0o644)
	os.WriteFile(dstPath+"-shm", []byte("garbage"), 0o644)

	if err := RehydrateDB(dstPath)(ctx, data, 3); err != nil {
		t.Fatalf("RehydrateDB: %v", err)
	}

	if got := readNotes(t, dstPath); len(got) != 3 || got[0] != "one" {
		t.Errorf("after restore rows: got %v want [one two three]", got)
	}
	if _, err := os.Stat(dstPath + "-wal"); !os.IsNotExist(err) {
		t.Error("stale -wal sidecar should have been removed")
	}
}

// TestRehydrateDBRejectsCorruptBlob verifies a non-sqlite blob never clobbers a
// good DB (validation happens before the swap).
func TestRehydrateDBRejectsCorruptBlob(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dstPath := filepath.Join(dir, "good.db")
	makeDB(t, dstPath, 1, []string{"keepme"})

	err := RehydrateDB(dstPath)(ctx, []byte("this is not a sqlite file"), 99)
	if err == nil {
		t.Fatal("expected validation error for corrupt blob, got nil")
	}

	// The good DB must be untouched.
	if got := readNotes(t, dstPath); len(got) != 1 || got[0] != "keepme" {
		t.Errorf("good DB was clobbered by corrupt restore: got %v", got)
	}
}

// TestBackupRestoreRoundtripThroughLibrary is the end-to-end roundtrip at the
// library layer: SnapshotDB → Compactor (upload to mock S3) → Restorer
// (download) → RehydrateDB (swap in), then verify the rehydrated DB matches.
func TestBackupRestoreRoundtripThroughLibrary(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s3 := newMockSnapshotS3()
	lf := newFakeLeaseFacade()

	srcPath := filepath.Join(dir, "live.db")
	makeDB(t, srcPath, 12, []string{"red", "green", "blue"})

	// Backup via Compactor using the real VACUUM-INTO snapshot.
	compactor := NewCompactor(
		CompactorConfig{NodeID: "node-rt", LeaseTTL: 30 * time.Second},
		lf, s3, SnapshotDB(srcPath),
	)
	if err := compactor.Run(ctx); err != nil {
		t.Fatalf("Compactor.Run: %v", err)
	}
	if !s3.has(latestKey) {
		t.Fatal("latest.json not written by backup")
	}

	// Restore into a fresh location via the real RehydrateDB.
	dstPath := filepath.Join(dir, "restored.db")
	restorer := NewRestorer(s3, RehydrateDB(dstPath))
	res, err := restorer.Restore(ctx)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Version != 12 {
		t.Errorf("restored version: got %d want 12", res.Version)
	}

	if got := readNotes(t, dstPath); len(got) != 3 || got[0] != "red" || got[2] != "blue" {
		t.Errorf("restored rows: got %v want [red green blue]", got)
	}
}

// TestBuildRestorerRequiresClient verifies the production builder guards inputs.
func TestBuildRestorerRequiresClient(t *testing.T) {
	if _, err := BuildRestorer(nil, "/tmp/x.db"); err == nil {
		t.Error("BuildRestorer(nil, ...) should error")
	}
}
