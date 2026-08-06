package core

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"
)

// pairScheme and pairHost are the fixed scheme/host of a pairing payload:
//
//	vulos://pair?name=<url-encoded box name>&addr=<host:port>&spki=<base64 SPKI SHA-256>
//
// This exact shape is a cross-repo contract: the box side encodes it into the
// QR code it displays, this package decodes it. EncodePairPayload and
// ParsePairPayload are the ONE implementation of that shape — see doc.go on
// why divergent duplicate implementations are the thing to avoid.
const (
	pairScheme = "vulos"
	pairHost   = "pair"
)

// ErrUnknownPairScheme is returned by ParsePairPayload when the payload is
// not a "vulos://pair" URL — e.g. it isn't a Vulos pairing QR code at all, or
// is a payload from an incompatible future/past version of the scheme.
var ErrUnknownPairScheme = errors.New("clients/core: not a vulos pairing payload")

// ErrMalformedPairPayload is returned by ParsePairPayload when the payload
// parses as a vulos://pair URL but is missing or malformed one of the
// required fields (name, addr).
var ErrMalformedPairPayload = errors.New("clients/core: malformed pairing payload")

// ErrMalformedPairPin is returned by ParsePairPayload when the spki field is
// present but is not a valid base64 SHA-256 SPKI fingerprint.
var ErrMalformedPairPin = errors.New("clients/core: malformed pairing payload spki fingerprint")

// EncodePairPayload renders the pairing payload a box shows the user (as a QR
// code) in the form parsed by ParsePairPayload / Pair. name is URL-encoded by
// net/url; addr is expected to already be a "host:port" string.
func EncodePairPayload(name, addr string, fp Fingerprint) string {
	q := url.Values{}
	q.Set("name", name)
	q.Set("addr", addr)
	q.Set("spki", fp.String())
	u := url.URL{
		Scheme:   pairScheme,
		Host:     pairHost,
		RawQuery: q.Encode(),
	}
	return u.String()
}

// ParsePairPayload parses a payload produced by EncodePairPayload. It does no
// network I/O — it only validates the payload's shape — so it is safe to call
// on untrusted input (e.g. straight off a scanned QR code) before deciding
// whether to attempt a connection at all.
func ParsePairPayload(payload string) (name, addr string, fp Fingerprint, err error) {
	u, perr := url.Parse(payload)
	if perr != nil {
		return "", "", Fingerprint{}, fmt.Errorf("%w: %v", ErrUnknownPairScheme, perr)
	}
	if u.Scheme != pairScheme || u.Host != pairHost {
		return "", "", Fingerprint{}, fmt.Errorf("%w: got scheme=%q host=%q", ErrUnknownPairScheme, u.Scheme, u.Host)
	}

	q := u.Query()
	name = q.Get("name")
	addr = q.Get("addr")
	spki := q.Get("spki")

	switch {
	case name == "":
		return "", "", Fingerprint{}, fmt.Errorf("%w: missing name", ErrMalformedPairPayload)
	case addr == "":
		return "", "", Fingerprint{}, fmt.Errorf("%w: missing addr", ErrMalformedPairPayload)
	case spki == "":
		return "", "", Fingerprint{}, fmt.Errorf("%w: missing spki", ErrMalformedPairPayload)
	}
	if _, _, herr := net.SplitHostPort(addr); herr != nil {
		return "", "", Fingerprint{}, fmt.Errorf("%w: addr %q: %v", ErrMalformedPairPayload, addr, herr)
	}

	fp, ferr := ParseFingerprint(spki)
	if ferr != nil {
		return "", "", Fingerprint{}, fmt.Errorf("%w: %v", ErrMalformedPairPin, ferr)
	}

	return name, addr, fp, nil
}

// pairDialTimeout bounds how long Pair waits to establish and verify the TLS
// connection during pairing.
const pairDialTimeout = 15 * time.Second

// Pair performs trust-on-first-use pairing against a box.
//
// payload is the contents of the QR code displayed by the box, which carries
// its address and SPKI fingerprint. Pair dials addr with a TLS config pinned
// to the fingerprint PARSED FROM THE PAYLOAD — never one read off the wire —
// so the live connection's certificate must match what the payload (and, out
// of band, the box's own screen) asserted before anything is trusted. Only
// once that handshake succeeds is the pin passed to store.Save; a failed
// handshake (wrong/tampered pin, unreachable box) never reaches the store.
func Pair(ctx context.Context, payload string, store Store) (Box, error) {
	if store == nil {
		return Box{}, errors.New("clients/core: Pair: store is nil")
	}

	name, addr, fp, err := ParsePairPayload(payload)
	if err != nil {
		return Box{}, err
	}

	candidate := Box{Name: name, Addr: addr, Pin: fp}

	cfg, err := TLSConfig(candidate)
	if err != nil {
		// Unreachable in practice — ParsePairPayload only returns a non-zero
		// fp, so candidate is always Paired() — but this is a security
		// primitive, so the check stays rather than being assumed away.
		return Box{}, fmt.Errorf("clients/core: pair: %w", err)
	}

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: pairDialTimeout},
		Config:    cfg,
	}
	dialCtx, cancel := context.WithTimeout(ctx, pairDialTimeout)
	defer cancel()

	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		// Covers both an unreachable box AND a box whose certificate does not
		// match the payload's spki — tls.Dialer runs the handshake (and thus
		// VerifyPeerCertificate) as part of DialContext, so a pin mismatch
		// surfaces here as a dial error, before store.Save is ever reached.
		return Box{}, fmt.Errorf("clients/core: pair with %q (%s): %w", name, addr, err)
	}
	_ = conn.Close()

	if err := store.Save(ctx, name, fp); err != nil {
		return Box{}, fmt.Errorf("clients/core: pair: save pin for %q: %w", name, err)
	}

	return candidate, nil
}
