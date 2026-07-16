// llmkeys_pg_test.go — Postgres integration tests for the llmkeys store.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/llmkeys/...
package llmkeys_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/llmkeys"
)

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	return dsn
}

func devKEK() []byte { return make([]byte, 32) }

func openPGTestStore(t *testing.T) llmkeys.Store {
	t.Helper()
	dsn := pgDSN(t)
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("llmkeys_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := llmkeys.OpenSQLStore(db, devKEK())
	if err != nil {
		db.Close()
		t.Fatalf("llmkeys.OpenSQLStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS llmkeys_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_PutAndList(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.Put(ctx, "pg-acc1", "openai", "sk-test-key"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	infos, err := st.List(ctx, "pg-acc1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 || infos[0].Provider != "openai" {
		t.Errorf("list = %+v, want [{openai}]", infos)
	}
}

func TestPG_PutIdempotent(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.Put(ctx, "pg-acc2", "anthropic", "key-v1"); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if err := st.Put(ctx, "pg-acc2", "anthropic", "key-v2"); err != nil {
		t.Fatalf("second Put: %v", err)
	}
	// The key was updated — Get should return key-v2.
	got, err := st.Get(ctx, "pg-acc2", "anthropic")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "key-v2" {
		t.Errorf("got %q, want key-v2", got)
	}
}

func TestPG_Delete(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.Put(ctx, "pg-acc3", "mistral", "key-m"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := st.Delete(ctx, "pg-acc3", "mistral"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Get(ctx, "pg-acc3", "mistral"); err != llmkeys.ErrNotFound {
		t.Errorf("after delete Get err = %v, want ErrNotFound", err)
	}
}

func TestPG_CrossAccountIsolation(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.Put(ctx, "pg-acc4", "openai", "secret-key"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	other, err := st.List(ctx, "pg-acc-other")
	if err != nil {
		t.Fatalf("List other: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("cross-account list len = %d, want 0", len(other))
	}
}
