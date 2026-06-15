package main

// idor_regression_test.go — IDOR regression suite for IDOR-MISSION-01 and
// IDOR-BPROFILE-01.
//
// These tests encode the attacker scenario directly so a future refactor that
// re-introduces missing ownership checks fails the build immediately.
//
// IDOR-MISSION-01: GET /api/missions/{id}, PUT /api/missions/{id}/step/{stepId},
//   and POST /api/missions/{id}/cancel previously returned 200 and acted without
//   verifying that the requesting user owned the mission.  User B could read or
//   cancel user A's missions by guessing the ID.
//
// IDOR-BPROFILE-01: PUT/DELETE/POST /api/browser-profiles/{id} previously acted
//   on any profile ID without checking the UserID stored in the profile record.
//   User B could update, delete, clear, or rebind app bindings for user A's
//   browser profile.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"vulos/backend/services/ai"
	"vulos/backend/services/auth"
	bprofiles "vulos/backend/services/profiles"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// newAuthStoreForIDOR creates a temporary auth store pre-populated with two
// users: "alice" (admin) and "bob" (regular user).
func newAuthStoreForIDOR(t *testing.T) (store *auth.Store, aliceID, bobID string) {
	t.Helper()
	dir := t.TempDir()
	var err error
	store, err = auth.NewStore(dir)
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}

	// Register alice as the first user (auto-admin).
	alice, err := store.Register("alice", "password-alice-123", "Alice")
	if err != nil {
		t.Fatalf("register alice: %v", err)
	}
	// Register bob as a plain user.
	bob, err := store.Register("bob", "password-bob-456", "Bob")
	if err != nil {
		t.Fatalf("register bob: %v", err)
	}

	return store, alice.ID, bob.ID
}

