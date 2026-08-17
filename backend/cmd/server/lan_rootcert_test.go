package main

// lan_rootcert_test.go — ROOTDIST-01.
//
// Two properties, and both of them have bitten this repository before:
//
//  1. Neither route may be reachable without a session. services/security_test.go's
//     SEC-HARD-08 asserts publicPaths is an exhaustive allow-list, which is the
//     structural argument; this is the empirical one — a real request through
//     the real middleware at the real path.
//  2. The download must REFUSE to hand out a root that is not a CA or that is
//     unconstrained, and it must refuse on the READ path, because the documented
//     manual flow copies a file straight to the root path and never touches the
//     puller's write-side check.
//
// Every refusal assertion is preceded by a control on the same handler with a
// legitimate root, so "refused" cannot be produced by a handler that refuses
// everything.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/config"
	"vulos/backend/internal/lanca"
	"vulos/backend/services/auth"
)

// constrainedRootPEM mints a real name-constrained root through the production
// CA package — the artefact an owner would actually be handed.
func constrainedRootPEM(t *testing.T) []byte {
	t.Helper()
	root, err := lanca.NewRoot("test bench")
	if err != nil {
		t.Fatalf("lanca.NewRoot: %v", err)
	}
	return root.CertPEM()
}

// unconstrainedCAPEM is a CA identical in every visible respect except that it
// has no permittedSubtrees — the difference no install dialog on any OS shows.
func unconstrainedCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: "Unlimited Root CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestLANRootCertRoutes_RequireSession drives both routes through the real auth
// middleware with no credentials.
func TestLANRootCertRoutes_RequireSession(t *testing.T) {
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	handler := auth.NewHandler(store)
	handler.OnUserCreated = nil
	handler.OnUserLogin = nil
	handler.OnRoleChanged = nil

	// A REAL, valid root is on disk for this test. If the gate ever stops
	// working the handler runs and serves it, so the failure is a served
	// certificate rather than an empty 404 that could pass for a gate.
	dir := t.TempDir()
	rootPath := filepath.Join(dir, "lan-root.crt")
	if err := os.WriteFile(rootPath, constrainedRootPEM(t), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VULOS_LAN_ROOT_CERT", rootPath)

	mux := http.NewServeMux()
	handler.Register(mux)
	registerLANPairingRoutes(mux, &config.Config{Hostname: "testbox"}, nil)
	registerLANRootCertRoutes(mux)

	srv := httptest.NewServer(handler.Middleware(mux))
	defer srv.Close()

	for _, path := range []string{"/api/lan/rootcert", rootCertDownloadPath} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("ROOTDIST-01 REGRESSION: GET %s returned %d without a session, want 401. "+
				"These routes disclose the box's name, its LAN address and the owner's CA label; "+
				"body was %q", path, resp.StatusCode, string(body))
		}
		if strings.Contains(string(body), "BEGIN CERTIFICATE") {
			t.Fatalf("GET %s served certificate material to an unauthenticated caller", path)
		}
	}
}

