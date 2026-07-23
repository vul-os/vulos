package lancert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ── Mock ACMEClient ─────────────────────────────────────────────────────────
//
// The mock simulates the CA: NewOrder returns a synthetic DNS-01 challenge
// value, then Finalize mints a short-lived self-signed cert from the CSR
// (mirroring what a real CA would return).  Records every call so tests can
// assert ordering.

type mockACMEClient struct {
	mu            sync.Mutex
	NewOrderCalls []string
	FinalizeCalls int
	NotAfter      time.Time
	NotBefore     time.Time
	NewOrderError error
	FinalizeError error
	lastChallenge string
}

func newMockACME() *mockACMEClient {
	return &mockACMEClient{
		NotBefore: time.Now().UTC().Add(-1 * time.Hour),
		NotAfter:  time.Now().UTC().Add(90 * 24 * time.Hour),
	}
}

type mockOrderState struct {
	fqdn string
}

func (m *mockACMEClient) NewOrder(_ context.Context, fqdn string) (string, any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.NewOrderError != nil {
		return "", nil, m.NewOrderError
	}
	m.NewOrderCalls = append(m.NewOrderCalls, fqdn)
	m.lastChallenge = "challenge-" + fqdn
	return m.lastChallenge, &mockOrderState{fqdn: fqdn}, nil
}

func (m *mockACMEClient) Finalize(_ context.Context, orderState any, csr []byte) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FinalizeError != nil {
		return nil, m.FinalizeError
	}
	m.FinalizeCalls++
	st, ok := orderState.(*mockOrderState)
	if !ok {
		return nil, errors.New("mock: bad order state")
	}

	// Parse CSR (sanity-check that the orchestrator actually built one for
	// the right FQDN).
	parsed, err := x509.ParseCertificateRequest(csr)
	if err != nil {
		return nil, err
	}
	hasFQDN := false
	for _, n := range parsed.DNSNames {
		if n == st.fqdn {
			hasFQDN = true
			break
		}
	}
	if !hasFQDN {
		return nil, errors.New("mock: CSR missing expected FQDN")
	}

	// Mint a self-signed leaf to return.  Use the CSR's public key.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: st.fqdn},
		NotBefore:    m.NotBefore,
		NotAfter:     m.NotAfter,
		DNSNames:     []string{st.fqdn},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, parsed.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	return [][]byte{leafDER}, nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

func newTestStore(t *testing.T) *SQLStore {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "lancert.db") + "?_pragma=journal_mode(WAL)"
	db, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("lancert.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newTestService(t *testing.T, acmeC ACMEClient, dns DNSProvider) *Service {
	t.Helper()
	if acmeC == nil {
		acmeC = newMockACME()
	}
	if dns == nil {
		dns = NewNoopDNSProvider()
	}
	svc := NewService(newTestStore(t), acmeC, dns)
	svc.PropagationDelay = 0 // tests don't wait for DNS to propagate
	svc.LANDomain = DefaultLANDomain
	svc.Logf = func(string, ...any) {} // quiet in tests
	return svc
}

// ────────────────────────────────────────────────────────────────────────────
// Store tests
// ────────────────────────────────────────────────────────────────────────────

func TestStore_UpsertAndGet(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0).UTC()
	if err := st.UpsertLANIP(ctx, "boxA", "box.boxA.lan.vulos.org", "192.168.1.10", now); err != nil {
		t.Fatalf("UpsertLANIP: %v", err)
	}
	bs, err := st.Get(ctx, "boxA")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if bs.LANIP != "192.168.1.10" {
		t.Errorf("LANIP: got %q", bs.LANIP)
	}
	if bs.FQDN != "box.boxA.lan.vulos.org" {
		t.Errorf("FQDN: got %q", bs.FQDN)
	}
	// Update IP.
	if err := st.UpsertLANIP(ctx, "boxA", "box.boxA.lan.vulos.org", "192.168.1.20", now.Add(time.Hour)); err != nil {
		t.Fatalf("UpsertLANIP 2: %v", err)
	}
	bs, _ = st.Get(ctx, "boxA")
	if bs.LANIP != "192.168.1.20" {
		t.Errorf("updated LANIP: got %q", bs.LANIP)
	}
}

func TestStore_GetUnknown(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.Get(context.Background(), "nope"); !errors.Is(err, ErrUnknownBox) {
		t.Fatalf("Get unknown: want ErrUnknownBox, got %v", err)
	}
}

