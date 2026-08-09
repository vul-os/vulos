package main

// routes_appnet_deploy_idor_test.go — IDOR-APPNET-DEPLOY-01.
//
// GET/POST /api/apps/{id}/deployment, /deprovision, /cache/purge,
// /cache/stats, and /domain{,/verify} (services/appnet's
// RegisterSubdomainHandlers, RegisterEdgeCacheHandlers,
// RegisterCustomDomainHandlers) previously took NO authorizer at all: any
// authenticated caller on a multi-user box could deprovision another user's
// published app, purge/inspect its edge cache, or attach/verify/remove a
// custom domain on it, merely by supplying its app ID in the path — the same
// "is logged in" mistaken for "is authorized" shape as the rest of this
// audit. This test drives the REAL production authorizer
// (appnetOwnerAuthorizer, wired in routes_newfeatures.go) over a real
// auth.Store and appnet.Manager, not a test stub, so a regression in the
// actual wiring fails here.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/services/appnet"
	"vulos/backend/services/auth"
)

// appnetDeployIDOREnv wires the real deployment/cache/custom-domain routes
// with appnetOwnerAuthorizer over a temp auth store seeded with an admin
// ("admin"), the app owner ("alice"), and a non-owner ("bob"). "notes" is
// owned by alice via a directly-inserted namespace (no real container needed
// — AddNamespace is the documented test seam for this).
func appnetDeployIDOREnv(t *testing.T) (mux *http.ServeMux, adminID, aliceID, bobID string) {
	t.Helper()
	t.Setenv("VULOS_CADDY_DIR", "noop")
	t.Setenv("VULOS_NGINX_DIR", "noop")

	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	admin, err := store.Register("admin", "adminpw123-secure!", "Admin")
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}
	alice, err := store.Register("alice", "alicepw123-secure!", "Alice")
	if err != nil {
		t.Fatalf("register alice: %v", err)
	}
	bob, err := store.Register("bob", "bobpw123-secure!", "Bob")
	if err != nil {
		t.Fatalf("register bob: %v", err)
	}

	netMgr := appnet.NewManager()
	netMgr.AddNamespace(alice.ID+"-notes", &appnet.Namespace{AppID: "notes", OwnerID: alice.ID})

	dir := t.TempDir()
	vis, err := appnet.NewVisibilityStoreAt(filepath.Join(dir, "vis.json"))
	if err != nil {
		t.Fatalf("vis store: %v", err)
	}
	ds, err := appnet.NewDeploymentStoreAt(filepath.Join(dir, "dep.json"))
	if err != nil {
		t.Fatalf("deployment store: %v", err)
	}
	// Seed an existing public deployment for "notes" so deprovision/cache/domain
	// all have something real to act on.
	if err := ds.Set(&appnet.Deployment{AppID: "notes", Profile: "default", FQDN: "notes--default.local.vulos.org"}); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}
	provisioner := appnet.NewProvisioner(ds, nil)

	cs, err := appnet.NewCustomDomainStoreAt(filepath.Join(dir, "cd.json"))
	if err != nil {
		t.Fatalf("custom domain store: %v", err)
	}
	ecm := appnet.NewEdgeCacheManager()

	authorize := appnetOwnerAuthorizer(store, netMgr)

	mux = http.NewServeMux()
	appnet.RegisterSubdomainHandlers(mux, vis, provisioner, netMgr, authorize)
	appnet.RegisterEdgeCacheHandlers(mux, ecm, provisioner, authorize)
	appnet.RegisterCustomDomainHandlers(mux, cs, provisioner, netMgr, authorize)
	return mux, admin.ID, alice.ID, bob.ID
}

