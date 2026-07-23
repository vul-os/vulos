// publicapi_pg_test.go — Postgres dual-backend tests for publicapi.Store.
// Skipped unless VULOS_TEST_POSTGRES is set.
package publicapi_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/publicapi"
)

func openPGTestStore(t *testing.T) *publicapi.Store {
	t.Helper()
	if os.Getenv("VULOS_TEST_POSTGRES") == "" {
		t.Skip("VULOS_TEST_POSTGRES not set")
	}
	db, err := cpdb.Open("publicapi")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := publicapi.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("publicapi.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestPG_GenerateAndAuthToken(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	raw, tok, err := st.GenerateToken(ctx, "pg-acct-1", "test-label")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if tok.AccountID != "pg-acct-1" {
		t.Errorf("account_id: want pg-acct-1, got %q", tok.AccountID)
	}

	acctID, err := st.AuthenticateToken(ctx, raw)
	if err != nil {
		t.Fatalf("AuthenticateToken: %v", err)
	}
	if acctID != "pg-acct-1" {
		t.Errorf("auth: want pg-acct-1, got %q", acctID)
	}

	if err := st.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	_, err = st.AuthenticateToken(ctx, raw)
	if err != publicapi.ErrAPITokenRevoked {
		t.Errorf("want ErrAPITokenRevoked, got %v", err)
	}
}
