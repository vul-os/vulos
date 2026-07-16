package appsplatform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAdapter is a minimal Office-like product adapter for tests.
type fakeAdapter struct{ acted int }

func (f *fakeAdapter) Product() string { return ProductOffice }

func (f *fakeAdapter) RequiredScope(actionOrKind string) string {
	switch actionOrKind {
	case "message.post":
		return ScopeAppsWrite
	case "history":
		return ScopeAppsRead
	default:
		return ""
	}
}

func (f *fakeAdapter) CanAccessTarget(app *App, target string) (bool, bool) {
	switch target {
	case "missing":
		return false, false // 404
	case "secret":
		return false, true // 403
	default:
		return true, true
	}
}

func (f *fakeAdapter) Act(_ context.Context, app *App, req ActionRequest, emit EmitFunc) (any, error) {
	f.acted++
	if emit != nil {
		emit(EventMessageCreated, map[string]any{"target": req.Target}, nil)
	}
	return map[string]any{"posted": true, "target": req.Target}, nil
}

func (f *fakeAdapter) Read(_ context.Context, app *App, req ReadRequest) (any, error) {
	return map[string]any{"kind": req.Kind, "params": req.Params}, nil
}

// header-based admin auth: "X-User: <id>" and "X-Admin: 1".
func headerAdmin(r *http.Request) (string, bool, bool) {
	u := r.Header.Get("X-User")
	if u == "" {
		return "", false, false
	}
	return u, r.Header.Get("X-Admin") == "1", true
}

