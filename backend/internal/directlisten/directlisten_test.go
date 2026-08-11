// directlisten_test.go — DIRECT-IP (OS side): the public listener enforces the
// SAME auth as the relay-fronted path (unauth -> 401), serves the probe (ownership
// proof), requires TLS, derives/validates the advertised endpoint, and is gated
// off by default. The self-reachability check is exercised end-to-end.
package directlisten

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// authParityHandler is a stand-in for the OS's real handler chain: it returns 401
// for any request without the X-Test-Auth header, mirroring authHandler.Middleware
// returning 401 when X-User-ID is unset. The point of the test is that the direct
// listener does not weaken THIS: whatever the handler enforces, direct enforces.
func authParityHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Auth") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("unauthorized"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("authed-ok"))
	})
}

// loopbackTLS builds a TLS config with a self-signed cert covering 127.0.0.1 so
// the listener path is exercised without real ACME. Returned as tlsConfigOverride.
func loopbackTLS(t *testing.T) *tls.Config {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "direct-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

// startDirect brings a direct Service up on loopback with a self-signed cert and
// returns it + an https client that trusts the self-signed cert.
func startDirect(t *testing.T, handler http.Handler) (*Service, *http.Client) {
	t.Helper()
	svc, err := New(Config{
		Handler:           handler,
		AdvertiseEndpoint: "https://localhost", // https required; overridden per-test via Addr below
		Addr:              "127.0.0.1:0",
		CertMode:          CertModeProvided,
		tlsConfigOverride: loopbackTLS(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { svc.Stop(context.Background()) })
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	return svc, client
}

func baseURL(svc *Service) string {
	// The listener bound an ephemeral port; talk to it directly over https.
	return "https://" + svc.Addr()
}

func TestDirect_AuthParity_UnauthIs401(t *testing.T) {
	svc, client := startDirect(t, authParityHandler())

	// No auth header -> 401, EXACTLY as the relay-fronted path would return. The
	// direct listener is not a bypass.
	resp, err := client.Get(baseURL(svc) + "/api/anything")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth request over direct must be 401, got %d", resp.StatusCode)
	}

	// With auth -> 200. Same handler, faster transport.
	req, _ := http.NewRequest("GET", baseURL(svc)+"/api/anything", nil)
	req.Header.Set("X-Test-Auth", "yes")
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatalf("authed GET: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("authed request should be 200, got %d", resp2.StatusCode)
	}
	body, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(body), "authed-ok") {
		t.Fatalf("authed body = %q", body)
	}
}

func TestDirect_ProbeEchoesNonce_AndBypassesAuthOnlyForProbe(t *testing.T) {
	svc, client := startDirect(t, authParityHandler())

	// The probe path is the ONE unauthenticated route (it echoes the relay's
	// nonce). It must answer 200 with the nonce EVEN THOUGH no auth header is set —
	// proving the box controls the endpoint without exposing any user data.
	req, _ := http.NewRequest("GET", baseURL(svc)+ProbePath, nil)
	req.Header.Set(ProbeHeader, "deadbeefcafe")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("probe GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe should be 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != "deadbeefcafe" {
		t.Fatalf("probe must echo the nonce, got %q", body)
	}

	// A probe with NO nonce is 400 (not a 200) — it can't be used as an open
	// reflector and doesn't prove anything.
	req2, _ := http.NewRequest("GET", baseURL(svc)+ProbePath, nil)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("empty probe: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("probe without a nonce must be 400, got %d", resp2.StatusCode)
	}
}

func TestDirect_TLSRequired_NoCleartext(t *testing.T) {
	// Mark whether the OS handler ever runs for a cleartext request; it must NOT —
	// the TLS listener rejects the plaintext bytes before any app handler sees them.
	var handlerRan bool
	svc, _ := startDirect(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerRan = true
		w.WriteHeader(http.StatusOK)
	}))

	// A plain-HTTP (cleartext) request to the TLS listener must NOT be served. Go's
	// TLS server answers a cleartext request with a 400 "client sent an HTTP request
	// to an HTTPS server" — never a 200, and the app handler never runs. Either a
	// transport error OR a non-2xx status (with the handler not running) proves no
	// cleartext fast path exists.
	plain := &http.Client{Timeout: 3 * time.Second}
	resp, err := plain.Get("http://" + svc.Addr() + "/api/anything")
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			t.Fatalf("cleartext request must NOT be served with 2xx, got %d", resp.StatusCode)
		}
	}
	if handlerRan {
		t.Fatal("the OS handler must never run for a cleartext request (TLS required)")
	}
}

func TestDirect_SelfReachability_PassesWhenServing(t *testing.T) {
	// A Service whose advertised endpoint IS its own loopback listener should pass
	// the self-reachability check (it serves the probe + echoes the nonce). We bind
	// an ephemeral port, then point the self-check at the ACTUAL bound address (no
	// racy rebind of a freed port).
	svc, _ := startDirect(t, authParityHandler())
	svc.setEndpointForTest("https://" + svc.Addr())
	if err := svc.CheckReachable(context.Background(), true /*insecure TLS: self-signed*/); err != nil {
		t.Fatalf("self-reachability should pass for a serving endpoint, got %v", err)
	}
}

