package lan

// lancert_puller.go — FIX-LANCERT-PULL-01: OS-side LAN-cert puller.
//
// This is the box-side counterpart to vulos-cloud's `cp/internal/lancert`
// package (see doc.go for the cross-repo contract). The cloud control-plane
// exposes two endpoints behind `X-Device-Auth: <CP_SHARED_SECRET>`:
//
//	POST /api/lancert/report-ip    { "box_id": "...", "lan_ip": "..." }
//	GET  /api/lancert/cert?box_id=<id>
//	  → 202 (issuance in progress)
//	  → 200 { "cert_pem": "...", "key_pem": "...", "fqdn": "...", "not_after": ... }
//
// The puller (a) reports the box's LAN IP at startup and on change, (b) polls
// the cert endpoint with exponential backoff until material is available, and
// (c) writes the PEMs atomically (tmp + rename) to [DefaultCertPath] /
// [DefaultKeyPath] — the same paths [LoadCertSource] mtime-watches, so a
// fresh cert is picked up by the next TLS handshake with no listener restart.
//
// Opt-in: the puller does nothing unless VULOS_LANCERT_ENABLE is truthy.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PullerConfig configures a LANCertPuller. Sensible zero-value defaults are
// filled in by [NewLANCertPuller]; the only strictly required field is BoxID.
type PullerConfig struct {
	// CloudBaseURL is the HTTPS base URL of the cloud control-plane that hosts
	// the lancert endpoints. Defaults to https://cp.vulos.org.
	CloudBaseURL string

	// SharedSecret is the value sent in the `X-Device-Auth` header on every
	// request. Defaults to the CP_SHARED_SECRET env var.
	SharedSecret string

	// BoxID identifies the box on the cloud side (the same id that forms the
	// `box.<id>.lan.vulos.org` FQDN). Required.
	BoxID string

	// LANIPProvider returns the LAN IP the puller should advertise. Called at
	// startup and on every poll tick so a DHCP change eventually propagates.
	// Defaults to [detectLANIP].
	LANIPProvider func() net.IP

	// CertPath / KeyPath override the on-disk destination paths. Defaults to
	// [DefaultCertPath] / [DefaultKeyPath] — the documented cross-repo contract.
	CertPath string
	KeyPath  string

	// InitialBackoff is the first sleep between cert-endpoint polls while the
	// cloud returns 202 (issuance in progress). Defaults to 2s.
	InitialBackoff time.Duration

	// MaxBackoff caps the exponential growth of the poll backoff. Defaults to 5m.
	MaxBackoff time.Duration

	// RenewCheckInterval is how often, after a successful pull, the puller
	// re-polls the cert endpoint to fetch a renewed cert. Defaults to 6h —
	// renewals come from the cloud side; this is a slow check, not a hot loop.
	RenewCheckInterval time.Duration

	// HTTPClient overrides the http.Client used for cloud calls. When supplied
	// the caller is fully responsible for the transport's TLS configuration
	// (this is the injection point tests use to point at an httptest.Server).
	// When nil, [NewLANCertPuller] builds a hardened client that requires HTTPS
	// and applies the pin in PinnedCACertPEM / PinnedSPKISHA256 (see below).
	HTTPClient *http.Client

	// PinnedCACertPEM, when non-empty, is a PEM bundle of one or more CA
	// certificates that is used as the *only* root set when validating the
	// control-plane's TLS certificate. This defends a first-boot box against a
	// DNS/transparent-proxy MITM that would otherwise present a cert chaining to
	// a publicly-trusted (but attacker-controlled, e.g. via a rogue ACME issuance
	// or a compromised intermediate) root and harvest the shared secret. Sourced
	// by default from the VULOS_LANCERT_CA_PEM / VULOS_LANCERT_CA_FILE env vars.
	PinnedCACertPEM string

	// PinnedSPKISHA256, when non-empty, is a set of base64-encoded SHA-256
	// SubjectPublicKeyInfo (SPKI) pins. At least one certificate in the verified
	// chain (leaf or any intermediate/root) must match one of these pins or the
	// handshake is rejected. This is an additional, key-continuity pin layered on
	// top of normal chain validation — it survives CA-root churn better than a
	// full cert pin. Sourced by default from VULOS_LANCERT_SPKI_PINS (comma- or
	// space-separated). The accepted format matches the `openssl ... | openssl
	// dgst -sha256 -binary | base64` SPKI-pin convention.
	PinnedSPKISHA256 []string

	// AllowInsecure, when true, downgrades the hardened defaults: it permits a
	// plaintext (http://) CloudBaseURL and skips pinning. This exists ONLY for
	// local dev / tests against an httptest.Server and is gated on the
	// VULOS_LANCERT_ALLOW_INSECURE env var when not set programmatically. It is
	// refused in production paths. Never set this against a real control-plane.
	AllowInsecure bool
}

