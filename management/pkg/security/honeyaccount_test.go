package security

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHoneypotSeed verifies that N honeypot accounts are seeded.
func TestHoneypotSeed(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.SeedHoneypotAccounts(ctx, 3); err != nil {
		t.Fatalf("SeedHoneypotAccounts: %v", err)
	}

	// Check that the first generated email is recognized.
	email := "dev_test_42@vulos.org"
	if !store.IsHoneypotAccount(ctx, email) {
		t.Errorf("want %s to be a honeypot account after seeding", email)
	}
}

// TestHoneypotLoginBlock verifies that a login attempt against a honeypot
// account is blocked and the IP is auto-blocked.
func TestHoneypotLoginBlock(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Seed one honeypot account.
	if err := store.SeedHoneypotAccounts(ctx, 1); err != nil {
		t.Fatalf("SeedHoneypotAccounts: %v", err)
	}

	downstreamCalled := false
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamCalled = true
		w.WriteHeader(http.StatusOK)
	})

	body := bytes.NewBufferString(`{"email":"dev_test_42@vulos.org","password":"anything"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.1:4567"
	rec := httptest.NewRecorder()

	HoneypotLoginMiddleware(store, downstream).ServeHTTP(rec, req)

	if downstreamCalled {
		t.Error("want downstream NOT called for honeypot account login")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("want 401 for honeypot login, got %d", rec.Code)
	}

	// IP should be blocked.
	if !store.IsBlocked(ctx, "203.0.113.1") {
		t.Error("want attacker IP auto-blocked after honeypot hit")
	}
}

// TestHoneypotRealAccountPasses verifies that a normal login passes through.
func TestHoneypotRealAccountPasses(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	_ = store.SeedHoneypotAccounts(ctx, 1)

	called := false
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	body := bytes.NewBufferString(`{"email":"alice@vulos.org","password":"pw"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.RemoteAddr = "1.1.1.1:1111"
	rec := httptest.NewRecorder()

	HoneypotLoginMiddleware(store, downstream).ServeHTTP(rec, req)

	if !called {
		t.Error("want downstream called for non-honeypot account")
	}
	_ = rec.Code
}