func TestDirect_SelfReachability_FailsWhenFirewalled(t *testing.T) {
	// Advertise an endpoint that nothing serves (a closed port) — the self-check
	// must FAIL, so the box would not advertise a firewalled/unreachable endpoint.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close() // now nothing listens here

	svc, err := New(Config{
		Handler:           authParityHandler(),
		AdvertiseEndpoint: "https://" + addr,
		Addr:              "127.0.0.1:0", // listener binds elsewhere; advertised endpoint is dead
		CertMode:          CertModeProvided,
		tlsConfigOverride: loopbackTLS(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { svc.Stop(context.Background()) })

	if err := svc.CheckReachable(context.Background(), true); err == nil {
		t.Fatal("self-reachability must FAIL for an unreachable/firewalled endpoint")
	}
}

func TestDirect_New_FailsClosed(t *testing.T) {
	// ACME mode without a hostname must be refused (fail closed, not an open
	// listener that cannot serve).
	if _, err := New(Config{Handler: authParityHandler(), CertMode: CertModeACME}); err == nil {
		t.Fatal("ACME mode without a hostname must error")
	}
	// Provided mode without cert files must be refused.
	if _, err := New(Config{Handler: authParityHandler(), CertMode: CertModeProvided}); err == nil {
		t.Fatal("provided mode without cert files must error")
	}
	// A nil handler must be refused.
	if _, err := New(Config{Hostname: "x.example.net", CertMode: CertModeACME, ACMECacheDir: "/tmp/x"}); err == nil {
		t.Fatal("nil handler must error")
	}
}

func TestDirect_EndpointDerivation(t *testing.T) {
	cases := []struct {
		name, host, addr, advertise, want string
		wantErr                           bool
	}{
		{name: "hostname default 443", host: "box1.example.net", addr: ":443", want: "https://box1.example.net"},
		{name: "hostname custom port", host: "box1.example.net", addr: ":8443", want: "https://box1.example.net:8443"},
		{name: "explicit https advertise", advertise: "https://direct.example.net/", want: "https://direct.example.net"},
		{name: "cleartext advertise refused", advertise: "http://direct.example.net", wantErr: true},
		{name: "no host no advertise refused", addr: ":443", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep, err := resolveEndpoint(Config{Hostname: tc.host, Addr: tc.addr, AdvertiseEndpoint: tc.advertise})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got endpoint %q", ep)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ep != tc.want {
				t.Fatalf("endpoint = %q, want %q", ep, tc.want)
			}
		})
	}
}

func TestDirect_EnvGatedOffByDefault(t *testing.T) {
	// With no env set, the listener is NOT enabled — pure relay behavior.
	t.Setenv("VULOS_DIRECT_ENABLE", "")
	c := FromEnv("/tmp/acme-cache")
	if c.Enabled {
		t.Fatal("direct listener must be OFF by default")
	}
	// Opt-in flips it on and defaults are sane.
	t.Setenv("VULOS_DIRECT_ENABLE", "1")
	t.Setenv("VULOS_DIRECT_HOSTNAME", "box1.example.net")
	c2 := FromEnv("/tmp/acme-cache")
	if !c2.Enabled || c2.Hostname != "box1.example.net" {
		t.Fatalf("opt-in parse failed: %+v", c2)
	}
	if c2.Addr != ":443" || c2.CertMode != CertModeACME {
		t.Fatalf("defaults wrong: %+v", c2)
	}
	if c2.ACMECacheDir != "/tmp/acme-cache" {
		t.Fatalf("default ACME cache not applied: %q", c2.ACMECacheDir)
	}
}

// The self-reachability probe can skip certificate verification, which exists so
// a test can point it at a self-signed loopback listener. Its doc comment says
// "production leaves it false", and today that is true — the single non-test
// caller passes false.
//
// A doc comment is not an enforcement. The failure mode is quiet in the worst
// direction: skipping verification makes the probe MORE likely to succeed, so a
// box would begin advertising a direct endpoint whose certificate nobody
// checked, and nothing would look broken. So production refuses the insecure
// path outright rather than relying on nobody passing true.
func TestInsecureTLSProbeIsRefusedInProduction(t *testing.T) {
	setProdForTest(t, true)
	svc := &Service{endpoint: "https://127.0.0.1:1"}
	err := svc.CheckReachable(context.Background(), true)
	if err == nil {
		t.Fatal("production accepted an unverified-TLS self-probe")
	}
	if !strings.Contains(err.Error(), "insecure") {
		t.Errorf("the refusal should say why it refused, got: %v", err)
	}
}

// The same call outside production still takes the insecure path — the flag has
// to keep working, or the loopback tests above are testing nothing.
func TestInsecureTLSProbeStillWorksOutsideProduction(t *testing.T) {
	setProdForTest(t, false)
	svc := &Service{endpoint: "https://127.0.0.1:1"}
	err := svc.CheckReachable(context.Background(), true)
	// 127.0.0.1:1 has nothing listening, so this fails — but it must fail as an
	// UNREACHABLE endpoint, not as a refusal, which is what proves the insecure
	// path was entered rather than short-circuited.
	if err == nil {
		t.Fatal("expected the probe to fail against a closed port")
	}
	if strings.Contains(err.Error(), "insecure") {
		t.Errorf("development refused instead of probing: %v", err)
	}
}

func setProdForTest(t *testing.T, on bool) {
	t.Helper()
	prev, had := os.LookupEnv("VULOS_ENV")
	if on {
		os.Setenv("VULOS_ENV", "prod")
	} else {
		os.Unsetenv("VULOS_ENV")
	}
	t.Cleanup(func() {
		if had {
			os.Setenv("VULOS_ENV", prev)
		} else {
			os.Unsetenv("VULOS_ENV")
		}
	})
}
