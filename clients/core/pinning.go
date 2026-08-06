package core

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrNotImplemented is returned by every function in this scaffold.
//
// These stubs return an explicit error rather than a zero value ON PURPOSE. A
// pinning check that returns (true, nil) before it is implemented would be
// indistinguishable from a working one at the call site, and would present an
// unauthenticated connection as a verified one. Failing loudly is the only safe
// placeholder for a security primitive.
//
// It is kept (unused by the implementation below) as the documented shape a
// not-yet-ported platform shim should return rather than a bare nil error.
var ErrNotImplemented = errors.New("clients/core: not implemented yet (scaffold)")

// ErrUnpaired is returned by TLSConfig (and anything built on it) when asked
// to authenticate a Box that has no stored pin. There is deliberately no way
// to obtain a *tls.Config for an unpaired Box: doing so would let a caller
// mistakenly connect with verification silently skipped.
var ErrUnpaired = errors.New("clients/core: box is unpaired (no stored pin) — pair before connecting")

// Fingerprint is a box's pinned identity: the SHA-256 of its certificate's
// SubjectPublicKeyInfo (SPKI), not of the whole certificate.
//
// SPKI rather than the full certificate so the box can renew or re-issue its
// self-signed cert — changing validity dates, adding a SAN — WITHOUT breaking
// every paired client, as long as it keeps the same key. Pinning the whole
// certificate would turn routine renewal into a re-pair for every device.
type Fingerprint struct {
	// SPKISHA256 is the raw 32-byte digest.
	SPKISHA256 [32]byte
}

// String renders the fingerprint in the form shown to a user for out-of-band
// comparison against what the box displays.
//
// This is standard base64 of the raw 32-byte SHA-256 digest — the same
// `openssl ... | openssl dgst -sha256 -binary | base64` SPKI-pin convention
// documented and consumed by backend/internal/lan/lancert_puller.go's
// decodeSPKIPins. That is this repo's ONE SPKI-pin string format; this
// package deliberately does not invent a second one (see doc.go).
func (f Fingerprint) String() string {
	return base64.StdEncoding.EncodeToString(f.SPKISHA256[:])
}

// FingerprintFromCert computes the Fingerprint of an X.509 certificate: the
// SHA-256 digest of its DER-encoded SubjectPublicKeyInfo.
//
// It hashes cert.RawSubjectPublicKeyInfo directly — the exact DER bytes as
// they appeared on the wire — rather than re-marshalling the parsed public
// key, so this is byte-for-byte the same input backend/internal/lan's SPKI
// pinning hashes.
func FingerprintFromCert(cert *x509.Certificate) Fingerprint {
	return Fingerprint{SPKISHA256: sha256.Sum256(cert.RawSubjectPublicKeyInfo)}
}

// ParseFingerprint parses a base64 SPKI-SHA-256 pin string (the format
// produced by Fingerprint.String / accepted by VULOS_LANCERT_SPKI_PINS) into
// a Fingerprint. It rejects anything that is not valid standard base64
// encoding exactly 32 bytes, rather than silently truncating or padding.
func ParseFingerprint(s string) (Fingerprint, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return Fingerprint{}, fmt.Errorf("clients/core: SPKI pin %q is not valid base64: %w", s, err)
	}
	if len(raw) != sha256.Size {
		return Fingerprint{}, fmt.Errorf("clients/core: SPKI pin %q decodes to %d bytes, want %d", s, len(raw), sha256.Size)
	}
	var fp Fingerprint
	copy(fp.SPKISHA256[:], raw)
	return fp, nil
}

// Box is a discovered or paired Vulos box.
type Box struct {
	// Name is the box's advertised hostname (e.g. "vulos").
	Name string
	// Addr is the host:port the client should dial.
	Addr string
	// Pin is the pinned identity. Zero value means UNPAIRED — callers must
	// treat an unpaired box as untrusted, never as "pin check skipped".
	Pin Fingerprint
}

// Paired reports whether this box has a stored pin.
func (b Box) Paired() bool { return b.Pin != Fingerprint{} }

// Store persists pins across restarts. Implementations back onto the platform
// keystore (Keychain, Android Keystore, DPAPI, libsecret) rather than a plain
// file, so a pin cannot be silently rewritten by anything with disk access.
type Store interface {
	Load(ctx context.Context, name string) (Fingerprint, error)
	Save(ctx context.Context, name string, fp Fingerprint) error
	Forget(ctx context.Context, name string) error
}

// TLSConfig returns a tls.Config that authenticates the box against its pin
// INSTEAD OF the public CA pool.
//
// It sets InsecureSkipVerify (disabling hostname/CA checking, which is
// meaningless for a self-signed cert on a LAN IP) and supplies its own
// VerifyPeerCertificate that compares the presented SPKI digest against the
// pin in constant time. Setting InsecureSkipVerify without that callback
// would disable verification entirely — the exact failure this package
// exists to prevent — so TLSConfig refuses to hand back a config at all for
// an unpaired Box: there is no code path that returns a config which skips
// verification.
func TLSConfig(b Box) (*tls.Config, error) {
	if !b.Paired() {
		return nil, fmt.Errorf("%w: %q", ErrUnpaired, b.Name)
	}
	pin := b.Pin // copy: captured by the closure below, independent of caller mutation

	return &tls.Config{
		// Meaningless for a self-signed LAN cert with no CA chain or DNS
		// hostname to validate against — see doc.go's "trust model". The
		// VerifyPeerCertificate callback below is the ENTIRE verification;
		// removing it while this flag stays set disables auth completely.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("clients/core: no certificate presented")
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("clients/core: parse leaf certificate: %w", err)
			}
			got := FingerprintFromCert(leaf)
			if subtle.ConstantTimeCompare(got.SPKISHA256[:], pin.SPKISHA256[:]) != 1 {
				return fmt.Errorf("clients/core: certificate SPKI %s does not match pinned %s for %q", got, pin, b.Name)
			}
			return nil
		},
	}, nil
}

// Client returns an http.Client whose transport authenticates the box by pin.
//
// Timeouts are deliberately conservative rather than absent: a box on a LAN
// that stops answering (powered off, network partition) should fail a
// request in seconds, not hang the caller indefinitely.
func Client(b Box) (*http.Client, error) {
	cfg, err := TLSConfig(b)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSClientConfig:       cfg,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}, nil
}
