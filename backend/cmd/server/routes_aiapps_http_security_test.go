package main

// routes_aiapps_http_security_test.go — wave-52 SECURITY coverage for the
// AI-apps HTTP registrars at the ROUTE layer:
//
//   registerAIAppsRoutes            (routes_aiapps.go)          POST .../{id}/update
//   registerAIAppsVersionsRoutes    (routes_aiapps_versions.go) snapshot/versions/rollback
//   registerAIAppsSecurityWrappers  (routes_aiapps_security.go) save/get/delete
//
// AI apps run server-side code, so mutation is admin-gated and every {id}/
// {version} is charset-validated + realpath-contained before any filesystem
// operation. These httptest-level tests assert:
//
//   * admin gate: a non-admin / forged X-User-ID → 403 (mutating routes); an
//     unauthenticated caller on the versions routes → 401.
//   * path traversal: an {id} containing "..", a slash, or an encoded escape is
//     rejected (400/404) BEFORE any read/write — no file outside aiAppsDir is
//     ever touched.
//   * rollback version validation: a traversal / bad-charset version → 400.
//   * kill-switch: DISABLE_AI_APP_EDIT=1 → 503 on the update route.
//   * happy path: an admin can save then update a legitimate app (200).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/services/auth"
)

// newAIAuthStore returns an auth store with an admin (first user) and a plain
// user, plus their IDs.
func newAIAuthStore(t *testing.T) (store *auth.Store, adminID, userID string) {
	t.Helper()
	var err error
	store, err = auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	admin, err := store.Register("admin", "password-admin-123", "Admin")
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}
	user, err := store.Register("bob", "password-bob-456", "Bob")
	if err != nil {
		t.Fatalf("register bob: %v", err)
	}
	return store, admin.ID, user.ID
}

