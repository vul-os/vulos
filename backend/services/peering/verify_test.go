package peering

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testVerifyGenKey generates a fresh ed25519 keypair for test use.
func testVerifyGenKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

// testVerifyMakeToken builds and signs a VerifyToken using the given private key.
func testVerifyMakeToken(t *testing.T, vulaID, email string, issuedAt int64, priv ed25519.PrivateKey, pub ed25519.PublicKey) *VerifyToken {
	t.Helper()
	tok := &VerifyToken{
		VulaID:     vulaID,
		Email:      email,
		IssuedAt:   issuedAt,
		SigningKey: base64.StdEncoding.EncodeToString(pub),
	}
	sig := ed25519.Sign(priv, verifyCanonicalPayload(tok))
	tok.Signature = base64.StdEncoding.EncodeToString(sig)
	return tok
}

// testVerifyNewService creates a VerifyService wired to a custom HTTP client
// (pointing at a test server) and a temp data directory.
func testVerifyNewService(t *testing.T, serverURL string) *VerifyService {
	t.Helper()
	dir := t.TempDir()
	svc := &VerifyService{
		dataDir: filepath.Join(dir, "identity"),
		baseURL: serverURL,
		client:  &http.Client{},
	}
	return svc
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

func TestVerifySignatureValid_OK(t *testing.T) {
	pub, priv := testVerifyGenKey(t)
	tok := testVerifyMakeToken(t, "vula:ed25519:abc", "alice@example.com", 1700000000, priv, pub)
	if err := verifySignatureValid(tok); err != nil {
		t.Fatalf("expected valid signature, got: %v", err)
	}
}

func TestVerifySignatureValid_Tampered(t *testing.T) {
	pub, priv := testVerifyGenKey(t)
	tok := testVerifyMakeToken(t, "vula:ed25519:abc", "alice@example.com", 1700000000, priv, pub)
	// Tamper with the email after signing.
	tok.Email = "eve@evil.com"
	if err := verifySignatureValid(tok); err == nil {
		t.Fatal("expected invalid signature for tampered token")
	}
}

func TestVerifySignatureValid_BadKey(t *testing.T) {
	_, priv := testVerifyGenKey(t)
	pub2, _ := testVerifyGenKey(t) // different key
	tok := testVerifyMakeToken(t, "vula:ed25519:abc", "alice@example.com", 1700000000, priv, pub2)
	// pub2 is used in SigningKey but was not used to sign — sig will be wrong.
	tok.Signature = base64.StdEncoding.EncodeToString([]byte("bad"))
	if err := verifySignatureValid(tok); err == nil {
		t.Fatal("expected error for bad signature bytes")
	}
}

func TestVerifySignatureValid_BadKeyLength(t *testing.T) {
	_, priv := testVerifyGenKey(t)
	pub, _ := testVerifyGenKey(t)
	tok := testVerifyMakeToken(t, "vula:ed25519:abc", "alice@example.com", 1700000000, priv, pub)
	tok.SigningKey = base64.StdEncoding.EncodeToString([]byte("tooshort"))
	if err := verifySignatureValid(tok); err == nil {
		t.Fatal("expected error for wrong-length signing key")
	}
}

// ---------------------------------------------------------------------------
// VerifyService.VerifiedEmail — unverified state
// ---------------------------------------------------------------------------

func TestVerifyService_UnverifiedReturnsEmpty(t *testing.T) {
	svc := &VerifyService{}
	if got := svc.VerifiedEmail(); got != "" {
		t.Fatalf("unverified instance should return empty string, got %q", got)
	}
}

func TestVerifyService_UnverifiedTokenNil(t *testing.T) {
	svc := &VerifyService{}
	if tok := svc.VerifiedToken(); tok != nil {
		t.Fatalf("unverified instance should return nil token, got %+v", tok)
	}
}

// ---------------------------------------------------------------------------
// Token persistence
// ---------------------------------------------------------------------------

func TestVerifySaveAndLoadToken(t *testing.T) {
	pub, priv := testVerifyGenKey(t)
	tok := testVerifyMakeToken(t, "vula:ed25519:xyz", "bob@example.com", 1700001000, priv, pub)

	dir := t.TempDir()
	svc := &VerifyService{dataDir: dir}

	if err := svc.verifySaveToken(tok); err != nil {
		t.Fatalf("save token: %v", err)
	}

	// Verify file permissions are restrictive.
	info, err := os.Stat(filepath.Join(dir, verifyTokenFile))
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if info.Mode()&0o077 != 0 {
		t.Errorf("token file has group/other bits set: %v", info.Mode())
	}

	// Load it back.
	svc2 := &VerifyService{dataDir: dir}
	if err := svc2.verifyLoadToken(); err != nil {
		t.Fatalf("load token: %v", err)
	}
	if svc2.VerifiedEmail() != "bob@example.com" {
		t.Fatalf("loaded email mismatch: %q", svc2.VerifiedEmail())
	}
}

func TestVerifyLoadToken_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, verifyTokenFile)
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &VerifyService{dataDir: dir}
	if err := svc.verifyLoadToken(); err == nil {
		t.Fatal("expected error for corrupt token file")
	}
}

