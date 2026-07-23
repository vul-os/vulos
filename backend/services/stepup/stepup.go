// Package stepup implements the generic "extra security step" gate used
// across the OS for sensitive/destructive actions that a normal session
// cookie must not be enough to authorise on its own (e.g. removing a
// fleet member, rotating a recovery key, exporting secrets).
//
// Pattern lifted from management/pkg/superadmin's RequireSuperAdmin chain
// (a hardware-key-gated admin session minted on top of the main session),
// simplified for a single box owner: re-proving the account PASSWORD mints
// a short-lived "elevated" token that privileged handlers require in
// addition to the normal X-User-ID session. The verify primitive itself
// (auth.Store.VerifyUserPassword) already documents itself as "the
// STEP-UP primitive for sensitive operations" — this package is the
// generic gate built on top of it, plus the token that remembers a
// successful step-up for TTL so a user isn't re-prompted on every click
// of a multi-step flow.
//
// A clean seam is left to swap the "how" (password re-verify) for
// WebAuthn/TOTP later without touching Mint/Verify/Require or any caller:
// swap what feeds Mint's userID (a successful factor check) and keep
// everything downstream identical.
//
// Fail-closed throughout: any error, missing input, or ambiguous state is
// treated as "not elevated".
package stepup

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"vulos/backend/internal/datadir"
)

// TTL is how long a minted elevated token remains valid. Deliberately
// short — this is a re-proof for the next few privileged actions, not a
// second session.
const TTL = 5 * time.Minute

// CookieName is the cookie that carries the elevated token once minted.
// Host-scoped (no Domain set), matching the rest of the OS's cookies.
const CookieName = "vulos_stepup"

// headerName lets non-cookie callers (e.g. a same-origin fetch that
// prefers to hold the token in memory rather than a cookie) present the
// token explicitly. The cookie is still the primary carrier.
const headerName = "X-Stepup-Token"

// domainLabel HMAC-domain-separates the key this package derives for
// stepup tokens from any other purpose that might derive a key from the
// same underlying secret, so a leaked stepup token can never be replayed
// as a session token (or vice versa) even though both ultimately trace
// back to the same on-disk secret.
const domainLabel = "vulos-stepup-token-v1"

// ErrNotElevated is returned by Require when the request has no
// currently-valid elevated token. Callers should surface this as 401/403
// and point the user at the step-up flow (POST /api/auth/stepup/verify).
var ErrNotElevated = errors.New("stepup: session is not elevated — complete /api/auth/stepup/verify first")

// Require fails closed: it returns nil only if the request carries a
// currently-valid elevated token bound to the verified X-User-ID stamped
// on the request by the auth middleware (never a client-supplied value —
// see auth.Handler.Middleware, which strips any inbound X-User-ID before
// re-stamping it from the validated session). Any other state — no
// session, no token, expired token, a token minted for a different user —
// is an error.
func Require(r *http.Request) error {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		return errors.New("stepup: no authenticated session")
	}
	token := tokenFromRequest(r)
	if token == "" {
		return ErrNotElevated
	}
	if !Verify(token, userID) {
		return ErrNotElevated
	}
	return nil
}

func tokenFromRequest(r *http.Request) string {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return r.Header.Get(headerName)
}

// Mint creates a fresh elevated token bound to userID, valid until the
// returned expiry (now + TTL). Callers must already have re-verified the
// user's identity through some factor (today: password) before calling
// this — Mint itself does not check anything about the caller.
func Mint(userID string) (token string, expires time.Time, err error) {
	if strings.TrimSpace(userID) == "" {
		return "", time.Time{}, errors.New("stepup: empty user id")
	}
	key, err := hmacKey()
	if err != nil {
		return "", time.Time{}, err
	}
	expires = time.Now().Add(TTL)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", time.Time{}, fmt.Errorf("stepup: rand: %w", err)
	}
	payload := userID + "|" + strconv.FormatInt(expires.Unix(), 10) + "|" +
		base64.RawURLEncoding.EncodeToString(nonce)
	sig := sign(key, payload)
	token = base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(sig)
	return token, expires, nil
}