// serveRootCertRoutes mounts the routes WITHOUT the auth middleware so the
// handler behaviour itself can be measured. Gating is proved above; this asks
// what the handler does once a request reaches it.
func serveRootCertRoutes(t *testing.T, rootPath string) *httptest.Server {
	t.Helper()
	t.Setenv("VULOS_LAN_ROOT_CERT", rootPath)
	mux := http.NewServeMux()
	registerLANRootCertRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestRootCertDownload_ServesTheRootAndItsFingerprint is the control for the
// refusal tests AND the fingerprint contract the whole flow rests on: the owner
// verifies out of band, so the value the box publishes must be the certificate's
// real SHA-256.
func TestRootCertDownload_ServesTheRootAndItsFingerprint(t *testing.T) {
	rootPEM := constrainedRootPEM(t)
	rootPath := filepath.Join(t.TempDir(), "lan-root.crt")
	if err := os.WriteFile(rootPath, rootPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := serveRootCertRoutes(t, rootPath)

	// Status route.
	resp, err := http.Get(srv.URL + "/api/lan/rootcert")
	if err != nil {
		t.Fatal(err)
	}
	var info rootCertInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !info.Present {
		t.Fatalf("CONTROL FAILED: a valid root on disk reported as absent (problem=%q)", info.Problem)
	}
	if info.SHA256 == "" {
		t.Fatal("no fingerprint published — the owner has nothing to verify the download against, " +
			"and the first fetch is over a connection they have not yet trusted")
	}
	if len(info.PermittedDNS) == 0 {
		t.Fatal("the panel cannot show what the root is limited to; permitted_dns was empty")
	}

	// Download route.
	dl, err := http.Get(srv.URL + rootCertDownloadPath)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(dl.Body)
	dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("CONTROL FAILED: download returned %d: %s", dl.StatusCode, body)
	}
	if !strings.Contains(string(body), "BEGIN CERTIFICATE") {
		t.Fatalf("download did not serve a PEM certificate: %q", string(body))
	}
	if strings.Contains(string(body), "PRIVATE KEY") {
		t.Fatal("the download contains PRIVATE KEY material — the CA key must never be on the box, let alone served")
	}
	if got := dl.Header.Get("X-Vulos-Root-SHA256"); got != info.SHA256 {
		t.Fatalf("the fingerprint on the download (%q) disagrees with the one the panel shows (%q) — "+
			"an owner comparing the panel's value would be verifying the wrong thing", got, info.SHA256)
	}
	if cd := dl.Header.Get("Content-Disposition"); !strings.Contains(cd, rootCertFileName) {
		t.Fatalf("Content-Disposition %q does not name %s; the browser will not save an installable file", cd, rootCertFileName)
	}
}

// TestRootCertDownload_RefusesAnUnconstrainedRoot. A control runs first on the
// same handler.
func TestRootCertDownload_RefusesAnUnconstrainedRoot(t *testing.T) {
	t.Setenv("VULOS_LANCERT_ALLOW_UNCONSTRAINED_ROOT", "")
	rootPath := filepath.Join(t.TempDir(), "lan-root.crt")
	srv := serveRootCertRoutes(t, rootPath)

	// CONTROL: constrained root downloads.
	if err := os.WriteFile(rootPath, constrainedRootPEM(t), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, err := http.Get(srv.URL + rootCertDownloadPath)
	if err != nil {
		t.Fatal(err)
	}
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("CONTROL FAILED: a constrained root would not download (%d)", ok.StatusCode)
	}

	// MEASURE: the same handler, an unconstrained CA hand-placed at the path.
	if err := os.WriteFile(rootPath, unconstrainedCAPEM(t), 0o644); err != nil {
		t.Fatal(err)
	}
	bad, err := http.Get(srv.URL + rootCertDownloadPath)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(bad.Body)
	bad.Body.Close()
	if bad.StatusCode == http.StatusOK || strings.Contains(string(body), "BEGIN CERTIFICATE") {
		t.Fatalf("ROOTDIST-01 REGRESSION: the box handed an owner an UNCONSTRAINED CA to install "+
			"(status %d). That grants their device a standing authority for ANY name, including public "+
			"sites — the exact property D101-B claims this design does not have, and no OS install "+
			"dialog shows the difference.", bad.StatusCode)
	}
	if !strings.Contains(string(body), "permittedSubtrees") {
		t.Fatalf("the refusal does not tell the owner WHY, so they will work around it: %q", string(body))
	}
}

// TestRootCertStatus_AbsentIsNotAnError: an owner who has never run the CA is in
// a normal state.
func TestRootCertStatus_AbsentIsNotAnError(t *testing.T) {
	srv := serveRootCertRoutes(t, filepath.Join(t.TempDir(), "nothing-here.crt"))

	resp, err := http.Get(srv.URL + "/api/lan/rootcert")
	if err != nil {
		t.Fatal(err)
	}
	var info rootCertInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status route returned %d for a box with no root; absent is a normal state, not a fault", resp.StatusCode)
	}
	if info.Present || info.Problem != "" {
		t.Fatalf("a box with no root reported present=%v problem=%q", info.Present, info.Problem)
	}
	if info.DownloadPath == "" {
		t.Fatal("download_path must be present even when the root is not, so the UI has one constant to link to")
	}

	dl, err := http.Get(srv.URL + rootCertDownloadPath)
	if err != nil {
		t.Fatal(err)
	}
	dl.Body.Close()
	if dl.StatusCode != http.StatusNotFound {
		t.Fatalf("download with no root returned %d, want 404", dl.StatusCode)
	}
}

// TestRootCertDownloadURL_UsesAnIPNotAName. The QR code exists for a phone, and
// a phone that cannot resolve mDNS is the case this entire flow exists for
// (Chrome on Android — see lanServiceRef.certIPs). A .local URL in the QR would
// fail on exactly the device it was added for.
func TestRootCertDownloadURL_UsesAnIPNotAName(t *testing.T) {
	got := rootCertDownloadURL()
	if got == "" {
		t.Skip("no LAN IP detected in this environment")
	}
	if !strings.HasSuffix(got, rootCertDownloadPath) {
		t.Fatalf("download URL %q does not end at the download route", got)
	}
	host := strings.TrimPrefix(got, "https://")
	host = strings.TrimSuffix(host, rootCertDownloadPath)
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	if net.ParseIP(h) == nil {
		t.Fatalf("the QR URL host %q is not an IP literal. A phone that cannot resolve .local is the "+
			"case this flow exists for, so a name here fails on exactly the device it was added for.", h)
	}
}
