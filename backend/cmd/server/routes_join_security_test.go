package main

// routes_join_security_test.go — wave-52 SECURITY coverage for the two
// UNAUTHENTICATED setup-time registrars:
//
//   registerJoinRoutes      (routes_join.go)      — POST /api/setup/join,
//                                                    GET  /api/setup/join/status
//   registerJoinCodeRoutes  (routes_joincode.go)  — POST /api/cluster/join-code (admin),
//                                                    POST /api/setup/join-code (public)
//
// These endpoints are public (added to auth.publicPaths) and perform network +
// crypto work, so they carry per-IP rate-limiters. The tests here drive the
// real registrars over httptest and assert the SECURITY-bearing branches:
//
//   * rate-limit: a burst of bad requests from one IP eventually returns 429
//     with Retry-After, and the limiter is keyed PER-IP (a fresh IP is not
//     limited by another IP's burst).
//   * join-code validation: an unknown short-code → 404, an expired one → 410,
//     and a one-time code is consumed (second decode → 404).
//   * forged identity: the admin-only POST /api/cluster/join-code trusts ONLY
//     the session-derived X-User-ID; a caller who forges a random/unknown
//     X-User-ID (i.e. one with no admin profile) is refused 403 — the header
//     alone never confers admin.
//   * provisioned gate: once the instance is "normal" (provisioned), the
//     public POST /api/setup/join is 409 and status polling is 403.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/auth"
	"vulos/backend/services/joincode"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// newJoinAuthStore builds an auth store with a single admin (the first
// registered user is auto-admin) and returns the store + the admin user ID.
func newJoinAuthStore(t *testing.T) (*auth.Store, string) {
	t.Helper()
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	admin, err := store.Register("admin", "password-admin-123", "Admin")
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}
	return store, admin.ID
}

