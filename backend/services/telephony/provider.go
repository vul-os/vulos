package telephony

// This file is the SECOND / THROWAWAY-number seam (TELE-02). It is DELIBERATELY
// distinct from the box SIM line in telephony.go: the SIM is the owner's real
// number (spoken through ModemManager/mmcli on the box), whereas a Provider is a
// virtual/second number reached over the internet — the "keep your SIM safe,
// carry a throwaway" half of the feature. The provider is a swappable seam like
// every other external dependency in Vulos: BYO, env-configured, and FAIL-CLOSED
// when unconfigured (an unconfigured box gets noopProvider, which never dials or
// sends and returns ErrNoProvider). We do NOT hardcode a paid vendor — the built
// -in httpProvider talks to a user-supplied HTTP endpoint (their own adapter in
// front of whatever carrier/vendor they choose), so no carrier name lives here.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrNoProvider is returned by every Provider action when no second-number
// backend is configured. Fail-closed: the box never falls back to a default
// vendor or silently drops the request — the caller gets a clear, honest error.
var ErrNoProvider = errors.New("telephony: no number provider configured")

// Provider is the second/throwaway-number backend seam. Implementations obtain
// and use a virtual number over the internet. Everything routed through here is
// SEPARATE from the box SIM; the owner gate still applies at the HTTP layer.
type Provider interface {
	// Name identifies the backend (for status/diagnostics), e.g. "http" or "none".
	Name() string
	// Configured reports whether the backend is usable. When false the whole
	// virtual line fails closed.
	Configured() bool
	// Number is the provisioned virtual/second number, or "" if none.
	Number() string
	// SendSMS sends body to `to` FROM the virtual number.
	SendSMS(to, body string) error
	// PlaceCall initiates a call to `to` from the virtual number (typically a
	// provider-bridged dial-out). Returns a provider call id when known.
	PlaceCall(to string) (callID string, err error)
}

// InboundVerifier is an OPTIONAL Provider capability: verifying that an inbound
// webhook (SMS/call event pushed to /api/telephony/provider/inbound) genuinely
// came from the configured backend. A provider that cannot verify inbound (the
// noop provider, or an http provider with no webhook secret) causes the webhook
// to reject everything — fail-closed, so the internet-facing ingress can never be
// spoofed into injecting a fake OTP into the owner's feed.
type InboundVerifier interface {
	VerifyInbound(signature string, rawBody []byte) bool
}

// ─── noop provider (the fail-closed default) ────────────────────────────────────

type noopProvider struct{}

func (noopProvider) Name() string                       { return "none" }
func (noopProvider) Configured() bool                   { return false }
func (noopProvider) Number() string                     { return "" }
func (noopProvider) SendSMS(_, _ string) error          { return ErrNoProvider }
func (noopProvider) PlaceCall(_ string) (string, error) { return "", ErrNoProvider }

// ─── http provider (BYO adapter) ────────────────────────────────────────────────

// httpProvider posts send/call requests as JSON to user-supplied HTTP endpoints
// and verifies inbound webhooks with an HMAC secret. It hardcodes NO vendor: the
// operator points VULOS_TELEPHONY_SEND_URL / _CALL_URL at their own adapter (a
// tiny shim in front of Twilio/Telnyx/a SIP gateway/anything), and points that
// adapter's inbound webhook back at /api/telephony/provider/inbound.
type httpProvider struct {
	number        string
	sendURL       string
	callURL       string
	token         string // optional bearer for OUTBOUND auth to the adapter
	webhookSecret string // HMAC-SHA256 secret for INBOUND webhook verification
	client        *http.Client
}

func (p *httpProvider) Name() string   { return "http" }
func (p *httpProvider) Number() string { return p.number }

// Configured requires a number plus at least one usable action endpoint. A
// half-configured provider (number only, no URLs) reports false so the line
// fails closed rather than pretending to work.
func (p *httpProvider) Configured() bool {
	return p.number != "" && (p.sendURL != "" || p.callURL != "")
}

func (p *httpProvider) SendSMS(to, body string) error {
	if p.sendURL == "" {
		return ErrNoProvider
	}
	// Same recipient/body hygiene as the SIM line. The payload is JSON-encoded
	// (not a comma-joined mmcli dict), so a comma/quote in the body cannot inject
	// a second field — but we still reject a malformed recipient and control chars
	// so a bad number never reaches the adapter.
	if !validNumber(to) {
		return fmt.Errorf("telephony: invalid recipient number")
	}
	if body == "" {
		return fmt.Errorf("telephony: empty body")
	}
	if strings.ContainsAny(body, "\x00") {
		return fmt.Errorf("telephony: message contains control characters")
	}
	return p.post(p.sendURL, map[string]string{
		"from": p.number,
		"to":   strings.TrimSpace(to),
		"body": body,
	}, nil)
}

