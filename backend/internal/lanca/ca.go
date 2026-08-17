package lanca

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"sort"
	"strings"
	"time"
)

// Lifetimes.
//
// RootTTL is deliberately long: this root is installed by hand on every device
// the owner uses, so rotating it is a manual chore on every one of them. Ten
// years is the interval at which that chore is acceptable.
//
// DefaultLeafTTL is 397 days, one day under the 398-day ceiling Apple and
// Chrome enforce on publicly trusted chains. Both vendors document that the
// ceiling does NOT apply to locally installed / user-added roots, so a longer
// leaf would very probably work — but "very probably works on every verifier"
// is not a property worth betting an owner's padlock on when the cost of
// staying under the line is one renewal a year. A caller that has measured its
// own fleet can raise it via [LeafRequest.TTL].
//
// Short-lived leaves (the 90-day web habit) are deliberately NOT the default
// here. That habit exists because public CAs have revocation infrastructure
// that browsers barely check, so expiry is the real revocation mechanism. This
// root has no OCSP responder and no CRL distribution point a browser would
// fetch — there is no revocation to approximate — so a short leaf buys nothing
// and costs availability on a box whose owner may not run the issuing tool for
// months. An expired leaf must never lock anyone out; that guarantee lives on
// the box side, not here.
const (
	RootTTL        = 10 * 365 * 24 * time.Hour
	DefaultLeafTTL = 397 * 24 * time.Hour

	// backdate absorbs clock skew between the issuing machine and the
	// verifying device. A box with no RTC and no NTP (offline first boot) can
	// be badly wrong; an hour covers ordinary skew without materially
	// extending the leaf.
	backdate = 1 * time.Hour
)

// Root is a name-constrained LAN certificate authority: a self-signed CA
// certificate plus the private key that signs leaves with it.
//
// The zero value is not usable. Build one with [NewRoot] (first run) or
// [LoadRoot] (every run after).
//
// THE KEY IN THIS STRUCT MUST NOT LIVE ON A VULOS BOX. It belongs on an
// operator's machine or in a control plane. Everything in this file is written
// so that the box never needs it: the box generates its own key, sends a CSR,
// and receives only a signed leaf.
type Root struct {
	Cert *x509.Certificate
	Key  crypto.Signer
	// DER is the root certificate's raw DER, kept so callers can emit PEM
	// without a re-marshal.
	DER []byte
}

// NewRoot mints a fresh name-constrained root CA.
//
// label is folded into the subject Common Name so an owner with several roots
// installed can tell them apart in a device's certificate list — on Android
// that list is the only place a user ever sees this thing, and "Vulos LAN Root
// CA" three times over is a usability failure, not a cosmetic one.
func NewRoot(label string) (*Root, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("lanca: generate root key: %w", err)
	}
	return newRootWithKey(key, label, time.Now())
}

func newRootWithKey(key crypto.Signer, label string, now time.Time) (*Root, error) {
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}

	cn := "Vulos LAN Root CA"
	if l := strings.TrimSpace(label); l != "" {
		cn = cn + " (" + l + ")"
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   cn,
			Organization: []string{"Vulos LAN (owner-operated)"},
		},
		NotBefore: now.Add(-backdate),
		NotAfter:  now.Add(RootTTL),

		BasicConstraintsValid: true,
		IsCA:                  true,
		// MaxPathLen 0 with MaxPathLenZero set encodes pathLenConstraint=0:
		// this root may sign end-entity certificates and NOTHING ELSE. It
		// cannot mint a subordinate CA, so the constraint set below cannot be
		// escaped by chaining through an intermediate.
		MaxPathLen:     0,
		MaxPathLenZero: true,
		KeyUsage:       x509.KeyUsageCertSign | x509.KeyUsageCRLSign,

		// THE NAME CONSTRAINTS. This is the extension that makes a stolen copy
		// of this root's private key unable to mint a usable certificate for
		// google.com. Marked CRITICAL: a verifier that does not understand the
		// extension is then required to reject the chain outright rather than
		// silently ignore the limits. Critical is the safe direction — it can
		// only ever cause a failure the owner will notice, never a silent
		// widening.
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         append([]string(nil), PermittedDNSDomains...),
		PermittedIPRanges:           PermittedIPRanges(),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("lanca: create root: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("lanca: reparse root: %w", err)
	}
	return &Root{Cert: cert, Key: key, DER: der}, nil
}

