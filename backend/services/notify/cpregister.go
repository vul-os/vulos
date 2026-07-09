package notify

// cpregister.go — CELL-SIDE registration client for PUSH send-on-behalf
// (vulos-unified-reachability-architecture, push section; closes the loop with
// the CP's POST /api/mail/push/{register,unregister}).
//
// # Why this exists
//
// The cell (managed box) holds a VAPID key pair and normally POSTs RFC 8291
// encrypted Web Push payloads DIRECTLY to the browser-vendor push service —
// outbound-only, sovereign, no central dependency (PUSH-CELL-01, webpush.go).
// That works only while the cell is AWAKE. A scale-to-zero managed cell (or an
// off home box) cannot send while asleep.
//
// So, in MANAGED mode, the cell REGISTERS its browser push subscriptions with
// the always-on CP so the CP can send a GENERIC, content-blind "new mail" push
// ON BEHALF of the sleeping cell. This file is the cell half of that contract:
// it SYNCs (register) subscriptions to the CP on add/change and UNREGISTERs them
// on removal, authenticated with the exact edge↔CP HMAC the CP verifies.
//
// # Flag-gating (self-host is byte-identical when unconfigured)
//
// The registrar is ACTIVE only when ALL of the CP register URL, the edge↔CP
// secret, and the cell's ULID are configured (managed mode). With any of them
// unset — the self-host default — the registrar is inert: newRegistrar returns
// nil, every method is a no-op, and the cell sends push DIRECTLY as today. No
// self-host box ever talks to a CP.
//
// # HMAC contract (MUST match the CP exactly)
//
// Header X-Vulos-Edge-CP-Auth = hex(HMAC-SHA256(secret,
// method"\n"path"\n"hex(sha256(body)))), keyed by MAIL_EDGE_CP_SECRET — the
// SAME construction the mail edge uses (vulos-cloud routes_mailedge.go
// computeEdgeCPAuth / vulos-deliver edge.cpAuthHeader). The body is the exact
// bytes POSTed.
//
// # Content-blindness
//
// The registration carries ONLY: the cell's ULID (so the CP derives the owning
// account server-side), the browser subscription (endpoint + p256dh + auth), and
// the cell's VAPID material (public + subject + private). NO mail content ever
// crosses this path. The VAPID PRIVATE key IS sent because send-on-behalf is
// literally the CP signing a Web Push in the cell's name — RFC 8292 VAPID
// requires the private key to mint the signed JWT, and the merged CP contract
// (cellpush.ValidateForRegister) fail-closes without it. It is a SECRET on the
// wire (TLS to the CP) and is NEVER logged here.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"vulos/backend/internal/safedial"
)

// cpAuthHeader is the request header carrying the edge↔CP HMAC. It MUST match
// the CP's edgeCPAuthHeader (vulos-cloud routes_mailedge.go).
const cpAuthHeader = "X-Vulos-Edge-CP-Auth"

// Register/unregister paths on the CP. These are the exact paths the CP HMAC is
// computed over (path is part of the signed material), so they must be byte-exact.
const (
	cpRegisterPath   = "/api/mail/push/register"
	cpUnregisterPath = "/api/mail/push/unregister"
)

// CPRegistrarConfig is the cell-side managed-mode configuration for the CP push
// registration client, sourced from the environment so the feature is
// CONFIG-GATED and FAIL-SAFE-OFF. With any field empty the registrar is inert.
type CPRegistrarConfig struct {
	// BaseURL is the CP origin the register/unregister paths are POSTed to
	// (VULOS_PUSH_CP_REGISTER_URL), e.g. "https://cp.vulos.to". A full URL ending
	// in a register path is also accepted (the origin is extracted). Managed cells
	// receive this at provisioning; self-host leaves it unset.
	BaseURL string
	// Secret is the edge↔CP HMAC key (VULOS_PUSH_CP_SECRET, provisioned to the
	// cell to equal the CP's MAIL_EDGE_CP_SECRET). It is a SECRET; never logged.
	Secret string
	// ULID is the cell's managed instance id (VULOS_ULID), from which the CP
	// derives the owning account server-side. The cell never claims an account id.
	ULID string
}

// LoadCPRegistrarConfig reads the managed-mode CP registration config from the
// environment. All values default empty → the registrar is inert (self-host).
func LoadCPRegistrarConfig() CPRegistrarConfig {
	return CPRegistrarConfig{
		BaseURL: strings.TrimSpace(os.Getenv("VULOS_PUSH_CP_REGISTER_URL")),
		Secret:  os.Getenv("VULOS_PUSH_CP_SECRET"), // never trim a secret
		ULID:    strings.TrimSpace(os.Getenv("VULOS_ULID")),
	}
}

// Enabled reports whether managed-mode CP registration is fully configured.
// Fail-safe: OFF unless the CP URL, the secret, AND the cell ULID are all set.
func (c CPRegistrarConfig) Enabled() bool {
	return c.BaseURL != "" && c.Secret != "" && c.ULID != ""
}

// cpHTTPClient is the outbound HTTP client used to reach the CP. Like the push
// send client (webpush.go), its dialer re-screens the RESOLVED IP at connect
// time (safedial Control hook) so a mis-set/hostile CP URL can't be used to make
// the box probe internal/metadata targets, and redirects are refused so a 30x
// can't bounce the authed POST to an internal host. allowLAN=true here: unlike a
// public push vendor, a self-hosted CP could legitimately live on the LAN.
var cpHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("cpregister: redirect not allowed")
	},
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Control: safedial.ControlFunc(true), Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
		MaxIdleConns:        1,
	},
}

