// Package integration contains feature-level HTTP smoke tests for the Vulos
// backend. They spin up an in-process httptest.Server so no listening port is
// required, exercise the real auth.Middleware + route set, and assert both
// functional and security invariants.
//
// Design notes:
//   - A temp HOME is used for every test (t.Setenv("HOME", t.TempDir())) so
//     no real user state is touched.
//   - The mux is built from the minimal set of services needed to satisfy the
//     invariants; heavyweight deps (PTY, GPU, Wine, browser streaming …) are
//     NOT wired here — correctness of the Middleware layer does not require them.
//   - Each security invariant is its own sub-test so failures are reported
//     individually.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/services/auth"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// buildServer creates a minimal in-process HTTP server wired with the real
// auth.Handler + auth.Middleware. A few representative protected and public
// routes are registered so every security sub-test can exercise the full
// middleware stack without needing the entire main.go dependency tree.
func buildServer(t *testing.T) *httptest.Server {
	t.Helper()

	// Isolate all on-disk state to a temp dir.
	home := t.TempDir()
	t.Setenv("HOME", home)

	dbDir := filepath.Join(home, "db")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatalf("setup: mkdir dbDir: %v", err)
	}

	// Real auth store — SQLite + in-memory fallback.
	store, err := auth.NewStore(dbDir)
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}

	handler := auth.NewHandler(store)
	// Silence sysuser callbacks that require root.
	handler.OnUserCreated = nil
	handler.OnUserLogin = nil
	handler.OnRoleChanged = nil

	mux := http.NewServeMux()

	// Auth routes (register, login, status, logout, me, …).
	handler.Register(mux)

	// Health — public.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	// /api/health — also public (cluster health variant used by main.go).
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	// A representative PROTECTED endpoint that reads X-User-ID.
	// /api/open is NOT in publicPaths, so middleware must enforce auth.
	mux.HandleFunc("GET /api/open", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			// Belt-and-suspenders: middleware should have already rejected.
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"user_id":%q}`, userID)
	})

	// A protected admin-only endpoint (mirrors /api/exec logic).
	mux.HandleFunc("POST /api/exec", func(w http.ResponseWriter, r *http.Request) {
		// Kill-switch: VULOS_DISABLE_EXEC=1 disables entirely.
		if os.Getenv("VULOS_DISABLE_EXEC") == "1" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"error":"exec endpoint disabled by configuration"}`)
			return
		}
		userID := r.Header.Get("X-User-ID")
		p, _ := store.GetProfile(userID)
		if p == nil || p.Role != auth.RoleAdmin {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"error":"admin only"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	// Setup join routes — public per publicPaths in auth/handlers.go.
	mux.HandleFunc("POST /api/setup/join", func(w http.ResponseWriter, r *http.Request) {
		// Minimal decode to test bad-input path without the real joinsync dep.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"invalid request body"}`)
			return
		}
		// Would normally call joinsync.Join; reflect no-S3 as a 502.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"S3 bucket unreachable"}`)
	})

	mux.HandleFunc("GET /api/setup/join/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"state":"idle"}`)
	})

	// Wrap with the real auth Middleware — this is what we're testing.
	wrapped := handler.Middleware(mux)
	srv := httptest.NewServer(wrapped)
	t.Cleanup(srv.Close)
	return srv
}

