// provision_test.go — unit tests for IDENTITY-01 Provision (provision.go).
//
// Covers:
//  1. Handle/domain validation (charset, length, domain rules).
//  2. Provisioning idempotency (second call with same address returns cached identity).
//  3. Conflict: handle already taken (cloud returns 409).
//  4. Wizard-cannot-be-skipped invariant: Provision returns ErrHandleInvalid on
//     an empty handle, ensuring the wizard cannot advance without a valid identity.
package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// ─── Test 1: Handle and domain validation ────────────────────────────────────

func TestValidateHandle(t *testing.T) {
	valid := []string{"alice", "bob123", "my-handle", "a_b_c", "abc", "a1b2c3d4"}
	for _, h := range valid {
		if err := validateHandle(h); err != nil {
			t.Errorf("validateHandle(%q): unexpected error: %v", h, err)
		}
	}

	invalid := []string{
		"",         // empty
		"ab",       // too short (< 3)
		"-alice",   // starts with hyphen
		"alice-",   // ends with hyphen
		"_alice",   // starts with underscore
		"ALICE",    // uppercase
		"alice!",   // invalid char
		"alice smith", // space
		string(make([]byte, 33)), // 33 chars — too long
	}
	for _, h := range invalid {
		if err := validateHandle(h); err == nil {
			t.Errorf("validateHandle(%q): expected error, got nil", h)
		}
	}
}

func TestValidateDomain(t *testing.T) {
	valid := []string{"vulos.org", "example.com", "mail.example.co.za"}
	for _, d := range valid {
		if err := validateDomain(d); err != nil {
			t.Errorf("validateDomain(%q): unexpected error: %v", d, err)
		}
	}

	invalid := []string{
		"",          // empty
		"nodot",     // no dot
		"a b.com",   // space
		"bad\tdomain.com", // tab
	}
	for _, d := range invalid {
		if err := validateDomain(d); err == nil {
			t.Errorf("validateDomain(%q): expected error, got nil", d)
		}
	}
}

// ─── Test 2: Provisioning idempotency ────────────────────────────────────────

func TestProvisionIdempotency(t *testing.T) {
	// Cloud stub — counts calls to ensure the second Provision skips it.
	callCount := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(claimResponse{
			Address:        "alice@vulos.org",
			PublicKeyB64:   "AAAA",
			JMAPSessionURL: "https://jmap.example.com/session",
		})
	}))
	defer stub.Close()

	t.Setenv("VULOS_CLOUD_API", stub.URL)

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "identity.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// First call — should hit the cloud API and persist.
	pi, err := Provision(ctx, store, "acct-1", "alice", "vulos.org", "passphrase1", stub.Client())
	if err != nil {
		t.Fatalf("Provision (first call): %v", err)
	}
	if pi.Address != "alice@vulos.org" {
		t.Errorf("address = %q, want alice@vulos.org", pi.Address)
	}
	if callCount != 1 {
		t.Errorf("cloud API calls after first Provision = %d, want 1", callCount)
	}

	// Second call with the same address — should return cached without hitting API.
	pi2, err := Provision(ctx, store, "acct-1", "alice", "vulos.org", "passphrase1", stub.Client())
	if err != nil {
		t.Fatalf("Provision (second call / idempotent): %v", err)
	}
	if pi2.Address != "alice@vulos.org" {
		t.Errorf("idempotent address = %q, want alice@vulos.org", pi2.Address)
	}
	// Cloud API must NOT have been called a second time.
	if callCount != 1 {
		t.Errorf("cloud API calls after idempotent Provision = %d, want 1 (no second call)", callCount)
	}
}

// ─── Test 3: Handle conflict (409 from cloud) ─────────────────────────────────

func TestProvisionHandleTaken(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(claimResponse{Error: "handle_taken"})
	}))
	defer stub.Close()

	t.Setenv("VULOS_CLOUD_API", stub.URL)

	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "identity.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	_, err = Provision(context.Background(), store, "acct-2", "takenhandle", "vulos.org", "pass", stub.Client())
	if err == nil {
		t.Fatal("expected ErrHandleTaken, got nil")
	}
	if err != ErrHandleTaken {
		t.Errorf("error = %v, want ErrHandleTaken", err)
	}
}

// ─── Test 4: Wizard-cannot-be-skipped invariant ───────────────────────────────
//
// The frozen invariant is: a Vulos identity is mandatory.  This test asserts
// that Provision refuses to proceed with an empty or invalid handle, so the
// wizard layer cannot circumvent the requirement by passing an empty value.

func TestProvisionMandatoryIdentityInvariant(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "identity.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Empty handle must be rejected before any network call.
	_, err = Provision(ctx, store, "acct-3", "", "vulos.org", "pass", nil)
	if err == nil {
		t.Fatal("empty handle: expected error, got nil")
	}
	if err != ErrHandleInvalid {
		t.Errorf("empty handle error = %v, want ErrHandleInvalid", err)
	}

	// Handles that could be used to bypass validation (whitespace, mixed case).
	bypassAttempts := []struct {
		handle string
		domain string
	}{
		{"  ", "vulos.org"},   // whitespace-only handle
		{"AB", "vulos.org"},   // too short + uppercase
		{"alice", ""},          // empty domain
		{"alice", "nodot"},     // domain without dot
	}
	for _, tc := range bypassAttempts {
		_, err := Provision(ctx, store, "acct-3", tc.handle, tc.domain, "pass", nil)
		if err == nil {
			t.Errorf("Provision(handle=%q, domain=%q): expected error (mandatory identity invariant), got nil", tc.handle, tc.domain)
		}
	}

	// Confirm store is still empty — no partial state written on failure.
	loaded, err := store.loadIdentity()
	if err != nil {
		t.Fatalf("loadIdentity after failed provisions: %v", err)
	}
	if loaded != nil {
		t.Errorf("identity was persisted despite invalid inputs — wizard-skip invariant violated")
	}
}