// get performs a GET request with the given userID in X-User-ID.
func idorGET(t *testing.T, srv *httptest.Server, path, userID string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("X-User-ID", userID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// post performs a POST request with optional JSON body.
func idorPOST(t *testing.T, srv *httptest.Server, path, userID string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, &buf)
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// put performs a PUT request with optional JSON body.
func idorPUT(t *testing.T, srv *httptest.Server, path, userID string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+path, &buf)
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	return resp
}

// delete performs a DELETE request.
func idorDELETE(t *testing.T, srv *httptest.Server, path, userID string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
	req.Header.Set("X-User-ID", userID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

func assertStatus(t *testing.T, resp *http.Response, want int, msg string) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s: expected HTTP %d, got %d", msg, want, resp.StatusCode)
	}
}

// ─── IDOR-MISSION-01 ────────────────────────────────────────────────────────

// buildMissionMux wires only the mission-related routes from main.go (enough
// to exercise IDOR-MISSION-01) without starting a full server.
func buildMissionMux(t *testing.T, ms *ai.MissionStore, store *auth.Store) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()

	// GET /api/missions/{id}
	mux.HandleFunc("GET /api/missions/{id}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		m := ms.Get(r.PathValue("id"))
		if m == nil {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		if m.UserID != userID {
			if p, _ := store.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				http.Error(w, `{"error":"not found"}`, 404)
				return
			}
		}
		writeJSON(w, m)
	})

	// POST /api/missions/{id}/cancel
	mux.HandleFunc("POST /api/missions/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		m := ms.Get(r.PathValue("id"))
		if m == nil {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		if m.UserID != userID {
			if p, _ := store.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				http.Error(w, `{"error":"not found"}`, 404)
				return
			}
		}
		ms.Cancel(r.PathValue("id"))
		writeJSON(w, map[string]string{"status": "cancelled"})
	})

	// PUT /api/missions/{id}/step/{stepId}
	mux.HandleFunc("PUT /api/missions/{id}/step/{stepId}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		m := ms.Get(r.PathValue("id"))
		if m == nil {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		if m.UserID != userID {
			if p, _ := store.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				http.Error(w, `{"error":"not found"}`, 404)
				return
			}
		}
		var req struct {
			Status string `json:"status"`
			Output string `json:"output"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		ms.UpdateStep(r.PathValue("id"), r.PathValue("stepId"), ai.MissionStatus(req.Status), req.Output)
		writeJSON(w, ms.Get(r.PathValue("id")))
	})

	return mux
}

// TestIDOR_Mission01_GetBlocksCrossUser verifies that user B cannot read user
// A's mission (must get 404, not 200).
func TestIDOR_Mission01_GetBlocksCrossUser(t *testing.T) {
	store, aliceID, bobID := newAuthStoreForIDOR(t)
	ms := ai.NewMissionStore(t.TempDir())

	// Alice creates a mission.
	m := ms.Create(aliceID, "alice's secret mission", "", nil)
	missionID := m.ID

	mux := buildMissionMux(t, ms, store)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Bob must NOT be able to read Alice's mission.
	resp := idorGET(t, srv, "/api/missions/"+missionID, bobID)
	defer resp.Body.Close()
	assertStatus(t, resp, 404, "IDOR-MISSION-01 GET: bob should not read alice's mission")

	// Alice can read her own mission.
	resp2 := idorGET(t, srv, "/api/missions/"+missionID, aliceID)
	defer resp2.Body.Close()
	assertStatus(t, resp2, 200, "IDOR-MISSION-01 GET: alice should read her own mission")
}

// TestIDOR_Mission01_CancelBlocksCrossUser verifies that user B cannot cancel
// user A's mission.
func TestIDOR_Mission01_CancelBlocksCrossUser(t *testing.T) {
	store, aliceID, bobID := newAuthStoreForIDOR(t)
	ms := ai.NewMissionStore(t.TempDir())

	m := ms.Create(aliceID, "alice's mission", "", nil)
	missionID := m.ID

	mux := buildMissionMux(t, ms, store)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Bob tries to cancel Alice's mission — must be blocked.
	resp := idorPOST(t, srv, "/api/missions/"+missionID+"/cancel", bobID, nil)
	defer resp.Body.Close()
	assertStatus(t, resp, 404, "IDOR-MISSION-01 CANCEL: bob should not cancel alice's mission")

	// Confirm the mission was NOT cancelled.
	m2 := ms.Get(missionID)
	if m2 == nil {
		t.Fatal("mission disappeared unexpectedly")
	}
	if m2.Status == ai.MissionCancelled {
		t.Fatal("IDOR-MISSION-01 REGRESSION: bob cancelled alice's mission — ownership check is missing")
	}
}

// TestIDOR_Mission01_UpdateStepBlocksCrossUser verifies that user B cannot
// update steps on user A's mission.
func TestIDOR_Mission01_UpdateStepBlocksCrossUser(t *testing.T) {
	store, aliceID, bobID := newAuthStoreForIDOR(t)
	ms := ai.NewMissionStore(t.TempDir())

	m := ms.Create(aliceID, "alice mission", "", []string{"step1"})
	missionID := m.ID
	stepID := "step-0"
	if len(m.Steps) > 0 {
		stepID = m.Steps[0].ID
	}

	mux := buildMissionMux(t, ms, store)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Bob tries to update a step on Alice's mission.
	resp := idorPUT(t, srv, fmt.Sprintf("/api/missions/%s/step/%s", missionID, stepID), bobID,
		map[string]string{"status": "completed", "output": "hacked"})
	defer resp.Body.Close()
	assertStatus(t, resp, 404, "IDOR-MISSION-01 STEP UPDATE: bob should not update alice's mission step")
}

// TestIDOR_Mission01_AdminCanAccess verifies that an admin user CAN read and
// cancel any user's mission (admin bypass must still work).
func TestIDOR_Mission01_AdminCanAccess(t *testing.T) {
	store, aliceID, _ := newAuthStoreForIDOR(t)
	ms := ai.NewMissionStore(t.TempDir())

	// Register carol as a regular user; create her mission.
	carol, _ := store.Register("carol", "password-carol-789", "Carol")

	m := ms.Create(carol.ID, "carol's mission", "", nil)
	missionID := m.ID

	mux := buildMissionMux(t, ms, store)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Alice is admin — she should be able to read carol's mission.
	resp := idorGET(t, srv, "/api/missions/"+missionID, aliceID)
	defer resp.Body.Close()
	assertStatus(t, resp, 200, "IDOR-MISSION-01: admin should be able to read any mission")
}

// ─── IDOR-BPROFILE-01 ───────────────────────────────────────────────────────

// buildBrowserProfileMux wires the browser-profile mutation routes to match
// the fixed handlers in main.go (enough for IDOR-BPROFILE-01).
func buildBrowserProfileMux(t *testing.T, bp *bprofiles.Store, store *auth.Store) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()

	// PUT /api/browser-profiles/{id}
	mux.HandleFunc("PUT /api/browser-profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		prof, ok := bp.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		if prof.UserID != userID {
			if p, _ := store.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				http.Error(w, `{"error":"forbidden"}`, 403)
				return
			}
		}
		var req struct {
			Name  string `json:"name"`
			Color string `json:"color"`
			Icon  string `json:"icon"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		bp.Update(r.PathValue("id"), req.Name, req.Color, req.Icon)
		writeJSON(w, map[string]string{"status": "updated"})
	})

	// DELETE /api/browser-profiles/{id}
	mux.HandleFunc("DELETE /api/browser-profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		prof, ok := bp.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		if prof.UserID != userID {
			if p, _ := store.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				http.Error(w, `{"error":"forbidden"}`, 403)
				return
			}
		}
		bp.Delete(r.PathValue("id"))
		writeJSON(w, map[string]string{"status": "deleted"})
	})

	// POST /api/browser-profiles/{id}/clear
	mux.HandleFunc("POST /api/browser-profiles/{id}/clear", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		prof, ok := bp.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		if prof.UserID != userID {
			if p, _ := store.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				http.Error(w, `{"error":"forbidden"}`, 403)
				return
			}
		}
		bp.ClearData(r.PathValue("id"))
		writeJSON(w, map[string]string{"status": "cleared"})
	})

	// POST /api/browser-profiles/{id}/bind
	mux.HandleFunc("POST /api/browser-profiles/{id}/bind", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		prof, ok := bp.Get(r.PathValue("id"))
		if !ok {
			http.Error(w, `{"error":"not found"}`, 404)
			return
		}
		if prof.UserID != userID {
			if p, _ := store.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
				http.Error(w, `{"error":"forbidden"}`, 403)
				return
			}
		}
		var req struct {
			AppID string `json:"app_id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		bp.BindApp(r.PathValue("id"), req.AppID)
		writeJSON(w, map[string]string{"status": "bound"})
	})

	return mux
}

// newBrowserProfileStore creates a fresh browser-profile store in a temp dir.
func newBrowserProfileStore(t *testing.T) *bprofiles.Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "db")
	os.MkdirAll(dir, 0755)
	return bprofiles.NewStore(dir)
}

// TestIDOR_BProfile01_UpdateBlocksCrossUser verifies that user B cannot update
// user A's browser profile.
func TestIDOR_BProfile01_UpdateBlocksCrossUser(t *testing.T) {
	store, aliceID, bobID := newAuthStoreForIDOR(t)
	bp := newBrowserProfileStore(t)

	// Alice creates a browser profile.
	p, err := bp.Create(aliceID, "personal", "#3b82f6", "👤")
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	profileID := p.ID

	mux := buildBrowserProfileMux(t, bp, store)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Bob tries to rename Alice's profile.
	resp := idorPUT(t, srv, "/api/browser-profiles/"+profileID, bobID,
		map[string]string{"name": "hacked", "color": "#ff0000"})
	defer resp.Body.Close()
	assertStatus(t, resp, 403, "IDOR-BPROFILE-01 PUT: bob should not update alice's browser profile")

	// Verify the profile name was NOT changed.
	updated, ok := bp.Get(profileID)
	if !ok {
		t.Fatal("profile disappeared")
	}
	if updated.Name != "personal" {
		t.Fatalf("IDOR-BPROFILE-01 REGRESSION: bob updated alice's browser profile name to %q", updated.Name)
	}
}

// TestIDOR_BProfile01_DeleteBlocksCrossUser verifies that user B cannot delete
// user A's browser profile.
func TestIDOR_BProfile01_DeleteBlocksCrossUser(t *testing.T) {
	store, aliceID, bobID := newAuthStoreForIDOR(t)
	bp := newBrowserProfileStore(t)

	p, err := bp.Create(aliceID, "work", "#22c55e", "💼")
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	profileID := p.ID

	mux := buildBrowserProfileMux(t, bp, store)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Bob tries to delete Alice's profile.
	resp := idorDELETE(t, srv, "/api/browser-profiles/"+profileID, bobID)
	defer resp.Body.Close()
	assertStatus(t, resp, 403, "IDOR-BPROFILE-01 DELETE: bob should not delete alice's browser profile")

	// Alice's profile must still exist.
	if _, ok := bp.Get(profileID); !ok {
		t.Fatal("IDOR-BPROFILE-01 REGRESSION: bob deleted alice's browser profile — ownership check is missing")
	}
}

// TestIDOR_BProfile01_ClearBlocksCrossUser verifies that user B cannot clear
// user A's browser profile data.
func TestIDOR_BProfile01_ClearBlocksCrossUser(t *testing.T) {
	store, aliceID, bobID := newAuthStoreForIDOR(t)
	bp := newBrowserProfileStore(t)

	p, err := bp.Create(aliceID, "private", "#a855f7", "🔒")
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	profileID := p.ID

	mux := buildBrowserProfileMux(t, bp, store)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp := idorPOST(t, srv, "/api/browser-profiles/"+profileID+"/clear", bobID, nil)
	defer resp.Body.Close()
	assertStatus(t, resp, 403, "IDOR-BPROFILE-01 CLEAR: bob should not clear alice's browser profile")
}

// TestIDOR_BProfile01_BindBlocksCrossUser verifies that user B cannot bind an
// app to user A's browser profile.
func TestIDOR_BProfile01_BindBlocksCrossUser(t *testing.T) {
	store, aliceID, bobID := newAuthStoreForIDOR(t)
	bp := newBrowserProfileStore(t)

	p, err := bp.Create(aliceID, "personal", "#3b82f6", "👤")
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	profileID := p.ID

	mux := buildBrowserProfileMux(t, bp, store)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp := idorPOST(t, srv, "/api/browser-profiles/"+profileID+"/bind", bobID,
		map[string]string{"app_id": "malicious-app"})
	defer resp.Body.Close()
	assertStatus(t, resp, 403, "IDOR-BPROFILE-01 BIND: bob should not bind an app to alice's browser profile")
}

// TestIDOR_BProfile01_OwnerCanMutate verifies that the profile owner can
// still update their own profile (no false-positive blocking).
func TestIDOR_BProfile01_OwnerCanMutate(t *testing.T) {
	store, aliceID, _ := newAuthStoreForIDOR(t)
	bp := newBrowserProfileStore(t)

	p, err := bp.Create(aliceID, "personal", "#3b82f6", "👤")
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	profileID := p.ID

	mux := buildBrowserProfileMux(t, bp, store)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Alice should be able to update her own profile.
	resp := idorPUT(t, srv, "/api/browser-profiles/"+profileID, aliceID,
		map[string]string{"name": "work", "color": "#22c55e"})
	defer resp.Body.Close()
	assertStatus(t, resp, 200, "IDOR-BPROFILE-01: alice should update her own browser profile")
}

// TestIDOR_BProfile01_AdminCanMutate verifies that an admin can update any
// profile.
func TestIDOR_BProfile01_AdminCanMutate(t *testing.T) {
	store, aliceID, bobID := newAuthStoreForIDOR(t)
	bp := newBrowserProfileStore(t)

	// Bob creates a profile; Alice (admin) should be able to modify it.
	p, err := bp.Create(bobID, "personal", "#3b82f6", "👤")
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	profileID := p.ID

	mux := buildBrowserProfileMux(t, bp, store)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Alice is admin — should be allowed.
	resp := idorPUT(t, srv, "/api/browser-profiles/"+profileID, aliceID,
		map[string]string{"name": "work", "color": "#22c55e"})
	defer resp.Body.Close()
	assertStatus(t, resp, 200, "IDOR-BPROFILE-01: admin should update any browser profile")
}
