package oauthprovider

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// randomToken returns a base64url (no padding) string carrying n bytes of
// cryptographic entropy.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oauthprovider: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken returns hex(sha256(raw)). Used to store auth codes, access/refresh
// tokens, and client secrets so a database leak never exposes a usable
// credential. SHA-256 (not a slow KDF) is appropriate here because every value
// we hash is itself high-entropy (>=128 bits of randomness), so brute-forcing
// the preimage is infeasible regardless of hash speed.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