func (p *httpProvider) PlaceCall(to string) (string, error) {
	if p.callURL == "" {
		return "", fmt.Errorf("telephony: provider does not support calls")
	}
	if !validNumber(to) {
		return "", fmt.Errorf("telephony: invalid number")
	}
	var resp struct {
		CallID string `json:"call_id"`
	}
	if err := p.post(p.callURL, map[string]string{
		"from": p.number,
		"to":   strings.TrimSpace(to),
	}, &resp); err != nil {
		return "", err
	}
	return resp.CallID, nil
}

// post marshals payload, POSTs it (with optional bearer auth), checks for a 2xx,
// and decodes the body into out when out != nil (best-effort — a non-JSON 2xx is
// still treated as success).
func (p *httpProvider) post(url string, payload any, out any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telephony: marshal request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("telephony: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("telephony: provider unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("telephony: provider returned %d", resp.StatusCode)
	}
	if out != nil && len(body) > 0 {
		_ = json.Unmarshal(body, out) // best-effort; a missing call_id is fine
	}
	return nil
}

// VerifyInbound checks an X-Vulos-Signature header against HMAC-SHA256(secret,
// rawBody). No secret configured => reject everything (fail-closed). Constant-time
// compare avoids a timing oracle on the signature.
func (p *httpProvider) VerifyInbound(signature string, rawBody []byte) bool {
	if p.webhookSecret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(p.webhookSecret))
	mac.Write(rawBody)
	want := hex.EncodeToString(mac.Sum(nil))
	got := strings.TrimPrefix(strings.TrimSpace(signature), "sha256=")
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// providerFromEnv builds the configured provider, or noopProvider when nothing is
// set (or the config is incomplete). VULOS_TELEPHONY_PROVIDER selects the backend:
//
//	""|"none"|"off"   → noopProvider (fail-closed default)
//	"http"|"webhook"  → httpProvider from:
//	    VULOS_TELEPHONY_NUMBER          the virtual/second number (required)
//	    VULOS_TELEPHONY_SEND_URL        POST endpoint for outbound SMS
//	    VULOS_TELEPHONY_CALL_URL        POST endpoint for outbound calls (optional)
//	    VULOS_TELEPHONY_PROVIDER_TOKEN  bearer sent to the endpoints (optional)
//	    VULOS_TELEPHONY_WEBHOOK_SECRET  HMAC secret for inbound webhook (required
//	                                    for inbound SMS/calls to be accepted)
func providerFromEnv() Provider {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VULOS_TELEPHONY_PROVIDER"))) {
	case "http", "webhook":
		p := &httpProvider{
			number:        strings.TrimSpace(os.Getenv("VULOS_TELEPHONY_NUMBER")),
			sendURL:       strings.TrimSpace(os.Getenv("VULOS_TELEPHONY_SEND_URL")),
			callURL:       strings.TrimSpace(os.Getenv("VULOS_TELEPHONY_CALL_URL")),
			token:         os.Getenv("VULOS_TELEPHONY_PROVIDER_TOKEN"),
			webhookSecret: os.Getenv("VULOS_TELEPHONY_WEBHOOK_SECRET"),
			client:        &http.Client{Timeout: 15 * time.Second},
		}
		if !p.Configured() {
			return noopProvider{} // incomplete config → fail closed, never half-on
		}
		return p
	default:
		return noopProvider{}
	}
}

// ─── inbound webhook handler ────────────────────────────────────────────────────

// handleProviderInbound is the internet-facing ingress the provider/adapter calls
// to push an inbound SMS or call event into the box. It is NOT owner-gated (the
// caller is the adapter, not a browser); instead it is authenticated by the
// provider's own inbound verification (HMAC). Fail-closed: a provider that can't
// verify inbound (noop, or http with no webhook secret) rejects every request, so
// this endpoint can never be tricked into injecting a forged OTP into the owner's
// live feed. On success the event is recorded on the virtual line, fanned out over
// the owner's WebSocket, and (for SMS) fired as an owner-targeted push.
func (s *Service) handleProviderInbound(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
		return
	}
	v, ok := s.provider.(InboundVerifier)
	if !ok || !v.VerifyInbound(r.Header.Get("X-Vulos-Signature"), body) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	var ev inboundEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	s.ingestInbound(ev)
	writeJSON(w, map[string]bool{"ok": true})
}
