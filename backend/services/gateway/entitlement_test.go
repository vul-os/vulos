package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"vulos/backend/services/appnet"
)

// entitlementFakeApp launches an httptest.Server that counts how many times
// it was hit, and wires it into mgr as appID's namespace for userID. Used to
// assert that a refused (entitlement-gated) request never reaches the app.
func entitlementFakeApp(t *testing.T, mgr *appnet.Manager, appID, userID string) *int32 {
	t.Helper()
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	addr := upstream.Listener.Addr().String()
	colon := strings.LastIndex(addr, ":")
	ip := addr[:colon]
	var port int
	fmt.Sscanf(addr[colon+1:], "%d", &port)

	ns := &appnet.Namespace{
		Name:    "vulos_" + appID,
		AppID:   appID,
		OwnerID: userID,
		NSIP:    ip,
		AppPort: port,
		Active:  true,
	}
	mgr.AddNamespace(userID+"-"+appID, ns)
	return &hits
}

// TestEntitlement_DisabledIsFullyOpen verifies the self-host/standalone
// default: gating off means every app dispatches regardless of the
// entitlements header, matching today's all-open behavior exactly.
func TestEntitlement_DisabledIsFullyOpen(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)
	g := New(store, mgr, pool)
	g.AllowApp("office", "office-pro")
	// SetEntitlementGating never called → disabled by default.
	hits := entitlementFakeApp(t, mgr, "office", userID)

	r := httptest.NewRequest(http.MethodGet, "/app/office/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (gating disabled must stay fully open), body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(hits) != 1 {
		t.Fatalf("expected the app to be dispatched to exactly once, got %d", *hits)
	}
}

// TestEntitlement_EnabledRefusesWithoutProduct verifies the cloud/os fail-
// closed path: a request without the required product's entitlement is
// refused with 402, and the app is never dispatched to.
func TestEntitlement_EnabledRefusesWithoutProduct(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)
	g := New(store, mgr, pool)
	g.SetEntitlementGating(true)
	g.AllowApp("office", "office-pro")
	hits := entitlementFakeApp(t, mgr, "office", userID)

	r := httptest.NewRequest(http.MethodGet, "/app/office/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402, body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(hits) != 0 {
		t.Fatal("app must never be dispatched to when entitlement is refused")
	}
}

// TestEntitlement_EnabledAllowsWithProduct verifies a vk_-style request that
// DOES carry the required product (simulated by setting the internal seam
// header directly, as auth.Handler.Middleware would after CP introspection)
// is allowed through.
func TestEntitlement_EnabledAllowsWithProduct(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)
	g := New(store, mgr, pool)
	g.SetEntitlementGating(true)
	g.AllowApp("office", "office-pro")
	hits := entitlementFakeApp(t, mgr, "office", userID)

	r := httptest.NewRequest(http.MethodGet, "/app/office/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set(entitlementsHeader, "os,mail,office-pro")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(hits) != 1 {
		t.Fatalf("expected the app to be dispatched to exactly once, got %d", *hits)
	}
}

// TestEntitlement_AppsWithNoRequirementStayOpen verifies that gating being
// enabled does not affect apps that declare no required product at all.
func TestEntitlement_AppsWithNoRequirementStayOpen(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)
	g := New(store, mgr, pool)
	g.SetEntitlementGating(true)
	// "notes" never registered via AllowApp — no requirement.
	entitlementFakeApp(t, mgr, "notes", userID)

	r := httptest.NewRequest(http.MethodGet, "/app/notes/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

func TestEntitlement_HeaderNameMatchesSeamContract(t *testing.T) {
	if entitlementsHeader != "X-Vulos-Entitlements-Products" {
		t.Fatalf("entitlementsHeader = %q, must match the documented seam contract", entitlementsHeader)
	}
	if !strings.HasPrefix(entitlementsHeader, "X-Vulos-") {
		t.Fatal("entitlementsHeader must live in the X-Vulos- namespace so stripInboundVulosHeaders strips it before an app ever sees it")
	}
}

func TestProductAllowed_EmptyProductAlwaysPasses(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	g := New(store, mgr, pool)
	g.SetEntitlementGating(true)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if ok, reason := g.ProductAllowed(r, ""); !ok {
		t.Fatalf("empty product must always pass, reason=%q", reason)
	}
}

func TestProductAllowed_FailsClosedWithoutHeader(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	g := New(store, mgr, pool)
	g.SetEntitlementGating(true)
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if ok, _ := g.ProductAllowed(r, "office-pro"); ok {
		t.Fatal("must fail closed when gating is enabled and no entitlement header is present")
	}
}

func TestEntitlementAllowed_DisabledAlwaysPasses(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	g := New(store, mgr, pool)
	// gating left disabled
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if ok, _ := g.ProductAllowed(r, "office-pro"); !ok {
		t.Fatal("gating disabled must always pass regardless of product")
	}
}