// LANCertPuller is the background goroutine that bridges cloud LANCERT-01
// with the OS's [LoadCertSource]. Construct with [NewLANCertPuller]; start
// with [LANCertPuller.Run] (typically as `go puller.Run(shutdownCtx)`).
type LANCertPuller struct {
	cfg PullerConfig
}

// PullerEnabled reports whether the LANCERT puller is opted-in for this
// process. Truthy: VULOS_LANCERT_ENABLE in {"1","true","yes"}, case-insensitive.
func PullerEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VULOS_LANCERT_ENABLE"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// NewLANCertPuller returns a LANCertPuller with defaults filled in. It does
// NOT do any network I/O; call [LANCertPuller.Run] to start the background
// loop.
//
// SECURITY (audit P0-2): the puller sends CP_SHARED_SECRET on every request, so
// the transport MUST be authenticated. Unless AllowInsecure is set (dev/test
// only), this constructor refuses a plaintext CloudBaseURL and, when it builds
// the http.Client itself, pins the control-plane's TLS chain to either a
// configured CA bundle or a set of SPKI pins. A caller that injects its own
// HTTPClient owns its TLS config (that is the test seam); we still refuse a
// plaintext base URL in that case unless AllowInsecure is set.
func NewLANCertPuller(cfg PullerConfig) (*LANCertPuller, error) {
	if strings.TrimSpace(cfg.BoxID) == "" {
		return nil, errors.New("lan: PullerConfig.BoxID is required")
	}
	if cfg.CloudBaseURL == "" {
		cfg.CloudBaseURL = "https://cp.vulos.org"
	}
	if cfg.SharedSecret == "" {
		cfg.SharedSecret = os.Getenv("CP_SHARED_SECRET")
	}
	if cfg.LANIPProvider == nil {
		cfg.LANIPProvider = detectLANIP
	}
	if cfg.CertPath == "" {
		cfg.CertPath = DefaultCertPath
	}
	if cfg.KeyPath == "" {
		cfg.KeyPath = DefaultKeyPath
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 2 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 5 * time.Minute
	}
	if cfg.RenewCheckInterval <= 0 {
		cfg.RenewCheckInterval = 6 * time.Hour
	}

	// Resolve the insecure escape hatch (env opt-in only).
	if !cfg.AllowInsecure {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("VULOS_LANCERT_ALLOW_INSECURE"))) {
		case "1", "true", "yes":
			cfg.AllowInsecure = true
		}
	}

	// Source pins from the environment when not set programmatically.
	if cfg.PinnedCACertPEM == "" {
		cfg.PinnedCACertPEM = lancertCAPEMFromEnv()
	}
	if len(cfg.PinnedSPKISHA256) == 0 {
		cfg.PinnedSPKISHA256 = splitPins(os.Getenv("VULOS_LANCERT_SPKI_PINS"))
	}

	// Refuse plaintext: the shared secret travels on this transport, so a
	// plaintext base URL would let a network attacker harvest it. Allowed only
	// behind the explicit insecure escape hatch.
	base, err := url.Parse(cfg.CloudBaseURL)
	if err != nil {
		return nil, fmt.Errorf("lan: PullerConfig.CloudBaseURL %q: %w", cfg.CloudBaseURL, err)
	}
	if base.Scheme != "https" && !cfg.AllowInsecure {
		return nil, fmt.Errorf("lan: PullerConfig.CloudBaseURL must be https (got %q); "+
			"set AllowInsecure / VULOS_LANCERT_ALLOW_INSECURE=1 only for local dev", base.Scheme)
	}

	if cfg.HTTPClient == nil {
		client, cErr := newPinnedHTTPClient(cfg)
		if cErr != nil {
			return nil, cErr
		}
		cfg.HTTPClient = client
	}
	return &LANCertPuller{cfg: cfg}, nil
}

