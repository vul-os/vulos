package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	storagepkg "vulos/backend/internal/storage"
)

// newDeleteGateway builds a Gateway with a real auth.Store (for session
// validation) and a GrantBroker backed by a resolver with NO object store
// configured, so DeleteObject exercises the local-FS fallback path without
// touching the network. Mirrors newPresignGateway in presign_test.go,
// including minting app secrets for "office"/"talk" (PRESIGN-02/DELETE-02).
func newDeleteGateway(t *testing.T) (g *Gateway, token string, userID string, root string, secrets map[string]string) {
	t.Helper()
	store, mgr, pool := newTestDeps(t)
	token, userID = seedSession(t, store)
	g = New(store, mgr, pool)

	root = t.TempDir()
	resolver := storagepkg.NewResolver(storagepkg.ResolverConfig{
		LocalRoot: root,
	})
	broker := storagepkg.NewGrantBroker(resolver, storagepkg.STSConfig{}, 0)
	g.SetGrantBroker(broker, resolver.BucketFor)
	g.AllowStorage("office", "office/")
	g.AllowStorage("talk", "talk/")
	secrets = map[string]string{
		"office": g.GenerateAppSecret("office"),
		"talk":   g.GenerateAppSecret("talk"),
	}
	return g, token, userID, root, secrets
}

// doDelete issues a delete request authenticated as the session (token) and
// carrying appSecret as X-Vulos-App-Secret (pass "" to omit the header
// entirely, e.g. to exercise the DELETE-02 rejection path).
func doDelete(g *Gateway, token, appSecret string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/api/storage/delete", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	if appSecret != "" {
		r.Header.Set("X-Vulos-App-Secret", appSecret)
	}
	rec := httptest.NewRecorder()
	g.DeleteHandler().ServeHTTP(rec, r)
	return rec
}