// LoadRoot rebuilds a Root from its PEM certificate and PEM private key.
func LoadRoot(certPEM, keyPEM []byte) (*Root, error) {
	cblock, _ := pem.Decode(certPEM)
	if cblock == nil || cblock.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("lanca: root cert PEM is not a CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(cblock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("lanca: parse root cert: %w", err)
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("lanca: root cert is not a CA certificate")
	}

	kblock, _ := pem.Decode(keyPEM)
	if kblock == nil {
		return nil, fmt.Errorf("lanca: root key PEM is not decodable")
	}
	var signer crypto.Signer
	switch kblock.Type {
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(kblock.Bytes)
		if err != nil {
			return nil, fmt.Errorf("lanca: parse root EC key: %w", err)
		}
		signer = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(kblock.Bytes)
		if err != nil {
			return nil, fmt.Errorf("lanca: parse root PKCS8 key: %w", err)
		}
		s, ok := k.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("lanca: root key of type %T cannot sign", k)
		}
		signer = s
	default:
		return nil, fmt.Errorf("lanca: unsupported root key PEM type %q", kblock.Type)
	}
	return &Root{Cert: cert, Key: signer, DER: cblock.Bytes}, nil
}

// CertPEM returns the root certificate as PEM. This is the ONLY file that ever
// leaves the operator's machine for a device: it is public by construction and
// carries no secret.
func (r *Root) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: r.DER})
}

// LeafRequest describes one server certificate to issue.
//
// THE SAN SET IS AN INPUT, NEVER A CONSTANT. A hardcoded
// []string{"vulos.local", <something>} is a guess about what the box
// advertises, and it stops being true the moment avahi renames a colliding
// host to `vulos-2.local` or the owner sets VULOS_HOSTNAME. The names here must
// be DERIVED from what the box actually advertises and handed in; this package
// deliberately provides no default set for that reason.
type LeafRequest struct {
	// DNSNames and IPs become the leaf's subjectAltName. At least one DNS name
	// is required (the Common Name is taken from DNSNames[0], see below).
	DNSNames []string
	IPs      []net.IP

	// PublicKey is the BOX's public key — the key whose SPKI native clients
	// have already pinned. Issuing over this key rather than a freshly
	// generated one is what makes re-issuance invisible to a paired client.
	// Use [Root.IssueFromCSR] to take this from a CSR instead.
	PublicKey crypto.PublicKey

	// TTL overrides [DefaultLeafTTL] when non-zero.
	TTL time.Duration

	// now overrides the clock, for tests.
	now time.Time
}

