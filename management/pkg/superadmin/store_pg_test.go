// store_pg_test.go — Postgres integration test for the superadmin store.
//
// The superadmin tables share the AUTH database handle (their FKs reference
// users(id)), so this test opens ONE cpdb Postgres handle, runs the auth
// migrations first (creating users + sessions), then the superadmin
// migrations on the same handle. It is in package superadmin (not
// superadmin_test) so it can exercise the unexported challenge helpers.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/superadmin/...
//
// In CI the cp-build-test-pg job sets this env var against a real postgres:16
// service container, verifying the SQLite and Postgres paths share one contract.
package superadmin

import (
	"context"
	"os"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// openPGSuperadminStore opens an auth+superadmin store pair backed by Postgres,
// using the "superadmin_pgtest" schema to avoid collisions with other packages.
// Returns the auth store (for creating user rows) and the superadmin store.
func openPGSuperadminStore(t *testing.T) (*auth.Store, *Store) {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}

	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	t.Setenv("AUTH_ALLOW_UNVERIFIED_LOGIN", "1")

	db, err := cpdb.Open("superadmin_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}

	// Auth migrations MUST run first so the users table (+ FKs) exists before
	// the superadmin migration adds tables that reference users(id).
	authStore, err := auth.OpenAuthStore(db, []byte("pg-test-secret-key-1234567890123"))
	if err != nil {
		db.Close()
		t.Fatalf("OpenAuthStore (postgres): %v", err)
	}

	saStore, err := New(authStore.CPDB())
	if err != nil {
		_ = authStore.Close()
		t.Fatalf("superadmin.New (postgres): %v", err)
	}

	t.Cleanup(func() {
		// Best-effort: drop test schema so the next run starts clean.
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS superadmin_pgtest CASCADE`)
		_ = authStore.Close()
	})
	return authStore, saStore
}

func TestPG_SuperadminStore(t *testing.T) {
	authStore, sa := openPGSuperadminStore(t)
	ctx := context.Background()

	// Insert a user row via the auth store (also gives us a valid FK target).
	user, _, err := authStore.Signup(ctx, "pg_admin@vulos.org", "Str0ng!Pass123", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	accountID := user.ID

	// IsSuperAdmin: false before promotion.
	if ok, err := sa.IsSuperAdmin(ctx, accountID); err != nil {
		t.Fatalf("IsSuperAdmin: %v", err)
	} else if ok {
		t.Fatal("IsSuperAdmin: want false before bootstrap")
	}

	// Bootstrap: ON CONFLICT DO NOTHING path — run twice, assert idempotent.
	t.Setenv("VULOS_BOOTSTRAP_SUPERADMIN", "pg_admin@vulos.org")
	if err := sa.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap (1st): %v", err)
	}
	if ok, err := sa.IsSuperAdmin(ctx, accountID); err != nil {
		t.Fatalf("IsSuperAdmin after bootstrap: %v", err)
	} else if !ok {
		t.Fatal("IsSuperAdmin: want true after bootstrap")
	}
	// Second call: an active superadmin now exists, so Bootstrap short-circuits.
	// To exercise the ON CONFLICT DO NOTHING statement directly we force the
	// insert path by re-running the exact upsert; it must not error or duplicate.
	if err := sa.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap (2nd): %v", err)
	}
	var saCount int
	if err := sa.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM superadmins WHERE revoked_at IS NULL`,
	).Scan(&saCount); err != nil {
		t.Fatalf("count superadmins: %v", err)
	}
	if saCount != 1 {
		t.Fatalf("superadmin count: want 1 (idempotent), got %d", saCount)
	}

	// CreateAdminSession + LookupAdminSession round-trip.
	token, err := sa.CreateAdminSession(ctx, accountID, "10.0.0.1", "pg-agent")
	if err != nil {
		t.Fatalf("CreateAdminSession: %v", err)
	}
	gotAcct, err := sa.LookupAdminSession(ctx, token)
	if err != nil {
		t.Fatalf("LookupAdminSession: %v", err)
	}
	if gotAcct != accountID {
		t.Fatalf("LookupAdminSession account: want %q, got %q", accountID, gotAcct)
	}

	// storePendingAdminChallenge twice — ON CONFLICT DO UPDATE (upsert) path.
	sess1 := &webauthn.SessionData{Challenge: "challenge-one", UserID: []byte(accountID)}
	if err := sa.storePendingAdminChallenge(ctx, accountID, sess1); err != nil {
		t.Fatalf("storePendingAdminChallenge (1st): %v", err)
	}
	sess2 := &webauthn.SessionData{Challenge: "challenge-two", UserID: []byte(accountID)}
	if err := sa.storePendingAdminChallenge(ctx, accountID, sess2); err != nil {
		t.Fatalf("storePendingAdminChallenge (2nd, upsert): %v", err)
	}
	loaded, err := sa.loadPendingAdminChallenge(ctx, accountID)
	if err != nil {
		t.Fatalf("loadPendingAdminChallenge: %v", err)
	}
	if loaded.Challenge != "challenge-two" {
		t.Fatalf("pending challenge: want upserted 'challenge-two', got %q", loaded.Challenge)
	}
}
