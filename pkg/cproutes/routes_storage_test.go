// routes_storage_test.go — HTTP handler tests for the storage routes.
package cproutes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/billingport"
	"github.com/vul-os/vulos-management/pkg/storage"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// buildAuthStore returns an in-memory auth.Store for tests.
func buildAuthStore(t *testing.T) *auth.Store {
	t.Helper()
	st, err := openAuthStoreForTest("file:memdb_storage_routes?mode=memory&cache=shared", []byte("test-secret"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// sessionFor creates a user + session and returns the session token.
func sessionFor(t *testing.T, st *auth.Store, email string) (userID, token string) {
	t.Helper()
	u, tok, err := st.Signup(context.Background(), email, "password-1234", "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	return u.ID, tok
}

// storageTestEnv holds all test fixtures.
type storageTestEnv struct {
	mux    *http.ServeMux
	svc    *storage.Service
	authSt *auth.Store
	mem    *storage.MemProvider // the underlying provider, for tests that need to seed/inspect objects directly
}

func newStorageTestEnv(t *testing.T) *storageTestEnv {
	t.Helper()
	return newStorageEnvWith(t, nil) // nil resolver: quota enforcement skipped in tests
}

// newStorageEnvWith builds the env with a caller-supplied billing store (the
// Files quota tests need a real one).
//
// Each env gets its OWN VULOS_DB_DIR: registerStorageRoutes opens the Files index
// through the cpdb seam, so without a per-test dir every env would share one
// files.db — and write it into the source tree.
func newStorageEnvWith(t *testing.T, ent billingport.EntitlementResolver) *storageTestEnv {
	t.Helper()
	t.Setenv("VULOS_DB_DIR", t.TempDir())
	authSt := buildAuthStore(t)
	mem := storage.NewMemProvider()
	st := storage.NewMemStore()
	svc := &storage.Service{
		Store: st,
		ProviderForAccount: func(_ context.Context, _ string) (storage.Provider, error) {
			return mem, nil
		},
		// These route tests exercise the managed (cloud-like) path where buckets are
		// provisioned; inject a real provisioner backed by the MemProvider, mirroring
		// how the cloud composition root injects a Tigris-backed StorageProvisioner.
		Provisioner: storage.ProvisionerFromProvider(mem),
	}
	mux := http.NewServeMux()
	RegisterStorage(mux, svc, authSt, ent)
	return &storageTestEnv{mux: mux, svc: svc, authSt: authSt, mem: mem}
}

func (e *storageTestEnv) do(t *testing.T, method, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestStorageRoutes_GetConfig_Unauthenticated(t *testing.T) {
	e := newStorageTestEnv(t)
	w := e.do(t, http.MethodGet, "/api/storage/config?account_id=acct1", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestStorageRoutes_GetConfig_NotFound(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "alice@example.com")
	w := e.do(t, http.MethodGet, "/api/storage/config?account_id="+uid, nil, tok)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStorageRoutes_PutGetDeleteConfig(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "bob@example.com")

	// PUT config
	body := map[string]any{
		"account_id": uid,
		"byo":        false,
		"region":     "auto",
		"bucket":     "vulos-" + strings.ToLower(uid),
	}
	w := e.do(t, http.MethodPost, "/api/storage/config", body, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT config: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var cfg storage.Config
	if err := json.NewDecoder(w.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if cfg.Bucket != "vulos-"+strings.ToLower(uid) {
		t.Errorf("bucket mismatch: got %q", cfg.Bucket)
	}
	if cfg.SecretKey != "" {
		t.Error("SecretKey must not appear in response")
	}

	// GET config
	w = e.do(t, http.MethodGet, "/api/storage/config?account_id="+uid, nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("GET config: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// DELETE config
	w = e.do(t, http.MethodDelete, "/api/storage/config?account_id="+uid, nil, tok)
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE config: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// GET again → 404
	w = e.do(t, http.MethodGet, "/api/storage/config?account_id="+uid, nil, tok)
	if w.Code != http.StatusNotFound {
		t.Errorf("after delete: expected 404, got %d", w.Code)
	}
}

func TestStorageRoutes_SelfOnly(t *testing.T) {
	e := newStorageTestEnv(t)
	_, tok := sessionFor(t, e.authSt, "carol@example.com")

	// Try to access another account's config.
	w := e.do(t, http.MethodGet, "/api/storage/config?account_id=other-account", nil, tok)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for cross-account access, got %d", w.Code)
	}
}

func TestStorageRoutes_PutConfig_BYOInvalidEndpoint(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "dan@example.com")

	body := map[string]any{
		"account_id": uid,
		"byo":        true,
		"endpoint":   "http://insecure.example.com", // not https
		"bucket":     "my-bucket",
	}
	w := e.do(t, http.MethodPost, "/api/storage/config", body, tok)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-https BYO endpoint, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStorageRoutes_PutConfig_BucketRequired(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "eve@example.com")

	body := map[string]any{
		"account_id": uid,
		"byo":        false,
		// no bucket
	}
	w := e.do(t, http.MethodPost, "/api/storage/config", body, tok)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing bucket, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStorageRoutes_Usage_NotFound(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "frank@example.com")

	w := e.do(t, http.MethodGet, "/api/storage/usage?account_id="+uid, nil, tok)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing usage, got %d", w.Code)
	}
}

func TestStorageRoutes_Usage_Found(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "grace@example.com")

	// Insert a usage sample directly via Store.
	_ = e.svc.InsertUsage(context.Background(), storage.UsageSample{
		AccountID:   uid,
		Bucket:      "vulos-bucket",
		SizeBytes:   2048,
		ObjectCount: 3,
		SampledAt:   time.Now().UTC(),
	})

	w := e.do(t, http.MethodGet, "/api/storage/usage?account_id="+uid, nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var sample storage.UsageSample
	if err := json.NewDecoder(w.Body).Decode(&sample); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sample.SizeBytes != 2048 {
		t.Errorf("expected SizeBytes=2048, got %d", sample.SizeBytes)
	}
}

func TestStorageRoutes_PresignGet(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "henry@example.com")

	// The caller's OWN canonical managed bucket must be honored.
	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))
	body := map[string]any{
		"account_id":  uid,
		"bucket":      ownBucket,
		"key":         "path/to/object",
		"ttl_seconds": 60,
	}
	w := e.do(t, http.MethodPost, "/api/storage/presign/get", body, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.URL == "" {
		t.Error("expected non-empty URL")
	}
}

// TestStorageRoutes_PresignForeignBucketDenied is the STORAGE-IDOR regression:
// a managed account passing another tenant's bucket ("vulos-<victimULID>") must
// be refused (403), even though account_id == its own session, because the
// shared managed Tigris credential could otherwise presign ANY vulos-* bucket.
func TestStorageRoutes_PresignForeignBucketDenied(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "henry-idor@example.com")

	victimBucket := "vulos-" + strings.ToLower(boxULID("victim-account-999"))
	for _, op := range []string{"get", "put"} {
		body := map[string]any{
			"account_id":  uid, // own account — passes selfOnly
			"bucket":      victimBucket,
			"key":         "latest.snap.enc",
			"ttl_seconds": 60,
		}
		w := e.do(t, http.MethodPost, "/api/storage/presign/"+op, body, tok)
		if w.Code != http.StatusForbidden {
			t.Fatalf("presign/%s foreign bucket: expected 403, got %d: %s", op, w.Code, w.Body.String())
		}
	}
}

func TestStorageRoutes_PresignPut(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "irene@example.com")

	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))
	body := map[string]any{
		"account_id":  uid,
		"bucket":      ownBucket,
		"key":         "path/to/object",
		"ttl_seconds": 120,
	}
	w := e.do(t, http.MethodPost, "/api/storage/presign/put", body, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.URL == "" {
		t.Error("expected non-empty URL")
	}
}

// TestStorageRoutes_PresignAppScoped_Allowed verifies STORAGE-SCOPING-01: a
// presign request naming a known app_id and a key inside that app's own
// <accountID>/<appID>/ prefix succeeds.
func TestStorageRoutes_PresignAppScoped_Allowed(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "meet-app@example.com")
	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))

	body := map[string]any{
		"account_id":  uid,
		"bucket":      ownBucket,
		"key":         uid + "/meet/doc-1.bin",
		"app_id":      "meet",
		"ttl_seconds": 60,
	}
	w := e.do(t, http.MethodPost, "/api/storage/presign/put", body, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for a key inside the app's own prefix, got %d: %s", w.Code, w.Body.String())
	}
}

