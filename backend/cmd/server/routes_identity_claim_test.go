package main

// routes_identity_claim_test.go — IDENTITY-01 coverage for the @vulos.to claim
// proxies (routes_identity_claim.go):
//
//   GET  /api/identity/check  — forwards handle/domain + Cookie to the CP, relays
//                               CP status/body, and returns {"offline":true} when
//                               the CP is unreachable.
//   POST /api/identity/claim  — forwards ONLY {handle,domain} + Cookie (never an
//                               account id) to the CP, relays CP status/body
//                               verbatim, and is OS-session gated by the auth
//                               middleware (401 without a session).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/auth"
)

// fakeCP stands up a minimal control-plane that records the last request it saw
// and replies with a canned status + body.
type fakeCP struct {
	srv        *httptest.Server
	lastPath   string
	lastQuery  string
	lastCookie string
	lastBody   string
	status     int
	body       string
}

func newFakeCP(t *testing.T, status int, body string) *fakeCP {
	t.Helper()
	cp := &fakeCP{status: status, body: body}
	cp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cp.lastPath = r.URL.Path
		cp.lastQuery = r.URL.RawQuery
		cp.lastCookie = r.Header.Get("Cookie")
		b, _ := io.ReadAll(r.Body)
		cp.lastBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(cp.status)
		w.Write([]byte(cp.body))
	}))
	t.Cleanup(cp.srv.Close)
	return cp
}

// ── GET /api/identity/check ─────────────────────────────────────────────────

func TestIdentityCheck_ForwardsAndRelays(t *testing.T) {
	cp := newFakeCP(t, http.StatusOK, `{"address":"alice@vulos.to","available":true}`)
	t.Setenv("VULOS_CLOUD_API_URL", cp.srv.URL)

	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	r := httptest.NewRequest(http.MethodGet, "/api/identity/check?handle=alice&domain=vulos.to", nil)
	r.Header.Set("Cookie", "cp_session=abc123")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("check status = %d, want 200", w.Code)
	}
	if cp.lastPath != "/api/identity/check" {
		t.Fatalf("CP path = %q, want /api/identity/check", cp.lastPath)
	}
	if !strings.Contains(cp.lastQuery, "handle=alice") || !strings.Contains(cp.lastQuery, "domain=vulos.to") {
		t.Fatalf("CP query = %q, want handle+domain forwarded", cp.lastQuery)
	}
	if cp.lastCookie != "cp_session=abc123" {
		t.Fatalf("CP cookie = %q, want the incoming Cookie forwarded", cp.lastCookie)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("relayed body not JSON: %v", err)
	}
	if got["available"] != true || got["address"] != "alice@vulos.to" {
		t.Fatalf("relayed body = %v, want CP body verbatim", got)
	}
}

func TestIdentityCheck_OfflineWhenCPUnreachable(t *testing.T) {
	// Point at a closed port so the request fails at the transport layer.
	t.Setenv("VULOS_CLOUD_API_URL", "http://127.0.0.1:1")

	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	r := httptest.NewRequest(http.MethodGet, "/api/identity/check?handle=alice", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("offline status = %d, want 200 (soft state)", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("offline body not JSON: %v", err)
	}
	if got["offline"] != true {
		t.Fatalf("offline body = %v, want {\"offline\":true}", got)
	}
}

func TestIdentityCheck_MissingHandle400(t *testing.T) {
	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	r := httptest.NewRequest(http.MethodGet, "/api/identity/check", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing-handle status = %d, want 400", w.Code)
	}
}

// ── POST /api/identity/claim ────────────────────────────────────────────────

func TestIdentityClaim_ForwardsCookieAndRelaysStatus(t *testing.T) {
	cp := newFakeCP(t, http.StatusConflict, `{"error":"taken"}`)
	t.Setenv("VULOS_CLOUD_API_URL", cp.srv.URL)

	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	// Include a client-supplied account_id to prove the OS handler drops it.
	body := `{"handle":"alice","domain":"vulos.to","account_id":"attacker-supplied"}`
	r := httptest.NewRequest(http.MethodPost, "/api/identity/claim", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Cookie", "cp_session=sess-xyz")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	// CP 409 must be relayed verbatim.
	if w.Code != http.StatusConflict {
		t.Fatalf("claim status = %d, want 409 relayed", w.Code)
	}
	if cp.lastPath != "/api/identity/claim" {
		t.Fatalf("CP path = %q, want /api/identity/claim", cp.lastPath)
	}
	if cp.lastCookie != "cp_session=sess-xyz" {
		t.Fatalf("CP cookie = %q, want the incoming Cookie forwarded", cp.lastCookie)
	}
	// SECURITY: the OS handler must NOT forward a client-supplied account id.
	var forwarded map[string]any
	if err := json.Unmarshal([]byte(cp.lastBody), &forwarded); err != nil {
		t.Fatalf("forwarded body not JSON: %v", err)
	}
	if _, ok := forwarded["account_id"]; ok {
		t.Fatalf("forwarded body leaked account_id: %v — OS must not trust client-supplied account", forwarded)
	}
	if forwarded["handle"] != "alice" || forwarded["domain"] != "vulos.to" {
		t.Fatalf("forwarded body = %v, want only handle+domain", forwarded)
	}
}

func TestIdentityClaim_MissingHandle400(t *testing.T) {
	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)

	r := httptest.NewRequest(http.MethodPost, "/api/identity/claim", strings.NewReader(`{"domain":"vulos.to"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing-handle status = %d, want 400", w.Code)
	}
}

// TestIdentityClaim_RequiresLocalSession asserts the auth middleware gates the
// claim proxy: without an OS session it is 401 (it is NOT in publicPaths), while
// /api/identity/check is public (setup-time) and passes through.
func TestIdentityClaim_RequiresLocalSession(t *testing.T) {
	cp := newFakeCP(t, http.StatusOK, `{"address":"alice@vulos.to","claimed":true}`)
	t.Setenv("VULOS_CLOUD_API_URL", cp.srv.URL)

	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// Register a first user so the instance is "provisioned" — otherwise the
	// middleware may allow unauthenticated first-setup registration paths.
	if _, err := store.Register("owner", "password-owner-123", "Owner"); err != nil {
		t.Fatalf("register owner: %v", err)
	}
	h := auth.NewHandler(store)

	mux := http.NewServeMux()
	registerIdentityClaimRoutes(mux)
	srv := httptest.NewServer(h.Middleware(mux))
	t.Cleanup(srv.Close)

	// claim WITHOUT a session cookie → middleware 401 (never reaches the proxy).
	res, err := http.Post(srv.URL+"/api/identity/claim", "application/json",
		strings.NewReader(`{"handle":"alice","domain":"vulos.to"}`))
	if err != nil {
		t.Fatalf("post claim: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated claim status = %d, want 401 (auth-gated)", res.StatusCode)
	}

	// check is public (setup-time) → passes the middleware and reaches the proxy.
	res2, err := http.Get(srv.URL + "/api/identity/check?handle=alice")
	if err != nil {
		t.Fatalf("get check: %v", err)
	}
	defer res2.Body.Close()
	if res2.StatusCode == http.StatusUnauthorized {
		t.Fatalf("check returned 401 — it must be public (setup-time), not auth-gated")
	}
}
