// Package cloudclient is the OS-side HTTP client for the Vulos Cloud control
// plane's browser-grade auth surface (UNIFIED-SIGNIN).
//
// The CP guards its auth POSTs with two browser-oriented gates that a naive
// server-side client trips over:
//
//  1. A CSRF Origin/Referer allowlist — every state-changing request must
//     carry an allowed Origin header (the SPA origins; localhost:5173/4173 in
//     local mode, the canonical site origins in prod).
//  2. An adaptive proof-of-work CaptchaGate on /api/auth/{login,signup,
//     forgot-password}: a PoW-403 carries {captcha_needed:"true"}; the client
//     must GET /api/captcha/challenge?route=<path>, find a nonce such that
//     SHA256(challenge+nonce) has `difficulty` leading zero bits, and retry
//     once with the X-Vulos-PoW: "challenge:nonce" header.
//
// This package solves both in Go (mirroring the SPA reference client
// vulos-cloud/src/auth/pow.js), carries a cookie jar so the vc_session cookie
// issued by a successful login authenticates the follow-up broker calls, and
// exposes the exact flows the OS needs:
//
//   - Login       — POST /api/auth/login with structured step handling
//     (email_verification_required / totp_required / passkey_* / invalid
//     credentials), including the /api/auth/totp/verify second step.
//   - Signup      — POST /api/auth/signup (handle-based; PoW + Origin).
//   - BrokerPubkey — GET /api/profile/broker/pubkey (login-broker Ed25519 key).
//   - IssueLoginToken — POST /api/profile/login/issue; splits the
//     "base64std(payload).base64std(sig)" wire token into the raw payload
//     bytes + signature the OS verifier consumes.
//
// The client is per-flow: construct one, run a login + issue sequence, drop
// it. The cookie jar is intentionally in-memory only — the OS never persists
// the user's cloud session.
package cloudclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ─── Construction ─────────────────────────────────────────────────────────────

// Client is a cookie-jarred, PoW-solving, Origin-carrying HTTP client for one
// Vulos Cloud auth flow.
type Client struct {
	baseURL string
	origin  string
	hc      *http.Client
}

// New builds a Client for the CP at baseURL (no trailing slash required).
//
// The Origin header value is resolved as:
//  1. VULOS_CLOUD_ORIGIN env, when set (must be an allowed CP origin);
//  2. for localhost/127.0.0.1 CPs, http://localhost:5173 (the CP's local-env
//     CSRF allowlist admits the Vite dev origin, NOT the CP's own address);
//  3. otherwise https://<host of baseURL> (prod CPs allow their site origin).
func New(baseURL string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	jar, _ := cookiejar.New(nil) // only errors on a nil PublicSuffixList option
	return &Client{
		baseURL: baseURL,
		origin:  resolveOrigin(baseURL),
		hc: &http.Client{
			Timeout: 30 * time.Second,
			Jar:     jar,
		},
	}
}

// resolveOrigin derives the Origin header for baseURL (see New).
func resolveOrigin(baseURL string) string {
	if env := strings.TrimSpace(os.Getenv("VULOS_CLOUD_ORIGIN")); env != "" {
		return strings.TrimRight(env, "/")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return baseURL
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return "http://localhost:5173"
	}
	return "https://" + host
}

// Origin returns the Origin header value this client sends (for logging/tests).
func (c *Client) Origin() string { return c.origin }

// ─── Errors ───────────────────────────────────────────────────────────────────

var (
	// ErrInvalidCredentials — the CP rejected the email/password pair.
	ErrInvalidCredentials = errors.New("cloudclient: invalid credentials")
	// ErrInvalidTOTP — the CP rejected the submitted TOTP / recovery code.
	ErrInvalidTOTP = errors.New("cloudclient: invalid TOTP code")
	// ErrRateLimited — the CP throttled the request (429).
	ErrRateLimited = errors.New("cloudclient: rate limited by cloud")
	// ErrIssueForbidden — the CP refused to mint a login token (403 on issue).
	ErrIssueForbidden = errors.New("cloudclient: cloud refused to issue a login token")
	// ErrIssueReauthRequired — the CP requires a fresh password login before a
	// no-2FA account may mint (session too old).
	ErrIssueReauthRequired = errors.New("cloudclient: cloud requires a fresh sign-in to issue a login token")
)

// StatusError carries a non-2xx CP response for callers that relay it.
type StatusError struct {
	Status int
	Body   []byte
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("cloudclient: cloud returned HTTP %d: %s", e.Status, truncate(e.Body, 200))
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}

// ─── Proof-of-work (sha256-hashcash) ─────────────────────────────────────────

// hasLeadingZeroBits mirrors the CP's verifyHashcash bit-for-bit.
func hasLeadingZeroBits(sum [32]byte, bits int) bool {
	for i := 0; i < bits; i++ {
		if (sum[i/8]>>(7-(i%8)))&1 != 0 {
			return false
		}
	}
	return true
}

