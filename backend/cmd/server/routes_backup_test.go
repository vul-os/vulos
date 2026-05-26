package main

// HTTP endpoint tests for the admin backup/restore routes.
//
// They exercise the wiring in routes_backup.go through an in-memory S3 mock
// (no live MinIO), asserting:
//   - backup endpoint snapshots + uploads
//   - restore endpoint downloads + rehydrates
//   - restore is admin-gated AND requires the destructive confirm guard
//   - a full backup → restore roundtrip through the HTTP layer
//   - non-admin / unauthenticated callers are rejected

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"vulos/backend/services/auth"
	syncsvc "vulos/backend/services/sync"
)

// ── in-memory S3 mock satisfying syncsvc.SnapshotS3 ──────────────────────────

type memS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemS3() *memS3 { return &memS3{objects: make(map[string][]byte)} }

func (m *memS3) PutEncrypted(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.objects[key] = cp
	return nil
}
func (m *memS3) GetEncrypted(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}
func (m *memS3) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}
func (m *memS3) DeleteObject(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

// ── in-memory LeaseFacade satisfying syncsvc.LeaseFacade ─────────────────────

type memLease struct {
	mu    sync.Mutex
	held  bool
	fence int64
}

func (l *memLease) AcquireSnapshot(_ context.Context, _ string, _ time.Duration) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fence++
	l.held = true
	return l.fence, nil
}
func (l *memLease) ReleaseSnapshot(_ context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = false
	return nil
}

// ── test DB helpers ──────────────────────────────────────────────────────────

func dsn(path string) string {
	return path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
}

