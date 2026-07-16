// auth_pg_test.go — Postgres integration tests for the auth store.
//
// These tests are skipped unless VULOS_TEST_POSTGRES is set to a valid
// Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/auth/...
//
// In CI the cp-build-test-pg job sets this env var and runs against a real
// postgres:16 service container, verifying that both the SQLite and Postgres
// paths pass the same behavioural contract.
package auth

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// pgAuthDSN returns the Postgres DSN or skips the test.
func pgAuthDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	return dsn
}

// openPGAuthStore opens an auth store backed by Postgres using the
// "auth_pgtest" schema to avoid collisions with other test packages.
func openPGAuthStore(t *testing.T) *Store {
	t.Helper()
	dsn := pgAuthDSN(t)

	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("auth_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}

	st, err := OpenAuthStore(db, []byte("pg-test-secret-key-1234567890123"))
	if err != nil {
		db.Close()
		t.Fatalf("OpenAuthStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		// Best-effort: drop test schema so the next run starts clean.
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS auth_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

// ─── PG: full behavioural contract ───────────────────────────────────────────

func TestPG_SignupAndLookupSession(t *testing.T) {
	st := openPGAuthStore(t)
	ctx := context.Background()

	user, token, err := st.Signup(ctx, "pg_alice@vulos.org", "Str0ng!Pass123", "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	if user.Email != "pg_alice@vulos.org" {
		t.Errorf("email: want pg_alice@vulos.org, got %q", user.Email)
	}
	if token == "" {
		t.Fatal("expected non-empty session token")
	}

	got, err := st.LookupSession(ctx, token)
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("user ID mismatch: want %q, got %q", user.ID, got.ID)
	}
}

func TestPG_RevokeSession(t *testing.T) {
	st := openPGAuthStore(t)
	ctx := context.Background()

	_, token, err := st.Signup(ctx, "pg_bob@vulos.org", "Str0ng!Pass123", "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	if err := st.RevokeSession(ctx, token); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	_, err = st.LookupSession(ctx, token)
	if err == nil {
		t.Error("expected error after session revoke, got nil")
	}
}

func TestPG_DuplicateEmailRejected(t *testing.T) {
	st := openPGAuthStore(t)
	ctx := context.Background()

	if _, _, err := st.Signup(ctx, "pg_carol@vulos.org", "Str0ng!Pass123", "127.0.0.1", "ua"); err != nil {
		t.Fatalf("first Signup: %v", err)
	}
	_, _, err := st.Signup(ctx, "pg_carol@vulos.org", "Str0ng!Pass123", "127.0.0.1", "ua")
	if err == nil {
		t.Error("expected duplicate email error, got nil")
	}
}

func TestPG_Rebind(t *testing.T) {
	st := openPGAuthStore(t)
	ctx := context.Background()

	// Exercises Signup (INSERT users + INSERT sessions) and LookupSession (SELECT)
	// with $N placeholders end-to-end against a real Postgres connection.
	_, token, err := st.Signup(ctx, "pg_rebind@vulos.org", "Str0ng!Pass123", "10.0.0.1", "rebind-agent")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	u, err := st.LookupSession(ctx, token)
	if err != nil {
		t.Fatalf("LookupSession: %v", err)
	}
	if u.Email != "pg_rebind@vulos.org" {
		t.Errorf("email: want pg_rebind@vulos.org, got %q", u.Email)
	}
}