// TestStorageRoutes_PresignAppScoped_WrongAppDenied verifies a key that lives
// under a DIFFERENT app's prefix (or outside the account's prefix entirely) is
// refused, even though the bucket + account_id are the caller's own — the
// per-app isolation boundary is the object key, not just the bucket.
func TestStorageRoutes_PresignAppScoped_WrongAppDenied(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "wrong-app@example.com")
	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))

	cases := []struct {
		name string
		key  string
	}{
		{"different app's prefix", uid + "/files/secret.bin"},
		{"bare prefix, no object", uid + "/meet/"},
		{"path traversal escape", uid + "/meet/../files/secret.bin"},
		{"missing account segment", "meet/doc-1.bin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{
				"account_id":  uid,
				"bucket":      ownBucket,
				"key":         tc.key,
				"app_id":      "meet",
				"ttl_seconds": 60,
			}
			w := e.do(t, http.MethodPost, "/api/storage/presign/put", body, tok)
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s: expected 403, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

// TestStorageRoutes_PresignAppScoped_UnknownAppRejected verifies an
// unrecognised app_id is rejected outright (400), never silently ignored —
// STORAGE-SCOPING-01's whitelist can't be bypassed with an arbitrary string.
func TestStorageRoutes_PresignAppScoped_UnknownAppRejected(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "unknown-app@example.com")
	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))

	body := map[string]any{
		"account_id":  uid,
		"bucket":      ownBucket,
		"key":         uid + "/definitely-not-a-real-app/doc.bin",
		"app_id":      "definitely-not-a-real-app",
		"ttl_seconds": 60,
	}
	w := e.do(t, http.MethodPost, "/api/storage/presign/put", body, tok)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown app_id, got %d: %s", w.Code, w.Body.String())
	}
}

