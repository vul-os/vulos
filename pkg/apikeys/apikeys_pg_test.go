// apikeys_pg_test.go — Postgres integration tests for the apikeys store.
//
// These tests are skipped unless VULOS_TEST_POSTGRES is set to a valid
// Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/apikeys/...
//
// In CI the cp-build-test-pg job sets this env var and runs against a real
// postgres:16 service container, verifying that both the SQLite and Postgres
// paths pass the same behavioural contract.
package apikeys_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/apikeys"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// pgDSN returns the Postgres DSN or skips the test.
func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	return dsn
}

// openPGTestStore opens an apikeys store backed by Postgres, using the
// "apikeys_test" schema to avoid collisions with production data.
func openPGTestStore(t *testing.T) *apikeys.Store {
	t.Helper()
	dsn := pgDSN(t)

	// Unique schema per test run prevents parallel test interference.
	// We use a fixed name "apikeys_pgtest" so the schema is cleaned up between
	// runs automatically (cpdb.Open creates it if absent; old rows are reused
	// and that's fine because each test issues fresh keys with unique hashes).
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("apikeys_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}

	st, err := apikeys.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("apikeys.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		// Best-effort: drop test schema so the next run starts clean.
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS apikeys_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

// ─── PG: full behavioural contract ───────────────────────────────────────────

func TestPG_IssueIntrospectRoundtrip(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	rawKey, meta, err := st.IssueKey(ctx, "pg_acc1", "pg key",
		[]string{"documents.read"}, []string{"office"})
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	if !strings.HasPrefix(rawKey, apikeys.KeyPrefix) {
		t.Errorf("raw key missing vk_ prefix: %q", rawKey)
	}
	_ = meta

	res, err := st.Introspect(ctx, rawKey)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if !res.Valid {
		t.Fatal("expected valid=true")
	}
	if res.Account != "pg_acc1" {
		t.Errorf("account: want pg_acc1, got %q", res.Account)
	}
	if len(res.Scopes) != 1 || res.Scopes[0] != "documents.read" {
		t.Errorf("scopes: %v", res.Scopes)
	}
}

func TestPG_RevokeKey(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	rawKey, meta, err := st.IssueKey(ctx, "pg_acc2", "revoke me", nil, nil)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	if err := st.RevokeKey(ctx, "pg_acc2", meta.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	res, err := st.Introspect(ctx, rawKey)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if res.Valid {
		t.Error("revoked key must be invalid")
	}
}

func TestPG_CrossAccountRevokeBlocked(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	_, meta, err := st.IssueKey(ctx, "pg_alice", "alice key", nil, nil)
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}
	err = st.RevokeKey(ctx, "pg_bob", meta.ID)
	if err == nil {
		t.Error("bob must not revoke alice's key")
	}
}

func TestPG_ListKeysEmpty(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	keys, err := st.ListKeys(ctx, "pg_nobody")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if keys == nil {
		t.Error("ListKeys must return empty slice, not nil")
	}
}

func TestPG_UnknownKeyInvalid(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	res, err := st.Introspect(ctx, "vk_unknownkeyforpostgrestestxxxxxxxxxxxxxxxx")
	if err != nil {
		t.Fatalf("Introspect unknown: %v", err)
	}
	if res.Valid {
		t.Error("unknown key must be invalid")
	}
}

// TestPG_Rebind verifies that $N placeholders actually work in executed queries
// (this exercises cpdb.Rebind end-to-end against a real Postgres connection).
func TestPG_Rebind(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	// Issue + introspect exercises all four query paths with $N placeholders.
	rawKey, meta, err := st.IssueKey(ctx, "pg_rebind", "rebind test",
		[]string{"relay.send"}, []string{"relay"})
	if err != nil {
		t.Fatalf("IssueKey: %v", err)
	}

	keys, err := st.ListKeys(ctx, "pg_rebind")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("expected at least one key")
	}

	res, err := st.Introspect(ctx, rawKey)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if !res.Valid {
		t.Fatal("key should be valid")
	}

	if err := st.RevokeKey(ctx, "pg_rebind", meta.ID); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
}