// SolveChallenge brute-forces a nonce so SHA256(challenge+nonce) has
// `difficulty` leading zero bits and returns the ready-to-send header value
// "challenge:nonce". ctx cancellation aborts the search.
func SolveChallenge(ctx context.Context, challenge string, difficulty int) (string, error) {
	if difficulty < 0 || difficulty > 256 {
		return "", fmt.Errorf("cloudclient: implausible PoW difficulty %d", difficulty)
	}
	for nonce := 0; ; nonce++ {
		if nonce%4096 == 0 && ctx.Err() != nil {
			return "", ctx.Err()
		}
		ns := strconv.Itoa(nonce)
		if hasLeadingZeroBits(sha256.Sum256([]byte(challenge+ns)), difficulty) {
			return challenge + ":" + ns, nil
		}
	}
}

// solvePow fetches a fresh challenge for route and solves it.
func (c *Client) solvePow(ctx context.Context, route string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/captcha/challenge?route="+url.QueryEscape(route), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("cloudclient: fetch PoW challenge: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cloudclient: PoW challenge endpoint returned %d", resp.StatusCode)
	}
	var body struct {
		Challenge  string `json:"challenge"`
		Difficulty int    `json:"difficulty"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("cloudclient: decode PoW challenge: %w", err)
	}
	if body.Challenge == "" {
		return "", errors.New("cloudclient: PoW challenge endpoint returned no challenge")
	}
	return SolveChallenge(ctx, body.Challenge, body.Difficulty)
}

// needsPow reports whether (status, body) is the CaptchaGate's "solve a PoW
// first" shape: 403 + {"captcha_needed":"true"} (string or bool).
func needsPow(status int, body []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	var m struct {
		CaptchaNeeded any `json:"captcha_needed"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return false
	}
	switch v := m.CaptchaNeeded.(type) {
	case string:
		return v == "true"
	case bool:
		return v
	}
	return false
}

// ─── Core JSON POST with Origin + transparent PoW retry ──────────────────────

// postJSON POSTs body to path with the Origin header set. On a PoW-403 it
// solves a challenge for the path and retries exactly once with X-Vulos-PoW.
// Returns the final status code + response body.
func (c *Client) postJSON(ctx context.Context, path string, body any) (int, []byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, nil, fmt.Errorf("cloudclient: marshal request: %w", err)
	}
	status, respBody, err := c.doPost(ctx, path, payload, "")
	if err != nil {
		return 0, nil, err
	}
	if !needsPow(status, respBody) {
		return status, respBody, nil
	}
	pow, err := c.solvePow(ctx, path)
	if err != nil {
		return 0, nil, fmt.Errorf("cloudclient: solve PoW for %s: %w", path, err)
	}
	return c.doPost(ctx, path, payload, pow)
}

func (c *Client) doPost(ctx context.Context, path string, payload []byte, pow string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", c.origin)
	req.Header.Set("User-Agent", "vulos-os/1.0 cloudclient")
	if pow != "" {
		req.Header.Set("X-Vulos-PoW", pow)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("cloudclient: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return 0, nil, fmt.Errorf("cloudclient: read response %s: %w", path, err)
	}
	return resp.StatusCode, respBody, nil
}

// getJSON GETs path (cookie-jarred; no PoW — the gate only guards POSTs).
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "vulos-os/1.0 cloudclient")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("cloudclient: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return fmt.Errorf("cloudclient: read response %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return &StatusError{Status: resp.StatusCode, Body: body}
	}
	return json.Unmarshal(body, out)
}

// ─── Login (with structured steps) ───────────────────────────────────────────

// Login steps surfaced to the UI. Empty step means a full session was issued.
const (
	StepEmailVerificationRequired = "email_verification_required"
	StepTOTPRequired              = "totp_required"
	StepPasskeyRequired           = "passkey_required"
)

// LoginOutcome is the result of a Login call.
type LoginOutcome struct {
	// Step is "" on full-session success; otherwise one of the Step* constants
	// telling the UI which follow-up the CLOUD account needs.
	Step string
	// User is the CP's user JSON on success (raw relay; no interpretation).
	User json.RawMessage
}

