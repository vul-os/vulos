package oauthprovider

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// PKCE (RFC 7636) support. This provider only accepts the S256 method — the
// "plain" method is deliberately unsupported because it offers no protection
// against authorization-code interception.
const (
	// PKCEMethodS256 is the only code_challenge_method this provider accepts.
	PKCEMethodS256 = "S256"
)

// ValidChallenge reports whether challenge is a syntactically plausible S256
// code_challenge: a non-empty base64url (no padding) string within the
// RFC 7636 length bounds (43..128 chars for a base64url-encoded SHA-256).
func ValidChallenge(challenge string) bool {
	if len(challenge) < 43 || len(challenge) > 128 {
		return false
	}
	// Must decode as base64url without padding.
	_, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil
}

// VerifyS256 reports whether verifier, transformed with the S256 method,
// matches the stored code_challenge. The comparison is constant-time.
//
//	S256: code_challenge == BASE64URL-ENCODE(SHA256(ASCII(code_verifier)))
func VerifyS256(verifier, challenge string) bool {
	// RFC 7636 §4.1: the verifier is 43..128 chars of [A-Za-z0-9-._~].
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}
