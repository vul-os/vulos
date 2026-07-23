// routes_storagesel_authz_test.go — item 8 regression: device-authed sync-mode
// reads must bind to the device's OWN account, not a client-supplied account_id.
package cproutes

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSyncModeBindsToDeviceAccount(t *testing.T) {
	const secret = "device-shared-secret"
	t.Setenv("CP_SHARED_SECRET", secret)

	owner := fakeResolver{owner: map[string]string{"ulid-A": "acct-A"}}
	h := &storageSelHandlers{sel: nil, auth: nil, owner: owner}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/storage/sync-mode", h.getSyncMode)

	// Device owns acct-A but claims acct-B → must be rejected (cross-account).
	req := httptest.NewRequest(http.MethodGet, "/api/storage/sync-mode?account_id=acct-B", nil)
	req.Header.Set("X-Device-Auth", secret)
	req.Header.Set("X-Device-ULID", "ulid-A")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-account sync-mode: want 403, got %d: %s", rec.Code, rec.Body.String())
	}

	// Missing X-Device-ULID → fail closed (400), never trusts the query param.
	req = httptest.NewRequest(http.MethodGet, "/api/storage/sync-mode?account_id=acct-A", nil)
	req.Header.Set("X-Device-Auth", secret)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing ULID: want 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
