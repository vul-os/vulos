// Package osroute verifies the X-Vulos-OS-Route router token that the cloud
// control-plane (CP) stamps on requests it hands off to an OS box.
//
// PROVENANCE, NOT PER-REQUEST AUTH.
//
// The token is a short-lived (default 5 min), AUDIENCE-BOUND, HMAC-SHA256 signed
// handoff credential minted by the CP apex. It proves a routing decision came
// from the legitimate router and not from a forgery aimed straight at the box's
// public port. The wire format mirrors the CP minter exactly
// (github.com/vul-os/vulos-management/pkg/osrouter):
//
//	base64url(payloadJSON) + "." + base64url(HMAC-SHA256(secret, payloadJSON))
//
// This package is a VERIFIER ONLY — the OS box never mints these tokens, only the
// CP does. It is intentionally self-contained (stdlib only) so the signed OS
// image carries no extra dependency.
//
// SELF-HOST CORRECTNESS (fail-closed WITHOUT locking owners out).
//
// A self-hosted box with no cloud router in front of it has NO signing secret
// configured, so the verifier is disabled and the Middleware is a no-op passthrough:
// the box is directly reachable and this check is inert. The router token is only
// meaningful when the CP sits in front, which is precisely when VULOS_ROUTER_SECRET
// is set.
//
// When a secret IS configured, the Middleware is fail-closed on a PRESENTED token:
// a request that carries an X-Vulos-OS-Route header (or ?os_rt= query param) must
// carry a VALID one — forged, tampered, expired, wrong-issuer or wrong-audience
// tokens are rejected 403. A request with NO token is passed through to the box's
// own authentication layer (which fail-closes on its session gate on its own).
// This is deliberate: the router token is a short-lived HANDOFF credential and is
// NOT re-stamped on every request the relay forwards, so requiring it on every
// request would lock out the box owner's own already-authenticated sessions minutes
// after login. Rejecting only a present-but-invalid token still fully closes the
// stated hole — a direct forgery cannot fabricate a validly-signed router token
// (only the CP holds the secret), and it cannot use the box without a session
// (the existing auth gate).
package osroute

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

// RouterTokenHeader is the header the CP stamps the token on.
const RouterTokenHeader = "X-Vulos-OS-Route"

// RouterTokenQueryParam is the query parameter the handoff redirect carries the
// token on (https://<box>.os.<domain>/?os_rt=<token>).
const RouterTokenQueryParam = "os_rt"

// RouterTokenIssuer is stamped on every valid router token by the CP minter.
const RouterTokenIssuer = "vulos-cp-osr"

// Env vars that configure the verifier.
const (
	// EnvSecret is the current router-token signing secret (shared with the CP
	// minter). Empty ⇒ verifier disabled (self-host / directly-reachable box).
	// Multiple secrets may be supplied comma-separated to support key rotation
	// (any one that verifies is accepted).
	EnvSecret = "VULOS_ROUTER_SECRET"
	// EnvSecretPrev is an optional previous secret kept valid during rotation.
	EnvSecretPrev = "VULOS_ROUTER_SECRET_PREV"
	// EnvAudience, when set, pins the box host the token must be minted for
	// (`<box-id>.os.<domain>`). Left empty the signature/issuer/expiry are still
	// enforced but the audience binding is not — safe when a relay may rewrite the
	// Host header so the box cannot derive its own public os-host reliably.
	EnvAudience = "VULOS_ROUTER_AUDIENCE"
)

var (
	// ErrMalformed is returned for a token that is not the two-part shape.
	ErrMalformed = errors.New("osroute: malformed token")
	// ErrSignature is returned when the HMAC does not verify.
	ErrSignature = errors.New("osroute: signature mismatch")
	// ErrExpired is returned when the token exp is in the past.
	ErrExpired = errors.New("osroute: token expired")
	// ErrAudience is returned when aud does not match the expected box host.
	ErrAudience = errors.New("osroute: audience mismatch")
	// ErrIssuer is returned when iss is not RouterTokenIssuer.
	ErrIssuer = errors.New("osroute: bad issuer")
	// ErrNoSecret is returned when no signing secret is configured.
	ErrNoSecret = errors.New("osroute: no signing secret")
	// ErrFields is returned when a required claim is empty.
	ErrFields = errors.New("osroute: sub, org and aud are required")
)

