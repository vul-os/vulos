// integrations_pg_test.go — Postgres integration tests for the integrations store.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/integrations/... -run TestPG_
package integrations_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/integrations"
)

func openPGIntegrationsStore(t *testing.T) *integrations.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("integrations_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := integrations.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("integrations.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS integrations_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_Integrations_UpsertGet(t *testing.T) {
	st := openPGIntegrationsStore(t)
	ctx := context.Background()

	c := integrations.Connection{
		AccountID:       "pg-acct1",
		Provider:        integrations.ProviderGoogle,
		RefreshTokenEnc: "enc-rt-1",
		AccessTokenEnc:  "enc-at-1",
		AccessExpiry:    time.Unix(1_700_000_000, 0).UTC(),
		Scopes:          "openid email",
		AccountEmail:    "user@gmail.com",
		AccountSub:      "sub-pg-1",
	}
	if err := st.Upsert(ctx, c); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := st.Get(ctx, "pg-acct1", integrations.ProviderGoogle)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RefreshTokenEnc != "enc-rt-1" {
		t.Errorf("RefreshTokenEnc: want enc-rt-1, got %q", got.RefreshTokenEnc)
	}
	if got.AccountEmail != "user@gmail.com" {
		t.Errorf("AccountEmail: want user@gmail.com, got %q", got.AccountEmail)
	}
}

func TestPG_Integrations_GetNotConnected(t *testing.T) {
	st := openPGIntegrationsStore(t)
	ctx := context.Background()

	_, err := st.Get(ctx, "pg-ghost", integrations.ProviderGoogle)
	if err != integrations.ErrNotConnected {
		t.Fatalf("want ErrNotConnected, got %v", err)
	}
}

func TestPG_Integrations_SetAccessToken(t *testing.T) {
	st := openPGIntegrationsStore(t)
	ctx := context.Background()

	c := integrations.Connection{
		AccountID: "pg-acct2", Provider: integrations.ProviderDropbox,
		RefreshTokenEnc: "enc-rt", AccessTokenEnc: "old-at",
	}
	if err := st.Upsert(ctx, c); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	expiry := time.Unix(1_800_000_000, 0).UTC()
	if err := st.SetAccessToken(ctx, "pg-acct2", integrations.ProviderDropbox, "new-at", expiry); err != nil {
		t.Fatalf("SetAccessToken: %v", err)
	}
	got, err := st.Get(ctx, "pg-acct2", integrations.ProviderDropbox)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccessTokenEnc != "new-at" {
		t.Errorf("AccessTokenEnc: want new-at, got %q", got.AccessTokenEnc)
	}
}

func TestPG_Integrations_SetRefreshToken(t *testing.T) {
	st := openPGIntegrationsStore(t)
	ctx := context.Background()

	c := integrations.Connection{
		AccountID: "pg-acct3", Provider: integrations.ProviderMicrosoft,
		RefreshTokenEnc: "old-rt",
	}
	if err := st.Upsert(ctx, c); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := st.SetRefreshToken(ctx, "pg-acct3", integrations.ProviderMicrosoft, "new-rt"); err != nil {
		t.Fatalf("SetRefreshToken: %v", err)
	}
	got, err := st.Get(ctx, "pg-acct3", integrations.ProviderMicrosoft)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RefreshTokenEnc != "new-rt" {
		t.Errorf("RefreshTokenEnc: want new-rt, got %q", got.RefreshTokenEnc)
	}
}

func TestPG_Integrations_ListAndDelete(t *testing.T) {
	st := openPGIntegrationsStore(t)
	ctx := context.Background()

	for _, prov := range []string{integrations.ProviderGoogle, integrations.ProviderDropbox} {
		c := integrations.Connection{AccountID: "pg-listacct", Provider: prov, RefreshTokenEnc: "rt"}
		if err := st.Upsert(ctx, c); err != nil {
			t.Fatalf("Upsert %s: %v", prov, err)
		}
	}

	list, err := st.List(ctx, "pg-listacct")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 connections, got %d", len(list))
	}

	if err := st.Delete(ctx, "pg-listacct", integrations.ProviderGoogle); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, err = st.List(ctx, "pg-listacct")
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(list) != 1 || list[0].Provider != integrations.ProviderDropbox {
		t.Fatalf("want 1 dropbox connection after delete, got %+v", list)
	}
}

func TestPG_Integrations_Rebind(t *testing.T) {
	// Smoke test: exercises all query paths with $N placeholders on a real
	// Postgres connection (verifies cpdb.Rebind end-to-end).
	st := openPGIntegrationsStore(t)
	ctx := context.Background()

	c := integrations.Connection{
		AccountID: "pg-rebind", Provider: integrations.ProviderGoogle, RefreshTokenEnc: "rt",
	}
	if err := st.Upsert(ctx, c); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := st.Get(ctx, "pg-rebind", integrations.ProviderGoogle); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := st.List(ctx, "pg-rebind"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := st.SetRefreshToken(ctx, "pg-rebind", integrations.ProviderGoogle, "rt2"); err != nil {
		t.Fatalf("SetRefreshToken: %v", err)
	}
	if err := st.Delete(ctx, "pg-rebind", integrations.ProviderGoogle); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
