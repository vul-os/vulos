package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	storagepkg "vulos/backend/internal/storage"
)

// newPresignGateway builds a Gateway with a real auth.Store (for session
// validation) and a GrantBroker backed by a resolver with NO object store
// configured, so MintRead/MintWrite exercise the local-FS fallback path
// (GrantLocal) without touching the network.
func newPresignGateway(t *testing.T) (*Gateway, string /*token*/, string /*userID*/) {
	t.Helper()
	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)
	g := New(store, mgr, pool)

	resolver := storagepkg.NewResolver(storagepkg.ResolverConfig{
		LocalRoot: t.TempDir(),
	})
	broker := storagepkg.NewGrantBroker(resolver, storagepkg.STSConfig{}, 0)
	g.SetGrantBroker(broker, resolver.BucketFor)
	g.AllowStorage("office", "office/")
	g.AllowStorage("talk", "talk/")
	return g, token, userID
}

func doPresign(g *Gateway, token string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/api/storage/presign", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	g.PresignHandler().ServeHTTP(rec, r)
	return rec
}

func TestPresignHandler_Unauthorized(t *testing.T) {
	g, _, _ := newPresignGateway(t)
	rec := doPresign(g, "not-a-real-token", presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPresignHandler_AppNotPermitted(t *testing.T) {
	g, token, _ := newPresignGateway(t)
	rec := doPresign(g, token, presignRequest{AppID: "notes", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

func TestPresignHandler_NoBrokerConfigured(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, _ := seedSession(t, store)
	g := New(store, mgr, pool)
	g.AllowStorage("office", "office/")
	// SetGrantBroker never called — broker stays nil.
	rec := doPresign(g, token, presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestPresignHandler_InvalidKeyRejected(t *testing.T) {
	g, token, _ := newPresignGateway(t)
	cases := []string{"", "/abs/path", "../escape", "a/../../b", "a/../..", "a\\b"}
	for _, k := range cases {
		rec := doPresign(g, token, presignRequest{AppID: "office", Method: "GET", Key: k})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("key %q: status = %d, want 400, body=%s", k, rec.Code, rec.Body.String())
		}
	}
}

func TestPresignHandler_InvalidMethodRejected(t *testing.T) {
	g, token, _ := newPresignGateway(t)
	rec := doPresign(g, token, presignRequest{AppID: "office", Method: "DELETE", Key: "a.txt"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// BUG FIX regression (2026-07-12, ENTITLE-01 gap): a caller with no
// entitlement for a premium (product-gated) app must not be able to mint
// presigned storage access under that app's prefix by going straight to
// this endpoint — the same gate app-dispatch (Handler()) enforces must
// apply here too.
func TestPresignHandler_EntitlementGating_RefusesWithoutProduct(t *testing.T) {
	g, token, _ := newPresignGateway(t)
	g.AllowApp("office", "office-pro")
	g.SetEntitlementGating(true)

	rec := doPresign(g, token, presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402, body=%s", rec.Code, rec.Body.String())
	}
}

// The same premium app must succeed once the request carries the required
// product entitlement.
func TestPresignHandler_EntitlementGating_AllowsWithProduct(t *testing.T) {
	g, token, _ := newPresignGateway(t)
	g.AllowApp("office", "office-pro")
	g.SetEntitlementGating(true)

	raw, _ := json.Marshal(presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	r := httptest.NewRequest(http.MethodPost, "/api/storage/presign", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Vulos-Entitlements-Products", "office-pro")
	rec := httptest.NewRecorder()
	g.PresignHandler().ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

// Entitlement gating is OFF by default (self-host/standalone) — a
// product-gated app must still be reachable without SetEntitlementGating.
func TestPresignHandler_EntitlementGating_OffByDefault(t *testing.T) {
	g, token, _ := newPresignGateway(t)
	g.AllowApp("office", "office-pro")
	// SetEntitlementGating never called — stays false.

	rec := doPresign(g, token, presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (gating off by default), body=%s", rec.Code, rec.Body.String())
	}
}

func TestPresignHandler_MintsGrant_GETandPUT(t *testing.T) {
	g, token, userID := newPresignGateway(t)

	for _, method := range []string{"GET", "PUT"} {
		rec := doPresign(g, token, presignRequest{AppID: "office", Method: method, Key: "documents/report.docx"})
		if rec.Code != http.StatusOK {
			t.Fatalf("method %s: status = %d, body=%s", method, rec.Code, rec.Body.String())
		}
		var grant storagepkg.ObjectGrant
		if err := json.Unmarshal(rec.Body.Bytes(), &grant); err != nil {
			t.Fatalf("decode grant: %v", err)
		}
		wantKey := userID + "/office/documents/report.docx"
		if grant.Key != wantKey {
			t.Fatalf("method %s: grant.Key = %q, want %q", method, grant.Key, wantKey)
		}
		// No object store configured in this test → local-FS fallback grant,
		// never a raw AccessKey/Secret.
		if grant.Type != storagepkg.GrantLocal {
			t.Fatalf("method %s: grant.Type = %q, want local", method, grant.Type)
		}
		if grant.Creds.AccessKey != "" || grant.Creds.SecretKey != "" {
			t.Fatalf("method %s: presign must never carry a raw bucket-wide credential, got %+v", method, grant.Creds)
		}
	}
}

// SECURITY: app A's presign request can never produce a key under app B's
// prefix — the handler ALWAYS composes "<userID>/<itsOwnPrefix>/<relKey>"
// itself from the app_id's registered prefix, so an app can only ever mint
// grants under its own namespace regardless of what key it asks for.
func TestPresignHandler_CrossAppIsolationByConstruction(t *testing.T) {
	g, token, userID := newPresignGateway(t)

	recOffice := doPresign(g, token, presignRequest{AppID: "office", Method: "PUT", Key: "shared.txt"})
	recTalk := doPresign(g, token, presignRequest{AppID: "talk", Method: "PUT", Key: "shared.txt"})

	var gOffice, gTalk storagepkg.ObjectGrant
	if err := json.Unmarshal(recOffice.Body.Bytes(), &gOffice); err != nil {
		t.Fatalf("decode office grant: %v", err)
	}
	if err := json.Unmarshal(recTalk.Body.Bytes(), &gTalk); err != nil {
		t.Fatalf("decode talk grant: %v", err)
	}

	wantOffice := userID + "/office/shared.txt"
	wantTalk := userID + "/talk/shared.txt"
	if gOffice.Key != wantOffice {
		t.Fatalf("office grant key = %q, want %q", gOffice.Key, wantOffice)
	}
	if gTalk.Key != wantTalk {
		t.Fatalf("talk grant key = %q, want %q", gTalk.Key, wantTalk)
	}
	if gOffice.Key == gTalk.Key {
		t.Fatal("office and talk must never resolve to the same object key")
	}
}

func TestSanitizePresignKey(t *testing.T) {
	valid := map[string]string{
		"a.txt":            "a.txt",
		"dir/a.txt":        "dir/a.txt",
		"./dir/a.txt":      "dir/a.txt",
		"dir/./a.txt":      "dir/a.txt",
	}
	for in, want := range valid {
		got, ok := sanitizePresignKey(in)
		if !ok {
			t.Fatalf("sanitizePresignKey(%q) unexpectedly rejected", in)
		}
		if got != want {
			t.Fatalf("sanitizePresignKey(%q) = %q, want %q", in, got, want)
		}
	}

	invalid := []string{"", "/abs", "../up", "a/../../b", "a/..", "..", "a\\b", "a\x00b"}
	for _, in := range invalid {
		if _, ok := sanitizePresignKey(in); ok {
			t.Fatalf("sanitizePresignKey(%q) unexpectedly accepted", in)
		}
	}
}
