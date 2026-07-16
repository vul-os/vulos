package appsplatform

import (
	"net/http"
	"strings"
	"testing"
)

// These tests pin the seam's audience-binding contract: an app backend is
// reached ONLY with its own scoped vat_ token (never a user/product session),
// and that token is bound to the exact audience (products) its app targets. They
// are the regression guards behind "app backends must never receive the user
// session token" and "a token can never act outside its audience".

// TestRuntimeNeverAcceptsASession proves the runtime (app-backend) routes are
// authenticated by the Bearer app token ALONE: a request carrying a product
// session — whether as the product's own X-User session header or as a browser
// Cookie — but no Bearer token is rejected 401 on every runtime route. The user
// session must never cross into the token plane an app backend sees.
func TestRuntimeNeverAcceptsASession(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	// A real, valid app exists — but the caller presents a SESSION, not the token.
	reg.Create(CreateParams{Name: "x", OwnerID: "alice", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})

	sessionOnly := map[string]string{
		"X-User":  "alice", // the product's own management session
		"X-Admin": "1",
		"Cookie":  "vc_session=totally-a-valid-looking-session; other=1", // a browser cookie
	}
	routes := []struct{ method, path, body string }{
		{"GET", "/api/apps/v1/auth.test", ""},
		{"POST", "/api/apps/v1/act", `{"action":"message.post","target":"general"}`},
		{"GET", "/api/apps/v1/read?kind=history&target=general", ""},
		{"GET", "/api/apps/v1/events", ""},
	}
	for _, rt := range routes {
		w := do(h, rt.method, rt.path, rt.body, sessionOnly)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with a session but no bearer token: got %d, want 401 (session must not reach an app backend)", rt.method, rt.path, w.Code)
		}
	}
}

// TestRuntimeRejectsTokenPresentedAsCookie confirms the app token authenticates
// ONLY via the Authorization: Bearer header — the same secret placed in a Cookie
// is ignored (401). This keeps the app credential off the ambient-cookie channel
// a browser would replay, so it can only be presented deliberately.
func TestRuntimeRejectsTokenPresentedAsCookie(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	c, _ := reg.Create(CreateParams{Name: "x", OwnerID: "a", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})

	// The very same valid token, but in a Cookie instead of Authorization.
	cookie := map[string]string{"Cookie": "token=" + c.Token, "Content-Type": "application/json"}
	if w := do(h, "GET", "/api/apps/v1/auth.test", "", cookie); w.Code != http.StatusUnauthorized {
		t.Fatalf("token in a cookie must not authenticate, got %d", w.Code)
	}
	// Sanity: the same token in the Authorization header DOES authenticate.
	if w := do(h, "GET", "/api/apps/v1/auth.test", "", bearerH(c.Token)); w.Code != http.StatusOK {
		t.Fatalf("token in Authorization header should authenticate, got %d", w.Code)
	}
}

// TestEmptyAudienceTokenReachesNothing proves audience binding at its boundary:
// an app that targets NO product has an empty audience, so its token is 403 on
// the runtime routes (it targets nothing to act on) and the app is invisible in
// every product's consolidation list. A token can never act outside its audience.
func TestEmptyAudienceTokenReachesNothing(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	// Created directly against the registry with no products — an empty audience.
	c, err := reg.Create(CreateParams{Name: "orphan", OwnerID: "a", Products: []string{}, Scopes: []string{ScopeAppsWrite}})
	if err != nil {
		t.Fatal(err)
	}
	if c.App.TargetsProduct(ProductOffice) {
		t.Fatal("app with no products should target nothing")
	}
	// Runtime routes: the token authenticates (it is a real token) but is 403 —
	// it targets no product this (or any) mount serves.
	if w := do(h, "GET", "/api/apps/v1/auth.test", "", bearerH(c.Token)); w.Code != http.StatusForbidden {
		t.Fatalf("empty-audience token should 403 at runtime, got %d: %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/apps/v1/act", `{"action":"message.post","target":"general"}`, bearerH(c.Token)); w.Code != http.StatusForbidden {
		t.Fatalf("empty-audience token should 403 on act, got %d", w.Code)
	}
	// Consolidation list: invisible to the product surface.
	admin := map[string]string{"X-User": "root", "X-Admin": "1"}
	w := do(h, "GET", "/api/apps", "", admin)
	if strings.Contains(w.Body.String(), "orphan") {
		t.Fatalf("empty-audience app must not appear in the product's GET /api/apps: %s", w.Body)
	}
}

// TestAuthTestReflectsExactAudience pins that auth.test reports the token's OWN
// audience — exactly the app's granted products and scopes — and nothing wider.
// The identity an app backend sees is scoped to what was granted, never a
// wildcard.
func TestAuthTestReflectsExactAudience(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	c, _ := reg.Create(CreateParams{
		Name: "scoped", OwnerID: "a",
		Products: []string{ProductOffice},
		Scopes:   []string{ScopeAppsRead},
	})
	w := do(h, "GET", "/api/apps/v1/auth.test", "", bearerH(c.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("auth.test failed: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	// Audience is exactly {office} / {apps:read}; it must not report products or
	// scopes it was never granted.
	for _, want := range []string{`"` + ProductOffice + `"`, `"` + ScopeAppsRead + `"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("auth.test omitted granted audience %s: %s", want, body)
		}
	}
	for _, deny := range []string{`"` + ProductBoard + `"`, `"` + ProductFiles + `"`, `"` + ScopeAppsWrite + `"`} {
		if strings.Contains(body, deny) {
			t.Fatalf("auth.test leaked ungranted audience %s: %s", deny, body)
		}
	}
}