func TestVerifyLoadToken_InvalidSignature(t *testing.T) {
	pub, _ := testVerifyGenKey(t)
	_, otherPriv := testVerifyGenKey(t)
	// Sign with wrong key.
	tok := testVerifyMakeToken(t, "vula:ed25519:xyz", "alice@example.com", 1700001000, otherPriv, pub)

	dir := t.TempDir()
	data, _ := json.Marshal(tok)
	if err := os.WriteFile(filepath.Join(dir, verifyTokenFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	svc := &VerifyService{dataDir: dir}
	if err := svc.verifyLoadToken(); err == nil {
		t.Fatal("expected error loading token with invalid signature")
	}
}

// ---------------------------------------------------------------------------
// HTTP mock server helpers
// ---------------------------------------------------------------------------

// testVerifyMockServer creates an httptest server that mimics the vulos.org
// /api/verify/* endpoints.  It accepts any code "123456" as valid.
func testVerifyMockServer(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// POST /api/verify/send — always succeeds.
	mux.HandleFunc("POST /api/verify/send", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"sent"}`))
	})

	// POST /api/verify/confirm — accepts code "123456", issues signed token.
	mux.HandleFunc("POST /api/verify/confirm", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			VulaID string `json:"vula_id"`
			Email  string `json:"email"`
			Code   string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Code != "123456" {
			http.Error(w, "invalid code", http.StatusUnprocessableEntity)
			return
		}
		tok := testVerifyMakeToken(t, req.VulaID, req.Email, 1700000000, priv, pub)
		w.Header().Set("Content-Type", "application/json")
		data, _ := json.Marshal(tok)
		_, _ = w.Write(data)
	})

	return httptest.NewServer(mux)
}

// ---------------------------------------------------------------------------
// Integration: verifySend + verifyConfirm via mock server
// ---------------------------------------------------------------------------

func TestVerifySend_OK(t *testing.T) {
	pub, priv := testVerifyGenKey(t)
	srv := testVerifyMockServer(t, pub, priv)
	defer srv.Close()

	svc := testVerifyNewService(t, srv.URL)
	if err := svc.verifySend("vula:ed25519:abc", "alice@example.com"); err != nil {
		t.Fatalf("verifySend: %v", err)
	}
}

func TestVerifySend_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	svc := testVerifyNewService(t, srv.URL)
	if err := svc.verifySend("vula:ed25519:abc", "alice@example.com"); err == nil {
		t.Fatal("expected error from server 500")
	}
}

func TestVerifyConfirm_OK(t *testing.T) {
	pub, priv := testVerifyGenKey(t)
	srv := testVerifyMockServer(t, pub, priv)
	defer srv.Close()

	svc := testVerifyNewService(t, srv.URL)
	if err := svc.verifyConfirm("vula:ed25519:abc", "alice@example.com", "123456"); err != nil {
		t.Fatalf("verifyConfirm: %v", err)
	}
	if svc.VerifiedEmail() != "alice@example.com" {
		t.Fatalf("expected verified email, got %q", svc.VerifiedEmail())
	}
	// Token file must exist on disk.
	if _, err := os.Stat(filepath.Join(svc.dataDir, verifyTokenFile)); err != nil {
		t.Fatalf("token file not written: %v", err)
	}
}

func TestVerifyConfirm_WrongCode(t *testing.T) {
	pub, priv := testVerifyGenKey(t)
	srv := testVerifyMockServer(t, pub, priv)
	defer srv.Close()

	svc := testVerifyNewService(t, srv.URL)
	if err := svc.verifyConfirm("vula:ed25519:abc", "alice@example.com", "999999"); err == nil {
		t.Fatal("expected error for wrong code")
	}
	// Service must remain unverified.
	if email := svc.VerifiedEmail(); email != "" {
		t.Fatalf("must remain unverified, got %q", email)
	}
}

func TestVerifyConfirm_TamperedToken(t *testing.T) {
	// Server signs with one key but reports a different public key — verification must fail.
	pub, _ := testVerifyGenKey(t)
	_, badPriv := testVerifyGenKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "confirm") {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var req struct {
			VulaID string `json:"vula_id"`
			Email  string `json:"email"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// Sign with badPriv but advertise pub — signature mismatch.
		tok := testVerifyMakeToken(t, req.VulaID, req.Email, 1700000000, badPriv, pub)
		data, _ := json.Marshal(tok)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	svc := testVerifyNewService(t, srv.URL)
	if err := svc.verifyConfirm("vula:ed25519:abc", "alice@example.com", "123456"); err == nil {
		t.Fatal("expected error for tampered token from server")
	}
	if svc.VerifiedEmail() != "" {
		t.Fatal("must remain unverified after tampered token")
	}
}

// ---------------------------------------------------------------------------
// HTTP handler tests via RegisterVerifyHandlers
// ---------------------------------------------------------------------------

func testVerifyMuxWithHandlers(t *testing.T, svc *VerifyService) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	RegisterVerifyHandlers(mux, svc)
	return mux
}

func TestRegisterVerifyHandlers_Send(t *testing.T) {
	pub, priv := testVerifyGenKey(t)
	upstream := testVerifyMockServer(t, pub, priv)
	defer upstream.Close()

	svc := testVerifyNewService(t, upstream.URL)
	mux := testVerifyMuxWithHandlers(t, svc)

	body := `{"vula_id":"vula:ed25519:abc","email":"alice@example.com"}`
	req := httptest.NewRequest("POST", "/api/peering/identity/verify", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRegisterVerifyHandlers_Send_MissingFields(t *testing.T) {
	svc := &VerifyService{baseURL: "http://unused", client: &http.Client{}}
	mux := testVerifyMuxWithHandlers(t, svc)

	req := httptest.NewRequest("POST", "/api/peering/identity/verify", strings.NewReader(`{"vula_id":""}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestRegisterVerifyHandlers_Confirm(t *testing.T) {
	pub, priv := testVerifyGenKey(t)
	upstream := testVerifyMockServer(t, pub, priv)
	defer upstream.Close()

	svc := testVerifyNewService(t, upstream.URL)
	mux := testVerifyMuxWithHandlers(t, svc)

	body := `{"vula_id":"vula:ed25519:abc","email":"alice@example.com","code":"123456"}`
	req := httptest.NewRequest("POST", "/api/peering/identity/confirm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var tok VerifyToken
	if err := json.Unmarshal(rr.Body.Bytes(), &tok); err != nil {
		t.Fatalf("response not a valid token: %v", err)
	}
	if tok.Email != "alice@example.com" {
		t.Fatalf("unexpected email in returned token: %q", tok.Email)
	}
}

func TestRegisterVerifyHandlers_Status_Unverified(t *testing.T) {
	svc := &VerifyService{}
	mux := testVerifyMuxWithHandlers(t, svc)

	req := httptest.NewRequest("GET", "/api/peering/identity/verify/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if v, _ := resp["verified"].(bool); v {
		t.Fatal("expected verified=false for unverified service")
	}
}

func TestRegisterVerifyHandlers_Status_Verified(t *testing.T) {
	pub, priv := testVerifyGenKey(t)
	tok := testVerifyMakeToken(t, "vula:ed25519:abc", "alice@example.com", 1700000000, priv, pub)

	svc := &VerifyService{token: tok}
	mux := testVerifyMuxWithHandlers(t, svc)

	req := httptest.NewRequest("GET", "/api/peering/identity/verify/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if v, _ := resp["verified"].(bool); !v {
		t.Fatal("expected verified=true")
	}
	if email, _ := resp["email"].(string); email != "alice@example.com" {
		t.Fatalf("unexpected email %q", email)
	}
}

// ---------------------------------------------------------------------------
// Base URL configurable via env
// ---------------------------------------------------------------------------

func TestVerifyNewService_BaseURLFromEnv(t *testing.T) {
	t.Setenv("VULOS_VERIFY_URL", "https://custom.example.com/")
	dir := t.TempDir()
	svc, err := VerifyNewService(dir)
	if err != nil {
		t.Fatalf("VerifyNewService: %v", err)
	}
	want := "https://custom.example.com"
	if svc.baseURL != want {
		t.Fatalf("baseURL = %q, want %q", svc.baseURL, want)
	}
}

func TestVerifyNewService_BaseURLDefault(t *testing.T) {
	t.Setenv("VULOS_VERIFY_URL", "")
	dir := t.TempDir()
	svc, err := VerifyNewService(dir)
	if err != nil {
		t.Fatalf("VerifyNewService: %v", err)
	}
	if svc.baseURL != verifyDefaultBaseURL {
		t.Fatalf("baseURL = %q, want %q", svc.baseURL, verifyDefaultBaseURL)
	}
}

// ---------------------------------------------------------------------------
// Unverified instance still works (no panic, no error)
// ---------------------------------------------------------------------------

func TestVerifyService_UnverifiedWorksNormally(t *testing.T) {
	// Create a service that has never been verified and confirm it doesn't panic
	// and returns clean state for all public accessors.
	svc := &VerifyService{}
	email := svc.VerifiedEmail()
	tok := svc.VerifiedToken()
	if email != "" {
		t.Errorf("VerifiedEmail on unverified should be empty, got %q", email)
	}
	if tok != nil {
		t.Errorf("VerifiedToken on unverified should be nil")
	}

	// HTTP status endpoint must also work without panicking.
	mux := http.NewServeMux()
	RegisterVerifyHandlers(mux, svc)
	req := httptest.NewRequest("GET", "/api/peering/identity/verify/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status handler returned %d on unverified service", rr.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON from status handler: %v", err)
	}
	if verified, _ := resp["verified"].(bool); verified {
		t.Error("unverified service must report verified=false")
	}

	// Dummy usage to silence the fmt import if linter complains.
	_ = fmt.Sprintf("peering verify package unverified test ok")
}