// TestStorageRoutes_PresignUnscoped_StillWorks verifies omitting app_id
// entirely (the pre-existing, unscoped presign contract) is unaffected —
// STORAGE-SCOPING-01 is additive/opt-in, not a breaking change for callers
// that haven't adopted per-app prefixes yet.
func TestStorageRoutes_PresignUnscoped_StillWorks(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "unscoped@example.com")
	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))

	body := map[string]any{
		"account_id":  uid,
		"bucket":      ownBucket,
		"key":         "anything/goes/here.bin",
		"ttl_seconds": 60,
	}
	w := e.do(t, http.MethodPost, "/api/storage/presign/put", body, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for an unscoped (no app_id) presign, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// POST /api/storage/delete — scoped server-side object delete (PERFECTION
// PASS 2026-07-12, "scoped object DELETE (cloud)")
// ---------------------------------------------------------------------------

func TestStorageRoutes_Delete_Unauthenticated(t *testing.T) {
	e := newStorageTestEnv(t)
	w := e.do(t, http.MethodPost, "/api/storage/delete", map[string]any{
		"account_id": "acct1", "bucket": "vulos-x", "app_id": "meet", "key": "doc.bin",
	}, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// TestStorageRoutes_Delete_HappyPath verifies a scoped delete actually removes
// the object server-side (the app never sees a presigned delete URL or a raw
// credential) and returns 204.
func TestStorageRoutes_Delete_HappyPath(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "delete-meet@example.com")
	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))

	// Seed the object directly in the provider.
	if err := e.mem.PutObject(context.Background(), ownBucket, uid+"/meet/doc-1.bin", strings.NewReader("hello"), 5); err != nil {
		t.Fatalf("seed object: %v", err)
	}

	w := e.do(t, http.MethodPost, "/api/storage/delete", map[string]any{
		"account_id": uid,
		"bucket":     ownBucket,
		"app_id":     "meet",
		"key":        "doc-1.bin", // relative to <accountID>/meet/
	}, tok)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// The object must actually be gone.
	if _, err := e.mem.GetObject(context.Background(), ownBucket, uid+"/meet/doc-1.bin"); err == nil {
		t.Fatal("expected object to be deleted, but GetObject succeeded")
	}
}

// TestStorageRoutes_Delete_RequiresAppID verifies app_id is mandatory (unlike
// presign, which allows an unscoped legacy request) — delete is destructive
// and this is a brand-new endpoint, so it enforces scoping unconditionally.
func TestStorageRoutes_Delete_RequiresAppID(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "delete-noapp@example.com")
	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))

	w := e.do(t, http.MethodPost, "/api/storage/delete", map[string]any{
		"account_id": uid,
		"bucket":     ownBucket,
		"key":        "doc-1.bin",
	}, tok)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing app_id, got %d: %s", w.Code, w.Body.String())
	}
}