// lancertCAPEMFromEnv returns the pinned CA PEM from VULOS_LANCERT_CA_PEM (raw
// PEM) or VULOS_LANCERT_CA_FILE (path to a PEM file), whichever is set. A
// missing file is treated as "no pin" (logged by the caller path); we do not
// fail construction here so the env can be set on hosts that lack the file yet.
func lancertCAPEMFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("VULOS_LANCERT_CA_PEM")); v != "" {
		return v
	}
	if path := strings.TrimSpace(os.Getenv("VULOS_LANCERT_CA_FILE")); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			return string(b)
		}
		log.Printf("[lancert-puller] VULOS_LANCERT_CA_FILE=%s unreadable — ignoring", path)
	}
	return ""
}

// splitPins parses a comma/space/newline-separated list of base64 SPKI pins.
func splitPins(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// newPinnedHTTPClient builds the hardened http.Client used when the caller did
// not inject one. It enforces:
//   - TLS 1.2+ with full chain verification (never InsecureSkipVerify);
//   - when PinnedCACertPEM is set, that bundle is the ONLY accepted root pool
//     (system roots are not consulted), so a publicly-trusted-but-rogue chain
//     is rejected;
//   - when PinnedSPKISHA256 is set, at least one cert in the verified chain
//     must match one of the SPKI pins.
func newPinnedHTTPClient(cfg PullerConfig) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if cfg.PinnedCACertPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.PinnedCACertPEM)) {
			return nil, errors.New("lan: PinnedCACertPEM contained no usable certificates")
		}
		tlsCfg.RootCAs = pool
	}

	if len(cfg.PinnedSPKISHA256) > 0 {
		pins, err := decodeSPKIPins(cfg.PinnedSPKISHA256)
		if err != nil {
			return nil, err
		}
		// VerifyConnection runs AFTER normal chain verification (because
		// InsecureSkipVerify is false), so we can trust verifiedChains.
		tlsCfg.VerifyConnection = func(cs tls.ConnectionState) error {
			for _, chain := range cs.VerifiedChains {
				for _, cert := range chain {
					sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
					if _, ok := pins[sum]; ok {
						return nil
					}
				}
			}
			return errors.New("lan: control-plane TLS chain did not match any configured SPKI pin")
		}
	}

	transport := &http.Transport{
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: transport}, nil
}

// decodeSPKIPins decodes base64 SPKI pins into a set of 32-byte SHA-256 sums.
func decodeSPKIPins(pins []string) (map[[32]byte]struct{}, error) {
	out := make(map[[32]byte]struct{}, len(pins))
	for _, p := range pins {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("lan: SPKI pin %q is not valid base64: %w", p, err)
		}
		if len(raw) != sha256.Size {
			return nil, fmt.Errorf("lan: SPKI pin %q decodes to %d bytes, want %d", p, len(raw), sha256.Size)
		}
		var sum [32]byte
		copy(sum[:], raw)
		out[sum] = struct{}{}
	}
	return out, nil
}

// certResponse mirrors the JSON body returned by GET /api/lancert/cert on 200.
type certResponse struct {
	BoxID    string `json:"box_id"`
	FQDN     string `json:"fqdn"`
	CertPEM  string `json:"cert_pem"`
	KeyPEM   string `json:"key_pem"`
	NotAfter string `json:"not_after"`
}

