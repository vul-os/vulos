package lan

import (
	"crypto/tls"
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
			if f.cached != nil {
				return f.cached, nil
			}
			return f.fallback.Certificate(hello)
		}
		f.cached = &cert
		f.loadedAt.cert = certStat.ModTime()
		f.loadedAt.key = keyStat.ModTime()
	}
	return f.cached, nil
}
