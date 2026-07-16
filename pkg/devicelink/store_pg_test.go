// store_pg_test.go — NEON-PGTEST: Postgres-backed coverage for the cpdb
// SQLStore device-link state machine. Skips unless VULOS_TEST_POSTGRES points
// at a reachable Postgres cluster (same gate as every other *_pg_test.go).
package devicelink

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// openPGStore returns a fresh Postgres-schema-backed SQLStore, or skips.
func openPGStore(t *testing.T) *SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("devicelink_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	s, err := OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLStore: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS devicelink_pgtest CASCADE`)
		_ = s.Close()
	})
	return s
}

// TestPG_DeviceLink_StartApprovePoll exercises the full state machine on
// Postgres (Rebind + dialect migrations must produce identical behaviour).
func TestPG_DeviceLink_StartApprovePoll(t *testing.T) {
	ctx := context.Background()
	s := openPGStore(t)

	start, err := s.StartLink(ctx, "https://cloud.example/app/link", 0, 0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if start.DeviceCode == "" || start.UserCode == "" {
		t.Fatalf("empty codes: %+v", start)
	}

	// Poll before approval → pending.
	if _, err := s.Poll(ctx, start.DeviceCode); !errors.Is(err, ErrPending) {
		t.Fatalf("want ErrPending, got %v", err)
	}

	// Approve then poll → credential bound to the account.
	if err := s.Approve(ctx, start.UserCode, "acct-pg-1"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	cred, err := s.Poll(ctx, start.DeviceCode)
	if err != nil {
		t.Fatalf("poll after approve: %v", err)
	}
	if cred.AccountID != "acct-pg-1" || cred.Token == "" {
		t.Fatalf("bad credential: %+v", cred)
	}

	// Credential resolves back to the account.
	acct, err := s.ResolveCredential(ctx, cred.Token)
	if err != nil || acct != "acct-pg-1" {
		t.Fatalf("resolve credential: acct=%q err=%v", acct, err)
	}

	// Replay: a second poll must NOT mint a second credential.
	if _, err := s.Poll(ctx, start.DeviceCode); !errors.Is(err, ErrConsumed) {
		t.Fatalf("want ErrConsumed on second poll, got %v", err)
	}
}

// TestPG_DeviceLink_UnknownCode → ErrNotFound (no oracle).
func TestPG_DeviceLink_UnknownCode(t *testing.T) {
	ctx := context.Background()
	s := openPGStore(t)
	if _, err := s.Poll(ctx, "no-such-device-code"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := s.Approve(ctx, "NOSUCH", "acct-x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approve unknown → want ErrNotFound, got %v", err)
	}
}