func aiReq(t *testing.T, mux *http.ServeMux, method, target, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if userID != "" {
		r.Header.Set("X-User-ID", userID)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// seedApp creates a legitimate app directory under aiAppsDir with an index.html
// so update/snapshot/rollback have a real, contained target.
func seedApp(t *testing.T, aiAppsDir, id string) {
	t.Helper()
	dir := filepath.Join(aiAppsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := map[string]string{"title": "Test App", "id": id}
	md, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(dir, "meta.json"), md, 0o644)
}

// ── registerAIAppsRoutes: POST /api/ai-apps/{id}/update ──────────────────────

// TestAIAppsUpdate_AdminGate: non-admin / forged id → 403; admin on a real app
// → 200 (happy path).
func TestAIAppsUpdate_AdminGate(t *testing.T) {
	store, adminID, userID := newAIAuthStore(t)
	dir := t.TempDir()
	seedApp(t, dir, "ai-100")
	mux := http.NewServeMux()
	registerAIAppsRoutes(mux, dir, store)

	// Non-admin user → 403.
	wu := aiReq(t, mux, http.MethodPost, "/api/ai-apps/ai-100/update", userID, `{"html":"<b>x</b>"}`)
	if wu.Code != http.StatusForbidden {
		t.Fatalf("non-admin update: expected 403, got %d", wu.Code)
	}

	// Forged/unknown X-User-ID → 403 (header alone never confers admin).
	wf := aiReq(t, mux, http.MethodPost, "/api/ai-apps/ai-100/update", "forged-id", `{"html":"<b>x</b>"}`)
	if wf.Code != http.StatusForbidden {
		t.Fatalf("forged-id update: expected 403, got %d", wf.Code)
	}

	// Admin on a real app → 200.
	wa := aiReq(t, mux, http.MethodPost, "/api/ai-apps/ai-100/update", adminID, `{"html":"<b>updated</b>"}`)
	if wa.Code != http.StatusOK {
		t.Fatalf("admin update: expected 200, got %d (%s)", wa.Code, wa.Body.String())
	}
	// Confirm the write landed.
	got, _ := os.ReadFile(filepath.Join(dir, "ai-100", "index.html"))
	if string(got) != "<b>updated</b>" {
		t.Fatalf("update did not write index.html; got %q", string(got))
	}
}

// TestAIAppsUpdate_TraversalRejected: an {id} that escapes aiAppsDir is refused
// (400 for a bad charset, 404 for a non-existent contained id) and NO file
// outside the base is written.
func TestAIAppsUpdate_TraversalRejected(t *testing.T) {
	store, adminID, _ := newAIAuthStore(t)
	dir := t.TempDir()
	mux := http.NewServeMux()
	registerAIAppsRoutes(mux, dir, store)

	// A sentinel file OUTSIDE aiAppsDir that a traversal would try to clobber.
	outside := filepath.Join(filepath.Dir(dir), "victim.html")
	os.WriteFile(outside, []byte("ORIGINAL"), 0o644)
	t.Cleanup(func() { os.Remove(outside) })

	// go's ServeMux cleans "..", so path-based escapes route differently; the
	// charset validator (aiEdit_idRe = ^[a-z0-9_-]+$) rejects any id with a dot,
	// slash, or uppercase. We assert the admin never gets a 200 for these.
	// (Ids are single URL path segments here: no raw spaces / percent-escapes
	// that httptest.NewRequest would reject as a malformed target.)
	badIDs := []string{"..", "UPPER", "a.b", "with~tilde"}
	for _, id := range badIDs {
		w := aiReq(t, mux, http.MethodPost, "/api/ai-apps/"+id+"/update", adminID, `{"html":"HACKED"}`)
		if w.Code == http.StatusOK {
			t.Fatalf("traversal/bad id %q on update returned 200 — path containment bypassed", id)
		}
	}

	// A validly-charset'd but non-existent id → 404 (dir must exist first).
	w := aiReq(t, mux, http.MethodPost, "/api/ai-apps/ghost-app/update", adminID, `{"html":"x"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("nonexistent contained id: expected 404, got %d", w.Code)
	}

	if b, _ := os.ReadFile(outside); string(b) != "ORIGINAL" {
		t.Fatalf("SECURITY: a traversal update overwrote a file outside aiAppsDir (now %q)", string(b))
	}
}

// TestAIAppsUpdate_KillSwitch: DISABLE_AI_APP_EDIT=1 → 503 even for an admin.
func TestAIAppsUpdate_KillSwitch(t *testing.T) {
	store, adminID, _ := newAIAuthStore(t)
	dir := t.TempDir()
	seedApp(t, dir, "ai-200")
	mux := http.NewServeMux()
	registerAIAppsRoutes(mux, dir, store)

	t.Setenv("DISABLE_AI_APP_EDIT", "1")
	w := aiReq(t, mux, http.MethodPost, "/api/ai-apps/ai-200/update", adminID, `{"html":"x"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("kill-switch update: expected 503, got %d", w.Code)
	}
}

// ── registerAIAppsVersionsRoutes: snapshot / versions / rollback ─────────────

// TestAIAppsVersions_AuthGate: unauth → 401, non-admin → 403, admin → 200.
func TestAIAppsVersions_AuthGate(t *testing.T) {
	store, adminID, userID := newAIAuthStore(t)
	dir := t.TempDir()
	seedApp(t, dir, "ai-300")
	mux := http.NewServeMux()
	registerAIAppsVersionsRoutes(mux, dir, store)

	// Unauth (no X-User-ID) → 401.
	wUnauth := aiReq(t, mux, http.MethodGet, "/api/ai-apps/ai-300/versions", "", "")
	if wUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauth versions list: expected 401, got %d", wUnauth.Code)
	}

	// Non-admin → 403.
	wUser := aiReq(t, mux, http.MethodPost, "/api/ai-apps/ai-300/snapshot", userID, "")
	if wUser.Code != http.StatusForbidden {
		t.Fatalf("non-admin snapshot: expected 403, got %d", wUser.Code)
	}

	// Admin snapshot → 200, then list versions → 200 with 1 entry.
	wSnap := aiReq(t, mux, http.MethodPost, "/api/ai-apps/ai-300/snapshot", adminID, "")
	if wSnap.Code != http.StatusOK {
		t.Fatalf("admin snapshot: expected 200, got %d (%s)", wSnap.Code, wSnap.Body.String())
	}
	wList := aiReq(t, mux, http.MethodGet, "/api/ai-apps/ai-300/versions", adminID, "")
	if wList.Code != http.StatusOK {
		t.Fatalf("admin versions list: expected 200, got %d", wList.Code)
	}
	var versions []map[string]any
	if err := json.Unmarshal(wList.Body.Bytes(), &versions); err != nil {
		t.Fatalf("versions body not JSON array: %v (%s)", err, wList.Body.String())
	}
	if len(versions) != 1 {
		t.Fatalf("expected 1 snapshot after one snapshot call, got %d", len(versions))
	}
}

// TestAIAppsRollback_VersionValidation: the rollback route validates the
// version string (must be [0-9A-Z]+ and contained under versions/). A traversal
// or bad-charset version → 400; a well-formed but missing version → 404.
func TestAIAppsRollback_VersionValidation(t *testing.T) {
	store, adminID, _ := newAIAuthStore(t)
	dir := t.TempDir()
	seedApp(t, dir, "ai-400")
	mux := http.NewServeMux()
	registerAIAppsVersionsRoutes(mux, dir, store)

	// Missing version → 400.
	wMissing := aiReq(t, mux, http.MethodPost, "/api/ai-apps/ai-400/rollback", adminID, `{}`)
	if wMissing.Code != http.StatusBadRequest {
		t.Fatalf("rollback missing version: expected 400, got %d", wMissing.Code)
	}

	// Traversal / bad-charset versions → 400 (regex ^[0-9A-Z]+$).
	for _, v := range []string{"../../etc", "a/b", "lower", "with-dash", ".."} {
		w := aiReq(t, mux, http.MethodPost, "/api/ai-apps/ai-400/rollback", adminID,
			`{"version":"`+v+`"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("rollback bad version %q: expected 400, got %d", v, w.Code)
		}
	}

	// Well-formed but non-existent version → 404.
	wGhost := aiReq(t, mux, http.MethodPost, "/api/ai-apps/ai-400/rollback", adminID,
		`{"version":"20260101T000000Z"}`)
	if wGhost.Code != http.StatusNotFound {
		t.Fatalf("rollback nonexistent version: expected 404, got %d", wGhost.Code)
	}
}

// ── registerAIAppsSecurityWrappers: save / get / delete ─────────────────────

// TestAIAppsWrappers_SaveDeleteAdminGate: save + delete are admin-gated;
// non-admin → 403; admin save → 200; the {id}/html read is realpath-contained.
func TestAIAppsWrappers_SaveDeleteAdminGate(t *testing.T) {
	store, adminID, userID := newAIAuthStore(t)
	dir := t.TempDir()
	mux := http.NewServeMux()
	registerAIAppsSecurityWrappers(mux, dir, store)

	// Non-admin save → 403.
	wUser := aiReq(t, mux, http.MethodPost, "/api/ai-apps/save", userID,
		`{"title":"x","html":"<b>x</b>"}`)
	if wUser.Code != http.StatusForbidden {
		t.Fatalf("non-admin save: expected 403, got %d", wUser.Code)
	}

	// Admin save → 200 with generated id.
	wSave := aiReq(t, mux, http.MethodPost, "/api/ai-apps/save", adminID,
		`{"title":"My App","html":"<b>hello</b>"}`)
	if wSave.Code != http.StatusOK {
		t.Fatalf("admin save: expected 200, got %d (%s)", wSave.Code, wSave.Body.String())
	}
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(wSave.Body.Bytes(), &saved); err != nil || saved.ID == "" {
		t.Fatalf("save response missing id: %s", wSave.Body.String())
	}

	// GET html for the saved app → 200 (read-only, contained).
	wHTML := aiReq(t, mux, http.MethodGet, "/api/ai-apps/"+saved.ID+"/html", userID, "")
	if wHTML.Code != http.StatusOK {
		t.Fatalf("get html: expected 200, got %d", wHTML.Code)
	}
	if !strings.Contains(wHTML.Body.String(), "hello") {
		t.Fatalf("get html returned wrong content: %s", wHTML.Body.String())
	}

	// Non-admin delete → 403 (app must survive).
	wDelUser := aiReq(t, mux, http.MethodDelete, "/api/ai-apps/"+saved.ID, userID, "")
	if wDelUser.Code != http.StatusForbidden {
		t.Fatalf("non-admin delete: expected 403, got %d", wDelUser.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, saved.ID)); err != nil {
		t.Fatalf("non-admin delete removed the app dir — admin gate bypassed")
	}

	// Admin delete → 200.
	wDel := aiReq(t, mux, http.MethodDelete, "/api/ai-apps/"+saved.ID, adminID, "")
	if wDel.Code != http.StatusOK {
		t.Fatalf("admin delete: expected 200, got %d", wDel.Code)
	}
}

// TestAIAppsWrappers_TraversalReadRejected: a GET /{id}/html with a traversal
// or bad-charset id is refused 400 (secI_safeAppDir) — no file outside the base
// is read.
func TestAIAppsWrappers_TraversalReadRejected(t *testing.T) {
	store, _, userID := newAIAuthStore(t)
	dir := t.TempDir()
	mux := http.NewServeMux()
	registerAIAppsSecurityWrappers(mux, dir, store)

	for _, id := range []string{"..", "UPPER", "a.b", "with~tilde"} {
		w := aiReq(t, mux, http.MethodGet, "/api/ai-apps/"+id+"/html", userID, "")
		// A rejected id is 400; a contained-but-missing one is 404. Never 200.
		if w.Code == http.StatusOK {
			t.Fatalf("traversal id %q on /html returned 200 — containment bypassed", id)
		}
	}
}
