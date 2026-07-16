// oauthprovider_pg_test.go — Postgres integration tests for the oauthprovider
// store.
//
// These tests are skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres
// DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/oauthprovider/...
//
// In CI the cp-build-test-pg job sets this env var and runs against a real
// postgres:16 service container, verifying that both the SQLite and Postgres
// paths pass the same behavioural contract.
package oauthprovider

import (
	"context"
	"os"
	"testing"
	"time"

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

// openPGTestStore opens an oauthprovider store backed by Postgres, using the
// "oauthprovider_pgtest" schema to avoid collisions with production data.
func openPGTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := pgDSN(t)

	// Dev KEK is sufficient for the behavioural contract under test (signing
	// keys are exercised in the SQLite suite); avoid requiring a KEK secret here.
	t.Setenv("VULOS_DEV", "true")
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("oauthprovider_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}

	st, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("oauthprovider.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		// Best-effort: drop test schema so the next run starts clean.
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS oauthprovider_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_ClientRoundtrip(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	want := Client{
		ClientID:     "vcid_pg_client_1",
		SecretHash:   "deadbeef",
		IsPublic:     false,
		Name:         "PG App",
		RedirectURIs: []string{"https://app.example.com/cb"},
		Scopes:       []string{ScopeOpenID, "email"},
		OwnerUserID:  "pg_owner_1",
	}
	if err := st.CreateClient(ctx, want); err != nil {
		t.Fatalf("CreateClient: %v", err)
	}

	got, err := st.GetClient(ctx, want.ClientID)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if got.ClientID != want.ClientID || got.Name != want.Name || got.OwnerUserID != want.OwnerUserID {
		t.Fatalf("client mismatch: got %+v", got)
	}
	if got.IsPublic != want.IsPublic {
		t.Errorf("is_public: want %v, got %v", want.IsPublic, got.IsPublic)
	}
	if len(got.RedirectURIs) != 1 || got.RedirectURIs[0] != want.RedirectURIs[0] {
		t.Errorf("redirect URIs mismatch: %v", got.RedirectURIs)
	}
	if len(got.Scopes) != 2 {
		t.Errorf("scopes mismatch: %v", got.Scopes)
	}

	if _, err := st.GetClient(ctx, "vcid_does_not_exist"); err != ErrClientNotFound {
		t.Errorf("expected ErrClientNotFound, got %v", err)
	}
}

func TestPG_TokenRoundtrip(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	tok := Token{
		TokenHash: "pg_token_hash_1",
		TokenType: "refresh",
		ClientID:  "vcid_pg_tok",
		UserID:    "pg_user_tok",
		Scope:     "openid email",
		ExpiresAt: time.Now().Add(time.Hour),
		FamilyID:  "fam_pg_1",
	}
	if err := st.SaveToken(ctx, tok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	got, err := st.GetToken(ctx, tok.TokenHash, "refresh")
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if got.UserID != tok.UserID || got.Scope != tok.Scope {
		t.Fatalf("token mismatch: got %+v", got)
	}
	if got.FamilyID != tok.FamilyID {
		t.Errorf("family_id: want %q, got %q", tok.FamilyID, got.FamilyID)
	}

	// Revoke + cascade by hash, then expect not-found.
	if err := st.RevokeTokenByHash(ctx, tok.TokenHash); err != nil {
		t.Fatalf("RevokeTokenByHash: %v", err)
	}
	if _, err := st.GetToken(ctx, tok.TokenHash, "refresh"); err != ErrTokenNotFound {
		t.Errorf("expected ErrTokenNotFound after revoke, got %v", err)
	}
}

func TestPG_ConsentUpsert(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	if err := st.UpsertConsent(ctx, "pg_consent_user", "vcid_pg_consent", "openid"); err != nil {
		t.Fatalf("UpsertConsent: %v", err)
	}
	// Upsert again with a wider scope — must update via ON CONFLICT, not error.
	if err := st.UpsertConsent(ctx, "pg_consent_user", "vcid_pg_consent", "openid email"); err != nil {
		t.Fatalf("UpsertConsent (update): %v", err)
	}

	g, err := st.GetConsent(ctx, "pg_consent_user", "vcid_pg_consent")
	if err != nil {
		t.Fatalf("GetConsent: %v", err)
	}
	if g.Scope != "openid email" {
		t.Errorf("scope: want %q, got %q", "openid email", g.Scope)
	}

	// Revoke → GetConsent returns ErrConsentNotFound.
	if err := st.RevokeConsent(ctx, "pg_consent_user", "vcid_pg_consent"); err != nil {
		t.Fatalf("RevokeConsent: %v", err)
	}
	if _, err := st.GetConsent(ctx, "pg_consent_user", "vcid_pg_consent"); err != ErrConsentNotFound {
		t.Errorf("expected ErrConsentNotFound after revoke, got %v", err)
	}
}
