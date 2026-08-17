package lan

// lanroot.go — ROOTDIST-01: the box's copy of the LAN CA ROOT certificate.
//
// # The gap this closes
//
// D101 built a name-constrained private root (internal/lanca), an operator tool
// that runs it (cmd/vulos-lanca), and a box-side puller that installs the LEAF
// it signs (lancert_puller.go). Nothing carried the ROOT to the box. That is
// not a cosmetic omission: the root is the only part of the design a human
// actually has to touch — a browser shows a padlock on https://vulos.local if
// and only if the device it runs on has the root installed — and the box was
// the one machine guaranteed to be reachable from every device on the LAN while
// holding no copy of it. The owner's only route was to move a file off the
// operator machine by hand, to every phone and laptop, out of band.
//
// So the box now keeps the root PEM at [DefaultRootPath], alongside the leaf it
// already keeps at [DefaultCertPath]. It is PUBLIC material: a certificate,
// containing a public key and nothing secret, which is why it is written 0644
// while the leaf's key stays 0600. Storing it changes no trust property — the
// box does not gain the ability to issue anything, because the CA PRIVATE key
// is still refused on box paths by lanca.CheckNotOnBox.
//
// # What is refused, and why refusing is the safe direction
//
// A root is the most dangerous file an owner can be asked to install: it is a
// standing grant of authority on their device. Two shapes must never be handed
// out, so both are rejected on write AND again on read (the manual flow drops a
// file in by hand and never passes through the puller):
//
//   - NOT A CA. A leaf accidentally copied to the root path would be useless in
//     a trust store and would teach the owner the flow is broken.
//   - UNCONSTRAINED. A CA with no permittedSubtrees can vouch for `google.com`
//     on every device the owner installs it on. That is precisely the property
//     D101-B claims this design does not have, and the difference is invisible
//     in every install dialog on every OS. Refused unless the operator sets
//     VULOS_LANCERT_ALLOW_UNCONSTRAINED_ROOT, which exists so the refusal can be
//     tested and overridden knowingly rather than worked around silently.
//
// Refusing costs the owner the padlock and leaves the one-click self-signed
// warning they already had (fileloader.go always falls back). Accepting costs
// them a device-wide authority they cannot see. The asymmetry is not close.

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// DefaultRootPath is where the box keeps the CA root certificate it hands to
// browsers. It sits beside [DefaultCertPath] / [DefaultKeyPath] deliberately:
// one directory holds everything the LAN TLS story needs, and an operator doing
// this by hand has one place to look.
//
// This is the certificate ONLY. The CA private key must never be on the box —
// lanca.CheckNotOnBox refuses to write it under /var/lib/vulos for exactly this
// reason, and nothing here weakens that.
const DefaultRootPath = "/var/lib/vulos/tls/lan-root.crt"

// rootFileMode is the on-disk mode for the root certificate: world-readable,
// unlike the 0600 used for key material. A certificate is public by
// construction, and making it readable is the point — it is about to be
// downloaded by every device in the house.
const rootFileMode os.FileMode = 0o644

var (
	// ErrRootNotPresent means no root certificate has reached this box. It is a
	// normal state, not a fault: the CA is operated off-box and an owner may
	// never have run it.
	ErrRootNotPresent = errors.New("lan: no LAN CA root certificate on this box")

	// ErrRootNotCA means the file at the root path is a certificate but not a
	// certificate authority.
	ErrRootNotCA = errors.New("lan: the certificate at the LAN root path is not a CA (BasicConstraints CA:FALSE) — installing it in a device trust store would do nothing")

	// ErrRootUnconstrained means the CA carries no name constraints and could
	// therefore vouch for any name on earth.
	ErrRootUnconstrained = errors.New("lan: REFUSING an UNCONSTRAINED LAN CA root: it carries no X.509 permittedSubtrees, so a device that installs it would trust it for ANY name — including public sites. " +
		"D101 accepted a device-wide root only because it is cryptographically limited to .local/.lan/.home.arpa/lan.vulos.org and private IP space. " +
		"Set VULOS_LANCERT_ALLOW_UNCONSTRAINED_ROOT=1 only if you understand you are handing every device an unlimited authority")
)

