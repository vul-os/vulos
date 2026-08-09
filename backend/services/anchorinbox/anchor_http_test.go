package anchorinbox_test

// anchor_http_test.go — IDOR-ANCHORINBOX-01 regression.
//
// handleAnchorProvision/handleAnchorStatus used to read account_id straight
// from the request body/query string, so any authenticated profile could
// provision or read another account's anchor-inbox entitlement record just by
// guessing/enumerating its account_id. These tests prove account_id is always
// derived from the caller's own X-User-ID, never a client-supplied value.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/anchorinbox"
)

func newAnchorMux(t *testing.T) *http.ServeMux {
	t.Helper()
	s := openTestStore(t)
	mux := http.NewServeMux()
	anchorinbox.RegisterAnchorHandlers(mux, s)
	return mux
}

// TestAnchorProvision_IgnoresClientSuppliedAccountID: alice POSTs a body
// claiming bob's account_id — the record actually created must be alice's own
// (keyed by her X-User-ID), never bob's.
func TestAnchorProvision_IgnoresClientSuppliedAccountID(t *testing.T) {
	mux := newAnchorMux(t)

	req := httptest.NewRequest(http.MethodPost, "/api/anchor-inbox/provision",
		strings.NewReader(`{"account_id":"bob"}`))
	req.Header.Set("X-User-ID", "alice")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("provision = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var st struct {
		AccountID string `json:"account_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.AccountID != "alice" {
		t.Fatalf("IDOR-ANCHORINBOX-01 regression: alice provisioned an entitlement record for account_id=%q (should be her own X-User-ID, alice)", st.AccountID)
	}

	// Bob's account must be untouched — a status check as bob (his real
	// X-User-ID) must 404, not find the record alice's spoofed body targeted.
	req2 := httptest.NewRequest(http.MethodGet, "/api/anchor-inbox/status", nil)
	req2.Header.Set("X-User-ID", "bob")
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("IDOR-ANCHORINBOX-01 regression: bob's account was provisioned by alice's request: status %d, body %s", rec2.Code, rec2.Body.String())
	}
}

// TestAnchorStatus_CannotReadAnotherAccountsRecord: alice cannot read bob's
// anchor-inbox status by supplying his account_id as a query param — the
// endpoint no longer accepts one at all; only her own X-User-ID is consulted.
func TestAnchorStatus_CannotReadAnotherAccountsRecord(t *testing.T) {
	mux := newAnchorMux(t)

	// Provision bob's own record (as bob).
	provReq := httptest.NewRequest(http.MethodPost, "/api/anchor-inbox/provision", strings.NewReader(`{}`))
	provReq.Header.Set("X-User-ID", "bob")
	provReq.Header.Set("Content-Type", "application/json")
	provRec := httptest.NewRecorder()
	mux.ServeHTTP(provRec, provReq)
	if provRec.Code != http.StatusOK {
		t.Fatalf("provision bob: %d: %s", provRec.Code, provRec.Body.String())
	}

	// Alice tries the old attack shape (?account_id=bob) — the param is now
	// ignored entirely, so she gets HER OWN (nonexistent) record, not bob's.
	req := httptest.NewRequest(http.MethodGet, "/api/anchor-inbox/status?account_id=bob", nil)
	req.Header.Set("X-User-ID", "alice")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("IDOR-ANCHORINBOX-01 regression: alice read bob's anchor-inbox status via ?account_id=bob: %s", rec.Body.String())
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("alice status (no record of her own) = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// TestAnchorProvisionStatus_RequireAuth: both endpoints reject a caller with
// no X-User-ID at all.
func TestAnchorProvisionStatus_RequireAuth(t *testing.T) {
	mux := newAnchorMux(t)

	provReq := httptest.NewRequest(http.MethodPost, "/api/anchor-inbox/provision", strings.NewReader(`{}`))
	provReq.Header.Set("Content-Type", "application/json")
	provRec := httptest.NewRecorder()
	mux.ServeHTTP(provRec, provReq)
	if provRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth provision = %d, want 401", provRec.Code)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/anchor-inbox/status", nil)
	statusRec := httptest.NewRecorder()
	mux.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", statusRec.Code)
	}
}
