// Package apptoken mints and verifies short-lived, audience-bound identity
// tokens the CP reverse-proxy front door injects when forwarding an
// already-authenticated request to a lower-trust Class-P app backend
// (office/board/files/mail). (Talk and Meet were withdrawn as products
// entirely, 2026-07-15: comms are third-party now.)
//
// SECURITY-C1: the CP reverse-proxy front door (deployed in the vulos-cloud
// control-plane repo, backend/cp/cmd/server — NOT in this OSS module, which
// operates no app proxy) STRIPs the visiting user's live `vc_session` cookie
// and any `Authorization` header before forwarding, so a compromised or
// malicious app backend can never replay the user's CP session against the
// session-gated CP routes.
// When an upstream legitimately needs to know WHO the request is for, the
// proxy injects one of these tokens instead. Unlike the raw session it:
//
//   - is AUDIENCE-BOUND to exactly one app (aud="office", "mail", …). A token
//     minted for office is meaningless to any other app and to the CP itself.
//   - is SHORT-LIVED (default 2 minutes) so a leaked token is useless quickly.
//   - is NOT a `vc_session`: it is delivered in the `X-Vulos-App-Auth` header,
//     never as the session cookie, and CP session routes only ever read the
//     `vc_session` cookie — so presenting an apptoken to a CP session route is
//     rejected as "not authenticated" (see apptoken_reject test).
//
// Format (compact, self-contained, no external deps): base64url(payloadJSON) +
// "." + base64url(HMAC-SHA256(secret, payloadJSON)). The payload carries the
// claims below. Verification is constant-time on the MAC.
package apptoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Issuer is the fixed `iss` stamped on every minted token so an upstream (or a
// shared verifier) can reject tokens that did not originate at the CP.
const Issuer = "vulos-cp"

// HeaderName is the request header the proxy injects the token into. It is
// deliberately NOT a cookie and NOT Authorization, so it can never be mistaken
// for — or replayed as — a CP session credential.
const HeaderName = "X-Vulos-App-Auth"

// DefaultTTL is the lifetime of a minted token when the Minter is constructed
// with ttl <= 0. Kept short: the token is minted fresh per proxied request.
const DefaultTTL = 2 * time.Minute

// maxTTL clamps any configured TTL — an app-identity token must never be
// long-lived.
const maxTTL = 15 * time.Minute

var (
	// ErrMalformed is returned when a token is not the expected two-part shape.
	ErrMalformed = errors.New("apptoken: malformed token")
	// ErrBadSignature is returned when the HMAC does not verify.
	ErrBadSignature = errors.New("apptoken: signature mismatch")
	// ErrExpired is returned when the token's exp is in the past.
	ErrExpired = errors.New("apptoken: token expired")
	// ErrAudience is returned when the token's aud does not match the caller's
	// expected audience.
	ErrAudience = errors.New("apptoken: audience mismatch")
	// ErrIssuer is returned when the token's iss is not Issuer.
	ErrIssuer = errors.New("apptoken: bad issuer")
	// ErrNoSecret is returned when the Minter/verify is called with no secret.
	ErrNoSecret = errors.New("apptoken: no signing secret configured")
	// ErrEmptyField is returned by Mint when subject or audience is empty.
	ErrEmptyField = errors.New("apptoken: subject and audience are required")
)

// Claims is the payload of an app-identity token.
type Claims struct {
	// Sub is the Vulos user id (cp users.id) the request is on behalf of.
	Sub string `json:"sub"`
	// Aud is the target app slug this token is valid for (e.g. "office").
	Aud string `json:"aud"`
	// Iss is always Issuer.
	Iss string `json:"iss"`
	// Iat / Exp are unix seconds.
	Iat int64 `json:"iat"`
	// Exp is unix seconds; the token is invalid at or after this instant.
	Exp int64 `json:"exp"`
}

// Minter mints app-identity tokens with a fixed secret and TTL.
type Minter struct {
	secret []byte
	ttl    time.Duration
	nowFn  func() time.Time
}

// NewMinter returns a Minter signing with secret. ttl <= 0 falls back to
// DefaultTTL; ttl above maxTTL is clamped. secret must be non-empty for Mint
// to succeed.
func NewMinter(secret []byte, ttl time.Duration) *Minter {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl > maxTTL {
		ttl = maxTTL
	}
	// Defensive copy so callers can't mutate the secret after construction.
	s := make([]byte, len(secret))
	copy(s, secret)
	return &Minter{secret: s, ttl: ttl, nowFn: time.Now}
}

// Mint issues a fresh token for user sub, bound to audience aud.
func (m *Minter) Mint(sub, aud string) (string, error) {
	if m == nil {
		return "", ErrNoSecret
	}
	return m.MintAt(sub, aud, m.nowFn())
}

// MintAt is Mint with an explicit issue time, so a caller (a test, a replay of a
// known instant) can produce a deterministic token — including a deliberately
// stale one, to prove the TTL is enforced rather than assumed.
func (m *Minter) MintAt(sub, aud string, at time.Time) (string, error) {
	if m == nil || len(m.secret) == 0 {
		return "", ErrNoSecret
	}
	if sub == "" || aud == "" {
		return "", ErrEmptyField
	}
	now := at.UTC()
	c := Claims{
		Sub: sub,
		Aud: aud,
		Iss: Issuer,
		Iat: now.Unix(),
		Exp: now.Add(m.ttl).Unix(),
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := sign(m.secret, payload)
	return body + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// Looks reports whether token has the SHAPE of an app-identity token — without
// any secret, and therefore without proving it is authentic.
//
// This exists so credential-class enforcement can be explicit rather than
// accidental. A CP session token is 32 random bytes in base64url, an alphabet
// that contains no ".", so a token carrying a "." with an app-token payload can
// never be a session — and the session gate rejects it up front (see
// auth.RequireSession) instead of relying on the incidental fact that it would
// also miss the sessions table.
//
// A forged/garbage token can of course be made to "look" like one; that is
// fine, because every path that ACTS on an app token still verifies it with
// Verify. Looks only decides which credential class a token is claiming to be.
func Looks(token string) bool {
	body, sig, ok := strings.Cut(token, ".")
	if !ok || body == "" || sig == "" {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return false
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return false
	}
	return c.Iss == Issuer
}

// Verify checks token's signature, issuer, audience and expiry against secret
// and expectedAud, returning the parsed Claims on success. Provided so a shared
// verifier (or this repo's tests) can validate a minted token; the upstream app
// backends verify with their own copy of the secret + this format.
func Verify(secret []byte, token, expectedAud string, now time.Time) (Claims, error) {
	if expectedAud == "" {
		return Claims{}, ErrAudience
	}
	c, err := parse(secret, token, now)
	if err != nil {
		return Claims{}, err
	}
	if c.Aud != expectedAud {
		return Claims{}, ErrAudience
	}
	return c, nil
}

// VerifyAny is Verify against a rotation window: it accepts token if ANY of
// secrets verifies it. Used by the CP, whose signing key is derived from
// CP_SHARED_SECRET and must therefore accept both the current and the previous
// key while a rotation is in flight. Returns the first successful Claims.
//
// expectedAud may be "" to skip the audience check — the ONLY caller allowed to
// do that is session introspection, which is service-authed and cannot know
// which app is asking. Every capability-bearing path (storage presign/delete)
// passes a concrete audience.
func VerifyAny(secrets [][]byte, token, expectedAud string, now time.Time) (Claims, error) {
	if len(secrets) == 0 {
		return Claims{}, ErrNoSecret
	}
	var lastErr error = ErrBadSignature
	for _, s := range secrets {
		c, err := parse(s, token, now)
		if err != nil {
			// An expired/malformed token is expired/malformed under every key —
			// keep the specific reason rather than reporting a signature error.
			if !errors.Is(err, ErrBadSignature) {
				return Claims{}, err
			}
			lastErr = err
			continue
		}
		if expectedAud != "" && c.Aud != expectedAud {
			return Claims{}, ErrAudience
		}
		return c, nil
	}
	return Claims{}, lastErr
}

// parse verifies the MAC, issuer and expiry — everything except the audience.
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
	gotMAC, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	wantMAC := sign(secret, payload)
	if !hmac.Equal(gotMAC, wantMAC) {
		return Claims{}, ErrBadSignature
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Claims{}, ErrMalformed
	}
	if c.Iss != Issuer {
		return Claims{}, ErrIssuer
	}
	if c.Sub == "" || c.Aud == "" {
		return Claims{}, ErrEmptyField
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