// Claims is the payload of a router token (mirrors the CP minter).
type Claims struct {
	Sub string `json:"sub"` // CP account id the handoff is on behalf of
	Org string `json:"org"` // org whose cluster the box belongs to
	Aud string `json:"aud"` // target box host (`<box-id>.os.<domain>`)
	Iss string `json:"iss"` // always RouterTokenIssuer
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// Verifier holds the accepted signing secrets and the optional pinned audience.
// A nil or Enabled()==false Verifier makes Middleware a passthrough (self-host).
type Verifier struct {
	secrets  [][]byte
	audience string // lower-cased; "" ⇒ audience binding not enforced
}

// VerifierFromEnv builds a Verifier from the environment. When EnvSecret is unset
// (or blank) it returns a disabled verifier — the box is directly reachable and
// the router-token check is inert.
func VerifierFromEnv() *Verifier {
	var secrets [][]byte
	for _, raw := range []string{os.Getenv(EnvSecret), os.Getenv(EnvSecretPrev)} {
		for _, part := range strings.Split(raw, ",") {
			if s := strings.TrimSpace(part); s != "" {
				secrets = append(secrets, []byte(s))
			}
		}
	}
	return &Verifier{
		secrets:  secrets,
		audience: strings.ToLower(strings.TrimSpace(os.Getenv(EnvAudience))),
	}
}

// NewVerifier builds a Verifier from explicit secrets + optional pinned audience
// (used by tests and callers that source config themselves).
func NewVerifier(secrets [][]byte, audience string) *Verifier {
	cp := make([][]byte, 0, len(secrets))
	for _, s := range secrets {
		if len(s) > 0 {
			b := make([]byte, len(s))
			copy(b, s)
			cp = append(cp, b)
		}
	}
	return &Verifier{secrets: cp, audience: strings.ToLower(strings.TrimSpace(audience))}
}

// Enabled reports whether at least one signing secret is configured. When false,
// Middleware is a no-op and Verify always returns ErrNoSecret.
func (v *Verifier) Enabled() bool { return v != nil && len(v.secrets) > 0 }

// Verify checks token against every configured secret (rotation) and, when an
// audience is pinned, that the token was minted for that box host. It returns the
// parsed claims on success.
func (v *Verifier) Verify(token string, now time.Time) (Claims, error) {
	if !v.Enabled() {
		return Claims{}, ErrNoSecret
	}
	var last error = ErrSignature
	for _, s := range v.secrets {
		c, err := parse(s, token, now)
		if err != nil {
			if !errors.Is(err, ErrSignature) {
				// A structurally-valid token that fails a non-signature check
				// (expiry/issuer/fields) fails outright — trying other secrets
				// cannot change the verdict.
				return Claims{}, err
			}
			last = err
			continue
		}
		if v.audience != "" && c.Aud != v.audience {
			return Claims{}, ErrAudience
		}
		return c, nil
	}
	return Claims{}, last
}

// Middleware verifies the router token fail-closed when the verifier is enabled.
// See the package doc for the exact (non-locking) semantics. When the verifier is
// disabled it returns next unchanged (zero overhead on self-hosted boxes).
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	if !v.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimSpace(r.Header.Get(RouterTokenHeader))
		if tok == "" {
			tok = strings.TrimSpace(r.URL.Query().Get(RouterTokenQueryParam))
		}
		if tok == "" {
			// No router token presented → defer to the box's own auth layer.
			// (The token is a handoff credential, not a per-request gate; a
			// blanket requirement here would lock out the owner's own sessions.)
			next.ServeHTTP(w, r)
			return
		}
		if _, err := v.Verify(tok, time.Now()); err != nil {
			http.Error(w, "forbidden: invalid os-route token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// parse decodes and cryptographically verifies token against a single secret.
func parse(secret []byte, token string, now time.Time) (Claims, error) {
	if len(secret) == 0 {
		return Claims{}, ErrNoSecret
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok || body == "" || sig == "" {
		return Claims{}, ErrMalformed
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	// Compare the CANONICAL base64 encoding of the expected MAC against the
	// presented sig string in constant time. Decoding the presented sig and
	// comparing raw bytes is unsafe: Go's non-strict base64 decoder discards the
	// trailing "unused" bits of the final character, so flipping them leaves the
	// decoded MAC unchanged and a tampered token would verify.
	wantSig := base64.RawURLEncoding.EncodeToString(sign(secret, payload))
	if !hmac.Equal([]byte(sig), []byte(wantSig)) {
		return Claims{}, ErrSignature
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Claims{}, ErrMalformed
	}
	if c.Iss != RouterTokenIssuer {
		return Claims{}, ErrIssuer
	}
	if c.Sub == "" || c.Org == "" || c.Aud == "" {
		return Claims{}, ErrFields
	}
	if now.UTC().Unix() >= c.Exp {
		return Claims{}, ErrExpired
	}
	return c, nil
}

func sign(secret, payload []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(payload)
	return h.Sum(nil)
}