func appnetDeployReq(t *testing.T, mux *http.ServeMux, method, path, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if userID != "" {
		r.Header.Set("X-User-ID", userID)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// TestAppnetDeployIDOR_NonOwnerCannotDeprovision is the core regression: bob
// (authenticated, not admin, not the app owner) must not be able to tear down
// alice's published "notes" app.
func TestAppnetDeployIDOR_NonOwnerCannotDeprovision(t *testing.T) {
	mux, _, _, bobID := appnetDeployIDOREnv(t)

	rec := appnetDeployReq(t, mux, "POST", "/api/apps/notes/deprovision", bobID, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("IDOR-APPNET-DEPLOY-01 regression: bob deprovisioned alice's app: status %d, body %s", rec.Code, rec.Body.String())
	}
}

// TestAppnetDeployIDOR_OwnerCanDeprovision proves the fix isn't a blanket
// lockout: the actual owner can still tear down her own app.
func TestAppnetDeployIDOR_OwnerCanDeprovision(t *testing.T) {
	mux, _, aliceID, _ := appnetDeployIDOREnv(t)

	rec := appnetDeployReq(t, mux, "POST", "/api/apps/notes/deprovision", aliceID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner deprovision: status %d, want 200, body %s", rec.Code, rec.Body.String())
	}
}

// TestAppnetDeployIDOR_NonOwnerCannotReadDeployment: deployment info (FQDN,
// TLS status) is also gated — not just the mutation.
func TestAppnetDeployIDOR_NonOwnerCannotReadDeployment(t *testing.T) {
	mux, _, _, bobID := appnetDeployIDOREnv(t)

	rec := appnetDeployReq(t, mux, "GET", "/api/apps/notes/deployment", bobID, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("IDOR-APPNET-DEPLOY-01 regression: bob read alice's deployment info: status %d, body %s", rec.Code, rec.Body.String())
	}
}

// TestAppnetDeployIDOR_NonOwnerCannotPurgeOrReadCache covers the PUBWEB-03
// edge-cache surface.
func TestAppnetDeployIDOR_NonOwnerCannotPurgeOrReadCache(t *testing.T) {
	mux, _, _, bobID := appnetDeployIDOREnv(t)

	if rec := appnetDeployReq(t, mux, "POST", "/api/apps/notes/cache/purge", bobID, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("IDOR-APPNET-DEPLOY-01 regression: bob purged alice's app cache: status %d", rec.Code)
	}
	if rec := appnetDeployReq(t, mux, "GET", "/api/apps/notes/cache/stats", bobID, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("IDOR-APPNET-DEPLOY-01 regression: bob read alice's app cache stats: status %d", rec.Code)
	}
}

// TestAppnetDeployIDOR_NonOwnerCannotAttachCustomDomain covers the PUBWEB-07
// custom-domain surface — the highest-impact of the three, since an attacker
// who could attach a domain to someone else's app could redirect/hijack
// traffic meant for it.
func TestAppnetDeployIDOR_NonOwnerCannotAttachCustomDomain(t *testing.T) {
	mux, _, _, bobID := appnetDeployIDOREnv(t)

	rec := appnetDeployReq(t, mux, "POST", "/api/apps/notes/domain", bobID, `{"domain":"evil.example.com"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("IDOR-APPNET-DEPLOY-01 regression: bob attached a custom domain to alice's app: status %d, body %s", rec.Code, rec.Body.String())
	}
	if rec := appnetDeployReq(t, mux, "GET", "/api/apps/notes/domain", bobID, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("IDOR-APPNET-DEPLOY-01 regression: bob read alice's custom domain record: status %d", rec.Code)
	}
	if rec := appnetDeployReq(t, mux, "DELETE", "/api/apps/notes/domain", bobID, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("IDOR-APPNET-DEPLOY-01 regression: bob removed alice's custom domain: status %d", rec.Code)
	}
}

// TestAppnetDeployIDOR_AdminCanActOnAnyApp: RoleAdmin is still allowed through
// — this is an ownership gate, not a total lockout for admins acting on a
// non-admin's app.
func TestAppnetDeployIDOR_AdminCanActOnAnyApp(t *testing.T) {
	mux, adminID, _, _ := appnetDeployIDOREnv(t)

	rec := appnetDeployReq(t, mux, "GET", "/api/apps/notes/cache/stats", adminID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin cache/stats: status %d, want 200, body %s", rec.Code, rec.Body.String())
	}
}

// TestAppnetDeployIDOR_UnauthenticatedRejected: no X-User-ID at all is
// rejected the same as a wrong one (appnetOwnerAuthorizer's userID=="" branch).
func TestAppnetDeployIDOR_UnauthenticatedRejected(t *testing.T) {
	mux, _, _, _ := appnetDeployIDOREnv(t)

	rec := appnetDeployReq(t, mux, "POST", "/api/apps/notes/deprovision", "", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated deprovision: status %d, want 403, body %s", rec.Code, rec.Body.String())
	}
}