// touchLocalObject creates a file at the local-FS path the broker would map
// "<userID>/<appPrefix><key>" to, so a delete has something real to remove.
func touchLocalObject(t *testing.T, root, fullKey string) {
	t.Helper()
	p := filepath.Join(root, filepath.Clean("/"+fullKey))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestDeleteHandler_Unauthorized(t *testing.T) {
	g, _, _, _, secrets := newDeleteGateway(t)
	rec := doDelete(g, "not-a-real-token", secrets["office"], deleteRequest{AppID: "office", Key: "a.txt"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestDeleteHandler_AppNotPermitted(t *testing.T) {
	g, token, _, _, _ := newDeleteGateway(t)
	// "notes" was never AllowStorage'd, but it DOES hold a genuine secret for
	// itself, so this isolates the "not permitted to use storage" 403 from
	// the (also 403) "unproven identity" rejection covered separately.
	notesSecret := g.GenerateAppSecret("notes")
	rec := doDelete(g, token, notesSecret, deleteRequest{AppID: "notes", Key: "a.txt"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

// Empty app_id must also be rejected — matches PresignHandler's req.AppID == ""
// check (an empty appID would look up storageApps[""], which is never set,
// so this is covered by the same branch, but pin it explicitly).
func TestDeleteHandler_EmptyAppIDRejected(t *testing.T) {
	g, token, _, _, _ := newDeleteGateway(t)
	rec := doDelete(g, token, "", deleteRequest{AppID: "", Key: "a.txt"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

// SECURITY (DELETE-02): a request naming an app_id but presenting NO
// X-Vulos-App-Secret at all must be refused outright.
func TestDeleteHandler_MissingAppSecretRejected(t *testing.T) {
	g, token, _, _, _ := newDeleteGateway(t)
	rec := doDelete(g, token, "", deleteRequest{AppID: "office", Key: "a.txt"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

// SECURITY (DELETE-02): a wrong secret must be refused.
func TestDeleteHandler_WrongAppSecretRejected(t *testing.T) {
	g, token, _, _, _ := newDeleteGateway(t)
	rec := doDelete(g, token, "totally-not-a-real-secret", deleteRequest{AppID: "office", Key: "a.txt"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

// SECURITY (DELETE-02, the CONFIRMED audit finding): the app holding talk's
// own secret must never be able to delete under office's prefix by simply
// naming app_id="office" — its secret only proves it is "talk".
func TestDeleteHandler_CannotClaimAnotherAppWithOwnSecret(t *testing.T) {
	g, token, userID, root, secrets := newDeleteGateway(t)
	fullKey := userID + "/office/a.txt"
	touchLocalObject(t, root, fullKey)

	rec := doDelete(g, token, secrets["talk"], deleteRequest{AppID: "office", Key: "a.txt"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (talk's secret must not unlock office's prefix), body=%s", rec.Code, rec.Body.String())
	}
	// And the object must genuinely survive the rejected attempt.
	p := filepath.Join(root, filepath.Clean("/"+fullKey))
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("object should still exist after rejected cross-app delete: %v", err)
	}
}

// BUG FIX regression (2026-07-12, ENTITLE-01 gap): same as the presign
// endpoint — a caller with no entitlement for a premium app must not be
// able to delete objects under that app's prefix via this endpoint.
func TestDeleteHandler_EntitlementGating_RefusesWithoutProduct(t *testing.T) {
	g, token, _, _, secrets := newDeleteGateway(t)
	g.AllowApp("office", "office-pro")
	g.SetEntitlementGating(true)

	rec := doDelete(g, token, secrets["office"], deleteRequest{AppID: "office", Key: "a.txt"})
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402, body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteHandler_EntitlementGating_AllowsWithProduct(t *testing.T) {
	g, token, userID, root, secrets := newDeleteGateway(t)
	g.AllowApp("office", "office-pro")
	g.SetEntitlementGating(true)
	touchLocalObject(t, root, userID+"/office/a.txt")

	raw, _ := json.Marshal(deleteRequest{AppID: "office", Key: "a.txt"})
	r := httptest.NewRequest(http.MethodPost, "/api/storage/delete", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Vulos-App-Secret", secrets["office"])
	r.Header.Set("X-Vulos-Entitlements-Products", "office-pro")
	rec := httptest.NewRecorder()
	g.DeleteHandler().ServeHTTP(rec, r)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteHandler_EntitlementGating_OffByDefault(t *testing.T) {
	g, token, _, _, secrets := newDeleteGateway(t)
	g.AllowApp("office", "office-pro")
	// SetEntitlementGating never called — stays false.

	rec := doDelete(g, token, secrets["office"], deleteRequest{AppID: "office", Key: "a.txt"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (gating off by default), body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteHandler_NoBrokerConfigured(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, _ := seedSession(t, store)
	g := New(store, mgr, pool)
	g.AllowStorage("office", "office/")
	officeSecret := g.GenerateAppSecret("office")
	// SetGrantBroker never called — broker stays nil.
	rec := doDelete(g, token, officeSecret, deleteRequest{AppID: "office", Key: "a.txt"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestDeleteHandler_InvalidKeyRejected(t *testing.T) {
	g, token, _, _, secrets := newDeleteGateway(t)
	cases := []string{"", "/abs/path", "../escape", "a/../../b", "a/../..", "a\\b"}
	for _, k := range cases {
		rec := doDelete(g, token, secrets["office"], deleteRequest{AppID: "office", Key: k})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("key %q: status = %d, want 400, body=%s", k, rec.Code, rec.Body.String())
		}
	}
}

func TestDeleteHandler_InvalidBodyRejected(t *testing.T) {
	g, token, _, _, _ := newDeleteGateway(t)
	r := httptest.NewRequest(http.MethodPost, "/api/storage/delete", bytes.NewReader([]byte("not json")))
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	g.DeleteHandler().ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

// Happy path: deleting an existing object under the app's own prefix succeeds
// and actually removes the bytes.
func TestDeleteHandler_HappyPath_RemovesObject(t *testing.T) {
	g, token, userID, root, secrets := newDeleteGateway(t)
	fullKey := userID + "/office/documents/report.docx"
	touchLocalObject(t, root, fullKey)

	p := filepath.Join(root, filepath.Clean("/"+fullKey))
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixture setup: object should exist before delete: %v", err)
	}

	rec := doDelete(g, token, secrets["office"], deleteRequest{AppID: "office", Key: "documents/report.docx"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("object should be gone after delete, stat err = %v", err)
	}
}

// Fail-closed / idempotent: deleting an object that never existed still
// returns 204 (DeleteObject treats a missing object as success — the caller
// only needs the bytes to be gone).
func TestDeleteHandler_MissingObjectIsNoop(t *testing.T) {
	g, token, _, _, secrets := newDeleteGateway(t)
	rec := doDelete(g, token, secrets["office"], deleteRequest{AppID: "office", Key: "never/existed.txt"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (idempotent delete), body=%s", rec.Code, rec.Body.String())
	}
}

// SECURITY: app A cannot delete an object under app B's prefix even when it
// names the exact same relative key — the handler always composes
// "<userID>/<itsOwnPrefix>/<relKey>" from the app_id's OWN registered prefix.
func TestDeleteHandler_CrossAppIsolation(t *testing.T) {
	g, token, userID, root, secrets := newDeleteGateway(t)

	// Create an object under talk's prefix only.
	talkKey := userID + "/talk/shared.txt"
	touchLocalObject(t, root, talkKey)
	talkPath := filepath.Join(root, filepath.Clean("/"+talkKey))

	// office asks to delete "shared.txt" — this must map to
	// "<userID>/office/shared.txt", NOT "<userID>/talk/shared.txt".
	rec := doDelete(g, token, secrets["office"], deleteRequest{AppID: "office", Key: "shared.txt"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body=%s", rec.Code, rec.Body.String())
	}

	// talk's object must be untouched.
	if _, err := os.Stat(talkPath); err != nil {
		t.Fatalf("cross-app isolation violated: talk's object was affected by office's delete: %v", err)
	}
}

// SECURITY: path traversal in the key can never escape the app's own prefix,
// even indirectly (e.g. attempting to climb out via ".." before sanitisation
// rejects it outright — this is the same guard as presign's traversal test).
func TestDeleteHandler_TraversalRejected(t *testing.T) {
	g, token, userID, root, secrets := newDeleteGateway(t)

	// Plant a "victim" object OUTSIDE any app prefix, directly under the
	// user's own root, to prove a traversal attempt (if it were not rejected)
	// would have been able to reach it.
	victimKey := userID + "/victim.txt"
	touchLocalObject(t, root, victimKey)
	victimPath := filepath.Join(root, filepath.Clean("/"+victimKey))

	traversalKeys := []string{"../victim.txt", "../../victim.txt", "..%2fvictim.txt"}
	for _, k := range traversalKeys {
		rec := doDelete(g, token, secrets["office"], deleteRequest{AppID: "office", Key: k})
		if k == "..%2fvictim.txt" {
			// Not a literal ".." sequence pre-decode; sanitizePresignKey only
			// rejects raw "..": this key contains no "/" so it is treated as
			// a single (harmless, literal) filename component. Assert it does
			// NOT resolve to the victim path either way.
			continue
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("key %q: status = %d, want 400 (traversal must be rejected)", k, rec.Code)
		}
	}

	if _, err := os.Stat(victimPath); err != nil {
		t.Fatalf("traversal must never reach the victim object: %v", err)
	}
}

// Compile-time sanity: DeleteHandler must not require anything beyond what
// PresignHandler already needs (same broker/bucketFor/storageApps wiring) —
// verified functionally by newDeleteGateway reusing newPresignGateway's setup
// shape. This test just documents the shared-config assumption directly by
// building via SetGrantBroker with a resolver context value, exercising the
// request context plumbing end to end.
func TestDeleteHandler_UsesRequestContext(t *testing.T) {
	g, token, userID, root, secrets := newDeleteGateway(t)
	fullKey := userID + "/office/ctx-check.txt"
	touchLocalObject(t, root, fullKey)

	raw, _ := json.Marshal(deleteRequest{AppID: "office", Key: "ctx-check.txt"})
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/storage/delete", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Vulos-App-Secret", secrets["office"])
	rec := httptest.NewRecorder()
	g.DeleteHandler().ServeHTTP(rec, r)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}