// RootInfo is everything the box knows about its copy of the CA root — enough
// for an owner to decide whether to install it and to verify afterwards that
// what landed on their device is what left the box.
type RootInfo struct {
	// PEM is the certificate itself, exactly as it will be served.
	PEM []byte

	Subject string
	Issuer  string

	NotBefore time.Time
	NotAfter  time.Time

	// SHA256Hex is the colon-separated uppercase hex SHA-256 of the certificate
	// DER — the value `openssl x509 -fingerprint -sha256 -noout` prints and the
	// one most OS certificate viewers show. THIS is the fingerprint to compare.
	SHA256Hex string

	// SHA1Hex is the same in SHA-1. It is here only because some certificate
	// dialogs still show SHA-1 and nothing else, and a user staring at a dialog
	// that offers no SHA-256 is otherwise stuck. SHA-1 is collision-weak and
	// must never be the value a decision rests on when SHA-256 is available.
	SHA1Hex string

	// SPKIBase64 is the base64 SHA-256 of the root's SubjectPublicKeyInfo, in
	// the same encoding the pairing pin uses. This is NOT the box's pin — the
	// box's pin is its own leaf key (see fingerprint.go) and is unrelated.
	SPKIBase64 string

	// PermittedDNS / PermittedIP are the name constraints, rendered. Empty means
	// unconstrained, which [ParseRootPEM] refuses by default.
	PermittedDNS []string
	PermittedIP  []string

	// PathLenZero reports the pathLenConstraint=0 that stops this root signing a
	// subordinate CA.
	PathLenZero bool
}

// Constrained reports whether the root carries any name constraint at all.
func (r *RootInfo) Constrained() bool {
	return len(r.PermittedDNS) > 0 || len(r.PermittedIP) > 0
}

// Expired reports whether the root is outside its validity window at t. A root
// past NotAfter still installs on most platforms but validates nothing, so the
// surface that offers it has to be able to say so.
func (r *RootInfo) Expired(t time.Time) bool {
	return t.Before(r.NotBefore) || t.After(r.NotAfter)
}

// AllowUnconstrainedRoot reports whether the operator has explicitly opted into
// handling a root with no name constraints. Same env-var shape as the puller's
// other escape hatches (VULOS_LANCERT_ALLOW_ISSUER_KEY, VULOS_LANCERT_ALLOW_INSECURE).
func AllowUnconstrainedRoot() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VULOS_LANCERT_ALLOW_UNCONSTRAINED_ROOT"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// ParseRootPEM parses and VETS a PEM root certificate.
//
// The vetting is the reason this is not three lines of x509 in the handler: a
// root that is not a CA, or that is not name-constrained, must never reach an
// owner's trust store, and the only place that can be enforced for BOTH the
// puller path and the copy-a-file-in path is a function both call.
//
// The first CERTIFICATE block wins; trailing blocks are ignored rather than
// rejected so a bundle that concatenates the root with something else still
// works, and so the leaf-plus-root ordering an issuer might send cannot be
// silently misread as the root (the first block of such a bundle is the leaf,
// which fails the CA check loudly).
func ParseRootPEM(pemBytes []byte) (*RootInfo, error) {
	var der []byte
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			der = block.Bytes
			break
		}
	}
	if der == nil {
		return nil, errors.New("lan: no PEM CERTIFICATE block in the LAN root certificate")
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("lan: parse LAN root certificate: %w", err)
	}
	if !cert.IsCA {
		return nil, ErrRootNotCA
	}

	info := &RootInfo{
		PEM:          pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		Subject:      cert.Subject.CommonName,
		Issuer:       cert.Issuer.CommonName,
		NotBefore:    cert.NotBefore,
		NotAfter:     cert.NotAfter,
		SHA256Hex:    colonHex(sha256Sum(der)),
		SHA1Hex:      colonHex(sha1Sum(der)),
		SPKIBase64:   spkiBase64(cert),
		PermittedDNS: append([]string(nil), cert.PermittedDNSDomains...),
		PermittedIP:  renderIPNets(cert.PermittedIPRanges),
		PathLenZero:  cert.MaxPathLenZero,
	}
	if !info.Constrained() && !AllowUnconstrainedRoot() {
		return nil, ErrRootUnconstrained
	}
	return info, nil
}