// TestStorageRoutes_Delete_UnknownAppRejected mirrors the presign whitelist
// guard: an unrecognised app_id must be rejected outright (400).
func TestStorageRoutes_Delete_UnknownAppRejected(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "delete-unknownapp@example.com")
	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))

	w := e.do(t, http.MethodPost, "/api/storage/delete", map[string]any{
		"account_id": uid,
		"bucket":     ownBucket,
		"app_id":     "not-a-real-app",
		"key":        "doc-1.bin",
	}, tok)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown app_id, got %d: %s", w.Code, w.Body.String())
	}
}

// TestStorageRoutes_Delete_ForeignBucketDenied is the STORAGE-IDOR regression
// for delete: a caller cannot name another tenant's managed bucket.
func TestStorageRoutes_Delete_ForeignBucketDenied(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "delete-idor@example.com")
	victimBucket := "vulos-" + strings.ToLower(boxULID("victim-account-delete"))

	w := e.do(t, http.MethodPost, "/api/storage/delete", map[string]any{
		"account_id": uid,
		"bucket":     victimBucket,
		"app_id":     "meet",
		"key":        "doc-1.bin",
	}, tok)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a foreign bucket, got %d: %s", w.Code, w.Body.String())
	}
}

// TestStorageRoutes_Delete_TraversalRejected verifies a relative key trying to
// escape the app's own prefix via ".." is refused, and that the object (seeded
// under a DIFFERENT app's prefix) survives untouched.
func TestStorageRoutes_Delete_TraversalRejected(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "delete-traversal@example.com")
	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))

	// A secret belonging to a different app, adjacent to meet/'s prefix.
	victimKey := uid + "/files/secret.bin"
	if err := e.mem.PutObject(context.Background(), ownBucket, victimKey, strings.NewReader("shh"), 3); err != nil {
		t.Fatalf("seed victim object: %v", err)
	}

	cases := []struct {
		name string
		key  string
	}{
		{"path traversal escape", "../files/secret.bin"},
		{"empty relative key", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := e.do(t, http.MethodPost, "/api/storage/delete", map[string]any{
				"account_id": uid,
				"bucket":     ownBucket,
				"app_id":     "meet",
				"key":        tc.key,
			}, tok)
			if tc.key == "" {
				if w.Code != http.StatusBadRequest {
					t.Fatalf("%s: expected 400, got %d: %s", tc.name, w.Code, w.Body.String())
				}
			} else if w.Code != http.StatusForbidden {
				t.Fatalf("%s: expected 403, got %d: %s", tc.name, w.Code, w.Body.String())
			}
		})
	}

	// The other app's object must have survived every attempt.
	if _, err := e.mem.GetObject(context.Background(), ownBucket, victimKey); err != nil {
		t.Fatalf("expected victim object to survive traversal attempts, got: %v", err)
	}
}

// TestStorageRoutes_Delete_NotFoundMapsTo404 verifies deleting an already-gone
// (or never-existing) object maps to 404, not a silent 204 or a 5xx.
func TestStorageRoutes_Delete_NotFoundMapsTo404(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "delete-notfound@example.com")
	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))

	w := e.do(t, http.MethodPost, "/api/storage/delete", map[string]any{
		"account_id": uid,
		"bucket":     ownBucket,
		"app_id":     "meet",
		"key":        "never-existed.bin",
	}, tok)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a non-existent object, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStorageRoutes_SnapshotURL(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "jack@example.com")

	// The caller's OWN canonical box ULID must be accepted.
	path := "/api/storage/snapshot-url?account_id=" + uid + "&ulid=" + boxULID(uid)
	w := e.do(t, http.MethodGet, path, nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(resp.URL, "vulos-") {
		t.Errorf("expected bucket name in snapshot URL, got %q", resp.URL)
	}
}

// TestStorageRoutes_SnapshotForeignULIDDenied is the STORAGE-IDOR regression for
// the snapshot path: a client-supplied ulid for another tenant's box must 403.
func TestStorageRoutes_SnapshotForeignULIDDenied(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "jack-idor@example.com")

	victimULID := boxULID("some-victim-account")
	path := "/api/storage/snapshot-url?account_id=" + uid + "&ulid=" + victimULID
	w := e.do(t, http.MethodGet, path, nil, tok)
	if w.Code != http.StatusForbidden {
		t.Fatalf("snapshot foreign ulid: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStorageRoutes_SnapshotURL_MissingULID(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "kate@example.com")

	w := e.do(t, http.MethodGet, "/api/storage/snapshot-url?account_id="+uid, nil, tok)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing ulid, got %d", w.Code)
	}
}
