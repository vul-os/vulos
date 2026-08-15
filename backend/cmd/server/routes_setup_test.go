package main

// routes_setup_test.go — first-boot completion.
//
// The defect these tests exist for: GET /api/setup/status stats a marker file
// that NOTHING in the product wrote. The wizard touched it through POST
// /api/exec, which is admin-gated and kill-switchable (VULOS_DISABLE_EXEC →
// 503), so on a box with exec disabled the marker was never written, the wizard
// ran again on the next boot, and the account step then failed on a duplicate
// username. The box was stuck in a wizard it could not leave.
//
// These tests drive registerSetupRoutes — the SAME function main() calls — so a
// regression in the shipped route fails here rather than passing against a copy.
// They assert three separable things, because they can regress separately:
//
//	1. the writer and the reader agree about which file means "set up",
//	2. only the box owner can end setup, and NOBODY can on an account-less box,
//	3. the exec kill switch does not disable it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"vulos/backend/services/auth"
)

// newSetupTestMux returns the real registered routes, an admin id and a
// non-admin id, with setupMarkerPath pointed at a temp dir for the test.
//
// The marker path is redirected rather than mocked away: these tests must prove
// that the POST handler's write is visible to the GET handler's stat, and a fake
// filesystem in between would prove only that the fake agrees with itself.
func newSetupTestMux(t *testing.T) (*http.ServeMux, string, string) {
	t.Helper()
	// FindOrCreateUser never grants admin on its own (privilege-escalation guard
	// in services/auth); VULOS_BOOTSTRAP_ADMIN_EMAIL is the documented way.
	t.Setenv("VULOS_BOOTSTRAP_ADMIN_EMAIL", "owner@example.com")

	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	owner := store.FindOrCreateUser("test", "setup-owner", "owner@example.com", "Owner", "", true)
	guest := store.FindOrCreateUser("test", "setup-guest", "guest@example.com", "Guest", "", true)
	if owner == nil || guest == nil {
		t.Fatal("could not create test profiles")
	}
	if p, _ := store.GetProfile(owner.ID); p == nil || p.Role != auth.RoleAdmin {
		t.Fatalf("first user is not admin — test premise broken (role=%v)", p)
	}
	if p, _ := store.GetProfile(guest.ID); p == nil || p.Role == auth.RoleAdmin {
		t.Fatal("second user is admin — test cannot distinguish the gate")
	}

	setupMarkerAt(t, filepath.Join(t.TempDir(), "vulos", ".setup-complete"))

	mux := http.NewServeMux()
	registerSetupRoutes(mux, store)
	return mux, owner.ID, guest.ID
}

// setupMarkerAt redirects the package-level marker path for one test and
// restores it afterwards.
func setupMarkerAt(t *testing.T, path string) {
	t.Helper()
	prev := setupMarkerPath
	setupMarkerPath = path
	t.Cleanup(func() { setupMarkerPath = prev })
}

func setupReq(mux *http.ServeMux, method, path, userID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// setupStatus reads the boolean the shell reads.
func setupStatus(t *testing.T, mux *http.ServeMux) bool {
	t.Helper()
	rec := setupReq(mux, "GET", "/api/setup/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/setup/status: %d, want 200", rec.Code)
	}
	var out map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out["setup_complete"]
}

// TestSetupComplete_OwnerMarksItAndStatusAgrees is the whole point: the wizard
// posts once, and the probe the shell makes on the NEXT boot answers true.
//
// It asserts through the status route rather than by stat-ing the file, because
// the bug was two halves of the product disagreeing about the mechanism — a test
// that checked the file directly would have passed against the broken build too.
func TestSetupComplete_OwnerMarksItAndStatusAgrees(t *testing.T) {
	mux, ownerID, _ := newSetupTestMux(t)

	if setupStatus(t, mux) {
		t.Fatal("a box with no marker reported setup_complete=true — test premise broken")
	}

	rec := setupReq(mux, "POST", "/api/setup/complete", ownerID)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner POST /api/setup/complete: %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if !setupStatus(t, mux) {
		t.Fatal("setup was marked complete and the status route still says false — " +
			"the writer and the reader disagree about the marker, which is the " +
			"original defect in a new place")
	}
	if _, err := os.Stat(setupMarkerPath); err != nil {
		t.Fatalf("marker not on disk at %s: %v", setupMarkerPath, err)
	}
}