// Issue signs a server leaf for req. The returned PEM is the leaf ALONE; the
// caller is responsible for deciding whether to append the root (a server
// generally should not send a self-signed root it expects the client to
// already have installed).
//
// Every name in req is checked against this CA's constraints BEFORE signing.
// See [CheckDNSName] for why a tool that could issue an unusable leaf but
// doesn't is worth the duplicated logic.
func (r *Root) Issue(req LeafRequest) (certPEM []byte, leaf *x509.Certificate, err error) {
	if len(req.DNSNames) == 0 {
		return nil, nil, fmt.Errorf("lanca: LeafRequest needs at least one DNS name")
	}
	if req.PublicKey == nil {
		return nil, nil, fmt.Errorf("lanca: LeafRequest needs the box's public key")
	}

	dns := dedupeLower(req.DNSNames)
	for _, n := range dns {
		if err := CheckDNSName(n); err != nil {
			return nil, nil, err
		}
	}
	for _, ip := range req.IPs {
		if err := CheckIP(ip); err != nil {
			return nil, nil, err
		}
	}

	now := req.now
	if now.IsZero() {
		now = time.Now()
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = DefaultLeafTTL
	}
	notAfter := now.Add(ttl)
	// A leaf must never outlive the root that vouches for it; a chain where it
	// does fails at the root's expiry anyway, but with a confusing error.
	if notAfter.After(r.Cert.NotAfter) {
		notAfter = r.Cert.NotAfter
	}

	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		// The Common Name is set to the FIRST SAN, not to a human label.
		// Reason: several verifiers (historically NSS, and some platform
		// verifiers still) apply dNSName constraints to a Common Name that
		// parses as a hostname. A CN of "Vulos LAN" is not a hostname and is
		// usually skipped — but "usually" is the word that turns into a
		// support ticket. A CN that is itself inside the permitted subtree is
		// correct under every reading.
		Subject: pkix.Name{
			CommonName:   dns[0],
			Organization: []string{"Vulos LAN (owner-operated)"},
		},
		NotBefore: now.Add(-backdate),
		NotAfter:  notAfter,

		BasicConstraintsValid: true,
		IsCA:                  false,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},

		DNSNames:    dns,
		IPAddresses: append([]net.IP(nil), req.IPs...),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, r.Cert, req.PublicKey, r.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("lanca: sign leaf: %w", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("lanca: reparse leaf: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), parsed, nil
}

// IssueFromCSR is the production issuance path: the box generates nothing new,
// signs a CSR with the key it ALREADY has (the one whose SPKI is pinned and
// persisted by the OS), and the CA signs over that same public key.
//
// This is what makes re-issuance invisible to a paired native client. It also
// means the CA never sees, transports, or stores a box private key — the
// operator tool cannot leak what it never had.
//
// The CSR's own SAN extension is IGNORED. Only ns is used. A CSR is
// self-signed by the requester, so anything inside it is attacker-chosen the
// moment the requesting box is compromised; the names must come from the
// operator's view of what should be issued, not from the requester's claim.
// The CSR is used for exactly two things: proving possession of the private
// key, and carrying the public key.
func (r *Root) IssueFromCSR(csrPEM []byte, ns NameSet, ttl time.Duration) (certPEM []byte, leaf *x509.Certificate, err error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, nil, fmt.Errorf("lanca: input is not a PEM CERTIFICATE REQUEST block")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("lanca: parse CSR: %w", err)
	}
	// Proof of possession. Without this check the CA would happily sign over a
	// public key the requester does not control, which is a free way to get a
	// certificate minted for someone else's key.
	if err := csr.CheckSignature(); err != nil {
		return nil, nil, fmt.Errorf("lanca: CSR signature does not verify: %w", err)
	}
	return r.Issue(LeafRequest{
		DNSNames:  ns.DNSNames,
		IPs:       ns.IPs,
		PublicKey: csr.PublicKey,
		TTL:       ttl,
	})
}

// NameSet is the set of names a box actually advertises: the single source of
// truth the certificate's SANs are derived from.
//
// It exists as a named type rather than two loose slices so that the derivation
// has somewhere to live and so a caller cannot silently pass the arguments in
// the wrong order. The two-box collision (every box shipping hostname `vulos`,
// avahi renaming the loser to `vulos-2.local`, and that name not being in the
// certificate) is exactly what happens when this set is guessed instead of
// observed.
type NameSet struct {
	DNSNames []string
	IPs      []net.IP
}

// NewNameSet normalises and validates an observed name set. It lowercases and
// de-duplicates DNS names, drops the trailing dot an mDNS responder may carry,
// and refuses anything this CA could not issue for — so a caller finds out at
// the point of derivation, not at the point a browser shows an error.
func NewNameSet(dnsNames []string, ips []net.IP) (NameSet, error) {
	dns := dedupeLower(dnsNames)
	if len(dns) == 0 {
		return NameSet{}, fmt.Errorf("lanca: name set is empty — the certificate's SANs must be derived from the names the box advertises, and the box advertised none")
	}
	for _, n := range dns {
		if err := CheckDNSName(n); err != nil {
			return NameSet{}, err
		}
	}
	out := make([]net.IP, 0, len(ips))
	seen := map[string]bool{}
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		if err := CheckIP(ip); err != nil {
			return NameSet{}, err
		}
		if k := ip.String(); !seen[k] {
			seen[k] = true
			out = append(out, ip)
		}
	}
	return NameSet{DNSNames: dns, IPs: out}, nil
}

// dedupeLower lowercases, trims, drops empties and trailing dots, and removes
// duplicates while keeping first-seen order (so DNSNames[0] — which becomes the
// Common Name — stays the caller's intended primary name).
func dedupeLower(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// SortedPermittedCIDRs returns the permitted IP CIDRs as text, sorted, for
// display in a tool's output.
func SortedPermittedCIDRs() []string {
	out := append([]string(nil), permittedIPCIDRs...)
	sort.Strings(out)
	return out
}

func newSerial() (*big.Int, error) {
	// 128 random bits: comfortably above the 64-bit entropy floor the Baseline
	// Requirements set, and this CA has no serial registry to deduplicate
	// against.
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("lanca: serial: %w", err)
	}
	return serial, nil
}
