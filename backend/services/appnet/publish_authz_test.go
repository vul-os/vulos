package appnet

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// newAuthzVisEnv wires RegisterVisibilityHandlers with a supplied authorizer.
func newAuthzVisEnv(t *testing.T, authz PublishAuthorizer) (*http.ServeMux, *VisibilityStore) {
	t.Helper()
	dir := t.TempDir()
	vis, err := NewVisibilityStoreAt(filepath.Join(dir, "visibility.json"))
	if err != nil {
		t.Fatalf("vis store: %v", err)
	}
	appStore := &AppStore{appsDir: filepath.Join(dir, "apps")}
	mux := http.NewServeMux()
	RegisterVisibilityHandlers(mux, appStore, vis, authz)
	return mux, vis
}

// TestVisibilityAPI_PublishAuthz verifies Finding 2: a caller the authorizer
// rejects cannot publish (403), while private is always allowed and an accepted
// caller can publish.
func TestVisibilityAPI_PublishAuthz(t *testing.T) {
	// Authorizer that only accepts user "admin".
	authz := func(r *http.Request, appID, visibility string) bool {
		return r.Header.Get("X-User-ID") == "admin"
	}
	mux, vis := newAuthzVisEnv(t, authz)

	patch := func(user, vs string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/apps/notes/visibility",
			strings.NewReader(`{"visibility":"`+vs+`"}`))
		req.Header.Set("Content-Type", "application/json")
		if user != "" {
			req.Header.Set("X-User-ID", user)
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}

	// Non-owner / non-admin cannot publish to public.
	if rr := patch("randouser", "public"); rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin publish public: got %d, want 403 (%s)", rr.Code, rr.Body.String())
	}
	// ...nor to local (any non-private exposure is gated).
	if rr := patch("randouser", "local"); rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin publish local: got %d, want 403", rr.Code)
	}
	if got := vis.Get("notes"); got != VisibilityPrivate {
		t.Fatalf("visibility changed despite 403: %q", got)
	}

	// Reverting/keeping private is always allowed, even unauthenticated.
	if rr := patch("", "private"); rr.Code != http.StatusOK {
		t.Fatalf("set private: got %d, want 200", rr.Code)
	}

	// Authorized caller can publish.
	if rr := patch("admin", "public"); rr.Code != http.StatusOK {
		t.Fatalf("admin publish: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if got := vis.Get("notes"); got != VisibilityPublic {
		t.Fatalf("visibility = %q after admin publish, want public", got)
	}
}
