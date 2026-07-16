// blocklist_pg_test.go — Postgres integration tests for the ddos blocklist store.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/ddos/...
package ddos

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	return dsn
}

func openPGBlocklist(t *testing.T) *BlocklistStore {
	t.Helper()
	dsn := pgDSN(t)
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("ddos_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	s, err := OpenBlocklistStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenBlocklistStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS ddos_pgtest CASCADE`)
		_ = s.Close()
	})
	return s
}

func TestPG_Blocklist_AddAndIsBlocked(t *testing.T) {
	s := openPGBlocklist(t)
	ctx := context.Background()

	if err := s.Add(ctx, "1.2.3.4", "pg-test", "admin", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !s.IsBlocked("1.2.3.4") {
		t.Fatal("expected IsBlocked=true for added IP")
	}
}

func TestPG_Blocklist_Remove(t *testing.T) {
	s := openPGBlocklist(t)
	ctx := context.Background()

	_ = s.Add(ctx, "9.9.9.9", "pg-test", "admin", nil)
	if err := s.Remove(ctx, "9.9.9.9"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if s.IsBlocked("9.9.9.9") {
		t.Fatal("should not be blocked after remove")
	}
}

func TestPG_Blocklist_TTLExpiry(t *testing.T) {
	s := openPGBlocklist(t)
	ctx := context.Background()

	ttl := -time.Second
	if err := s.Add(ctx, "7.7.7.7", "pg-expired", "system", &ttl); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if s.IsBlocked("7.7.7.7") {
		t.Fatal("expired TTL entry should not block")
	}
}

func TestPG_SetAndGetSetting(t *testing.T) {
	s := openPGBlocklist(t)
	ctx := context.Background()

	if err := s.SetSetting(ctx, "pg-mode", "strict"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	got, err := s.GetSetting(ctx, "pg-mode")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if got != "strict" {
		t.Errorf("got %q, want strict", got)
	}
}