// get performs a GET request against srv, optionally attaching headers.
func get(t *testing.T, srv *httptest.Server, path string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest GET %s: %v", path, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// postJSON performs a POST with a JSON body and optional headers.
func postJSON(t *testing.T, srv *httptest.Server, path string, body any, headers map[string]string) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("NewRequest POST %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// mustClose drains and closes a response body.
func mustClose(r *http.Response) {
	if r != nil && r.Body != nil {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
}

// decodeJSON decodes the response body into dst.
func decodeJSON(t *testing.T, r *http.Response, dst any) {
	t.Helper()
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		t.Fatalf("decodeJSON: %v", err)
	}
}

// sessionToken extracts the vulos_session cookie from a response.
func sessionToken(r *http.Response) string {
	for _, c := range r.Cookies() {
		if c.Name == "vulos_session" {
			return c.Value
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// test suite
// ---------------------------------------------------------------------------

// TestSmoke is the top-level container; each invariant is a sub-test.
func TestSmoke(t *testing.T) {
	t.Run("HealthReachableUnauthenticated", testHealthReachable)
	t.Run("ApiHealthReachableUnauthenticated", testAPIHealthReachable)
	t.Run("SEC_C1_SpoofedXUserIDStripped", testSpoofedHeaderStripped)
	t.Run("SEC_ProtectedRouteRequiresSession", testProtectedRouteRequiresSession)
	t.Run("SEC_AdminGate_NoSession", testAdminGateNoSession)
	t.Run("SEC_AdminGate_NonAdmin", testAdminGateNonAdmin)
	t.Run("SEC_KillSwitch_DisableExec", testKillSwitchDisableExec)
	t.Run("PublicPath_AuthStatus", testPublicAuthStatus)
	t.Run("AuthHappyPath_RegisterLoginProtected", testAuthHappyPath)
	t.Run("SetupJoin_PublicReachable", testSetupJoinPublic)
	t.Run("SetupJoin_MalformedInput_No500", testSetupJoinMalformed)
	t.Run("SetupJoinStatus_PublicReachable", testSetupJoinStatusPublic)
}

// testHealthReachable: GET /health must return 200 with no credentials.
func testHealthReachable(t *testing.T) {
	srv := buildServer(t)
	resp := get(t, srv, "/health", nil)
	defer mustClose(resp)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /health: want 200, got %d", resp.StatusCode)
	}
}

// testAPIHealthReachable: GET /api/health must not be 401'd by the session
// middleware.
//
// The mock route for it has been mounted in buildServer since this file was
// written, labelled "also public" — but nothing ever requested it, and the path
// was NOT in publicPaths, so the label was wrong for as long as it existed and
// no test noticed. Three docs pages and roadmap/NETWORK.md's external-router
// design tell you to curl this endpoint as a first diagnostic; someone doing
// that on a sick box got a 401 and concluded auth was broken.
//
// What the REAL handler serves an anonymous caller (the verdict only — no
// per-check detail) is pinned separately in
// backend/cmd/server/health_test.go: TestClusterHealth_AnonymousGetsVerdictOnly.
// This test covers only the middleware half of that contract.
func testAPIHealthReachable(t *testing.T) {
	srv := buildServer(t)
	resp := get(t, srv, "/api/health", nil)
	defer mustClose(resp)
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("GET /api/health unauthenticated: 401 — the auth middleware is gating the " +
			"endpoint the troubleshooting docs tell you to curl first. It must be in publicPaths.")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/health unauthenticated: want 200, got %d", resp.StatusCode)
	}
}

// testSpoofedHeaderStripped: SEC-A C1 invariant.
//
// A caller that sends "X-User-ID: attacker" without a valid session MUST NOT
// receive a 200 from a protected endpoint. The Middleware MUST strip the header
// before dispatching.
func testSpoofedHeaderStripped(t *testing.T) {
	srv := buildServer(t)

	resp := get(t, srv, "/api/open", map[string]string{
		"X-User-ID": "attacker-uid",
	})
	defer mustClose(resp)

	if resp.StatusCode == http.StatusOK {
		t.Errorf("SEC C1 VIOLATED: spoofed X-User-ID without session was accepted (200); want 401")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("SEC C1: want 401, got %d", resp.StatusCode)
	}
}

// testProtectedRouteRequiresSession: /api/open is not in publicPaths.
// Unauthenticated access must be rejected with 401.
func testProtectedRouteRequiresSession(t *testing.T) {
	srv := buildServer(t)

	resp := get(t, srv, "/api/open", nil)
	defer mustClose(resp)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/open unauthenticated: want 401, got %d", resp.StatusCode)
	}
}

// testAdminGateNoSession: POST /api/exec without any session must return 401
// (the middleware rejects before the handler even runs the admin check).
func testAdminGateNoSession(t *testing.T) {
	srv := buildServer(t)

	resp := postJSON(t, srv, "/api/exec", map[string]string{"command": "id"}, nil)
	defer mustClose(resp)

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /api/exec no-session: want 401, got %d", resp.StatusCode)
	}
}

// testAdminGateNonAdmin: an authenticated but non-admin user must be rejected
// with 403 on POST /api/exec.
func testAdminGateNonAdmin(t *testing.T) {
	srv := buildServer(t)

	// Register the first user (becomes admin automatically).
	r1 := postJSON(t, srv, "/api/auth/register", map[string]string{
		"username": "admin1",
		"password": "Str0ng!Pass1",
	}, nil)
	if r1.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(r1.Body)
		r1.Body.Close()
		t.Fatalf("register admin: want 200, got %d — %s", r1.StatusCode, body)
	}
	mustClose(r1)

	// Register a second (non-admin) user — requires admin session.
	// First, log in as admin to get a session token.
	loginResp := postJSON(t, srv, "/api/auth/login", map[string]string{
		"username": "admin1",
		"password": "Str0ng!Pass1",
	}, nil)
	if loginResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(loginResp.Body)
		loginResp.Body.Close()
		t.Fatalf("login admin: want 200, got %d — %s", loginResp.StatusCode, body)
	}
	adminToken := sessionToken(loginResp)
	mustClose(loginResp)
	if adminToken == "" {
		t.Fatal("admin login: no session cookie returned")
	}

	// Register second user as admin.
	r2 := postJSON(t, srv, "/api/auth/register", map[string]string{
		"username": "nonadmin",
		"password": "Str0ng!Pass2",
	}, map[string]string{
		"Authorization": "Bearer " + adminToken,
	})
	if r2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(r2.Body)
		r2.Body.Close()
		t.Fatalf("register non-admin: want 200, got %d — %s", r2.StatusCode, body)
	}
	mustClose(r2)

	// Login as non-admin user.
	loginResp2 := postJSON(t, srv, "/api/auth/login", map[string]string{
		"username": "nonadmin",
		"password": "Str0ng!Pass2",
	}, nil)
	if loginResp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(loginResp2.Body)
		loginResp2.Body.Close()
		t.Fatalf("login non-admin: want 200, got %d — %s", loginResp2.StatusCode, body)
	}
	nonAdminToken := sessionToken(loginResp2)
	mustClose(loginResp2)
	if nonAdminToken == "" {
		t.Fatal("non-admin login: no session cookie returned")
	}

	// Attempt to call /api/exec as non-admin — must be 403.
	execResp := postJSON(t, srv, "/api/exec", map[string]string{"command": "id"}, map[string]string{
		"Authorization": "Bearer " + nonAdminToken,
	})
	defer mustClose(execResp)

	if execResp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /api/exec non-admin: want 403, got %d", execResp.StatusCode)
	}
}