// TestSetupComplete_SurvivesExecKillSwitch. The route must not inherit the trap
// it was built to remove: an operator who disables the general-purpose exec
// endpoint must still be able to finish setting up their box.
func TestSetupComplete_SurvivesExecKillSwitch(t *testing.T) {
	t.Setenv("VULOS_DISABLE_EXEC", "1")
	mux, ownerID, _ := newSetupTestMux(t)

	rec := setupReq(mux, "POST", "/api/setup/complete", ownerID)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner POST with VULOS_DISABLE_EXEC set: %d (%s), want 200 — "+
			"completion must not be kill-switchable, or a hardened box is a box "+
			"that can never leave the wizard", rec.Code, rec.Body.String())
	}
	if !setupStatus(t, mux) {
		t.Fatal("VULOS_DISABLE_EXEC=1 and the marker was not written")
	}
}

// TestSetupComplete_RequiresTheOwner. Two separate refusals, and after each one
// the marker must still be absent — a 403 that wrote the file anyway would be a
// green test over a live bypass.
func TestSetupComplete_RequiresTheOwner(t *testing.T) {
	mux, ownerID, guestID := newSetupTestMux(t)

	if rec := setupReq(mux, "POST", "/api/setup/complete", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated POST: %d, want 401", rec.Code)
	}
	if setupStatus(t, mux) {
		t.Fatal("an unauthenticated caller was refused and setup was marked complete anyway")
	}

	if rec := setupReq(mux, "POST", "/api/setup/complete", guestID); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin POST: %d, want 403", rec.Code)
	}
	if setupStatus(t, mux) {
		t.Fatal("a non-admin was refused and setup was marked complete anyway")
	}

	// The gate must not have broken the feature.
	if rec := setupReq(mux, "POST", "/api/setup/complete", ownerID); rec.Code != http.StatusOK {
		t.Fatalf("owner POST: %d (%s), want 200", rec.Code, rec.Body.String())
	}
}

// TestSetupComplete_UnownedBoxCannotBeCompleted is the security property the
// admin check buys, stated on its own.
//
// A box that has just booted for the first time has NO accounts. Anyone who can
// reach it on the network can therefore present no valid session at all — and
// must not be able to end setup, which would leave the owner staring at a
// sign-in screen for an account nobody ever created.
func TestSetupComplete_UnownedBoxCannotBeCompleted(t *testing.T) {
	t.Setenv("VULOS_BOOTSTRAP_ADMIN_EMAIL", "")
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	if store.HasAnyUsers() {
		t.Fatal("a fresh store already has users — test premise broken")
	}
	setupMarkerAt(t, filepath.Join(t.TempDir(), "vulos", ".setup-complete"))
	mux := http.NewServeMux()
	registerSetupRoutes(mux, store)

	// No session, and an invented user id — the two shapes a stranger has.
	for _, id := range []string{"", "u-does-not-exist"} {
		rec := setupReq(mux, "POST", "/api/setup/complete", id)
		if rec.Code == http.StatusOK {
			t.Errorf("POST /api/setup/complete with X-User-ID=%q on an account-less box: 200 — "+
				"a stranger just skipped this box's setup", id)
		}
	}
	if setupStatus(t, mux) {
		t.Fatal("an account-less box reports setup_complete=true after refused calls")
	}
}

// TestSetupComplete_IsIdempotent. A retry must not become an error the user has
// to act on, and must not rewrite the record of when the box was set up.
func TestSetupComplete_IsIdempotent(t *testing.T) {
	mux, ownerID, _ := newSetupTestMux(t)

	if rec := setupReq(mux, "POST", "/api/setup/complete", ownerID); rec.Code != http.StatusOK {
		t.Fatalf("first POST: %d, want 200", rec.Code)
	}
	first, err := os.ReadFile(setupMarkerPath)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}

	rec := setupReq(mux, "POST", "/api/setup/complete", ownerID)
	if rec.Code != http.StatusOK {
		t.Fatalf("second POST: %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if out["setup_complete"] != true || out["already_complete"] != true {
		t.Errorf("second POST body = %v, want setup_complete + already_complete true", out)
	}
	second, err := os.ReadFile(setupMarkerPath)
	if err != nil {
		t.Fatalf("re-read marker: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("a repeat completion rewrote the marker:\n  before %q\n  after  %q", first, second)
	}
}

// TestSetupComplete_IsNotPublic. The session gate on this route lives in the
// auth middleware's allow-list, in another package. Adding a path there is a
// one-line edit that removes authentication from an endpoint, so the fact that
// THIS path is absent from it is asserted here, next to the reason.
func TestSetupComplete_IsNotPublic(t *testing.T) {
	if auth.PublicPaths()["/api/setup/complete"] {
		t.Fatal("/api/setup/complete is in auth.publicPaths — any unauthenticated " +
			"caller on the network can now end this box's setup before its owner does")
	}
	if !auth.PublicPaths()["/api/setup/status"] {
		t.Fatal("/api/setup/status left auth.publicPaths — the shell asks it before " +
			"anyone is signed in, so the wizard can no longer be reached")
	}
}
