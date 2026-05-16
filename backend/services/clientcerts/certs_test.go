package clientcerts

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/devicekey"
)

// --- helpers ----------------------------------------------------------------

// newTestKeyStore opens a software KeyStore backed by t's temp directory.
func newTestKeyStore(t *testing.T) devicekey.KeyStore {
	t.Helper()
	ks, err := devicekey.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open devicekey: %v", err)
	}
	t.Cleanup(func() { ks.Close() })
	return ks
}

// newTestStore creates a Store backed by a temporary directory and a
// software KeyStore.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	ks := newTestKeyStore(t)
	s, err := NewStore(t.TempDir(), ks)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// selfSignedCert generates a self-signed certificate + private key for testing.
// Returns PEM strings for cert and key.
func selfSignedCert(t *testing.T, cn string) (certPEM, keyPEM string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"Test Org"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}

// --- Store unit tests -------------------------------------------------------

func TestInstallAndStatus(t *testing.T) {
	s := newTestStore(t)

	certPEM, keyPEM := selfSignedCert(t, "test.example.com")

	if err := s.Install("test.example.com", certPEM, keyPEM, ""); err != nil {
		t.Fatalf("Install: %v", err)
	}

	info, err := s.Status("test.example.com")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if info.Domain != "test.example.com" {
		t.Errorf("domain = %q, want %q", info.Domain, "test.example.com")
	}
	if !strings.Contains(info.Subject, "test.example.com") {
		t.Errorf("subject %q does not contain CN", info.Subject)
	}
	if !strings.Contains(info.Issuer, "Test Org") {
		t.Errorf("issuer %q does not contain org", info.Issuer)
	}
	if info.Expired {
		t.Error("cert should not be expired")
	}
	if !info.HasKey {
		t.Error("HasKey should be true after install")
	}
}

func TestInstallPrivateKeySealed(t *testing.T) {
	s := newTestStore(t)

	certPEM, keyPEM := selfSignedCert(t, "sealed.example.com")

	if err := s.Install("sealed.example.com", certPEM, keyPEM, ""); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// The sealed key file must NOT contain the literal PEM header in plaintext.
	dir, _ := s.domainDir("sealed.example.com")
	sealedData, err := readFile(dir + "/client.key.enc")
	if err != nil {
		t.Fatalf("read sealed key file: %v", err)
	}
	if bytes.Contains(sealedData, []byte("BEGIN EC PRIVATE KEY")) {
		t.Error("private key stored as plaintext (not sealed)")
	}
	if bytes.Contains(sealedData, []byte("BEGIN PRIVATE KEY")) {
		t.Error("private key stored as plaintext (not sealed)")
	}
}

func TestUnsealKey(t *testing.T) {
	s := newTestStore(t)

	certPEM, keyPEM := selfSignedCert(t, "unseal.example.com")

	if err := s.Install("unseal.example.com", certPEM, keyPEM, ""); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, err := s.UnsealKey("unseal.example.com")
	if err != nil {
		t.Fatalf("UnsealKey: %v", err)
	}
	if got != keyPEM {
		t.Errorf("unsealed key does not match original\ngot:\n%s\nwant:\n%s", got, keyPEM)
	}
}

