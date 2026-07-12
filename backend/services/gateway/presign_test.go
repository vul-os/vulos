package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	storagepkg "vulos/backend/internal/storage"
)

// newPresignGateway builds a Gateway with a real auth.Store (for session
// validation) and a GrantBroker backed by a resolver with NO object store
// configured, so MintRead/MintWrite exercise the local-FS fallback path
// (GrantLocal) without touching the network. It also mints app secrets for
// "office" and "talk" via GenerateAppSecret — exactly what main.go does at
// POST /api/apps/launch — so tests can present the PRESIGN-02 app-identity
// proof (X-Vulos-App-Secret) the same way a real launched app would.
func newPresignGateway(t *testing.T) (g *Gateway, token string, userID string, secrets map[string]string) {
	t.Helper()
	store, mgr, pool := newTestDeps(t)
	token, userID = seedSession(t, store)
	g = New(store, mgr, pool)

	resolver := storagepkg.NewResolver(storagepkg.ResolverConfig{
		LocalRoot: t.TempDir(),
	})
	broker := storagepkg.NewGrantBroker(resolver, storagepkg.STSConfig{}, 0)
	g.SetGrantBroker(broker, resolver.BucketFor)
	g.AllowStorage("office", "office/")
	g.AllowStorage("talk", "talk/")
	secrets = map[string]string{
		"office": g.GenerateAppSecret("office"),
		"talk":   g.GenerateAppSecret("talk"),
	}
	return g, token, userID, secrets
}

// doPresign issues a presign request authenticated as the session (token) and
// carrying appSecret as X-Vulos-App-Secret (pass "" to omit the header
// entirely, e.g. to exercise the PRESIGN-02 rejection path).
func doPresign(g *Gateway, token, appSecret string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/api/storage/presign", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	if appSecret != "" {
		r.Header.Set("X-Vulos-App-Secret", appSecret)
	}
	rec := httptest.NewRecorder()
	g.PresignHandler().ServeHTTP(rec, r)
	return rec
}

