// routes_account_export_test.go — tests for BYO bucket routes + export bundle.
package cproutes

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/storage"
)

// ---------------------------------------------------------------------------
// BYO bucket routes
// ---------------------------------------------------------------------------

func TestBYO_Connect_Validate_Disconnect(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "byo-alice@example.com")

	// Substitute the BYO provider factory with a MemProvider that already has
	// the user's bucket — so live validation (ListBucket) succeeds without a
	// network call.
	mem := storage.NewMemProvider()
	if err := mem.EnsureBucket(context.Background(), "alice-data"); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}
	restore := storage.SetBYOProviderFactoryForTest(func(_ storage.Config) (storage.Provider, error) {
		return mem, nil
	})
	defer restore()

	// Initially no BYO.
	w := e.do(t, http.MethodGet, "/api/storage/byo", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("byo status: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var st byoStatusResponse
	_ = json.NewDecoder(w.Body).Decode(&st)
	if st.Connected {
		t.Fatal("expected not connected initially")
	}

	// Connect a valid BYO bucket.
	body := map[string]any{
		"endpoint":   "https://s3.example.com",
		"bucket":     "alice-data",
		"region":     "us-east-1",
		"access_key": "AKIA_TEST",
		"secret_key": "super-secret",
	}
	w = e.do(t, http.MethodPost, "/api/storage/byo", body, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("byo connect: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	st = byoStatusResponse{}
	_ = json.NewDecoder(w.Body).Decode(&st)
	if !st.BYO || !st.Connected || st.Bucket != "alice-data" {
		t.Fatalf("unexpected connect response: %+v", st)
	}

	// Config is persisted with byo=true and the secret is recoverable internally
	// but never serialised.
	cfg, err := e.svc.GetConfig(context.Background(), uid)
	if err != nil {
		t.Fatalf("GetConfig after connect: %v", err)
	}
	if !cfg.BYO || cfg.Bucket != "alice-data" {
		t.Fatalf("stored config wrong: %+v", cfg)
	}
	if strings.Contains(w.Body.String(), "super-secret") {
		t.Fatal("secret key leaked in connect response")
	}

	// Disconnect → 204, config gone.
	w = e.do(t, http.MethodDelete, "/api/storage/byo", nil, tok)
	if w.Code != http.StatusNoContent {
		t.Fatalf("byo disconnect: expected 204, got %d", w.Code)
	}
	if _, err := e.svc.GetConfig(context.Background(), uid); err == nil {
		t.Fatal("expected config removed after disconnect")
	}
}

func TestBYO_Connect_ValidationFails(t *testing.T) {
	e := newStorageTestEnv(t)
	_, tok := sessionFor(t, e.authSt, "byo-bob@example.com")

	// MemProvider WITHOUT the bucket → ListBucket returns ErrUnknownBucket →
	// validation fails → 400.
	mem := storage.NewMemProvider()
	restore := storage.SetBYOProviderFactoryForTest(func(_ storage.Config) (storage.Provider, error) {
		return mem, nil
	})
	defer restore()

	body := map[string]any{
		"endpoint":   "https://s3.example.com",
		"bucket":     "does-not-exist",
		"access_key": "AKIA",
		"secret_key": "secret",
	}
	w := e.do(t, http.MethodPost, "/api/storage/byo", body, tok)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on validation failure, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBYO_Connect_NonHTTPSEndpoint(t *testing.T) {
	e := newStorageTestEnv(t)
	_, tok := sessionFor(t, e.authSt, "byo-carol@example.com")

	body := map[string]any{
		"endpoint":   "http://s3.example.com", // not https
		"bucket":     "b",
		"access_key": "k",
		"secret_key": "s",
	}
	w := e.do(t, http.MethodPost, "/api/storage/byo", body, tok)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-https endpoint, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBYO_Connect_Unauthenticated(t *testing.T) {
	e := newStorageTestEnv(t)
	w := e.do(t, http.MethodPost, "/api/storage/byo", map[string]any{"bucket": "x"}, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Export bundle
// ---------------------------------------------------------------------------

func TestExport_RequiresSession(t *testing.T) {
	e := newStorageTestEnv(t)
	w := e.do(t, http.MethodPost, "/api/account/export", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated export, got %d", w.Code)
	}
}

func TestExport_BundleShape(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "export-dave@example.com")

	// Give the account a managed config + an object so the inventory is non-empty.
	_ = e.svc.PutConfig(context.Background(), storage.Config{
		AccountID: uid,
		BYO:       false,
		Region:    "auto",
		Bucket:    "vulos-" + strings.ToLower(uid),
	})
	if p, err := e.svc.ProviderForAccount(context.Background(), uid); err == nil {
		_ = p.PutObject(context.Background(), "vulos-"+strings.ToLower(uid), "notes/a.txt",
			bytes.NewReader([]byte("hello")), 5)
	}

	w := e.do(t, http.MethodPost, "/api/account/export", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("export: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("expected application/zip, got %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, ".zip") {
		t.Fatalf("expected zip attachment disposition, got %q", cd)
	}

	// Unzip and verify required entries are present.
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}

	required := []string{
		"manifest.json",
		"README.md",
		"RESTORE.md",
		"docker-compose.yml",
		"storage/objects.json",
		"calendar/calendar.ics",
		"contacts/contacts.vcf",
		"mail/README.md",
	}
	for _, name := range required {
		if _, ok := got[name]; !ok {
			t.Errorf("export bundle missing %q", name)
		}
	}

	// manifest is valid JSON with the account id.
	var m exportManifest
	if err := json.Unmarshal([]byte(got["manifest.json"]), &m); err != nil {
		t.Fatalf("manifest.json invalid: %v", err)
	}
	if m.Account.ID != uid {
		t.Errorf("manifest account id = %q, want %q", m.Account.ID, uid)
	}
	if m.Account.Email != "export-dave@example.com" {
		t.Errorf("manifest email wrong: %q", m.Account.Email)
	}

	// objects.json lists the object we wrote.
	if !strings.Contains(got["storage/objects.json"], "notes/a.txt") {
		t.Errorf("object inventory missing the written object: %s", got["storage/objects.json"])
	}

	// docker-compose is non-trivial.
	if !strings.Contains(got["docker-compose.yml"], "services:") {
		t.Errorf("docker-compose.yml looks empty: %s", got["docker-compose.yml"])
	}

	// calendar.ics is a valid VCALENDAR container.
	if !strings.Contains(got["calendar/calendar.ics"], "BEGIN:VCALENDAR") {
		t.Errorf("calendar.ics not a valid ICS container")
	}
}

func TestExport_BYOComposeUsesUserEndpoint(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "export-erin@example.com")

	_ = e.svc.PutConfig(context.Background(), storage.Config{
		AccountID: uid,
		BYO:       true,
		Endpoint:  "https://s3.my-cloud.example",
		Region:    "eu-west-1",
		Bucket:    "erin-bucket",
		AccessKey: "AK",
		SecretKey: "erin-secret-xyz",
	})

	w := e.do(t, http.MethodPost, "/api/account/export", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("export: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	var compose string
	for _, f := range zr.File {
		if f.Name == "docker-compose.yml" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			compose = string(b)
		}
	}
	if !strings.Contains(compose, "https://s3.my-cloud.example") {
		t.Errorf("BYO compose should reference the user's endpoint, got:\n%s", compose)
	}
	// secret must never be embedded in the compose.
	if strings.Contains(compose, "erin-secret-xyz") {
		t.Errorf("BYO compose leaked secret key")
	}
}
