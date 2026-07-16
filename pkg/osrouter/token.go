package osrouter

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// RouterToken is a short-lived, AUDIENCE-BOUND handoff credential the apex mints
// so the OS plane (`os.<domain>` / `<box-id>.os.<domain>`) can trust a routing
// decision without ever receiving the apex-scoped session cookie.
//
// Security properties (mirrors internal/apptoken, plus an org scope):
//   - Aud is the concrete BOX HOST (`<box-id>.os.<domain>`) — a token minted for
//     one box is meaningless to any other box and to the apex.
//   - Org is stamped so a box can assert the identity is scoped to its own org
//     (an app instance never sees a wrong-org identity — PART A.4).
//   - Short-lived (default 5 min, hard-capped) so a leaked handoff is useless
//     quickly.
//   - It is NOT a vc_session and is delivered out-of-band (query/header), never
//     as the session cookie.
//
// Format: base64url(payloadJSON) + "." + base64url(HMAC-SHA256(secret, payload)).

// RouterTokenIssuer is stamped on every router token.
const RouterTokenIssuer = "vulos-cp-osr"

// RouterTokenHeader is the header the OS plane reads the token from (when not a
// query parameter on the handoff redirect).
const RouterTokenHeader = "X-Vulos-OS-Route"

const (
	defaultRouterTTL = 5 * time.Minute
	maxRouterTTL     = 15 * time.Minute
)

var (
	// ErrTokenMalformed is returned for a token that is not the two-part shape.
	ErrTokenMalformed = errors.New("osrouter: malformed token")
	// ErrTokenSignature is returned when the HMAC does not verify.
	ErrTokenSignature = errors.New("osrouter: signature mismatch")
	// ErrTokenExpired is returned when the token exp is in the past.
	ErrTokenExpired = errors.New("osrouter: token expired")
	// ErrTokenAudience is returned when aud does not match the expected box host.
	ErrTokenAudience = errors.New("osrouter: audience mismatch")
	// ErrTokenIssuer is returned when iss is not RouterTokenIssuer.
	ErrTokenIssuer = errors.New("osrouter: bad issuer")
	// ErrTokenSecret is returned when no signing secret is configured.
	ErrTokenSecret = errors.New("osrouter: no signing secret")
	// ErrTokenFields is returned when a required claim is empty.
	ErrTokenFields = errors.New("osrouter: sub, org and aud are required")
)

// RouterClaims is the payload of a router token.
type RouterClaims struct {
	// Sub is the CP account id the handoff is on behalf of.
	Sub string `json:"sub"`
	// Org is the org whose cluster the box belongs to.
	Org string `json:"org"`
	// Aud is the target box host (`<box-id>.os.<domain>`).
	Aud string `json:"aud"`
	// Iss is always RouterTokenIssuer.
	Iss string `json:"iss"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// TokenMinter mints router tokens with a fixed secret + TTL.
type TokenMinter struct {
	secret []byte
	ttl    time.Duration
	nowFn  func() time.Time
}

// NewTokenMinter returns a minter signing with secret. ttl <= 0 falls back to the
// default; ttl above the cap is clamped.
func NewTokenMinter(secret []byte, ttl time.Duration) *TokenMinter {
	if ttl <= 0 {
		ttl = defaultRouterTTL
	}
	if ttl > maxRouterTTL {
		ttl = maxRouterTTL
	}
	s := make([]byte, len(secret))
	copy(s, secret)
	return &TokenMinter{secret: s, ttl: ttl, nowFn: time.Now}
}

// SetNow overrides the clock (tests).
func (m *TokenMinter) SetNow(f func() time.Time) { m.nowFn = f }

// Mint issues a token for account sub, scoped to org, bound to box host aud.
func (m *TokenMinter) Mint(sub, org, aud string) (string, error) {
	return m.MintAt(sub, org, aud, m.nowFn())
}

// MintAt is Mint with an explicit issue time (deterministic tokens for tests).
func (m *TokenMinter) MintAt(sub, org, aud string, at time.Time) (string, error) {
	if m == nil || len(m.secret) == 0 {
		return "", ErrTokenSecret
	}
	if sub == "" || org == "" || aud == "" {
		return "", ErrTokenFields
	}
	now := at.UTC()
	c := RouterClaims{
		Sub: sub, Org: org, Aud: strings.ToLower(aud), Iss: RouterTokenIssuer,
		Iat: now.Unix(), Exp: now.Add(m.ttl).Unix(),
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + base64.RawURLEncoding.EncodeToString(sign(m.secret, payload)), nil
}

// VerifyRouterToken checks the signature, issuer, expiry and audience (expectedAud
// is the box host the OS plane is serving) and returns the parsed claims.
func VerifyRouterToken(secret []byte, token, expectedAud string, now time.Time) (RouterClaims, error) {
	if expectedAud == "" {
		return RouterClaims{}, ErrTokenAudience
	}
	c, err := parseRouterToken(secret, token, now)
	if err != nil {
		return RouterClaims{}, err
	}
	if c.Aud != strings.ToLower(expectedAud) {
		return RouterClaims{}, ErrTokenAudience
	}
	return c, nil
}

// VerifyRouterTokenAny accepts token if ANY of secrets verifies it (rotation).
func VerifyRouterTokenAny(secrets [][]byte, token, expectedAud string, now time.Time) (RouterClaims, error) {
	if len(secrets) == 0 {
		return RouterClaims{}, ErrTokenSecret
	}
	var last error = ErrTokenSignature
	for _, s := range secrets {
		c, err := parseRouterToken(s, token, now)
		if err != nil {
			if !errors.Is(err, ErrTokenSignature) {
				return RouterClaims{}, err
			}
			last = err
			continue
		}
		if expectedAud != "" && c.Aud != strings.ToLower(expectedAud) {
			return RouterClaims{}, ErrTokenAudience
		}
		return c, nil
	}
	return RouterClaims{}, last
}

func parseRouterToken(secret []byte, token string, now time.Time) (RouterClaims, error) {
	if len(secret) == 0 {
		return RouterClaims{}, ErrTokenSecret
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok || body == "" || sig == "" {
		return RouterClaims{}, ErrTokenMalformed
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return RouterClaims{}, ErrTokenMalformed
	}
	// Compare the CANONICAL base64 encoding of the expected MAC against the
	// presented sig string in constant time. Decoding the presented sig and
	// comparing raw bytes is unsafe here: Go's non-strict base64 decoder
	// discards the trailing "unused" bits of the final character, so flipping
	// them leaves the decoded MAC unchanged and a tampered token would verify.
	wantSig := base64.RawURLEncoding.EncodeToString(sign(secret, payload))
	if !hmac.Equal([]byte(sig), []byte(wantSig)) {
		return RouterClaims{}, ErrTokenSignature
	}
	var c RouterClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return RouterClaims{}, ErrTokenMalformed
	}
	if c.Iss != RouterTokenIssuer {
		return RouterClaims{}, ErrTokenIssuer
	}
	if c.Sub == "" || c.Org == "" || c.Aud == "" {
		return RouterClaims{}, ErrTokenFields
	}
	if now.UTC().Unix() >= c.Exp {
		return RouterClaims{}, ErrTokenExpired
	}
	return c, nil
}

func sign(secret, payload []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(payload)
	return h.Sum(nil)
}
