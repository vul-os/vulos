package lan

// fingerprint.go — PAIR-01: the box side of certificate pinning.
//
// Native clients (clients/core/pinning.go, clients/core/pair.go) pin a box's
// certificate by the SHA-256 digest of its DER-encoded SubjectPublicKeyInfo
// (SPKI), not by the certificate itself — see certsource.go's "PERSISTED
// IDENTITY" doc for why that survives cert re-mints across restarts. This
// file computes that same digest on the box side and builds/parses the
// `vulos://pair?...` payload a box shows a user so a client can pair.
//
// The payload shape is a cross-repo contract fixed by clients/core/pair.go
// (EncodePairPayload / ParsePairPayload):
//
//	vulos://pair?name=<url-encoded box name>&addr=<host:port>&spki=<base64 SPKI SHA-256>
//
// BuildPairingURI/ParsePairingURI here are constructed with the exact same
// net/url primitives (url.Values + url.URL) that clients/core/pair.go uses,
// so the two sides agree byte-for-byte (in particular: url.Values.Encode
// alphabetizes query keys — addr, name, spki — regardless of Set order).

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// pairScheme and pairHost mirror the constants of the same name in
// clients/core/pair.go. Duplicated rather than shared because backend/ and
// clients/ are separate Go modules with no dependency between them (and
// backend must never depend on client code) — see doc.go for that boundary.
const (
	pairScheme = "vulos"
	pairHost   = "pair"
)

// ErrNoCertificate is returned when a CertSource cannot produce a certificate
// to fingerprint at all (distinct from a parse failure on cert bytes it did
// produce).
var ErrNoCertificate = errors.New("lan: cert source returned no certificate")

// SPKISHA256 returns the raw 32-byte SHA-256 digest of src's current
// certificate's DER-encoded SubjectPublicKeyInfo.
//
// This is computed from the KEY, not the certificate: SelfSignedCertSource
// (certsource.go) re-mints its certificate — new serial, new NotBefore/
// NotAfter — on every process start, but always from the SAME persisted
// private key (loadOrCreateKey), and a certificate's SPKI is a function of
// the public key alone. So this digest is stable across restarts even though
// the certificate bytes are not, which is exactly the property pinning
// depends on. Hashing the full certificate DER instead would break that
// stability the moment the box restarts.
func SPKISHA256(src CertSource) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if src == nil {
		return zero, errors.New("lan: CertSource is nil")
	}
	cert, err := src.Certificate(nil)
	if err != nil {
		return zero, fmt.Errorf("lan: get certificate: %w", err)
	}
	if cert == nil {
		return zero, ErrNoCertificate
	}

	leaf := cert.Leaf
	if leaf == nil {
		if len(cert.Certificate) == 0 {
			return zero, ErrNoCertificate
		}
		leaf, err = x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return zero, fmt.Errorf("lan: parse leaf certificate: %w", err)
		}
	}
	if len(leaf.RawSubjectPublicKeyInfo) == 0 {
		return zero, errors.New("lan: leaf certificate has no SubjectPublicKeyInfo bytes")
	}
	return sha256.Sum256(leaf.RawSubjectPublicKeyInfo), nil
}

// SPKIFingerprintBase64 renders SPKISHA256 as standard base64 — the exact
// string form clients/core/pinning.go's Fingerprint.String() produces, the
// format [decodeSPKIPins] in lancert_puller.go already parses, and the value
// that goes in a pairing payload's spki= field.
func SPKIFingerprintBase64(src CertSource) (string, error) {
	sum, err := SPKISHA256(src)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

// SPKIFingerprintHex renders SPKISHA256 as a human-readable, colon-separated
// uppercase hex string (the familiar openssl-fingerprint style), e.g.
// "AA:BB:CC:...:00". This is what an operator reads off the console / over
// SSH to compare aloud — the base64 form is what travels in the payload, this
// is what a human types or eyeballs.
func SPKIFingerprintHex(src CertSource) (string, error) {
	sum, err := SPKISHA256(src)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":"), nil
}