// CPRegistrar syncs the cell's push subscriptions to the CP so the CP can send
// generic content-blind push ON BEHALF of a sleeping cell. It is created only in
// managed mode; in self-host mode newCPRegistrar returns nil and every method is
// a no-op guarded by a nil-receiver check.
type CPRegistrar struct {
	cfg    CPRegistrarConfig
	origin string // resolved CP origin (scheme://host[:port])
	client *http.Client
}

// NewCPRegistrar builds a registrar from cfg. It returns nil when cfg is not
// fully configured (self-host / no CP) OR when BaseURL is not a valid absolute
// http(s) URL — a nil registrar is INERT (all methods no-op), which is exactly
// the sovereign direct-send behaviour. A nil *CPRegistrar is safe to call any
// method on.
func NewCPRegistrar(cfg CPRegistrarConfig) *CPRegistrar {
	if !cfg.Enabled() {
		return nil
	}
	origin, err := cpOrigin(cfg.BaseURL)
	if err != nil {
		log.Printf("[notify] push: CP register URL invalid, send-on-behalf disabled: %v", err)
		return nil
	}
	return &CPRegistrar{cfg: cfg, origin: origin, client: cpHTTPClient}
}

// cpOrigin extracts and validates the scheme://host[:port] origin from a
// configured CP base URL (which may be a bare origin or a full register URL). It
// requires an absolute http/https URL with a host.
func cpOrigin(base string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http(s), got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("no host in %q", base)
	}
	return u.Scheme + "://" + u.Host, nil
}

// Enabled reports whether this registrar is live (non-nil). Safe on a nil
// receiver so callers need not branch.
func (r *CPRegistrar) Enabled() bool { return r != nil }

// setClientForTest swaps the outbound HTTP client so a test can reach a
// loopback httptest server (the production client's SSRF dialer blocks
// loopback). Not for production use.
func (r *CPRegistrar) setClientForTest(c *http.Client) { r.client = c }

// registerBody is the register request payload. It mirrors the CP's
// cellPushRegisterReq field-for-field (json tags MUST match). It carries the
// cell ULID (→ CP derives the account), the subscription, and the VAPID pair.
type registerBody struct {
	ULID         string `json:"ulid"`
	Endpoint     string `json:"endpoint"`
	P256DH       string `json:"p256dh"`
	Auth         string `json:"auth"`
	VAPIDPublic  string `json:"vapid_public"`
	VAPIDPrivate string `json:"vapid_private"`
	VAPIDSubject string `json:"vapid_subject"`
}

// unregisterBody mirrors the CP's cellPushUnregisterReq.
type unregisterBody struct {
	ULID     string `json:"ulid"`
	Endpoint string `json:"endpoint"`
}

// Register syncs one subscription (plus the cell's VAPID material) to the CP.
// It is a no-op on a nil (self-host) registrar. cfg is the resolved VAPID config
// (webpush.go) — the SAME pair the cell uses for its own direct sends, so a
// send-on-behalf notice is indistinguishable to the vendor from a direct one.
// Best-effort: a CP error is logged (WITHOUT key material) and returned so the
// caller can decide, but it never blocks the user's own subscribe.
func (r *CPRegistrar) Register(ctx context.Context, sub PushSubscription, cfg PushConfig) error {
	if !r.Enabled() {
		return nil
	}
	if !cfg.Enabled() {
		// No VAPID material → the CP cannot sign a send-on-behalf; skip (the direct
		// path is off too). Not an error.
		return nil
	}
	body := registerBody{
		ULID:         r.cfg.ULID,
		Endpoint:     sub.Endpoint,
		P256DH:       sub.P256DH,
		Auth:         sub.Auth,
		VAPIDPublic:  cfg.VAPIDPublic,
		VAPIDPrivate: cfg.VAPIDPrivate,
		VAPIDSubject: cfg.VAPIDSubject,
	}
	return r.post(ctx, cpRegisterPath, body)
}

// Unregister removes one endpoint for this cell's account on the CP. No-op on a
// nil registrar. Idempotent on the CP side (unknown endpoint → 204).
func (r *CPRegistrar) Unregister(ctx context.Context, endpoint string) error {
	if !r.Enabled() {
		return nil
	}
	return r.post(ctx, cpUnregisterPath, unregisterBody{ULID: r.cfg.ULID, Endpoint: endpoint})
}

// post marshals payload, computes the edge↔CP HMAC over method/path/body, and
// POSTs it to origin+path. It treats any 2xx as success; a non-2xx is an error
// (the CP returns 204 on success for both routes). The VAPID private key and the
// secret are NEVER logged.
func (r *CPRegistrar) post(ctx context.Context, path string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("cpregister: marshal: %w", err)
	}
	auth := computeCPAuth([]byte(r.cfg.Secret), http.MethodPost, path, raw)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.origin+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("cpregister: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(cpAuthHeader, auth)

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("cpregister: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	// Drain a small amount so the connection can be reused / is cleanly closed.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cpregister: CP %s returned status %d", path, resp.StatusCode)
	}
	return nil
}

// computeCPAuth mirrors the CP's computeEdgeCPAuth exactly: HMAC-SHA256 over
// method"\n"path"\n"hex(sha256(body)). It MUST stay byte-identical to the CP.
func computeCPAuth(secret []byte, method, path string, body []byte) string {
	bodyDigest := sha256.Sum256(body)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(method))
	mac.Write([]byte("\n"))
	mac.Write([]byte(path))
	mac.Write([]byte("\n"))
	mac.Write([]byte(hex.EncodeToString(bodyDigest[:])))
	return hex.EncodeToString(mac.Sum(nil))
}