// LoadRootInfo reads and vets the root certificate at path. A missing file
// returns [ErrRootNotPresent], which callers are expected to render as "not set
// up yet" rather than as a fault.
func LoadRootInfo(path string) (*RootInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrRootNotPresent
		}
		return nil, fmt.Errorf("lan: read LAN root certificate %s: %w", path, err)
	}
	return ParseRootPEM(b)
}

// InstallRootPEM vets rootPEM and writes it atomically to path.
//
// issuedLeaf, when non-nil, is checked to actually CHAIN TO this root. That
// check is the difference between distributing a useful root and distributing
// a random CA: a root that did not sign the certificate this box serves gives
// the owner no padlock at all, while still granting their device a standing
// authority. There is no benign version of that mismatch, so it is refused.
func InstallRootPEM(path string, rootPEM []byte, issuedLeaf *x509.Certificate) (*RootInfo, error) {
	info, err := ParseRootPEM(rootPEM)
	if err != nil {
		return nil, err
	}
	if issuedLeaf != nil {
		if err := rootSignedLeaf(info.PEM, issuedLeaf); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return nil, fmt.Errorf("lan: mkdir for LAN root certificate: %w", err)
	}
	if err := writeFileAtomic(path, info.PEM, rootFileMode); err != nil {
		return nil, fmt.Errorf("lan: write LAN root certificate %s: %w", path, err)
	}
	return info, nil
}

// rootSignedLeaf verifies leaf against rootPEM as the only trusted root.
//
// It deliberately does NOT check expiry or key usage of the leaf: this asks one
// question — "did this root issue that certificate" — and a leaf that has since
// expired still answers it. Expiry is fileloader.go's business, and it already
// falls back to self-signed rather than serving a dead leaf.
func rootSignedLeaf(rootPEM []byte, leaf *x509.Certificate) error {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(rootPEM) {
		return errors.New("lan: the LAN root certificate could not be added to a verification pool")
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		Roots:       pool,
		CurrentTime: leaf.NotBefore.Add(time.Second),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		return fmt.Errorf("lan: REFUSING this LAN root certificate: it did NOT issue the certificate this box serves (%w). "+
			"Installing it on a device would grant a standing authority and still produce no padlock for this box", err)
	}
	return nil
}

// --- small helpers, kept unexported and local so nothing else grows a
// dependency on their exact shape ------------------------------------------

func sha256Sum(b []byte) []byte { s := sha256.Sum256(b); return s[:] }

// sha1Sum backs RootInfo.SHA1Hex. SHA-1 is used here ONLY to reproduce what a
// certificate dialog that shows nothing else displays; it is never used to make
// a trust decision in this codebase.
func sha1Sum(b []byte) []byte { s := sha1.Sum(b); return s[:] } //nolint:gosec // display parity with OS cert dialogs, never a trust decision

func spkiBase64(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// colonHex renders a digest the way certificate tooling does: uppercase hex
// pairs separated by colons, so a value copied off a phone screen can be
// compared against `openssl x509 -fingerprint` character for character.
func colonHex(sum []byte) string {
	const hexDigits = "0123456789ABCDEF"
	if len(sum) == 0 {
		return ""
	}
	out := make([]byte, 0, len(sum)*3-1)
	for i, b := range sum {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	return string(out)
}

func renderIPNets(nets []*net.IPNet) []string {
	out := make([]string, 0, len(nets))
	for _, n := range nets {
		if n == nil {
			continue
		}
		out = append(out, n.String())
	}
	return out
}

// dirOf is filepath.Dir without importing filepath into this file's surface —
// kept trivial on purpose; the puller already imports filepath for the same job.
func dirOf(path string) string {
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return "."
	}
	return path[:i]
}