// BuildPairingURI renders the `vulos://pair?...` payload for the given box
// name, dialable host:port address, and base64 SPKI fingerprint (as returned
// by SPKIFingerprintBase64). It is built with the identical net/url
// primitives clients/core/pair.go's EncodePairPayload uses (url.Values +
// url.URL), so the two sides produce byte-identical output — in particular,
// url.Values.Encode alphabetizes query parameters (addr, name, spki) rather
// than preserving Set order.
func BuildPairingURI(name, addr, spkiBase64 string) string {
	q := url.Values{}
	q.Set("name", name)
	q.Set("addr", addr)
	q.Set("spki", spkiBase64)
	u := url.URL{
		Scheme:   pairScheme,
		Host:     pairHost,
		RawQuery: q.Encode(),
	}
	return u.String()
}

// PairingPayload computes src's current SPKI fingerprint and builds the full
// pairing URI in one call.
func PairingPayload(name, addr string, src CertSource) (string, error) {
	spki, err := SPKIFingerprintBase64(src)
	if err != nil {
		return "", err
	}
	return BuildPairingURI(name, addr, spki), nil
}

// ErrUnknownPairScheme mirrors clients/core/pair.go's error of the same name:
// the payload is not a "vulos://pair" URL at all.
var ErrUnknownPairScheme = errors.New("lan: not a vulos pairing payload")

// ErrMalformedPairPayload mirrors clients/core/pair.go: the payload parses as
// a vulos://pair URL but is missing/malformed a required field.
var ErrMalformedPairPayload = errors.New("lan: malformed pairing payload")

// ParsePairingURI parses a payload produced by BuildPairingURI /
// PairingPayload, mirroring clients/core/pair.go's ParsePairPayload exactly
// (same field requirements, same addr host:port validation) so a round trip
// through this box-side pair produces the same (name, addr, spki) a real
// client would decode. It exists so this package's own tests can verify the
// payload it emits is well-formed without importing the client module
// (backend/ and clients/ are separate modules — see doc.go).
func ParsePairingURI(payload string) (name, addr, spkiBase64 string, err error) {
	u, perr := url.Parse(payload)
	if perr != nil {
		return "", "", "", fmt.Errorf("%w: %v", ErrUnknownPairScheme, perr)
	}
	if u.Scheme != pairScheme || u.Host != pairHost {
		return "", "", "", fmt.Errorf("%w: got scheme=%q host=%q", ErrUnknownPairScheme, u.Scheme, u.Host)
	}

	q := u.Query()
	name = q.Get("name")
	addr = q.Get("addr")
	spkiBase64 = q.Get("spki")

	switch {
	case name == "":
		return "", "", "", fmt.Errorf("%w: missing name", ErrMalformedPairPayload)
	case addr == "":
		return "", "", "", fmt.Errorf("%w: missing addr", ErrMalformedPairPayload)
	case spkiBase64 == "":
		return "", "", "", fmt.Errorf("%w: missing spki", ErrMalformedPairPayload)
	}
	if _, _, herr := net.SplitHostPort(addr); herr != nil {
		return "", "", "", fmt.Errorf("%w: addr %q: %v", ErrMalformedPairPayload, addr, herr)
	}
	return name, addr, spkiBase64, nil
}

// PairingAddr returns the host:port a native client should dial for LAN
// pairing: the box's detected LAN IP (the same address the LAN HTTPS
// listener pins itself to — see lanBindAddr in lan.go) combined with the port
// from httpsAddr, the LAN HTTPS listener's configured address (e.g. ":443" or
// "0.0.0.0:8443"). httpsAddr's host portion, if any, is ignored: DetectLANIP
// is authoritative for the routable address a remote client can actually
// dial, matching how lanBindAddr resolves a wildcard host at listen time. A
// missing/unparsable port falls back to 443, the documented default.
func PairingAddr(httpsAddr string) string {
	_, port, err := net.SplitHostPort(httpsAddr)
	if err != nil || port == "" {
		port = "443"
	}
	return net.JoinHostPort(DetectLANIP().String(), port)
}
