package lan

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mockCloud spins up a fake cloud control-plane that speaks the lancert wire
// contract (report-ip + cert) so the puller can be exercised end-to-end without
// touching the network.
type mockCloud struct {
	srv         *httptest.Server
	authHeader  string
	reportedIPs []string
	// pendingPolls is how many 202 responses to return before flipping to 200.
	pendingPolls atomic.Int32
	certPEM      string
	keyPEM       string
	pollCount    atomic.Int32
}

func newMockCloud(t *testing.T, authHeader string, pendingPolls int, certPEM, keyPEM string) *mockCloud {
	t.Helper()
	m := &mockCloud{
		authHeader: authHeader,
		certPEM:    certPEM,
		keyPEM:     keyPEM,
	}
	m.pendingPolls.Store(int32(pendingPolls))

	mux := http.NewServeMux()
	mux.HandleFunc("/api/lancert/report-ip", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Device-Auth") != authHeader {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body struct {
			BoxID string `json:"box_id"`
			LANIP string `json:"lan_ip"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		m.reportedIPs = append(m.reportedIPs, body.LANIP)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/lancert/cert", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Device-Auth") != authHeader {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		m.pollCount.Add(1)
		if m.pendingPolls.Add(-1) >= 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"box_id":    r.URL.Query().Get("box_id"),
			"fqdn":      "box.test.lan.vulos.org",
			"cert_pem":  m.certPEM,
			"key_pem":   m.keyPEM,
			"not_after": time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339),
		})
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

// seedSelfSignedPEMs produces a real cert+key pair PEM-encoded, reusing the
// in-package self-signed generator so the puller can verify a parseable cert.
func seedSelfSignedPEMs(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	gen := NewSelfSignedCertSource([]string{"box.test.lan.vulos.org"}, nil)
	c, err := gen.Certificate(nil)
	if err != nil {
		t.Fatalf("seed cert: %v", err)
	}
	certBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate[0]})
	keyDER, err := x509.MarshalPKCS8PrivateKey(c.PrivateKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return string(certBytes), string(keyBytes)
}

func TestPullerEnabled(t *testing.T) {
	t.Setenv("VULOS_LANCERT_ENABLE", "")
	if PullerEnabled() {
		t.Fatal("expected false when unset")
	}
	t.Setenv("VULOS_LANCERT_ENABLE", "1")
	if !PullerEnabled() {
		t.Fatal("expected true for 1")
	}
	t.Setenv("VULOS_LANCERT_ENABLE", "YES")
	if !PullerEnabled() {
		t.Fatal("expected true for YES")
	}
	t.Setenv("VULOS_LANCERT_ENABLE", "no")
	if PullerEnabled() {
		t.Fatal("expected false for no")
	}
}

func TestNewLANCertPuller_RequiresBoxID(t *testing.T) {
	if _, err := NewLANCertPuller(PullerConfig{}); err == nil {
		t.Fatal("expected error for missing BoxID")
	}
}

// TestLANCertPuller_PollAcceptedThenOK is the end-to-end happy path: the
// cloud returns 202 twice, then 200 with PEMs. The puller must report the IP,
// keep polling with backoff, and write the cert+key atomically.
func TestLANCertPuller_PollAcceptedThenOK(t *testing.T) {
	certPEM, keyPEM := seedSelfSignedPEMs(t)
	const secret = "shh-test-secret"
	cloud := newMockCloud(t, secret, 2, certPEM, keyPEM)

	dir := t.TempDir()
	certPath := filepath.Join(dir, "lan.crt")
	keyPath := filepath.Join(dir, "lan.key")

	puller, err := NewLANCertPuller(PullerConfig{
		CloudBaseURL: cloud.srv.URL,
		SharedSecret: secret,
		BoxID:        "test-box-01",
		CertPath:     certPath,
		KeyPath:      keyPath,
		LANIPProvider: func() net.IP {
			return net.IPv4(10, 0, 0, 42)
		},
		InitialBackoff:     5 * time.Millisecond,
		MaxBackoff:         20 * time.Millisecond,
		RenewCheckInterval: 1 * time.Hour,
		HTTPClient:         cloud.srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewLANCertPuller: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	runCtx, runCancel := context.WithCancel(ctx)
	go func() {
		puller.Run(runCtx)
		close(done)
	}()

	// Wait for the cert file to appear and parse correctly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(certPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Cancel run loop and wait.
	runCancel()
	<-done

	gotCert, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	gotKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if !strings.Contains(string(gotCert), "BEGIN CERTIFICATE") {
		t.Fatalf("cert file does not look like PEM: %q", gotCert)
	}
	if !strings.Contains(string(gotKey), "PRIVATE KEY") {
		t.Fatalf("key file does not look like PEM: %q", gotKey)
	}
	if len(cloud.reportedIPs) == 0 || cloud.reportedIPs[0] != "10.0.0.42" {
		t.Fatalf("expected reported IPs to contain 10.0.0.42, got %v", cloud.reportedIPs)
	}
	if cloud.pollCount.Load() < 3 {
		t.Fatalf("expected at least 3 polls (2x202 + 200), got %d", cloud.pollCount.Load())
	}

	// Files must be writable by LoadCertSource — verify perm is 0600 (key only;
	// cert is also 0600 for simplicity).
	st, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected 0600 on key, got %o", perm)
	}
}

// TestLANCertPuller_AtomicWrite ensures the puller writes via tmp+rename, so
// a partial file is never visible at the target path. We can prove this in a
// black-box way by reading the file mid-write would race, so instead we
// directly exercise writeFileAtomic + assert no stray .tmp files remain.
func TestLANCertPuller_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "lan.crt")
	key := filepath.Join(dir, "lan.key")
	if err := writePEMsAtomic(cert, key, "CERT-PEM", "KEY-PEM"); err != nil {
		t.Fatalf("writePEMsAtomic: %v", err)
	}
	if b, _ := os.ReadFile(cert); string(b) != "CERT-PEM" {
		t.Fatalf("cert: %q", b)
	}
	if b, _ := os.ReadFile(key); string(b) != "KEY-PEM" {
		t.Fatalf("key: %q", b)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("stray tmp file left behind: %s", e.Name())
		}
	}
}