func TestPresignHandler_Unauthorized(t *testing.T) {
	g, _, _, secrets := newPresignGateway(t)
	rec := doPresign(g, "not-a-real-token", secrets["office"], presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPresignHandler_AppNotPermitted(t *testing.T) {
	g, token, _, _ := newPresignGateway(t)
	// "notes" was never AllowStorage'd, but it DOES hold a genuine secret for
	// itself, so this test isolates the "not permitted to use storage" 403
	// from the (also 403) "unproven identity" rejection covered separately.
	notesSecret := g.GenerateAppSecret("notes")
	rec := doPresign(g, token, notesSecret, presignRequest{AppID: "notes", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

func TestPresignHandler_NoBrokerConfigured(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, _ := seedSession(t, store)
	g := New(store, mgr, pool)
	g.AllowStorage("office", "office/")
	officeSecret := g.GenerateAppSecret("office")
	// SetGrantBroker never called — broker stays nil.
	rec := doPresign(g, token, officeSecret, presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestPresignHandler_InvalidKeyRejected(t *testing.T) {
	g, token, _, secrets := newPresignGateway(t)
	cases := []string{"", "/abs/path", "../escape", "a/../../b", "a/../..", "a\\b"}
	for _, k := range cases {
		rec := doPresign(g, token, secrets["office"], presignRequest{AppID: "office", Method: "GET", Key: k})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("key %q: status = %d, want 400, body=%s", k, rec.Code, rec.Body.String())
		}
	}
}

func TestPresignHandler_InvalidMethodRejected(t *testing.T) {
	g, token, _, secrets := newPresignGateway(t)
	rec := doPresign(g, token, secrets["office"], presignRequest{AppID: "office", Method: "DELETE", Key: "a.txt"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// SECURITY (PRESIGN-02): a request that names an app_id in its body but
// presents NO X-Vulos-App-Secret at all must be refused outright — the
// client-supplied app_id is never trusted on its own.
func TestPresignHandler_MissingAppSecretRejected(t *testing.T) {
	g, token, _, _ := newPresignGateway(t)
	rec := doPresign(g, token, "", presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

// SECURITY (PRESIGN-02): a wrong (or another app's) secret must be refused,
// not silently accepted just because SOME app secret was supplied.
func TestPresignHandler_WrongAppSecretRejected(t *testing.T) {
	g, token, _, _ := newPresignGateway(t)
	rec := doPresign(g, token, "totally-not-a-real-secret", presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rec.Code, rec.Body.String())
	}
}

// SECURITY (PRESIGN-02, the CONFIRMED audit finding): the app that holds
// talk's own secret must NEVER be able to mint a grant under office's prefix
// by simply naming app_id="office" in its request body — the secret it holds
// only proves it is "talk", so a mismatched app_id claim must be rejected
// even though the caller is a genuinely-running, legitimately-secreted app.
func TestPresignHandler_CannotClaimAnotherAppWithOwnSecret(t *testing.T) {
	g, token, _, secrets := newPresignGateway(t)
	rec := doPresign(g, token, secrets["talk"], presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (talk's secret must not unlock office's prefix), body=%s", rec.Code, rec.Body.String())
	}
}

// BUG FIX regression (2026-07-12, ENTITLE-01 gap): a caller with no
// entitlement for a premium (product-gated) app must not be able to mint
// presigned storage access under that app's prefix by going straight to
// this endpoint — the same gate app-dispatch (Handler()) enforces must
// apply here too.
func TestPresignHandler_EntitlementGating_RefusesWithoutProduct(t *testing.T) {
	g, token, _, secrets := newPresignGateway(t)
	g.AllowApp("office", "office-pro")
	g.SetEntitlementGating(true)

	rec := doPresign(g, token, secrets["office"], presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402, body=%s", rec.Code, rec.Body.String())
	}
}

// The same premium app must succeed once the request carries the required
// product entitlement.
func TestPresignHandler_EntitlementGating_AllowsWithProduct(t *testing.T) {
	g, token, _, secrets := newPresignGateway(t)
	g.AllowApp("office", "office-pro")
	g.SetEntitlementGating(true)

	raw, _ := json.Marshal(presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	r := httptest.NewRequest(http.MethodPost, "/api/storage/presign", bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Vulos-App-Secret", secrets["office"])
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
	g, token, _, secrets := newPresignGateway(t)
	g.AllowApp("office", "office-pro")
	// SetEntitlementGating never called — stays false.

	rec := doPresign(g, token, secrets["office"], presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (gating off by default), body=%s", rec.Code, rec.Body.String())
	}
}

func TestPresignHandler_MintsGrant_GETandPUT(t *testing.T) {
	g, token, userID, secrets := newPresignGateway(t)

	for _, method := range []string{"GET", "PUT"} {
		rec := doPresign(g, token, secrets["office"], presignRequest{AppID: "office", Method: method, Key: "documents/report.docx"})
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
// grants under its own namespace regardless of what key it asks for. Each
// call here authenticates with THAT app's own secret (PRESIGN-02), which is
// what makes the app_id claim trustworthy in the first place.
func TestPresignHandler_CrossAppIsolationByConstruction(t *testing.T) {
	g, token, userID, secrets := newPresignGateway(t)

	recOffice := doPresign(g, token, secrets["office"], presignRequest{AppID: "office", Method: "PUT", Key: "shared.txt"})
	recTalk := doPresign(g, token, secrets["talk"], presignRequest{AppID: "talk", Method: "PUT", Key: "shared.txt"})

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

// SECURITY (PRESIGN-02 defense in depth): when the request also carries a
// recognisable app subdomain (NET-01), it must agree with the claimed
// app_id — a mismatch is rejected even with a genuine app secret.
func TestPresignHandler_SubdomainMismatchRejected(t *testing.T) {
	os.Setenv("VULOS_DOMAIN", "example.com")
	defer os.Unsetenv("VULOS_DOMAIN")

	g, token, _, secrets := newPresignGateway(t)
	raw, _ := json.Marshal(presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	r := httptest.NewRequest(http.MethodPost, "/api/storage/presign", bytes.NewReader(raw))
	r.Host = "talk.example.com"
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Vulos-App-Secret", secrets["office"])
	rec := httptest.NewRecorder()
	g.PresignHandler().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (host says talk, app_id claims office), body=%s", rec.Code, rec.Body.String())
	}
}

// Sanity: a matching subdomain + valid secret still succeeds.
func TestPresignHandler_SubdomainMatchAllowed(t *testing.T) {
	os.Setenv("VULOS_DOMAIN", "example.com")
	defer os.Unsetenv("VULOS_DOMAIN")

	g, token, _, secrets := newPresignGateway(t)
	raw, _ := json.Marshal(presignRequest{AppID: "office", Method: "GET", Key: "a.txt"})
	r := httptest.NewRequest(http.MethodPost, "/api/storage/presign", bytes.NewReader(raw))
	r.Host = "office.example.com"
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Vulos-App-Secret", secrets["office"])
	rec := httptest.NewRecorder()
	g.PresignHandler().ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

func TestSanitizePresignKey(t *testing.T) {
	valid := map[string]string{
		"a.txt":       "a.txt",
		"dir/a.txt":   "dir/a.txt",
		"./dir/a.txt": "dir/a.txt",
		"dir/./a.txt": "dir/a.txt",
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
