// Package directlisten is DIRECT-IP high-performance mode for the OS: a config-
// gated PUBLIC TLS listener that lets clients reach this box DIRECTLY (near-native
// latency + full bandwidth) instead of always routing through the Vulos relay.
//
// WHEN it runs: only when the box has a reachable public endpoint (a static IP or
// public hostname) AND the operator opts in. Off by default — most boxes are NAT'd
// / CGNAT and MUST stay on the relay path, which always works. This listener is the
// enabling primitive for high-performance use cases, not a replacement for the
// relay: clients try direct first and fall back to the relay tunnel on failure.
//
// SECURITY — direct means public exposure, so this is NOT a security downgrade:
//
//   - The listener serves the EXACT SAME http.Handler as the relay-fronted path
//     (secHeadersMiddleware(authHandler.Middleware(mainHandler)) in cmd/server).
//     It is the same app over a faster transport: the full auth/session/authz
//     stack, CSRF checks, rate limits, and security headers all apply identically.
//     There is no "direct is trusted" bypass — an unauthenticated request gets the
//     same 401 it would through the relay.
//   - TLS is REQUIRED. The listener only ever serves HTTPS: either an ACME/Let's-
//     Encrypt cert (when a public hostname is configured) or an operator-provided
//     cert. It never serves cleartext.
//   - OWNERSHIP PROOF: the box proves it controls the advertised endpoint by
//     serving DirectProbePath on this same public listener and echoing the relay's
//     one-time probe nonce. Combined with the relay's own SSRF-guarded probe, a box
//     cannot advertise an endpoint it does not actually serve. The probe path is
//     the ONLY unauthenticated route (it carries no user data — only the relay's
//     nonce echo) and is layered UNDER the auth handler, which it bypasses solely
//     for that one path.
package directlisten

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// ProbePath is the well-known path the relay GETs to verify this box's advertised
// direct endpoint (reachability + ownership). It MUST match vulos-relay's
// wire.DirectProbePath. The box echoes the relay's nonce header verbatim, proving
// it controls the endpoint. Kept as a local const so the OS does not import the
// relay Go module (the two repos share only the wire string, like gpuhost).
const ProbePath = "/_vulos-direct/probe"

// ProbeHeader carries the relay's one-time probe nonce; the box echoes it back in
// the response body. MUST match vulos-relay's wire.DirectProbeHeader.
const ProbeHeader = "X-Vulos-Direct-Probe"

// maxProbeNonce bounds the nonce we will echo, so the probe path can never be used
// to reflect a large attacker-supplied body.
const maxProbeNonce = 512

// CertMode selects how the public listener obtains its TLS certificate.
type CertMode int

const (
	// CertModeACME uses ACME/Let's-Encrypt (autocert) HTTP-01 for a public
	// hostname. Requires Hostname set and port 443 reachable for the challenge.
	CertModeACME CertMode = iota
	// CertModeProvided uses an operator-provided cert+key (files on disk). Used
	// when the box already has a cert (e.g. behind the operator's own PKI) or has
	// only a static IP (no hostname for ACME).
	CertModeProvided
)

// Config configures the direct public listener.
type Config struct {
	// Handler is the OS HTTP handler — the SAME one the relay-fronted path serves,
	// so auth/session/authz are identical. Required.
	Handler http.Handler

	// Hostname is the public DNS name this box is reachable at (e.g.
	// box1.example.net). Required for CertModeACME and for building the advertised
	// https endpoint. May be empty only for CertModeProvided with an IP endpoint.
	Hostname string

	// AdvertiseEndpoint overrides the advertised direct base URL. When empty it is
	// derived as https://<Hostname>[:port]. This is the URL handed to the relay for
	// clients to dial; it MUST be the public origin this listener answers on.
	AdvertiseEndpoint string

	// Addr is the public listen address. Defaults to ":443" (ACME + the standard
	// HTTPS port clients expect). Tests inject an ephemeral "127.0.0.1:0".
	Addr string

	// CertMode selects ACME vs a provided cert.
	CertMode CertMode

	// CertFile / KeyFile are the provided cert+key for CertModeProvided.
	CertFile, KeyFile string

	// ACMECacheDir is where autocert caches issued certs/account keys. Required for
	// CertModeACME (so a restart does not re-request from Let's Encrypt and hit
	// rate limits).
	ACMECacheDir string

	// ACMEEmail is the optional contact email for the ACME account.
	ACMEEmail string

	// tlsConfigOverride lets tests inject a ready TLS config (e.g. a self-signed
	// cert on loopback) so the listener path is exercised without real ACME.
	tlsConfigOverride *tls.Config
}

// Service is the box-side direct public listener + advertisement helper.
type Service struct {
	cfg      Config
	endpoint string // advertised https base URL

	mu      sync.Mutex
	srv     *http.Server
	ln      net.Listener
	started bool
}