func TestList(t *testing.T) {
	s := newTestStore(t)

	domains := []string{"alpha.example.com", "beta.example.com", "gamma.example.com"}
	for _, d := range domains {
		certPEM, keyPEM := selfSignedCert(t, d)
		if err := s.Install(d, certPEM, keyPEM, ""); err != nil {
			t.Fatalf("Install(%q): %v", d, err)
		}
	}

	infos, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != len(domains) {
		t.Fatalf("got %d certs, want %d", len(infos), len(domains))
	}
	// Build a set for O(1) lookup.
	found := map[string]bool{}
	for _, info := range infos {
		found[info.Domain] = true
	}
	for _, d := range domains {
		if !found[d] {
			t.Errorf("domain %q missing from list", d)
		}
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)

	certPEM, keyPEM := selfSignedCert(t, "delete.example.com")
	if err := s.Install("delete.example.com", certPEM, keyPEM, ""); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if err := s.Delete("delete.example.com"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := s.Status("delete.example.com"); err == nil {
		t.Error("Status after Delete should return an error")
	}

	// Second delete should also return an error.
	if err := s.Delete("delete.example.com"); err == nil {
		t.Error("second Delete should return an error")
	}
}

func TestGenerateCSR(t *testing.T) {
	s := newTestStore(t)

	info, err := s.GenerateCSR(CSRRequest{
		Domain: "csr.example.com",
		CN:     "My Vula Client",
		SANs:   []string{"csr.example.com", "alt.example.com"},
		O:      "Vula OS",
		C:      "ZA",
	})
	if err != nil {
		t.Fatalf("GenerateCSR: %v", err)
	}

	// Returned PEM must be a valid CSR.
	block, _ := pem.Decode([]byte(info.PEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("GenerateCSR returned invalid PEM: %q", info.PEM)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("parse CSR: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatalf("CSR signature invalid: %v", err)
	}
	if csr.Subject.CommonName != "My Vula Client" {
		t.Errorf("CN = %q, want %q", csr.Subject.CommonName, "My Vula Client")
	}
	if len(csr.DNSNames) != 2 {
		t.Errorf("got %d SAN DNS names, want 2", len(csr.DNSNames))
	}

	// A sealed private key must have been persisted for the domain.
	if _, err := s.UnsealKey("csr.example.com"); err != nil {
		t.Fatalf("UnsealKey after GenerateCSR: %v", err)
	}
}

func TestInvalidDomain(t *testing.T) {
	s := newTestStore(t)

	for _, bad := range []string{"../etc/passwd", "foo/bar", "."} {
		_, err := s.Status(bad)
		if err == nil {
			t.Errorf("Status(%q) should have returned an error", bad)
		}
	}
}

func TestExpiredCert(t *testing.T) {
	s := newTestStore(t)

	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(99),
		Subject:      pkix.Name{CommonName: "expired.example.com"},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     time.Now().Add(-24 * time.Hour), // already expired
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyDER, _ := x509.MarshalECPrivateKey(priv)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))

	if err := s.Install("expired.example.com", certPEM, keyPEM, ""); err != nil {
		t.Fatalf("Install: %v", err)
	}

	info, err := s.Status("expired.example.com")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !info.Expired {
		t.Error("Expired should be true for an expired certificate")
	}
}

// --- HTTP handler tests -----------------------------------------------------

func TestHandlerInstallAndStatus(t *testing.T) {
	s := newTestStore(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, s)

	certPEM, keyPEM := selfSignedCert(t, "handler.example.com")

	// Install.
	body := map[string]string{
		"domain":   "handler.example.com",
		"cert_pem": certPEM,
		"key_pem":  keyPEM,
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/certs/install", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("install: got %d, want 201; body: %s", w.Code, w.Body.String())
	}

	// Status.
	req2 := httptest.NewRequest(http.MethodGet, "/api/auth/certs/handler.example.com/status", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", w2.Code, w2.Body.String())
	}

	var info CertInfo
	if err := json.Unmarshal(w2.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if info.Domain != "handler.example.com" {
		t.Errorf("domain = %q, want %q", info.Domain, "handler.example.com")
	}
	if info.Expired {
		t.Error("cert should not be expired")
	}
	if !info.HasKey {
		t.Error("HasKey should be true")
	}
}

func TestHandlerList(t *testing.T) {
	s := newTestStore(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, s)

	for _, d := range []string{"list1.example.com", "list2.example.com"} {
		certPEM, keyPEM := selfSignedCert(t, d)
		body := map[string]string{"domain": d, "cert_pem": certPEM, "key_pem": keyPEM}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/certs/install", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("install %q: %d %s", d, w.Code, w.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/certs", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list: got %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var infos []CertInfo
	if err := json.Unmarshal(w.Body.Bytes(), &infos); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(infos) != 2 {
		t.Errorf("got %d certs, want 2", len(infos))
	}
}

func TestHandlerDelete(t *testing.T) {
	s := newTestStore(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, s)

	certPEM, keyPEM := selfSignedCert(t, "del.example.com")
	body := map[string]string{"domain": "del.example.com", "cert_pem": certPEM, "key_pem": keyPEM}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/certs/install", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodDelete, "/api/auth/certs/del.example.com", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("delete: got %d, want 200; body: %s", w2.Code, w2.Body.String())
	}

	// Subsequent status must 404.
	req3 := httptest.NewRequest(http.MethodGet, "/api/auth/certs/del.example.com/status", nil)
	w3 := httptest.NewRecorder()
	mux.ServeHTTP(w3, req3)
	if w3.Code != http.StatusNotFound {
		t.Errorf("status after delete: got %d, want 404", w3.Code)
	}
}

func TestHandlerGenerateCSR(t *testing.T) {
	s := newTestStore(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, s)

	body := map[string]any{
		"domain": "csr-handler.example.com",
		"cn":     "Vula mTLS Client",
		"sans":   []string{"csr-handler.example.com"},
		"o":      "Vula OS",
		"c":      "ZA",
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/certs/generate-csr", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("generate-csr: got %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var info CSRInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode CSRInfo: %v", err)
	}
	if info.Domain != "csr-handler.example.com" {
		t.Errorf("domain = %q", info.Domain)
	}

	block, _ := pem.Decode([]byte(info.PEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("expected CERTIFICATE REQUEST PEM, got %q", block)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("parse CSR: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatalf("CSR signature: %v", err)
	}
	if csr.Subject.CommonName != "Vula mTLS Client" {
		t.Errorf("CN = %q", csr.Subject.CommonName)
	}
}

func TestHandlerMissingFields(t *testing.T) {
	s := newTestStore(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, s)

	// Install without key_pem.
	body := map[string]string{"domain": "x.example.com", "cert_pem": "something"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/certs/install", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing key_pem: got %d, want 400", w.Code)
	}

	// generate-csr without cn.
	body2 := map[string]string{"domain": "x.example.com"}
	b2, _ := json.Marshal(body2)
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/certs/generate-csr", bytes.NewReader(b2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("missing cn: got %d, want 400", w2.Code)
	}
}

// --- helpers used only in tests --------------------------------------------

// readFile is a thin wrapper used in TestInstallPrivateKeySealed.
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