func seedDB(t *testing.T, path string, rows []string) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO t (v) VALUES (?)`, r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
}

func dbRows(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	rows, err := db.Query(`SELECT v FROM t ORDER BY id`)
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

// adminStore returns an auth.Store with one admin user and that user's ID.
func adminStore(t *testing.T) (*auth.Store, string) {
	t.Helper()
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	u, err := store.Register("admin", "password123", "Admin")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	p, ok := store.GetProfile(u.ID)
	if !ok || p.Role != auth.RoleAdmin {
		t.Fatalf("first registered user must be admin, got %+v ok=%v", p, ok)
	}
	return store, u.ID
}

// testDeps builds backupDeps backed by the in-memory S3 mock + fake lease,
// snapshotting/rehydrating srcPath/dstPath via the real VACUUM-INTO callbacks.
func testDeps(store *auth.Store, s3 syncsvc.SnapshotS3, srcPath, dstPath string) backupDeps {
	return backupDeps{
		authStore: store,
		dbPath:    dstPath,
		newCompactor: func() (*syncsvc.Compactor, error) {
			return syncsvc.NewCompactor(
				syncsvc.CompactorConfig{NodeID: "test-node"},
				&memLease{}, s3, syncsvc.SnapshotDB(srcPath),
			), nil
		},
		newRestorer: func() (*syncsvc.Restorer, error) {
			return syncsvc.NewRestorer(s3, syncsvc.RehydrateDB(dstPath)), nil
		},
	}
}

func doReq(t *testing.T, h http.Handler, method, path, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ── Tests ────────────────────────────────────────────────────────────────────

// TestBackupEndpointSnapshotsAndUploads: POST /api/admin/backup writes a
// snapshot blob + latest.json to the mock S3.
func TestBackupEndpointSnapshotsAndUploads(t *testing.T) {
	store, adminID := adminStore(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	seedDB(t, src, []string{"a", "b"})
	s3 := newMemS3()

	mux := http.NewServeMux()
	registerBackupRoutes(mux, testDeps(store, s3, src, filepath.Join(dir, "dst.db")))

	rec := doReq(t, mux, "POST", "/api/admin/backup", adminID, "")
	if rec.Code != 200 {
		t.Fatalf("backup: got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := s3.GetEncrypted(context.Background(), "cluster/snapshot/latest.json"); err != nil {
		t.Errorf("latest.json not uploaded: %v", err)
	}
}

// TestRestoreRequiresAdmin: unauthenticated and non-admin callers are rejected
// before any restore happens.
func TestRestoreRequiresAdmin(t *testing.T) {
	store, _ := adminStore(t)
	// Register a second, non-admin user.
	u2, err := store.Register("bob", "password123", "Bob")
	if err != nil {
		t.Fatalf("Register bob: %v", err)
	}

	dir := t.TempDir()
	s3 := newMemS3()
	mux := http.NewServeMux()
	registerBackupRoutes(mux, testDeps(store, s3, filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")))

	// Unauthenticated → 401.
	if rec := doReq(t, mux, "POST", "/api/admin/restore", "", `{"confirm":"RESTORE"}`); rec.Code != 401 {
		t.Errorf("unauthenticated restore: got %d want 401", rec.Code)
	}
	// Non-admin → 403.
	if rec := doReq(t, mux, "POST", "/api/admin/restore", u2.ID, `{"confirm":"RESTORE"}`); rec.Code != 403 {
		t.Errorf("non-admin restore: got %d want 403", rec.Code)
	}
}

// TestRestoreRequiresConfirmGuard: an admin without the confirm token is
// rejected (destructive-action guard), and nothing is restored.
func TestRestoreRequiresConfirmGuard(t *testing.T) {
	store, adminID := adminStore(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "dst.db")
	seedDB(t, src, []string{"snap"})
	seedDB(t, dst, []string{"original"})
	s3 := newMemS3()

	mux := http.NewServeMux()
	deps := testDeps(store, s3, src, dst)
	registerBackupRoutes(mux, deps)

	// First create a snapshot so a restore *could* happen.
	if rec := doReq(t, mux, "POST", "/api/admin/backup", adminID, ""); rec.Code != 200 {
		t.Fatalf("seed backup: %d %s", rec.Code, rec.Body.String())
	}

	// Admin but missing confirm → 400, DB untouched.
	rec := doReq(t, mux, "POST", "/api/admin/restore", adminID, `{}`)
	if rec.Code != 400 {
		t.Fatalf("restore without confirm: got %d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if got := dbRows(t, dst); len(got) != 1 || got[0] != "original" {
		t.Errorf("dst DB must be untouched without confirm, got %v", got)
	}

	// Wrong confirm token → 400.
	if rec := doReq(t, mux, "POST", "/api/admin/restore", adminID, `{"confirm":"yes"}`); rec.Code != 400 {
		t.Errorf("restore with wrong confirm: got %d want 400", rec.Code)
	}
}

// TestBackupRestoreRoundtripHTTP: end-to-end through the HTTP entrypoints —
// backup the src DB, then restore (with confirm) into the dst DB and verify the
// dst content matches the src snapshot.
func TestBackupRestoreRoundtripHTTP(t *testing.T) {
	store, adminID := adminStore(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "dst.db")
	seedDB(t, src, []string{"one", "two", "three"})
	seedDB(t, dst, []string{"STALE"})
	s3 := newMemS3()

	mux := http.NewServeMux()
	registerBackupRoutes(mux, testDeps(store, s3, src, dst))

	// Backup.
	if rec := doReq(t, mux, "POST", "/api/admin/backup", adminID, ""); rec.Code != 200 {
		t.Fatalf("backup: %d %s", rec.Code, rec.Body.String())
	}

	// Status should report a snapshot.
	statusRec := doReq(t, mux, "GET", "/api/admin/backup/status", adminID, "")
	if statusRec.Code != 200 {
		t.Fatalf("status: %d %s", statusRec.Code, statusRec.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("status decode: %v", err)
	}
	if has, _ := status["has_snapshot"].(bool); !has {
		t.Fatalf("status has_snapshot=false, want true: %v", status)
	}

	// Restore with confirm.
	rec := doReq(t, mux, "POST", "/api/admin/restore", adminID, `{"confirm":"RESTORE"}`)
	if rec.Code != 200 {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body.String())
	}

	// dst DB must now match src content.
	if got := dbRows(t, dst); len(got) != 3 || got[0] != "one" || got[2] != "three" {
		t.Errorf("restored dst rows: got %v want [one two three]", got)
	}
}

// TestRestoreNoSnapshot404: restore with confirm but nothing backed up → 404.
func TestRestoreNoSnapshot404(t *testing.T) {
	store, adminID := adminStore(t)
	dir := t.TempDir()
	s3 := newMemS3()
	mux := http.NewServeMux()
	registerBackupRoutes(mux, testDeps(store, s3, filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")))

	rec := doReq(t, mux, "POST", "/api/admin/restore", adminID, `{"confirm":"RESTORE"}`)
	if rec.Code != 404 {
		t.Errorf("restore with no snapshot: got %d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

// TestBackupUnavailableWhenNoCluster: nil factories (S3 not configured) → 503.
func TestBackupUnavailableWhenNoCluster(t *testing.T) {
	store, adminID := adminStore(t)
	mux := http.NewServeMux()
	registerBackupRoutes(mux, backupDeps{authStore: store, dbPath: "/tmp/x.db"})

	if rec := doReq(t, mux, "POST", "/api/admin/backup", adminID, ""); rec.Code != 503 {
		t.Errorf("backup with no cluster: got %d want 503", rec.Code)
	}
	if rec := doReq(t, mux, "POST", "/api/admin/restore", adminID, `{"confirm":"RESTORE"}`); rec.Code != 503 {
		t.Errorf("restore with no cluster: got %d want 503", rec.Code)
	}
}