// New validates cfg and builds a Service (binds nothing yet). It fails closed:
// an ACME mode without a hostname, or a provided mode without cert files, is an
// error rather than a listener that cannot serve.
func New(cfg Config) (*Service, error) {
	if cfg.Handler == nil {
		return nil, errors.New("directlisten: Handler is required")
	}
	if cfg.Addr == "" {
		cfg.Addr = ":443"
	}
	switch cfg.CertMode {
	case CertModeACME:
		if cfg.tlsConfigOverride == nil {
			if strings.TrimSpace(cfg.Hostname) == "" {
				return nil, errors.New("directlisten: ACME cert mode requires a public Hostname")
			}
			if strings.TrimSpace(cfg.ACMECacheDir) == "" {
				return nil, errors.New("directlisten: ACME cert mode requires ACMECacheDir")
			}
		}
	case CertModeProvided:
		if cfg.tlsConfigOverride == nil {
			if cfg.CertFile == "" || cfg.KeyFile == "" {
				return nil, errors.New("directlisten: provided cert mode requires CertFile and KeyFile")
			}
		}
	default:
		return nil, fmt.Errorf("directlisten: unknown cert mode %d", cfg.CertMode)
	}

	endpoint, err := resolveEndpoint(cfg)
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, endpoint: endpoint}, nil
}

// Endpoint returns the advertised direct base URL (https origin). This is the
// value handed to the relay so clients can discover + dial the box directly.
func (s *Service) Endpoint() string { return s.endpoint }

// Addr returns the bound listen address (meaningful after Start; how tests learn
// the ephemeral port).
func (s *Service) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.cfg.Addr
}

// Start binds the public TLS listener and serves. The handler served is wrapped
// so the UNAUTHENTICATED probe path is answered directly (echoing the relay's
// nonce) while EVERY OTHER path flows through the box's real auth handler — direct
// is not a security downgrade.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("directlisten: already started")
	}

	tlsCfg, err := s.tlsConfig()
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("directlisten: listen on %s: %w", s.cfg.Addr, err)
	}
	s.ln = ln
	tlsLn := tls.NewListener(ln, tlsCfg)
	srv := &http.Server{
		Handler:           s.wrap(s.cfg.Handler),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: streamed responses (SSE, downloads, WS) are long-lived,
		// matching the main OS server and the relay.
	}
	s.srv = srv
	go func() {
		if err := srv.Serve(tlsLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[direct] https serve error: %v", err)
		}
	}()
	log.Printf("[direct] public TLS listener serving on %s (endpoint=%s)", ln.Addr(), s.endpoint)
	s.started = true
	return nil
}

// Stop gracefully shuts the listener down.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return nil
	}
	err := s.srv.Shutdown(ctx)
	s.srv = nil
	s.started = false
	return err
}

// wrap layers the probe handler UNDER the auth handler: the probe path is the ONE
// unauthenticated route (it echoes only the relay's nonce; no user data), and
// everything else is the box's real, fully-authenticated handler. This is what
// keeps direct at auth parity with the relay path — there is no other bypass.
func (s *Service) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == ProbePath {
			serveProbe(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// serveProbe answers the relay's reachability/ownership probe by echoing the
// one-time nonce from ProbeHeader. Only a caller that reached THIS public TLS
// listener sees a request here, and only the relay knows the nonce it sent, so a
// correct echo proves this box controls the advertised endpoint. It returns 400
// (never 200) when no nonce is present, so it can't be used as an open reflector.
func serveProbe(w http.ResponseWriter, r *http.Request) {
	nonce := strings.TrimSpace(r.Header.Get(ProbeHeader))
	if nonce == "" || len(nonce) > maxProbeNonce {
		http.Error(w, "bad probe", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(nonce))
}

// tlsConfig builds the listener's TLS config per cert mode. ACME uses autocert
// with a HostWhitelist pinned to the box's hostname (so it never requests a cert
// for an attacker-supplied SNI).
func (s *Service) tlsConfig() (*tls.Config, error) {
	if s.cfg.tlsConfigOverride != nil {
		return s.cfg.tlsConfigOverride, nil
	}
	switch s.cfg.CertMode {
	case CertModeACME:
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(s.cfg.Hostname),
			Cache:      autocert.DirCache(s.cfg.ACMECacheDir),
			Email:      s.cfg.ACMEEmail,
		}
		cfg := m.TLSConfig()
		cfg.MinVersion = tls.VersionTLS12
		return cfg, nil
	case CertModeProvided:
		cert, err := tls.LoadX509KeyPair(s.cfg.CertFile, s.cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("directlisten: load cert: %w", err)
		}
		return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
	default:
		return nil, fmt.Errorf("directlisten: unknown cert mode %d", s.cfg.CertMode)
	}
}

// resolveEndpoint computes the advertised https base URL. An explicit
// AdvertiseEndpoint wins (and is validated as https); otherwise it is derived from
// Hostname + the listen port. The advertised endpoint MUST be https — a cleartext
// endpoint is refused (no cleartext fast path).
func resolveEndpoint(cfg Config) (string, error) {
	if ep := strings.TrimSpace(cfg.AdvertiseEndpoint); ep != "" {
		if !strings.HasPrefix(ep, "https://") {
			return "", errors.New("directlisten: AdvertiseEndpoint must be https")
		}
		return strings.TrimRight(ep, "/"), nil
	}
	host := strings.TrimSpace(cfg.Hostname)
	if host == "" {
		return "", errors.New("directlisten: cannot derive an endpoint without a Hostname or AdvertiseEndpoint")
	}
	// Derive host[:port] from the listen addr; omit :443 (the default clients use).
	_, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil || port == "" || port == "443" {
		return "https://" + host, nil
	}
	return "https://" + net.JoinHostPort(host, port), nil
}
