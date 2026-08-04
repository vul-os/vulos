package core

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
)

// ErrNotImplemented is returned by every function in this scaffold.
//
// These stubs return an explicit error rather than a zero value ON PURPOSE. A
// pinning check that returns (true, nil) before it is implemented would be
// indistinguishable from a working one at the call site, and would present an
// unauthenticated connection as a verified one. Failing loudly is the only safe
// placeholder for a security primitive.
var ErrNotImplemented = errors.New("clients/core: not implemented yet (scaffold)")

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
func (f Fingerprint) String() string { return "" } // TODO(scaffold)

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

// Discover finds Vulos boxes on the local network over mDNS.
//
// Discovery is NOT trust: a returned Box may be an impostor advertising the
// same name. Nothing discovered here may be connected to without a pin that was
// confirmed out of band via Pair.
func Discover(ctx context.Context) ([]Box, error) {
	return nil, ErrNotImplemented
}

// Pair performs trust-on-first-use pairing against a box.
//
// payload is the contents of the QR code displayed by the box, which carries
// its address and SPKI fingerprint. Pair MUST verify that the certificate
// presented by the live connection matches the fingerprint in the payload
// before storing anything — reading the fingerprint off the wire and storing
// that would pin whatever answered, which is no protection at all.
func Pair(ctx context.Context, payload string, store Store) (Box, error) {
	return Box{}, ErrNotImplemented
}

// TLSConfig returns a tls.Config that authenticates the box against its pin
// INSTEAD OF the public CA pool.
//
// It must set InsecureSkipVerify (disabling hostname/CA checking, which is
// meaningless for a self-signed cert on a LAN IP) and supply its own
// VerifyPeerCertificate that compares the presented SPKI digest against the
// pin. Setting InsecureSkipVerify without that callback would disable
// verification entirely — the exact failure this package exists to prevent.
func TLSConfig(b Box) (*tls.Config, error) {
	return nil, ErrNotImplemented
}

// Client returns an http.Client whose transport authenticates the box by pin.
func Client(b Box) (*http.Client, error) {
	return nil, ErrNotImplemented
}