// Verify reports whether token is a currently-valid elevated token minted
// for userID. Constant-time where it matters (signature and user-id
// comparison); a malformed token is simply invalid, never a panic.
func Verify(token, userID string) bool {
	if token == "" || userID == "" {
		return false
	}
	key, err := hmacKey()
	if err != nil {
		return false
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	sigGiven, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	if !hmac.Equal(sign(key, string(payloadRaw)), sigGiven) {
		return false
	}

	fields := strings.SplitN(string(payloadRaw), "|", 3)
	if len(fields) != 3 {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(fields[0]), []byte(userID)) != 1 {
		return false
	}
	expUnix, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return false
	}
	if time.Now().Unix() > expUnix {
		return false
	}
	return true
}

func sign(key []byte, payload string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// SetCookie writes token to w as the vulos_stepup cookie, expiring it at
// (approximately) the token's own expiry. Mirrors the isSecure/SameSite
// logic auth.go's session cookie uses (SameSite=None+Secure over HTTPS so
// it also works from subdomain iframes; SameSite=Lax over plain HTTP for
// local/dev), but is intentionally self-contained rather than reaching
// into the auth package's unexported cookie builder.
func SetCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, buildCookie(r, token, time.Until(expires)))
}

// ClearCookie deletes the elevated token cookie (e.g. on logout or an
// explicit "step down").
func ClearCookie(w http.ResponseWriter, r *http.Request) {
	c := buildCookie(r, "", 0)
	c.MaxAge = -1
	http.SetCookie(w, c)
}

func buildCookie(r *http.Request, token string, ttl time.Duration) *http.Cookie {
	remoteHost, _ := splitHostPort(r.RemoteAddr)
	fromTrustedProxy := remoteHost == "127.0.0.1" || remoteHost == "::1"
	isSecure := r.TLS != nil || (fromTrustedProxy && r.Header.Get("X-Forwarded-Proto") == "https")
	sameSite := http.SameSiteLaxMode
	if isSecure {
		sameSite = http.SameSiteNoneMode
	}
	maxAge := int(ttl.Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: sameSite,
		Secure:   isSecure,
	}
}

// splitHostPort is a tiny local stand-in for net.SplitHostPort that never
// errors on a bare host (no port) — RemoteAddr is host:port in practice,
// but this keeps buildCookie robust if that ever isn't true in a test.
func splitHostPort(hostport string) (host, port string) {
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return hostport, ""
	}
	return hostport[:i], hostport[i+1:]
}

// hmacKey derives a stepup-scoped signing key from the SAME on-disk secret
// backend/services/auth already uses to sign session tokens (auth.go's
// loadOrCreateSecret, stored at <dataDir>/db/auth.key) — domain-separated
// via HMAC so this package never introduces a new deploy secret.
func hmacKey() ([]byte, error) {
	secret, err := authSecret()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(domainLabel))
	return mac.Sum(nil), nil
}

func authSecretPath() string {
	return filepath.Join(datadir.Join("db"), "auth.key")
}

// authSecret loads backend/services/auth's session-signing secret.
// auth.Store never exposes that field (it is unexported by design), so
// this mirrors auth.go's loadOrCreateSecret exactly — same path, same
// 32-byte-random/0600 semantics — rather than forking a new secret. In
// normal boot order auth.NewStore runs before routes (including this
// package's) are ever exercised, so in practice this is always a read;
// the create-if-missing branch only matters for tests that use this
// package standalone.
func authSecret() ([]byte, error) {
	p := authSecretPath()
	if data, err := os.ReadFile(p); err == nil && len(data) >= 32 {
		return data, nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("stepup: rand: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return nil, fmt.Errorf("stepup: mkdir: %w", err)
	}
	if err := os.WriteFile(p, secret, 0600); err != nil {
		return nil, fmt.Errorf("stepup: write: %w", err)
	}
	return secret, nil
}
