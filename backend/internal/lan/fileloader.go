package lan

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"sync"
	"time"
)

// DefaultCertPath and DefaultKeyPath are the canonical on-disk locations the
// LAN cert puller (FIX-LANCERT-PULL-01) writes to and that
// [LoadCertSource] hot-reloads from. They MUST match the documented contract
// of the externally operated LAN-cert issuer — see doc.go for the contract
// rationale. The OS server bootstrap and the puller both reference these
// constants so the writer and reader never drift.
const (
	DefaultCertPath = "/var/lib/vulos/tls/lan.crt"
	DefaultKeyPath  = "/var/lib/vulos/tls/lan.key"
)

// LoadCertSource returns a CertSource for the LAN HTTPS listener.
//
// If certFile/keyFile are both set, it returns a fileCertSource that reads them
// from disk and hot-reloads on change — this is exactly how the externally
// issued, no-warning cert from LANCERT-01 (ACME DNS-01) plugs in: that task
// drops the renewed cert at these paths and this source picks it up with no
// restart. When the files are missing or fail to parse, it falls back to a
// self-signed dev cert covering hosts/ips so the box still has TLS material.
func LoadCertSource(certFile, keyFile string, hosts []string, ips []net.IP) CertSource {
	fallback := NewSelfSignedCertSource(hosts, ips)
	if certFile == "" || keyFile == "" {
		return fallback
	}
	return &fileCertSource{
		certFile: certFile,
		keyFile:  keyFile,
		fallback: fallback,
	}
}

// LoadDynamicCertSource is LoadCertSource with the self-signed fallback's SANs
// supplied by callbacks rather than fixed at construction.
//
// This is what the OS server uses: the box can be renamed live
// (POST /api/identity/hostname) and DHCP can move it to a new address, and the
// fallback certificate must follow both — a certificate that names neither the
// box's current name nor its current IP produces a NAME MISMATCH warning on
// top of the unknown-issuer one, which is two errors where there should be one.
// See NewDynamicSelfSignedCertSource for the re-mint rules and why re-minting
// does not break SPKI pinning.
//
// The externally issued cert at certFile/keyFile (LANCERT-01) is unaffected:
// its SANs are whatever the issuer put in it, and it still wins whenever it is
// present and parses.
func LoadDynamicCertSource(certFile, keyFile string, hostsFn func() []string, ipsFn func() []net.IP) CertSource {
	fallback := NewDynamicSelfSignedCertSource(hostsFn, ipsFn, defaultSelfSignedKeyPath())
	if certFile == "" || keyFile == "" {
		return fallback
	}
	return &fileCertSource{
		certFile: certFile,
		keyFile:  keyFile,
		fallback: fallback,
	}
}

// fileCertSource serves a cert+key pair read from disk, reloading when the
// files' modification times change so cert renewals take effect without a
// listener restart. Parse/read failures degrade gracefully to the fallback.
type fileCertSource struct {
	certFile string
	keyFile  string
	fallback CertSource

	mu       sync.Mutex
	cached   *tls.Certificate
	loadedAt struct{ cert, key time.Time }
}

// Certificate implements CertSource.
//
// SELECTION (FIX-LANCERT-SNI-01): the on-disk certificate wins only when it is
// actually USABLE for this handshake — currently valid, and covering the name
// the client asked for. Otherwise the self-signed fallback is served.
//
// Before this, the on-disk certificate won unconditionally once it parsed, and
// that produced two ways to lock an owner out of their own box:
//
//   - AN EXPIRED LEAF WAS SERVED FOREVER. Renewal of a CA-issued LAN cert needs
//     an operator to run a tool; a box that sat offline past its leaf's expiry
//     would serve the dead certificate to every client, and an expired
//     certificate is a HARD failure in a browser (NET::ERR_CERT_DATE_INVALID
//     has no proceed button on some platforms) — strictly worse than the
//     self-signed cert's one-click warning that this box shipped with. The
//     fallback is a 10-year self-signed cert that is always valid, so falling
//     back is always the better of the two.
//
//   - A NAME THE LEAF DID NOT COVER GOT A NAME MISMATCH. A name-constrained CA
//     cannot cover every name a box answers to — internal/lan's NameSet
//     includes bare single labels ("vulos"), and RFC 5280 permittedSubtrees has
//     no way to express "any single label" (see internal/lanca.FilterIssuable).
//     Serving a leaf without the requested SAN turns a one-click unknown-issuer
//     warning into a name-mismatch error, which is the same click plus a
//     confusing reason.
//
// WHAT THIS DELIBERATELY DOES NOT DO: it does not try to serve the CA-issued
// cert only to devices that have the root installed. THE HANDSHAKE CARRIES NO
// SUCH SIGNAL — browsers do not send the TLS `certificate_authorities`
// extension in a ClientHello, so a server cannot know which roots a client
// trusts. It does not need to: a device without the root that receives the
// CA-issued cert sees an unknown-issuer warning, which is exactly the one click
// it would have had anyway. Never a hard failure, never a lockout — including
// the first connection, where the owner is downloading the root itself.
func (f *fileCertSource) Certificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	certStat, errC := os.Stat(f.certFile)
	keyStat, errK := os.Stat(f.keyFile)
	if errC != nil || errK != nil {
		// Files not present (yet) — use the fallback so HTTPS still works.
		return f.fallback.Certificate(hello)
	}

	stale := f.cached == nil ||
		!certStat.ModTime().Equal(f.loadedAt.cert) ||
		!keyStat.ModTime().Equal(f.loadedAt.key)

	if stale {
		cert, err := tls.LoadX509KeyPair(f.certFile, f.keyFile)
		if err != nil {
			// Bad/partial write during a renewal — keep serving whatever we had,
			// else fall back rather than failing the handshake.
			if f.cached != nil && usableForHello(f.cached, hello, time.Now()) {
				return f.cached, nil
			}
			return f.fallback.Certificate(hello)
		}
		f.cached = &cert
		f.loadedAt.cert = certStat.ModTime()
		f.loadedAt.key = keyStat.ModTime()
	}

	if usableForHello(f.cached, hello, time.Now()) {
		return f.cached, nil
	}
	return f.fallback.Certificate(hello)
}

// usableForHello reports whether cert can serve this handshake.
//
// now is a parameter rather than a time.Now() call so the expiry behaviour can
// be tested without waiting a year or backdating a certificate.
func usableForHello(cert *tls.Certificate, hello *tls.ClientHelloInfo, now time.Time) bool {
	if cert == nil {
		return false
	}
	leaf := cert.Leaf
	if leaf == nil {
		// crypto/tls populates Leaf on LoadX509KeyPair, but a caller may have
		// built the tls.Certificate by hand. Parse rather than assume.
		if len(cert.Certificate) == 0 {
			return false
		}
		parsed, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return false
		}
		leaf = parsed
	}

	// Validity. Both ends are checked: a certificate whose NotBefore is in the
	// future fails in a browser exactly as hard as an expired one, and a box
	// with no RTC can boot with a badly wrong clock.
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return false
	}

	// Name. An empty ServerName means the client sent no SNI — which is what a
	// browser does when the URL is a bare IP literal. There is no name to check
	// in that case, so validity alone decides; the IP SAN (if any) is checked by
	// the client against the address it dialled, not by us.
	if hello == nil || hello.ServerName == "" {
		return true
	}
	return leaf.VerifyHostname(hello.ServerName) == nil
}