// Run is the background loop. It returns only when ctx is cancelled. Errors
// are logged but never fatal — soft-degrade is the right behaviour because
// [LoadCertSource] already falls back to a self-signed cert when the on-disk
// material is absent.
func (p *LANCertPuller) Run(ctx context.Context) {
	log.Printf("[lancert-puller] starting (cloud=%s box=%s cert=%s)",
		p.cfg.CloudBaseURL, p.cfg.BoxID, p.cfg.CertPath)

	// First-shot: report IP and pull. If the cert isn't ready yet, the
	// fetchOnce loop sleeps with backoff and retries. Once we have a cert we
	// move into the slow renewal-check cadence.
	for {
		if err := p.reportIP(ctx); err != nil {
			log.Printf("[lancert-puller] report-ip: %v (continuing)", err)
		}
		if err := p.fetchOnce(ctx); err != nil {
			log.Printf("[lancert-puller] fetch: %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(p.cfg.RenewCheckInterval):
			// Loop back: re-report IP (DHCP may have changed) and re-pull.
		}
	}
}

// ProbeOnce performs a single report-IP round-trip and returns its error. It
// exists as a SECURITY TEST SEAM (used by the backend/security pentest suite)
// so an external package can assert that the puller's hardened transport
// rejects a plaintext / unpinned / mismatched-cert control plane WITHOUT having
// to run the full background Run loop (which only logs errors). It performs no
// state mutation beyond what reportIP already does and is safe to call before
// Run. The returned error is non-nil whenever the TLS handshake or HTTP call
// failed — which is exactly the MITM-rejection signal the pentest asserts on.
func (p *LANCertPuller) ProbeOnce(ctx context.Context) error {
	return p.reportIP(ctx)
}

// reportIP POSTs the box's current LAN IP to /api/lancert/report-ip. Best
// effort: any non-2xx response is returned as an error so the caller can log
// it, but the puller keeps going either way.
func (p *LANCertPuller) reportIP(ctx context.Context) error {
	ip := p.cfg.LANIPProvider()
	if ip == nil {
		return errors.New("no LAN IP available")
	}
	body, err := json.Marshal(map[string]string{
		"box_id": p.cfg.BoxID,
		"lan_ip": ip.String(),
	})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	u, err := url.JoinPath(p.cfg.CloudBaseURL, "/api/lancert/report-ip")
	if err != nil {
		return fmt.Errorf("url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device-Auth", p.cfg.SharedSecret)

	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return nil
}

// fetchOnce polls the cert endpoint with exponential backoff until either
// (a) it gets 200 with cert+key (in which case it writes them and returns
// nil), (b) ctx is cancelled, or (c) the request errors irrecoverably.
func (p *LANCertPuller) fetchOnce(ctx context.Context) error {
	u, err := url.JoinPath(p.cfg.CloudBaseURL, "/api/lancert/cert")
	if err != nil {
		return fmt.Errorf("url: %w", err)
	}
	u = u + "?box_id=" + url.QueryEscape(p.cfg.BoxID)

	backoff := p.cfg.InitialBackoff
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return fmt.Errorf("new request: %w", err)
		}
		req.Header.Set("X-Device-Auth", p.cfg.SharedSecret)

		resp, err := p.cfg.HTTPClient.Do(req)
		if err != nil {
			// Network error — retry with backoff until ctx dies.
			log.Printf("[lancert-puller] get cert: %v (retry in %s)", err, backoff)
		} else {
			switch resp.StatusCode {
			case http.StatusOK:
				var cr certResponse
				dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
				if derr := dec.Decode(&cr); derr != nil {
					resp.Body.Close()
					return fmt.Errorf("decode 200 body: %w", derr)
				}
				resp.Body.Close()
				if cr.CertPEM == "" || cr.KeyPEM == "" {
					return errors.New("200 response missing cert_pem or key_pem")
				}
				if err := writePEMsAtomic(p.cfg.CertPath, p.cfg.KeyPath, cr.CertPEM, cr.KeyPEM); err != nil {
					return fmt.Errorf("write pems: %w", err)
				}
				log.Printf("[lancert-puller] installed cert for %s (not_after=%s) at %s",
					cr.FQDN, cr.NotAfter, p.cfg.CertPath)
				return nil
			case http.StatusAccepted:
				// Issuance still in progress. Drain + backoff.
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			default:
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				return fmt.Errorf("unexpected status %d", resp.StatusCode)
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > p.cfg.MaxBackoff {
			backoff = p.cfg.MaxBackoff
		}
	}
}

// writePEMsAtomic writes cert and key to their target paths atomically
// (tmp+rename) so [LoadCertSource]'s mtime watcher never observes a partially
// written file. Both files end up with 0600 perms — the cert is technically
// public but the key is not, and 0600 is the strictest safe default for both.
func writePEMsAtomic(certPath, keyPath, certPEM, keyPEM string) error {
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return fmt.Errorf("mkdir cert dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return fmt.Errorf("mkdir key dir: %w", err)
	}
	if err := writeFileAtomic(certPath, []byte(certPEM), 0o600); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	if err := writeFileAtomic(keyPath, []byte(keyPEM), 0o600); err != nil {
		return fmt.Errorf("write key: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to a sibling tmp file and renames it over path.
// On Linux/macOS rename(2) is atomic within a filesystem, which is what
// [LoadCertSource]'s mtime check relies on.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".lancert-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// On any error we want to clean up the tmp file; defer covers panic paths.
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
