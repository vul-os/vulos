// Package routing — acmedns.go
//
// GenerateAcmeDNSCreds produces per-ULID acme-dns TXT-delegation material so
// that the instance can prove ACME DNS-01 challenges via the acme-dns relay.
//
// Security invariant: this file NEVER generates, stores, or returns a TLS
// private key. Delegation only — the instance holds its own key pair; we only
// hand it a credential to update a TXT record on our relay.
package routing

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// AcmeDNSCreds is the TXT-delegation material returned in an enroll response.
// It gives the instance everything it needs to perform an acme-dns DNS-01
// challenge via the vulos relay. No TLS private key is ever included here.
type AcmeDNSCreds struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	Subdomain  string `json:"subdomain"`
	FullDomain string `json:"fulldomain"`
}

const acmeDNSBase = "auth.acme.vulos.org"

// GenerateAcmeDNSCreds returns fresh acme-dns credentials for the given
// canonical ULID. The subdomain is deterministically derived from the ULID
// (lowercased) so that re-enrollment for the same ULID produces the same
// subdomain (TXT record location stays stable). Username and password are
// cryptographically random on every call — callers must persist them.
//
// The returned struct contains NO TLS key material. (See package doc.)
func GenerateAcmeDNSCreds(ulid string) (AcmeDNSCreds, error) {
	username, err := randomHex(16)
	if err != nil {
		return AcmeDNSCreds{}, fmt.Errorf("acmedns: generate username: %w", err)
	}
	password, err := randomHex(32)
	if err != nil {
		return AcmeDNSCreds{}, fmt.Errorf("acmedns: generate password: %w", err)
	}

	// Subdomain is deterministic: lowercase ULID as a DNS label.
	// ULID is already canonical 26-char Crockford-base32 uppercase; lower-case
	// is valid in DNS labels and avoids case-sensitivity issues.
	subdomain := strings.ToLower(ulid)
	fullDomain := subdomain + "." + acmeDNSBase

	return AcmeDNSCreds{
		Username:   username,
		Password:   password,
		Subdomain:  subdomain,
		FullDomain: fullDomain,
	}, nil
}

// randomHex returns n random bytes encoded as a lowercase hex string (2n chars).
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