// testKillSwitchDisableExec: VULOS_DISABLE_EXEC=1 must cause /api/exec to
// return 503 even for an authenticated admin.
func testKillSwitchDisableExec(t *testing.T) {
	srv := buildServer(t)

	// Activate kill-switch for this sub-test only.
	t.Setenv("VULOS_DISABLE_EXEC", "1")

	// Register + login as admin so middleware lets us through.
	r1 := postJSON(t, srv, "/api/auth/register", map[string]string{
		"username": "ksadmin",
		"password": "Str0ng!KS991",
	}, nil)
	if r1.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(r1.Body)
		r1.Body.Close()
		t.Fatalf("register: want 200, got %d — %s", r1.StatusCode, body)
	}
	adminToken := sessionToken(r1)
	r1.Body.Close()
	if adminToken == "" {
		// Cookie may be on the register response already for first user.
		// If not, log in explicitly.
		loginR := postJSON(t, srv, "/api/auth/login", map[string]string{
			"username": "ksadmin",
			"password": "Str0ng!KS991",
		}, nil)
		adminToken = sessionToken(loginR)
		mustClose(loginR)
	}
	if adminToken == "" {
		t.Fatal("could not obtain admin session for kill-switch test")
	}

	resp := postJSON(t, srv, "/api/exec", map[string]string{"command": "id"}, map[string]string{
		"Authorization": "Bearer " + adminToken,
	})
	defer mustClose(resp)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("kill-switch VULOS_DISABLE_EXEC=1: want 503, got %d", resp.StatusCode)
	}
}