// Login performs the CP password login and, when totpCode is provided and the
// account requires TOTP, the /api/auth/totp/verify second step. On success the
// vc_session cookie is captured in the jar, authenticating follow-up calls
// (IssueLoginToken).
//
// Returned errors: ErrInvalidCredentials, ErrInvalidTOTP, ErrRateLimited, a
// *StatusError for other non-2xx responses, or transport errors.
func (c *Client) Login(ctx context.Context, email, password, totpCode string) (*LoginOutcome, error) {
	status, body, err := c.postJSON(ctx, "/api/auth/login", map[string]string{
		"email":    email,
		"password": password,
	})
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		// fall through to step handling below
	case http.StatusUnauthorized:
		return nil, ErrInvalidCredentials
	case http.StatusTooManyRequests:
		return nil, ErrRateLimited
	default:
		return nil, &StatusError{Status: status, Body: body}
	}

	var step struct {
		Step string `json:"step"`
	}
	_ = json.Unmarshal(body, &step) // absent "step" key ⇒ full-session user JSON

	switch step.Step {
	case "":
		return &LoginOutcome{User: body}, nil
	case "email_verification_required":
		return &LoginOutcome{Step: StepEmailVerificationRequired}, nil
	case "passkey_required", "passkey_2fa_required":
		// A WebAuthn ceremony cannot be driven from the box; the UI must tell
		// the user to sign in with a password-capable modality or use the
		// cloud console directly.
		return &LoginOutcome{Step: StepPasskeyRequired}, nil
	case "totp_required":
		if totpCode == "" {
			return &LoginOutcome{Step: StepTOTPRequired}, nil
		}
		return c.verifyTOTP(ctx, totpCode)
	default:
		return nil, fmt.Errorf("cloudclient: unrecognised login step %q", step.Step)
	}
}

// verifyTOTP completes the partial session with a TOTP code.
func (c *Client) verifyTOTP(ctx context.Context, code string) (*LoginOutcome, error) {
	status, body, err := c.postJSON(ctx, "/api/auth/totp/verify", map[string]string{"code": code})
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		return &LoginOutcome{User: body}, nil
	case http.StatusUnauthorized, http.StatusUnprocessableEntity:
		return nil, ErrInvalidTOTP
	case http.StatusTooManyRequests:
		return nil, ErrRateLimited
	default:
		return nil, &StatusError{Status: status, Body: body}
	}
}

// ─── Signup ───────────────────────────────────────────────────────────────────

// Signup creates a CP account. The CP is handle-based (HANDLE-01): it accepts
// {handle,password} and constructs the identity email <handle>@<domain>
// server-side; external email addresses are rejected. handle may be a bare
// handle or a full identity-domain address (CP tolerates the latter).
// Returns the CP's status code + raw body so callers relay structured errors.
func (c *Client) Signup(ctx context.Context, handle, password string) (int, []byte, error) {
	return c.postJSON(ctx, "/api/auth/signup", map[string]string{
		"handle":   handle,
		"password": password,
	})
}

// ─── Broker pubkey ────────────────────────────────────────────────────────────

// BrokerPubkey fetches the CP's active login-broker Ed25519 public key.
func (c *Client) BrokerPubkey(ctx context.Context) (ed25519.PublicKey, error) {
	var out struct {
		Active struct {
			PublicKey string `json:"public_key"`
		} `json:"active"`
	}
	if err := c.getJSON(ctx, "/api/profile/broker/pubkey", &out); err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(out.Active.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("cloudclient: decode broker pubkey: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("cloudclient: broker pubkey must be %d bytes, got %d",
			ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// ─── Login-token issuance ─────────────────────────────────────────────────────

// IssueLoginToken asks the CP to mint a device-bound login token for
// (ulid, profileName) using the session captured by a prior Login call.
//
// It splits the CP's wire format "base64std(payload).base64std(sig)" and
// returns the RAW payload bytes (exactly what the broker signed) plus the
// base64 signature — the two inputs the OS verifier consumes.
func (c *Client) IssueLoginToken(ctx context.Context, ulid, profileName string) (payload []byte, sigB64 string, err error) {
	status, body, err := c.postJSON(ctx, "/api/profile/login/issue", map[string]string{
		"ulid":         ulid,
		"profile_name": profileName,
	})
	if err != nil {
		return nil, "", err
	}
	switch status {
	case http.StatusOK:
		// parsed below
	case http.StatusForbidden:
		if strings.Contains(string(body), "reauth_required") {
			return nil, "", ErrIssueReauthRequired
		}
		return nil, "", fmt.Errorf("%w: %s", ErrIssueForbidden, truncate(body, 200))
	case http.StatusTooManyRequests:
		return nil, "", ErrRateLimited
	default:
		return nil, "", &StatusError{Status: status, Body: body}
	}

	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, "", fmt.Errorf("cloudclient: decode issue response: %w", err)
	}
	parts := strings.SplitN(out.Token, ".", 2)
	if len(parts) != 2 {
		return nil, "", errors.New("cloudclient: malformed wire token (want payload.sig)")
	}
	payload, err = base64.StdEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, "", fmt.Errorf("cloudclient: decode wire token payload: %w", err)
	}
	// Validate (and normalise) the signature encoding here so the verifier gets
	// a clean base64-std signature string.
	sigRaw, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, "", fmt.Errorf("cloudclient: decode wire token signature: %w", err)
	}
	return payload, base64.StdEncoding.EncodeToString(sigRaw), nil
}
