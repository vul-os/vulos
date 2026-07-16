package appsplatform

import (
	"encoding/json"
	"net/http"
	"testing"
)

// These tests harden the platform's HTTP authorization boundaries: an app
// cannot act as another app, a non-admin cannot reach another owner's apps, the
// session and token auth planes do not bleed into each other, and a token can
// never exceed its granted scopes.

func bearerH(tok string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + tok, "Content-Type": "application/json"}
}

// TestTokenIsolationAppCannotActAsAnother proves App A's token only ever
// authenticates App A: auth.test reports A's identity, never B's, and B's token
// reports B's. There is no path for one token to assume another app.
func TestTokenIsolationAppCannotActAsAnother(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	a, _ := reg.Create(CreateParams{Name: "A", OwnerID: "alice", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})
	b, _ := reg.Create(CreateParams{Name: "B", OwnerID: "bob", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})

	identity := func(tok string) string {
		w := do(h, "GET", "/api/apps/v1/auth.test", "", bearerH(tok))
		if w.Code != http.StatusOK {
			t.Fatalf("auth.test failed: %d %s", w.Code, w.Body)
		}
		var resp struct {
			AppID string `json:"app_id"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		return resp.AppID
	}
	if got := identity(a.Token); got != a.App.ID {
		t.Fatalf("A's token resolved to %q, want %q", got, a.App.ID)
	}
	if got := identity(b.Token); got != b.App.ID {
		t.Fatalf("B's token resolved to %q, want %q", got, b.App.ID)
	}
	if a.Token == b.Token || a.App.Incoming.ID == b.App.Incoming.ID {
		t.Fatal("two apps minted colliding token / webhook id")
	}
}

// TestCrossOwnerManagementDenied asserts a non-admin cannot read, update,
// delete, or rotate another owner's app — every route returns 404 (no existence
// leak) and the target is left untouched.
func TestCrossOwnerManagementDenied(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	a, _ := reg.Create(CreateParams{Name: "alice-app", OwnerID: "alice", Products: []string{ProductOffice}})
	bob := map[string]string{"X-User": "bob", "Content-Type": "application/json"}

	routes := []struct {
		method, path, body string
	}{
		{"GET", "/api/apps/" + a.App.ID, ""},
		{"PUT", "/api/apps/" + a.App.ID, `{"name":"hijacked"}`},
		{"DELETE", "/api/apps/" + a.App.ID, ""},
		{"POST", "/api/apps/" + a.App.ID + "/rotate/token", ""},
		{"POST", "/api/apps/" + a.App.ID + "/rotate/secret", ""},
	}
	for _, rt := range routes {
		w := do(h, rt.method, rt.path, rt.body, bob)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s by non-owner: got %d, want 404", rt.method, rt.path, w.Code)
		}
	}
	// The app and its token must be intact.
	if cur, err := reg.Get(a.App.ID); err != nil || cur.Name != "alice-app" {
		t.Fatalf("cross-owner write mutated the target: %+v err=%v", cur, err)
	}
	if _, err := reg.GetByTokenHash(HashToken(a.Token)); err != nil {
		t.Fatal("cross-owner rotate invalidated the victim's token")
	}
}

// TestAdminCanManageAcrossOwners confirms the admin flag is the documented
// escalation: an admin reaches another owner's app.
func TestAdminCanManageAcrossOwners(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	a, _ := reg.Create(CreateParams{Name: "alice-app", OwnerID: "alice", Products: []string{ProductOffice}})
	admin := map[string]string{"X-User": "root", "X-Admin": "1"}
	if w := do(h, "GET", "/api/apps/"+a.App.ID, "", admin); w.Code != http.StatusOK {
		t.Fatalf("admin GET should succeed, got %d", w.Code)
	}
	if w := do(h, "DELETE", "/api/apps/"+a.App.ID, "", admin); w.Code != http.StatusOK {
		t.Fatalf("admin DELETE should succeed, got %d", w.Code)
	}
}

// TestProductIsolationManagement asserts one product's mount cannot manage an
// app that does not target it, even for its owner (ownedApp enforces targeting).
func TestProductIsolationManagement(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice) // Office mount
	boardApp, _ := reg.Create(CreateParams{Name: "boarder", OwnerID: "alice", Products: []string{ProductBoard}})
	alice := map[string]string{"X-User": "alice", "Content-Type": "application/json"}

	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/api/apps/" + boardApp.App.ID, ""},
		{"PUT", "/api/apps/" + boardApp.App.ID, `{"name":"x"}`},
		{"DELETE", "/api/apps/" + boardApp.App.ID, ""},
	} {
		if w := do(h, rt.method, rt.path, rt.body, alice); w.Code != http.StatusNotFound {
			t.Errorf("Office mount managing a Board-only app: %s got %d, want 404", rt.method, w.Code)
		}
	}
}

// TestAuthPlanesDoNotCross confirms the session plane and the token plane are
// separate: a Bearer token does not authenticate the management API, and a
// product session does not authenticate the runtime API.
func TestAuthPlanesDoNotCross(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	c, _ := reg.Create(CreateParams{Name: "x", OwnerID: "alice", Products: []string{ProductOffice}})

	// A valid app token on a management route is NOT a session → 401.
	if w := do(h, "GET", "/api/apps", "", bearerH(c.Token)); w.Code != http.StatusUnauthorized {
		t.Fatalf("app token must not authenticate management API, got %d", w.Code)
	}
	// A valid session header on a runtime route is NOT a token → 401.
	if w := do(h, "GET", "/api/apps/v1/auth.test", "", map[string]string{"X-User": "alice"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("session must not authenticate runtime API, got %d", w.Code)
	}
}

// TestTokenCannotExceedGrantedScopes is the core extensibility invariant: a
// token may only perform actions whose required scope it was granted. Granting
// apps:write does not implicitly grant apps:read.
func TestTokenCannotExceedGrantedScopes(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	// Granted apps:write only.
	c, _ := reg.Create(CreateParams{Name: "scoped", OwnerID: "a", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})

	// apps:write action is allowed.
	if w := do(h, "POST", "/api/apps/v1/act", `{"action":"message.post","target":"general"}`, bearerH(c.Token)); w.Code != http.StatusOK {
		t.Fatalf("granted action should succeed, got %d: %s", w.Code, w.Body)
	}
	// apps:read read kind is NOT granted → 403.
	if w := do(h, "GET", "/api/apps/v1/read?kind=history&target=general", "", bearerH(c.Token)); w.Code != http.StatusForbidden {
		t.Fatalf("ungranted read kind should 403, got %d: %s", w.Code, w.Body)
	}
}

// TestMalformedBearerHeaderRejected fuzzes the Authorization header shape: only
// "Bearer <token>" authenticates; everything else is 401, never a 500.
func TestMalformedBearerHeaderRejected(t *testing.T) {
	h, _, _ := newTestHandler(t, ProductOffice)
	for _, auth := range []string{
		"",                   // absent (set below only when non-empty)
		"Bearer",             // no token
		"Bearer ",            // empty token
		"bearer vat_x",       // wrong case scheme
		"Basic dXNlcjpwdw==", // wrong scheme
		"Token vat_x",        // wrong scheme word
		"vat_x",              // bare token, no scheme
		"Bearer\tvat_x",      // tab instead of space
	} {
		hdr := map[string]string{}
		if auth != "" {
			hdr["Authorization"] = auth
		}
		w := do(h, "GET", "/api/apps/v1/auth.test", "", hdr)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: got %d, want 401", auth, w.Code)
		}
	}
}
