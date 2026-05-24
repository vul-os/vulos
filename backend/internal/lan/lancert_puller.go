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

	// HTTPClient overrides the http.Client used for cloud calls. Defaults to
	// a client with a 15s timeout. Tests inject a transport pointed at a
	// httptest.Server.
	HTTPClient *http.Client
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
// NOT do any I/O; call [LANCertPuller.Run] to start the background loop.
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
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &LANCertPuller{cfg: cfg}, nil
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