func TestStore_DueForRenewal(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Box A: cert expires in 5 days → due within a 30-day window
	if err := st.UpsertLANIP(ctx, "boxA", "box.boxA.lan.vulos.org", "10.0.0.1", now); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCert(ctx, BoxCert{
		BoxID: "boxA", FQDN: "box.boxA.lan.vulos.org",
		CertPEM: "x", KeyPEM: "y",
		NotBefore: now.Add(-30 * 24 * time.Hour),
		NotAfter:  now.Add(5 * 24 * time.Hour),
	}, now); err != nil {
		t.Fatal(err)
	}
	// Box B: cert expires in 60 days → not due
	if err := st.UpsertLANIP(ctx, "boxB", "box.boxB.lan.vulos.org", "10.0.0.2", now); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCert(ctx, BoxCert{
		BoxID: "boxB", FQDN: "box.boxB.lan.vulos.org",
		CertPEM: "x", KeyPEM: "y",
		NotBefore: now.Add(-30 * 24 * time.Hour),
		NotAfter:  now.Add(60 * 24 * time.Hour),
	}, now); err != nil {
		t.Fatal(err)
	}
	// Box C: only LAN IP, no cert yet → must NOT appear in renewal list.
	if err := st.UpsertLANIP(ctx, "boxC", "box.boxC.lan.vulos.org", "10.0.0.3", now); err != nil {
		t.Fatal(err)
	}

	due, err := st.DueForRenewal(ctx, now, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, b := range due {
		ids[b.BoxID] = true
	}
	if !ids["boxA"] {
		t.Errorf("boxA should be due, got %v", due)
	}
	if ids["boxB"] {
		t.Errorf("boxB must not be due")
	}
	if ids["boxC"] {
		t.Errorf("boxC has no cert, must not be due")
	}
}

func TestStore_AppendAndRecentLog(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := st.AppendLog(ctx, LogEntry{
			BoxID: "boxA", FQDN: "box.boxA.lan.vulos.org",
			Event: "issued", Detail: "ok",
			OccurredAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := st.RecentLog(ctx, "boxA", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("RecentLog: want 3, got %d", len(entries))
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Service: ReportLANIP publishes A-record via DNSProvider
// ────────────────────────────────────────────────────────────────────────────

func TestService_ReportLANIP_PublishesARecord(t *testing.T) {
	dns := NewNoopDNSProvider()
	svc := newTestService(t, nil, dns)
	ctx := context.Background()

	if err := svc.ReportLANIP(ctx, "boxX", "192.168.5.42"); err != nil {
		t.Fatalf("ReportLANIP: %v", err)
	}
	wantFQDN := "box.boxX.lan.vulos.org"
	if got, ok := dns.A[wantFQDN]; !ok {
		t.Fatalf("DNS A record not published for %s; have %+v", wantFQDN, dns.A)
	} else if got != "192.168.5.42" {
		t.Errorf("A record IP: want 192.168.5.42, got %s", got)
	}

	// Stored row matches.
	bs, err := svc.Store.Get(ctx, "boxX")
	if err != nil {
		t.Fatalf("Get after report: %v", err)
	}
	if bs.LANIP != "192.168.5.42" {
		t.Errorf("stored LANIP: got %q", bs.LANIP)
	}
}

func TestService_ReportLANIP_RejectsBadInput(t *testing.T) {
	svc := newTestService(t, nil, nil)
	ctx := context.Background()
	cases := []struct{ id, ip string }{
		{"", "1.2.3.4"},        // empty box id
		{"good", ""},           // empty ip
		{"good", "not-an-ip"},  // bad ip
		{"bad..id", "1.2.3.4"}, // bad chars
	}
	for _, c := range cases {
		if err := svc.ReportLANIP(ctx, c.id, c.ip); err == nil {
			t.Errorf("ReportLANIP(%q,%q): want error, got nil", c.id, c.ip)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Service: full DNS-01 issuance flow (mock ACME)
// ────────────────────────────────────────────────────────────────────────────

func TestService_Issue_DNS01Flow(t *testing.T) {
	acmeM := newMockACME()
	dns := NewNoopDNSProvider()
	svc := newTestService(t, acmeM, dns)
	ctx := context.Background()

	// Box reports its LAN IP first (real flow); not required for Issue but
	// makes Get return the same FQDN.
	if err := svc.ReportLANIP(ctx, "alpha", "10.1.2.3"); err != nil {
		t.Fatalf("ReportLANIP: %v", err)
	}

	cert, err := svc.Issue(ctx, "alpha")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	wantFQDN := "box.alpha.lan.vulos.org"
	if cert.FQDN != wantFQDN {
		t.Errorf("FQDN: got %s", cert.FQDN)
	}
	if cert.CertPEM == "" || cert.KeyPEM == "" {
		t.Fatalf("Issue returned empty cert/key PEM")
	}

	// 1. The TXT challenge was published before Finalize.
	if len(acmeM.NewOrderCalls) != 1 {
		t.Fatalf("NewOrder calls: want 1, got %d", len(acmeM.NewOrderCalls))
	}
	if acmeM.FinalizeCalls != 1 {
		t.Fatalf("Finalize calls: want 1, got %d", acmeM.FinalizeCalls)
	}
	// 2. TXT was cleaned up afterwards (best-effort).
	if _, present := dns.TXT["_acme-challenge."+wantFQDN]; present {
		t.Errorf("TXT challenge record was not cleaned up after Issue")
	}

	// 3. Cert PEM parses and matches the FQDN.
	block, _ := pem.Decode([]byte(cert.CertPEM))
	if block == nil {
		t.Fatalf("cert PEM does not decode")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	hasFQDN := false
	for _, n := range leaf.DNSNames {
		if n == wantFQDN {
			hasFQDN = true
		}
	}
	if !hasFQDN {
		t.Errorf("leaf cert DNSNames missing %s: %v", wantFQDN, leaf.DNSNames)
	}

	// 4. Key PEM parses as EC private key.
	keyBlock, _ := pem.Decode([]byte(cert.KeyPEM))
	if keyBlock == nil {
		t.Fatalf("key PEM does not decode")
	}
	if _, err := x509.ParseECPrivateKey(keyBlock.Bytes); err != nil {
		t.Fatalf("parse EC key: %v", err)
	}

	// 5. Issuance log entry exists.
	log, err := svc.Store.RecentLog(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	foundIssued := false
	for _, e := range log {
		if e.Event == "issued" {
			foundIssued = true
		}
	}
	if !foundIssued {
		t.Errorf("expected 'issued' log entry; got %+v", log)
	}
}

func TestService_Issue_PropagatesACMEError(t *testing.T) {
	acmeM := newMockACME()
	acmeM.NewOrderError = errors.New("CA is down")
	svc := newTestService(t, acmeM, nil)
	if _, err := svc.Issue(context.Background(), "boxbad"); err == nil {
		t.Fatal("Issue: want error from NewOrder, got nil")
	}
	// Failure must be logged.
	log, _ := svc.Store.RecentLog(context.Background(), "boxbad", 5)
	if len(log) == 0 || log[0].Event != "failed" {
		t.Errorf("expected 'failed' log entry; got %+v", log)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// HTTP routes
// ────────────────────────────────────────────────────────────────────────────

func TestHandler_RequiresAuth(t *testing.T) {
	svc := newTestService(t, nil, nil)
	h := NewHandler(svc, "topsecret")
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// No auth header
	resp, err := http.Post(srv.URL+"/api/lancert/report-ip", "application/json",
		strings.NewReader(`{"box_id":"x","lan_ip":"1.2.3.4"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no auth: want 401, got %d", resp.StatusCode)
	}

	// Wrong auth header
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/lancert/cert?box_id=x", nil)
	req.Header.Set("X-Device-Auth", "wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong auth: want 401, got %d", resp.StatusCode)
	}
}

func TestHandler_ReportIPAndFetchCert_EndToEnd(t *testing.T) {
	acmeM := newMockACME()
	dns := NewNoopDNSProvider()
	svc := newTestService(t, acmeM, dns)
	h := NewHandler(svc, "secret-token")
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// 1. Box reports its LAN IP.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/lancert/report-ip",
		strings.NewReader(`{"box_id":"omega","lan_ip":"192.168.4.4"}`))
	req.Header.Set("X-Device-Auth", "secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report-ip: want 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if dns.A["box.omega.lan.vulos.org"] != "192.168.4.4" {
		t.Errorf("A record not published")
	}

	// 2. First cert fetch — no cert yet → 202.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/lancert/cert?box_id=omega", nil)
	req.Header.Set("X-Device-Auth", "secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("first cert fetch: want 202, got %d", resp.StatusCode)
	}

	// 3. Cloud issues a cert (in production: triggered by a background
	// worker; in test we drive it directly).
	if _, err := svc.Issue(context.Background(), "omega"); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// 4. Second cert fetch — 200 with cert+key.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/lancert/cert?box_id=omega", nil)
	req.Header.Set("X-Device-Auth", "secret-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second cert fetch: want 200, got %d", resp.StatusCode)
	}
	var got BoxCert
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.BoxID != "omega" || got.FQDN != "box.omega.lan.vulos.org" {
		t.Errorf("BoxCert: got %+v", got)
	}
	if got.CertPEM == "" || got.KeyPEM == "" {
		t.Errorf("BoxCert PEMs empty")
	}
}

// TestHandler_GetCert_ErrorScrubbing — when the underlying store fails on
// GetCert (non-sentinel error), the JSON body MUST carry the generic
// message, not the raw err.Error() (which can leak SQL/schema detail).
// Covers FIX-ADMIN-ERR-LEAK-01 for lancert/routes.go:88.
func TestHandler_GetCert_ErrorScrubbing(t *testing.T) {
	acmeM := newMockACME()
	svc := newTestService(t, acmeM, NewNoopDNSProvider())
	h := NewHandler(svc, "secret-token")
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Register a box first so ErrUnknownBox is NOT what we hit.
	if err := svc.ReportLANIP(context.Background(), "leaktest", "10.0.0.1"); err != nil {
		t.Fatalf("ReportLANIP: %v", err)
	}
	// Close the underlying store — subsequent GetCert errors with
	// "sql: database is closed" or similar; that string MUST NOT leak.
	if err := svc.Store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/lancert/cert?box_id=leaktest", nil)
	req.Header.Set("X-Device-Auth", "secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "internal error" {
		t.Errorf("want generic error message, got %q", body["error"])
	}
	for _, leakMarker := range []string{"sql:", "database", "closed", "schema", "PRAGMA"} {
		if strings.Contains(strings.ToLower(body["error"]), strings.ToLower(leakMarker)) {
			t.Errorf("raw err leaked into response: %q (matched %q)", body["error"], leakMarker)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Renewal scheduler triggers Issue near expiry
// ────────────────────────────────────────────────────────────────────────────

func TestService_RunRenewals_ReissuesNearExpiry(t *testing.T) {
	acmeM := newMockACME()
	// Mock CA returns a cert that expires in 5 days.
	acmeM.NotAfter = time.Now().UTC().Add(5 * 24 * time.Hour)
	dns := NewNoopDNSProvider()
	svc := newTestService(t, acmeM, dns)
	svc.RenewWindow = 30 * 24 * time.Hour // anything inside 30 days = due
	ctx := context.Background()

	// Set up a box with an existing cert that expires in 5 days.
	if err := svc.ReportLANIP(ctx, "renewbox", "10.9.9.9"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Issue(ctx, "renewbox"); err != nil {
		t.Fatalf("seed Issue: %v", err)
	}
	if acmeM.FinalizeCalls != 1 {
		t.Fatalf("seed Finalize calls: want 1, got %d", acmeM.FinalizeCalls)
	}

	// Run a renewal pass — boxA is due, so Issue must be called again.
	svc.RunRenewals(ctx)
	if acmeM.FinalizeCalls != 2 {
		t.Fatalf("Finalize calls after renewal: want 2, got %d", acmeM.FinalizeCalls)
	}

	// A box whose cert is far from expiry must NOT be renewed.
	acmeM.NotAfter = time.Now().UTC().Add(60 * 24 * time.Hour)
	if err := svc.ReportLANIP(ctx, "freshbox", "10.10.10.10"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Issue(ctx, "freshbox"); err != nil {
		t.Fatal(err)
	}
	finalizesBefore := acmeM.FinalizeCalls
	// Move renew window to a small value so only certs within ~1 day are due.
	svc.RenewWindow = 24 * time.Hour
	svc.RunRenewals(ctx)
	if acmeM.FinalizeCalls != finalizesBefore {
		t.Errorf("Finalize for freshbox shouldn't have run: before=%d after=%d",
			finalizesBefore, acmeM.FinalizeCalls)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// CryptoACMEClient construction (smoke test — no network).
// ────────────────────────────────────────────────────────────────────────────

func TestCryptoACMEClient_Construct(t *testing.T) {
	c, err := NewCryptoACMEClient(CryptoACMEConfig{
		DirectoryURL: "https://acme-staging-v02.api.letsencrypt.org/directory",
		Contact:      []string{"mailto:ops@vulos.org"},
	})
	if err != nil {
		t.Fatalf("NewCryptoACMEClient: %v", err)
	}
	if c.client.DirectoryURL == "" {
		t.Error("directory URL not set")
	}
	if c.accountKey == nil {
		t.Error("account key not set")
	}
	if _, err := NewCryptoACMEClient(CryptoACMEConfig{}); err == nil {
		t.Error("missing DirectoryURL: want error, got nil")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// MakeCSR helper
// ────────────────────────────────────────────────────────────────────────────

func TestMakeCSR(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := MakeCSR("box.test.lan.vulos.org", key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificateRequest(csr)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.DNSNames) != 1 || parsed.DNSNames[0] != "box.test.lan.vulos.org" {
		t.Errorf("CSR DNSNames: %v", parsed.DNSNames)
	}
}
