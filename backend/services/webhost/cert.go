package webhost

import (
	"context"
	"errors"
)

// cert.go is the TLS-issuance SEAM for custom domains. Attaching a domain to a
// site is meaningless without a certificate the box can terminate on, but
// issuing one requires infrastructure that cannot be exercised in this build
// (a public-reachable ACME client, a Vulos-controlled DNS zone, a shared cert
// store, a fleet lock). So issuance lives behind an interface with an HONEST
// default that issues nothing and says so.
//
// REAL DESIGN (not implemented here — documented so the seam is not mistaken
// for a stub of something already working):
//
//	DNS-01 via acme-dns CNAME delegation. Each box does NOT get write access
//	to the owner's real DNS. Instead the owner points a one-time
//	_acme-challenge.<domain> CNAME at a per-domain record inside a
//	Vulos-controlled zone (…​.acme.vulos.org — the exact CNAME target is what
//	DomainManager.AttachDomain returns). certmagic's ACME client then satisfies
//	the DNS-01 challenge by writing the TXT into THAT delegated zone, never the
//	owner's. This is the standard acme-dns pattern and keeps the box out of the
//	owner's authoritative DNS entirely.
//
//	Fleet issuance/renewal. Certs and ACME account keys live in a SHARED store
//	(object storage / the box's bucket), fronted by certmagic's Storage
//	interface, so any box in a cell can present a domain's cert and renewals do
//	not duplicate. A DISTRIBUTED LOCK (certmagic already models this via
//	Storage.Lock) serializes issuance so N boxes racing on the same domain
//	produce one order, not N. Renewal is a background sweep behind the same
//	lock.
//
// None of that is wired here. certmagic and every ACME dependency are
// deliberately absent from go.mod: this file must compile OFFLINE with zero new
// module entries. When a real backend lands it implements CertProvider and is
// installed via WithCertProvider — the rest of the package does not change.

// ErrCertNotConfigured is returned by the default provider. It is not a
// failure of a working system; it means no TLS backend has been installed on
// this box, so custom domains can be ATTACHED (their DNS instructions
// returned) but will never reach "active".
var ErrCertNotConfigured = errors.New("webhost: no certificate backend configured (custom-domain TLS is not enabled on this box)")

// CertProvider is the swappable certificate backend for custom domains.
//
// EnsureCert requests (or confirms) a certificate for domain, blocking until
// the order settles or failing with a clean error. HasCert reports whether a
// usable certificate currently exists for domain WITHOUT attempting issuance —
// it is what DomainStatus consults to decide pending vs active.
type CertProvider interface {
	// EnsureCert obtains or renews a certificate for domain. Implementations
	// must be idempotent and safe to call concurrently across a fleet (see the
	// distributed-lock note above).
	EnsureCert(ctx context.Context, domain string) error
	// HasCert reports whether a valid certificate is presently available for
	// domain. It must not perform issuance.
	HasCert(domain string) bool
}

// NoopCertProvider is the honest default: it issues nothing and never claims a
// certificate exists. With it installed, an attached domain stays in the
// "pending" state indefinitely — the box is truthful that TLS is not enabled
// rather than pretending a domain is live.
type NoopCertProvider struct{}

// EnsureCert always reports that no backend is configured.
func (NoopCertProvider) EnsureCert(ctx context.Context, domain string) error {
	return ErrCertNotConfigured
}

// HasCert is always false: the no-op provider holds no certificates.
func (NoopCertProvider) HasCert(domain string) bool { return false }

// compile-time assertion.
var _ CertProvider = NoopCertProvider{}