// postFrom performs a POST with an explicit source IP (RemoteAddr) so the
// per-IP rate-limiter can be exercised deterministically. httptest.Server sets
// RemoteAddr from the real loopback connection, so to control the IP we call the
// handler directly through the mux with a synthesized *http.Request.
func jsReq(mux *http.ServeMux, method, target, remoteIP, userID, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if remoteIP != "" {
		r.RemoteAddr = remoteIP + ":40000"
	}
	if userID != "" {
		r.Header.Set("X-User-ID", userID)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// ── join rate-limiter (POST /api/setup/join) ────────────────────────────────

// TestJoin_RateLimiter_429UnderBurst verifies that a burst of invalid-body
// POSTs from one IP eventually returns 429 with a Retry-After header. The
// limiter is 5 records/min, so the 6th recorded request must be limited.
func TestJoin_RateLimiter_429UnderBurst(t *testing.T) {
	home := t.TempDir() // fresh, unprovisioned instance (no .vulos/db)
	mux := http.NewServeMux()
	registerJoinRoutes(mux, home)

	const ip = "203.0.113.7"
	var got429 bool
	// Each bad-body POST records the IP (rl.record) on the 400 path. After 5
	// records, limited() returns true and the next request is 429.
	for i := 0; i < 12; i++ {
		w := jsReq(mux, http.MethodPost, "/api/setup/join", ip, "", "not-json")
		if w.Code == http.StatusTooManyRequests {
			got429 = true
			if w.Header().Get("Retry-After") == "" {
				t.Fatalf("429 response missing Retry-After header")
			}
			break
		}
	}
	if !got429 {
		t.Fatalf("join rate-limiter never returned 429 under a 12-request burst — abuse throttle is broken")
	}
}

// TestJoin_RateLimiter_PerIP verifies the limiter is keyed per-IP: a second IP
// is NOT throttled by the first IP's burst.
func TestJoin_RateLimiter_PerIP(t *testing.T) {
	home := t.TempDir()
	mux := http.NewServeMux()
	registerJoinRoutes(mux, home)

	// Burn IP #1 into the limited state.
	for i := 0; i < 12; i++ {
		jsReq(mux, http.MethodPost, "/api/setup/join", "198.51.100.1", "", "not-json")
	}

	// A different IP's first request must NOT be 429.
	w := jsReq(mux, http.MethodPost, "/api/setup/join", "198.51.100.2", "", "not-json")
	if w.Code == http.StatusTooManyRequests {
		t.Fatalf("per-IP rate-limiter leaked: fresh IP got 429 because a DIFFERENT IP was throttled")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("fresh IP bad body: expected 400, got %d", w.Code)
	}
}

// TestJoin_ProvisionedGate blocks the public join endpoints once the instance
// is fully provisioned (bootmode "normal"): POST → 409, status GET → 403.
func TestJoin_ProvisionedGate(t *testing.T) {
	home := t.TempDir()
	// Make bootmode.Detect report "normal": db dir + instance.json present,
	// no active sync-state.
	dbDir := filepath.Join(home, "db")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "instance.json"), []byte(`{"id":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerJoinRoutes(mux, home)

	// POST must be 409 (setup already complete) — and must NOT touch rate-limit.
	wp := jsReq(mux, http.MethodPost, "/api/setup/join", "203.0.113.9", "",
		`{"bucket":"b","access":"a","secret":"s","passphrase":"p"}`)
	if wp.Code != http.StatusConflict {
		t.Fatalf("provisioned POST /api/setup/join: expected 409, got %d", wp.Code)
	}

	// Status polling must be 403 (avoid leaking sync-state to the public).
	ws := jsReq(mux, http.MethodGet, "/api/setup/join/status", "203.0.113.9", "", "")
	if ws.Code != http.StatusForbidden {
		t.Fatalf("provisioned GET /api/setup/join/status: expected 403, got %d", ws.Code)
	}
}

// TestJoin_StatusUnprovisioned returns idle progress (200) before provisioning.
func TestJoin_StatusUnprovisioned(t *testing.T) {
	home := t.TempDir()
	mux := http.NewServeMux()
	registerJoinRoutes(mux, home)

	w := jsReq(mux, http.MethodGet, "/api/setup/join/status", "203.0.113.9", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("unprovisioned status: expected 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("status body not JSON: %v", err)
	}
	if body["status"] != "idle" {
		t.Fatalf("expected idle status, got %v", body["status"])
	}
}

// ── join-code registrar (routes_joincode.go) ────────────────────────────────

// TestJoinCode_AdminGate_ForgedUserIDRejected proves the admin-only issue
// endpoint trusts ONLY a session-derived X-User-ID that maps to an admin
// profile. A forged/unknown X-User-ID confers nothing → 403. A real admin → 200.
func TestJoinCode_AdminGate_ForgedUserIDRejected(t *testing.T) {
	store, adminID := newJoinAuthStore(t)
	home := t.TempDir()
	// storage.json is optional for Issue (graceful absence), so no seed needed.

	mux := http.NewServeMux()
	registerJoinCodeRoutes(mux, home, store)

	// Forged / unknown user id — no profile → not admin → 403.
	wForged := jsReq(mux, http.MethodPost, "/api/cluster/join-code", "203.0.113.5", "attacker-forged-id", "")
	if wForged.Code != http.StatusForbidden {
		t.Fatalf("forged X-User-ID on admin route: expected 403, got %d", wForged.Code)
	}

	// Empty user id — also 403 (GetProfile("") not ok).
	wEmpty := jsReq(mux, http.MethodPost, "/api/cluster/join-code", "203.0.113.5", "", "")
	if wEmpty.Code != http.StatusForbidden {
		t.Fatalf("empty X-User-ID on admin route: expected 403, got %d", wEmpty.Code)
	}

	// Real admin — 200 with a short_code.
	wAdmin := jsReq(mux, http.MethodPost, "/api/cluster/join-code", "203.0.113.5", adminID, "")
	if wAdmin.Code != http.StatusOK {
		t.Fatalf("admin issue join-code: expected 200, got %d (body %s)", wAdmin.Code, wAdmin.Body.String())
	}
	var issued map[string]any
	if err := json.Unmarshal(wAdmin.Body.Bytes(), &issued); err != nil {
		t.Fatalf("issue body not JSON: %v", err)
	}
	if _, ok := issued["short_code"].(string); !ok {
		t.Fatalf("issued join-code missing short_code: %v", issued)
	}
}

// TestJoinCode_Decode_BadCodeRejected covers the POST /api/setup/join-code
// validation branches: missing code → 400, unknown code → 404.
func TestJoinCode_Decode_BadCodeRejected(t *testing.T) {
	store, _ := newJoinAuthStore(t)
	home := t.TempDir()
	mux := http.NewServeMux()
	registerJoinCodeRoutes(mux, home, store)

	// Missing short_code → 400.
	wEmpty := jsReq(mux, http.MethodPost, "/api/setup/join-code", "203.0.113.6", "", `{"short_code":""}`)
	if wEmpty.Code != http.StatusBadRequest {
		t.Fatalf("empty short_code: expected 400, got %d", wEmpty.Code)
	}

	// Unknown code → 404 (not found / already used).
	wUnknown := jsReq(mux, http.MethodPost, "/api/setup/join-code", "203.0.113.6", "",
		`{"short_code":"VULOS-ZZZZ-ZZZZ-ZZZZ"}`)
	if wUnknown.Code != http.StatusNotFound {
		t.Fatalf("unknown short_code: expected 404, got %d", wUnknown.Code)
	}
}

// TestJoinCode_Decode_ExpiredAndOneTimeUse seeds a real joincode DB via
// joincode.Issue, then exercises the HTTP decode path for:
//   - a valid code → 200 and one-time consumption (second decode → 404)
//   - an expired code → 410
func TestJoinCode_Decode_ExpiredAndOneTimeUse(t *testing.T) {
	store, _ := newJoinAuthStore(t)
	home := t.TempDir()
	mux := http.NewServeMux()
	registerJoinCodeRoutes(mux, home, store)

	// Issue a valid code (1h TTL) directly through the service.
	_, code, err := joincode.Issue(home, time.Hour)
	if err != nil {
		t.Fatalf("joincode.Issue: %v", err)
	}

	// First decode over HTTP → 200.
	w1 := jsReq(mux, http.MethodPost, "/api/setup/join-code", "203.0.113.8", "",
		`{"short_code":"`+code+`"}`)
	if w1.Code != http.StatusOK {
		t.Fatalf("valid join-code decode: expected 200, got %d (%s)", w1.Code, w1.Body.String())
	}

	// One-time use: a second decode of the SAME code must be 404.
	w2 := jsReq(mux, http.MethodPost, "/api/setup/join-code", "203.0.113.8", "",
		`{"short_code":"`+code+`"}`)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("reused join-code: expected 404 (one-time use), got %d", w2.Code)
	}

	// Issue a code that is already expired (negative TTL) → decode must be 410.
	_, expiredCode, err := joincode.Issue(home, -time.Minute)
	if err != nil {
		t.Fatalf("joincode.Issue(expired): %v", err)
	}
	w3 := jsReq(mux, http.MethodPost, "/api/setup/join-code", "203.0.113.8", "",
		`{"short_code":"`+expiredCode+`"}`)
	if w3.Code != http.StatusGone {
		t.Fatalf("expired join-code decode: expected 410, got %d", w3.Code)
	}
}

// TestJoinCode_RateLimiter_429UnderBurst drives the public decode endpoint's
// per-IP limiter: bad-code requests record the IP, and the 6th+ is 429.
func TestJoinCode_RateLimiter_429UnderBurst(t *testing.T) {
	store, _ := newJoinAuthStore(t)
	home := t.TempDir()
	mux := http.NewServeMux()
	registerJoinCodeRoutes(mux, home, store)

	const ip = "203.0.113.20"
	var got429 bool
	for i := 0; i < 12; i++ {
		w := jsReq(mux, http.MethodPost, "/api/setup/join-code", ip, "",
			`{"short_code":"VULOS-AAAA-BBBB-CCCC"}`)
		if w.Code == http.StatusTooManyRequests {
			got429 = true
			if w.Header().Get("Retry-After") == "" {
				t.Fatalf("join-code 429 missing Retry-After header")
			}
			break
		}
	}
	if !got429 {
		t.Fatalf("join-code rate-limiter never returned 429 under a 12-request burst")
	}
}
