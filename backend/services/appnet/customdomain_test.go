package appnet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// CustomDomainStore unit tests
// ---------------------------------------------------------------------------

func newTestCustomDomainStore(t *testing.T) *CustomDomainStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "custom_domains.json")
	cs, err := NewCustomDomainStoreAt(path)
	if err != nil {
		t.Fatalf("NewCustomDomainStoreAt: %v", err)
	}
	return cs
}

func TestCustomDomainStore_SetAndGet(t *testing.T) {
	cs := newTestCustomDomainStore(t)
	cd := &CustomDomain{
		AppID:          "myapp",
		Domain:         "mysite.example.com",
		ChallengeToken: "abc123",
		Status:         CustomDomainStatusPending,
		CreatedAt:      time.Now().UTC(),
	}
	if err := cs.Set(cd); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got := cs.Get("myapp")
	if got == nil {
		t.Fatal("Get returned nil after Set")
	}
	if got.Domain != "mysite.example.com" {
		t.Errorf("Domain = %q", got.Domain)
	}
	if got.Status != CustomDomainStatusPending {
		t.Errorf("Status = %q", got.Status)
	}
}

func TestCustomDomainStore_GetMissing(t *testing.T) {
	cs := newTestCustomDomainStore(t)
	if got := cs.Get("nope"); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestCustomDomainStore_Delete(t *testing.T) {
	cs := newTestCustomDomainStore(t)
	_ = cs.Set(&CustomDomain{AppID: "app", Domain: "a.example.com", ChallengeToken: "tok", Status: CustomDomainStatusPending, CreatedAt: time.Now()})
	if err := cs.Delete("app"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := cs.Get("app"); got != nil {
		t.Errorf("expected nil after Delete, got %+v", got)
	}
}

func TestCustomDomainStore_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cd.json")
	cs1, _ := NewCustomDomainStoreAt(path)
	_ = cs1.Set(&CustomDomain{
		AppID:          "notes",
		Domain:         "notes.mysite.com",
		ChallengeToken: "tok42",
		Status:         CustomDomainStatusVerified,
		CreatedAt:      time.Now().UTC(),
	})
	cs2, err := NewCustomDomainStoreAt(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	got := cs2.Get("notes")
	if got == nil || got.Domain != "notes.mysite.com" {
		t.Errorf("round-trip domain mismatch: %+v", got)
	}
	if got.Status != CustomDomainStatusVerified {
		t.Errorf("round-trip status = %q, want verified", got.Status)
	}
}

// ---------------------------------------------------------------------------
// generateChallengeToken
// ---------------------------------------------------------------------------

func TestGenerateChallengeToken(t *testing.T) {
	tok, err := generateChallengeToken()
	if err != nil {
		t.Fatalf("generateChallengeToken: %v", err)
	}
	if len(tok) != 64 {
		t.Errorf("token length = %d, want 64 hex chars", len(tok))
	}
	tok2, _ := generateChallengeToken()
	if tok == tok2 {
		t.Error("two generated tokens are identical (collision)")
	}
}

// ---------------------------------------------------------------------------
// TXTRecordName
// ---------------------------------------------------------------------------

func TestCustomDomain_TXTRecordName(t *testing.T) {
	cd := &CustomDomain{Domain: "mysite.example.com"}
	if got := cd.TXTRecordName(); got != "_vulos-verify.mysite.example.com" {
		t.Errorf("TXTRecordName = %q", got)
	}
}

// ---------------------------------------------------------------------------
// VerifyDNSTXT
// ---------------------------------------------------------------------------

func TestVerifyDNSTXT_Match(t *testing.T) {
	// Stub the DNS lookup to return the expected token.
	orig := dnsLookupTXT
	defer func() { dnsLookupTXT = orig }()
	dnsLookupTXT = func(name string) ([]string, error) {
		if name == "_vulos-verify.mysite.example.com" {
			return []string{"sometoken123"}, nil
		}
		return nil, nil
	}
	if err := VerifyDNSTXT("mysite.example.com", "sometoken123"); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

func TestVerifyDNSTXT_NoMatch(t *testing.T) {
	orig := dnsLookupTXT
	defer func() { dnsLookupTXT = orig }()
	dnsLookupTXT = func(name string) ([]string, error) {
		return []string{"wrongtoken"}, nil
	}
	if err := VerifyDNSTXT("mysite.example.com", "expectedtoken"); err == nil {
		t.Error("expected error for non-matching token, got nil")
	}
}

func TestVerifyDNSTXT_LookupError(t *testing.T) {
	orig := dnsLookupTXT
	defer func() { dnsLookupTXT = orig }()
	dnsLookupTXT = func(name string) ([]string, error) {
		return nil, &dnsError{msg: "no such host"}
	}
	if err := VerifyDNSTXT("nxdomain.example.com", "tok"); err == nil {
		t.Error("expected error on DNS lookup failure, got nil")
	}
}

// dnsError is a minimal error type used in tests to avoid importing net.
type dnsError struct{ msg string }

func (e *dnsError) Error() string { return e.msg }

// ---------------------------------------------------------------------------
// Caddy snippet
// ---------------------------------------------------------------------------

func TestWriteCustomDomainCaddySnippet(t *testing.T) {
	dir := t.TempDir()
	if err := writeCustomDomainCaddySnippet(dir, "myapp", "mysite.example.com", "127.0.0.1:9000"); err != nil {
		t.Fatalf("writeCustomDomainCaddySnippet: %v", err)
	}
	path := filepath.Join(dir, "myapp--custom.caddy")
	data, err := readFileString(path)
	if err != nil {
		t.Fatalf("read snippet: %v", err)
	}
	if !strings.Contains(data, "mysite.example.com") {
		t.Errorf("snippet missing domain: %s", data)
	}
	if !strings.Contains(data, "reverse_proxy 127.0.0.1:9000") {
		t.Errorf("snippet missing upstream: %s", data)
	}
	if !strings.Contains(data, "tls") {
		t.Errorf("snippet missing tls directive: %s", data)
	}
}

func TestWriteCustomDomainCaddySnippet_Noop(t *testing.T) {
	// "noop" dir must not create files or return error.
	if err := writeCustomDomainCaddySnippet("noop", "app", "d.com", "127.0.0.1:1"); err != nil {
		t.Errorf("noop: unexpected error: %v", err)
	}
}

func TestRemoveCustomDomainCaddySnippet_Idempotent(t *testing.T) {
	dir := t.TempDir()
	// Remove on a file that does not exist must not error.
	if err := removeCustomDomainCaddySnippet(dir, "nope"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// readFileString reads a file and returns its contents as a string.
func readFileString(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// newCustomDomainTestEnv creates an in-process test environment with stub DNS.
func newCustomDomainTestEnv(t *testing.T) (*http.ServeMux, *CustomDomainStore, *Provisioner) {
	t.Helper()
	t.Setenv("VULOS_DNS_API", "noop")
	t.Setenv("VULOS_INSTANCE_ID", "testulid")
	t.Setenv("VULOS_BASE_DOMAIN", "vulos.org")
	t.Setenv("VULOS_CADDY_DIR", "noop")

	dir := t.TempDir()
	ds, _ := NewDeploymentStoreAt(filepath.Join(dir, "dep.json"))
	cs, _ := NewCustomDomainStoreAt(filepath.Join(dir, "cd.json"))
	p := NewProvisioner(ds, nil)

	mux := http.NewServeMux()
	RegisterCustomDomainHandlers(mux, cs, p, nil)
	return mux, cs, p
}

// preProvision seeds a deployment so custom-domain handlers accept the app.
func preProvision(t *testing.T, p *Provisioner, appID string) {
	t.Helper()
	if _, err := p.ProvisionSubdomain(context.Background(), appID, "default", "127.0.0.1:9000"); err != nil {
		t.Fatalf("preProvision: %v", err)
	}
}

func TestCustomDomainAPI_Post_MissingDomain(t *testing.T) {
	mux, _, p := newCustomDomainTestEnv(t)
	preProvision(t, p, "calc")

	req := httptest.NewRequest(http.MethodPost, "/api/apps/calc/domain", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCustomDomainAPI_Post_AppNotDeployed(t *testing.T) {
	mux, _, _ := newCustomDomainTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/apps/unknown/domain",
		strings.NewReader(`{"domain":"x.example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}

func TestCustomDomainAPI_Post_IssuedChallenge(t *testing.T) {
	mux, _, p := newCustomDomainTestEnv(t)
	preProvision(t, p, "notes")

	req := httptest.NewRequest(http.MethodPost, "/api/apps/notes/domain",
		strings.NewReader(`{"domain":"mysite.example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body)
	}
	var resp customDomainResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != CustomDomainStatusPending {
		t.Errorf("status = %q, want pending", resp.Status)
	}
	if resp.Domain != "mysite.example.com" {
		t.Errorf("domain = %q", resp.Domain)
	}
	if len(resp.ChallengeToken) != 64 {
		t.Errorf("challenge_token length = %d, want 64", len(resp.ChallengeToken))
	}
	if resp.TXTRecord != "_vulos-verify.mysite.example.com" {
		t.Errorf("txt_record = %q", resp.TXTRecord)
	}
}

func TestCustomDomainAPI_Get_NotFound(t *testing.T) {
	mux, _, _ := newCustomDomainTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/apps/nope/domain", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCustomDomainAPI_Get_Found(t *testing.T) {
	mux, cs, p := newCustomDomainTestEnv(t)
	preProvision(t, p, "browser")

	// Seed a domain entry manually.
	_ = cs.Set(&CustomDomain{
		AppID:          "browser",
		Domain:         "browse.myco.io",
		ChallengeToken: "tok999",
		Status:         CustomDomainStatusPending,
		CreatedAt:      time.Now().UTC(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/apps/browser/domain", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body)
	}
	var resp customDomainResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Domain != "browse.myco.io" {
		t.Errorf("domain = %q", resp.Domain)
	}
}

func TestCustomDomainAPI_Verify_Pending_DNSFail(t *testing.T) {
	// Stub DNS to return no records.
	orig := dnsLookupTXT
	defer func() { dnsLookupTXT = orig }()
	dnsLookupTXT = func(_ string) ([]string, error) { return nil, nil }

	mux, cs, p := newCustomDomainTestEnv(t)
	preProvision(t, p, "app")
	_ = cs.Set(&CustomDomain{
		AppID:          "app",
		Domain:         "app.example.com",
		ChallengeToken: "correcttoken",
		Status:         CustomDomainStatusPending,
		CreatedAt:      time.Now().UTC(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/apps/app/domain/verify", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
	var body map[string]string
	json.NewDecoder(rr.Body).Decode(&body)
	if body["status"] != CustomDomainStatusPending {
		t.Errorf("body status = %q, want pending", body["status"])
	}
}

func TestCustomDomainAPI_Verify_Success(t *testing.T) {
	// Stub DNS to return the exact challenge token.
	orig := dnsLookupTXT
	defer func() { dnsLookupTXT = orig }()
	token := "verifiedtoken12345"
	dnsLookupTXT = func(name string) ([]string, error) {
		return []string{token}, nil
	}

	mux, cs, p := newCustomDomainTestEnv(t)
	preProvision(t, p, "shop")
	_ = cs.Set(&CustomDomain{
		AppID:          "shop",
		Domain:         "shop.myco.io",
		ChallengeToken: token,
		Status:         CustomDomainStatusPending,
		CreatedAt:      time.Now().UTC(),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/apps/shop/domain/verify", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body)
	}
	var resp customDomainResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Status != CustomDomainStatusVerified {
		t.Errorf("status = %q, want verified", resp.Status)
	}
	if resp.VerifiedAt == "" {
		t.Error("verified_at should be set")
	}

	// Confirm persistence.
	stored := cs.Get("shop")
	if stored == nil || stored.Status != CustomDomainStatusVerified {
		t.Errorf("stored status = %q, want verified", stored.Status)
	}
}

// TestCustomDomainAPI_Verify_ProxyWriteFailsClosed is the wave-34 fail-open
// regression: DNS verification succeeds but the Caddy (proxy-config) snippet
// write fails. The domain would never route, so the handler must NOT flip
// status to Verified — it must return an error and leave status untouched.
func TestCustomDomainAPI_Verify_ProxyWriteFailsClosed(t *testing.T) {
	orig := dnsLookupTXT
	defer func() { dnsLookupTXT = orig }()
	token := "verifiedtoken12345"
	dnsLookupTXT = func(name string) ([]string, error) {
		return []string{token}, nil
	}

	// Seed the deployment + domain under a working (noop) caddy dir.
	mux, cs, p := newCustomDomainTestEnv(t)
	preProvision(t, p, "shop")
	_ = cs.Set(&CustomDomain{
		AppID:          "shop",
		Domain:         "shop.myco.io",
		ChallengeToken: token,
		Status:         CustomDomainStatusPending,
		CreatedAt:      time.Now().UTC(),
	})

	// Now point the caddy dir (read at handler time via caddyDirFromEnv) at an
	// unwritable path so the snippet write fails.
	base := t.TempDir()
	notADir := filepath.Join(base, "iamafile")
	if err := os.WriteFile(notADir, []byte("x"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Setenv("VULOS_CADDY_DIR", filepath.Join(notADir, "caddy"))

	req := httptest.NewRequest(http.MethodPost, "/api/apps/shop/domain/verify", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code == http.StatusOK {
		t.Fatalf("expected non-200 on proxy-config write failure, got 200 (fail-open): %s", rr.Body)
	}

	// Status must NOT have advanced to Verified.
	stored := cs.Get("shop")
	if stored == nil {
		t.Fatal("domain record vanished")
	}
	if stored.Status == CustomDomainStatusVerified {
		t.Errorf("status advanced to Verified despite proxy-config write failure (fail-open)")
	}
}

func TestCustomDomainAPI_Verify_AlreadyVerified_Idempotent(t *testing.T) {
	mux, cs, p := newCustomDomainTestEnv(t)
	preProvision(t, p, "blog")
	now := time.Now().UTC()
	_ = cs.Set(&CustomDomain{
		AppID:          "blog",
		Domain:         "blog.myco.io",
		ChallengeToken: "tok",
		Status:         CustomDomainStatusVerified,
		CreatedAt:      now,
		VerifiedAt:     now,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/apps/blog/domain/verify", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	// Already verified → 200 idempotent response.
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestCustomDomainAPI_Delete(t *testing.T) {
	mux, cs, p := newCustomDomainTestEnv(t)
	preProvision(t, p, "wiki")
	_ = cs.Set(&CustomDomain{
		AppID:          "wiki",
		Domain:         "wiki.myco.io",
		ChallengeToken: "tok",
		Status:         CustomDomainStatusPending,
		CreatedAt:      time.Now().UTC(),
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/apps/wiki/domain", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body)
	}
	if got := cs.Get("wiki"); got != nil {
		t.Errorf("domain still present after DELETE: %+v", got)
	}
}

func TestCustomDomainAPI_Delete_NotFound(t *testing.T) {
	mux, _, _ := newCustomDomainTestEnv(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/apps/unknown/domain", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}