func newTestHandler(t *testing.T, product string) (*Handler, Registry, *fakeAdapter) {
	t.Helper()
	reg := NewMemoryRegistry()
	ad := &fakeAdapter{}
	var p ProductAdapter = ad
	if product == ProductBoard {
		p = boardAdapter{}
	}
	h, err := NewHandler(MountConfig{
		Adapter:    p,
		Registry:   reg,
		Dispatcher: NewDispatcher(reg, p.Product()),
		Admin:      headerAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, reg, ad
}

type boardAdapter struct{}

func (boardAdapter) Product() string                           { return ProductBoard }
func (boardAdapter) RequiredScope(string) string               { return "" }
func (boardAdapter) CanAccessTarget(*App, string) (bool, bool) { return true, true }
func (boardAdapter) Act(context.Context, *App, ActionRequest, EmitFunc) (any, error) {
	return nil, nil
}
func (boardAdapter) Read(context.Context, *App, ReadRequest) (any, error) { return nil, nil }

func do(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAdminCreateListGetDelete(t *testing.T) {
	h, _, _ := newTestHandler(t, ProductOffice)
	alice := map[string]string{"X-User": "alice"}

	// Create.
	w := do(h, "POST", "/api/apps", `{"name":"Echo","scopes":["apps:write"],"description":"echoes"}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("create code %d: %s", w.Code, w.Body)
	}
	var created struct {
		App           Summary `json:"app"`
		Token         string  `json:"token"`
		SigningSecret string  `json:"signing_secret"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	if created.Token == "" || created.SigningSecret == "" {
		t.Fatal("secrets not returned at create")
	}
	if !contains(created.App.Products, ProductOffice) {
		t.Fatalf("create should default-target the hosting product: %v", created.App.Products)
	}
	// Secrets must never appear in the summary JSON.
	if strings.Contains(string(mustJSON(created.App)), "vat_") || strings.Contains(string(mustJSON(created.App)), "vas_") {
		t.Fatal("summary leaked a secret")
	}
	id := created.App.ID

	// List (alice sees her own).
	w = do(h, "GET", "/api/apps", "", alice)
	var list []Summary
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("expected 1 app, got %d", len(list))
	}

	// Cross-owner GET is a 404 (no existence leak).
	w = do(h, "GET", "/api/apps/"+id, "", map[string]string{"X-User": "bob"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-owner get should 404, got %d", w.Code)
	}

	// Delete.
	w = do(h, "DELETE", "/api/apps/"+id, "", alice)
	if w.Code != http.StatusOK {
		t.Fatalf("delete code %d", w.Code)
	}
}

func TestListFiltersByProduct(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	// A board-only app must not appear in Office's GET /api/apps.
	reg.Create(CreateParams{Name: "boarder", OwnerID: "alice", Products: []string{ProductBoard}})
	reg.Create(CreateParams{Name: "officer", OwnerID: "alice", Products: []string{ProductOffice}})
	w := do(h, "GET", "/api/apps", "", map[string]string{"X-User": "alice", "X-Admin": "1"})
	var list []Summary
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Name != "officer" {
		t.Fatalf("product filter failed: %+v", list)
	}
}

func TestAdminUnauthenticated(t *testing.T) {
	h, _, _ := newTestHandler(t, ProductOffice)
	if w := do(h, "GET", "/api/apps", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", w.Code)
	}
}

func TestRuntimeAuthTestAndTokenReject(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	c, _ := reg.Create(CreateParams{Name: "x", OwnerID: "a", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})

	w := do(h, "GET", "/api/apps/v1/auth.test", "", map[string]string{"Authorization": "Bearer " + c.Token})
	if w.Code != http.StatusOK {
		t.Fatalf("auth.test code %d: %s", w.Code, w.Body)
	}
	// Bad token → 401.
	if w := do(h, "GET", "/api/apps/v1/auth.test", "", map[string]string{"Authorization": "Bearer vat_nope"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad token should 401, got %d", w.Code)
	}
	// Missing token → 401.
	if w := do(h, "GET", "/api/apps/v1/auth.test", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing token should 401, got %d", w.Code)
	}
}

func TestRuntimeScopeAndTargetEnforcement(t *testing.T) {
	h, reg, ad := newTestHandler(t, ProductOffice)
	// App without apps:write.
	noScope, _ := reg.Create(CreateParams{Name: "n", OwnerID: "a", Products: []string{ProductOffice}})
	bearer := func(tok string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + tok, "Content-Type": "application/json"}
	}
	w := do(h, "POST", "/api/apps/v1/act", `{"action":"message.post","target":"general"}`, bearer(noScope.Token))
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing scope should 403, got %d: %s", w.Code, w.Body)
	}

	// App with apps:write.
	ok, _ := reg.Create(CreateParams{Name: "y", OwnerID: "a", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})
	w = do(h, "POST", "/api/apps/v1/act", `{"action":"message.post","target":"general"}`, bearer(ok.Token))
	if w.Code != http.StatusOK || ad.acted != 1 {
		t.Fatalf("act should succeed, got %d acted=%d: %s", w.Code, ad.acted, w.Body)
	}
	// Target visibility: secret → 403, missing → 404.
	if w := do(h, "POST", "/api/apps/v1/act", `{"action":"message.post","target":"secret"}`, bearer(ok.Token)); w.Code != http.StatusForbidden {
		t.Fatalf("inaccessible target should 403, got %d", w.Code)
	}
	if w := do(h, "POST", "/api/apps/v1/act", `{"action":"message.post","target":"missing"}`, bearer(ok.Token)); w.Code != http.StatusNotFound {
		t.Fatalf("unknown target should 404, got %d", w.Code)
	}
}

func TestRuntimeProductTargetingRejected(t *testing.T) {
	// Mount is Office; an app that targets only Board must be 403 at runtime.
	h, reg, _ := newTestHandler(t, ProductOffice)
	c, _ := reg.Create(CreateParams{Name: "m", OwnerID: "a", Products: []string{ProductBoard}})
	w := do(h, "GET", "/api/apps/v1/auth.test", "", map[string]string{"Authorization": "Bearer " + c.Token})
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-product token should 403, got %d: %s", w.Code, w.Body)
	}
}

func TestIncomingWebhook(t *testing.T) {
	h, reg, ad := newTestHandler(t, ProductOffice)
	c, _ := reg.Create(CreateParams{Name: "hook", OwnerID: "a", Products: []string{ProductOffice}, DefaultTarget: "general", IncomingEnabled: true})
	w := do(h, "POST", "/api/apps/hooks/"+c.App.Incoming.ID, `{"text":"ci done"}`, map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusCreated || ad.acted != 1 {
		t.Fatalf("incoming webhook failed: code %d acted %d: %s", w.Code, ad.acted, w.Body)
	}
	// Unknown webhook id → 404.
	if w := do(h, "POST", "/api/apps/hooks/deadbeef", `{"text":"x"}`, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown webhook should 404, got %d", w.Code)
	}
	// Disabled webhook → 404.
	reg.Update(c.App.ID, UpdateParams{IncomingEnabled: ptr(false)})
	if w := do(h, "POST", "/api/apps/hooks/"+c.App.Incoming.ID, `{"text":"x"}`, nil); w.Code != http.StatusNotFound {
		t.Fatalf("disabled webhook should 404, got %d", w.Code)
	}
}

func TestRotateTokenEndpoint(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	c, _ := reg.Create(CreateParams{Name: "r", OwnerID: "alice", Products: []string{ProductOffice}})
	w := do(h, "POST", "/api/apps/"+c.App.ID+"/rotate/token", "", map[string]string{"X-User": "alice"})
	if w.Code != http.StatusOK {
		t.Fatalf("rotate-token code %d", w.Code)
	}
	var resp struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Token == "" || resp.Token == c.Token {
		t.Fatal("rotate-token should return a new token")
	}
	// Old token now rejected at runtime.
	if w := do(h, "GET", "/api/apps/v1/auth.test", "", map[string]string{"Authorization": "Bearer " + c.Token}); w.Code != http.StatusUnauthorized {
		t.Fatalf("old token should be invalid, got %d", w.Code)
	}
}

// TestHTTPCreateTokenTTL verifies the full HTTP path for token_ttl: a token
// created with a short TTL via the management API must be rejected by the
// runtime token-auth middleware once the TTL has elapsed.
func TestHTTPCreateTokenTTL(t *testing.T) {
	h, _, _ := newTestHandler(t, ProductOffice)
	alice := map[string]string{"X-User": "alice", "Content-Type": "application/json"}

	// Create an app with a 1-second TTL via the HTTP management API.
	w := do(h, "POST", "/api/apps", `{"name":"ttl-app","token_ttl":1}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("create code %d: %s", w.Code, w.Body)
	}
	var created struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	if created.Token == "" {
		t.Fatal("no token in create response")
	}

	// Token should be accepted immediately.
	if w := do(h, "GET", "/api/apps/v1/auth.test", "", map[string]string{"Authorization": "Bearer " + created.Token}); w.Code != http.StatusOK {
		t.Fatalf("token should be valid immediately after create, got %d: %s", w.Code, w.Body)
	}

	// Wait for the 1-second TTL to elapse (give it 200 ms of headroom).
	time.Sleep(1200 * time.Millisecond)

	// Token must now be rejected with 401 "token expired".
	w = do(h, "GET", "/api/apps/v1/auth.test", "", map[string]string{"Authorization": "Bearer " + created.Token})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired token should be 401, got %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "expired") {
		t.Fatalf("response should mention expiry, got: %s", w.Body)
	}
}

// testEncryptor returns a deterministic AESGCMEncryptor suitable for tests that
// exercise the SQLite persistence path. Any test that calls NewStandaloneRegistry
// (which now refuses to store signing secrets in cleartext) must pass this
// encryptor via WithSigningSecretEncryptor.
func testEncryptor(t *testing.T) *AESGCMEncryptor {
	t.Helper()
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	enc, err := NewAESGCMEncryptor(kek)
	if err != nil {
		t.Fatalf("testEncryptor: %v", err)
	}
	return enc
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