// testPublicAuthStatus: GET /api/auth/status is in publicPaths and must be
// reachable without credentials (positive control).
func testPublicAuthStatus(t *testing.T) {
	srv := buildServer(t)

	resp := get(t, srv, "/api/auth/status", nil)
	defer mustClose(resp)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/auth/status unauthenticated: want 200, got %d", resp.StatusCode)
	}
}

// testAuthHappyPath: register → login → reach a protected endpoint with the
// returned session token.
func testAuthHappyPath(t *testing.T) {
	srv := buildServer(t)

	// 1. Register (first user → admin, no existing users).
	regResp := postJSON(t, srv, "/api/auth/register", map[string]string{
		"username": "alice",
		"password": "Alicepass1!2",
	}, nil)
	if regResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(regResp.Body)
		regResp.Body.Close()
		t.Fatalf("register: want 200, got %d — %s", regResp.StatusCode, body)
	}

	// The register endpoint returns a session cookie for the first user.
	regToken := sessionToken(regResp)
	var regBody struct {
		Session struct {
			Token string `json:"token"`
		} `json:"session"`
	}
	decodeJSON(t, regResp, &regBody)
	if regToken == "" {
		regToken = regBody.Session.Token
	}

	// 2. If register didn't return a token, log in explicitly.
	if regToken == "" {
		loginResp := postJSON(t, srv, "/api/auth/login", map[string]string{
			"username": "alice",
			"password": "Alicepass1!2",
		}, nil)
		regToken = sessionToken(loginResp)
		mustClose(loginResp)
	}

	if regToken == "" {
		t.Fatal("auth happy path: no session token after register+login")
	}

	// 3. Use token to reach a protected endpoint (/api/open).
	resp := get(t, srv, "/api/open", map[string]string{
		"Authorization": "Bearer " + regToken,
	})
	defer mustClose(resp)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("happy path: GET /api/open with valid session: want 200, got %d", resp.StatusCode)
	}
}

// testSetupJoinPublic: POST /api/setup/join is in publicPaths; an
// unauthenticated request must not be rejected by the middleware (i.e. not 401).
func testSetupJoinPublic(t *testing.T) {
	srv := buildServer(t)

	// Send a valid-looking body so the handler parses it.
	resp := postJSON(t, srv, "/api/setup/join", map[string]string{
		"bucket":     "mybucket",
		"region":     "us-east-1",
		"passphrase": "test",
	}, nil)
	defer mustClose(resp)

	// Middleware must let it through (not 401). Handler may return 502 (no real S3),
	// 400 (bad request), etc. — anything other than 401.
	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("POST /api/setup/join: middleware should not block (got 401) — it's in publicPaths")
	}
}

// testSetupJoinMalformed: malformed body to POST /api/setup/join must be
// handled cleanly — no 500, no panic.
func testSetupJoinMalformed(t *testing.T) {
	srv := buildServer(t)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/setup/join",
		strings.NewReader("this is not json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/setup/join malformed: %v", err)
	}
	defer mustClose(resp)

	if resp.StatusCode == http.StatusInternalServerError {
		t.Errorf("POST /api/setup/join malformed input returned 500 — handler must reject cleanly")
	}
	// 400 is the expected outcome.
	if resp.StatusCode != http.StatusBadRequest {
		t.Logf("POST /api/setup/join malformed: got %d (expected 400)", resp.StatusCode)
	}
}

// testSetupJoinStatusPublic: GET /api/setup/join/status is in publicPaths.
// Unauthenticated access must return 200.
func testSetupJoinStatusPublic(t *testing.T) {
	srv := buildServer(t)

	resp := get(t, srv, "/api/setup/join/status", nil)
	defer mustClose(resp)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/setup/join/status unauthenticated: want 200, got %d", resp.StatusCode)
	}
}
