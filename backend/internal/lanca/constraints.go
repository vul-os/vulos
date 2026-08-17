// Package lanca implements the OPERATOR-SIDE certificate authority that issues
// the browser-trusted LAN certificate a Vulos box serves.
//
// # Why this package exists at all
//
// The founder wants three things at once on a box's LAN address: transport
// encryption, operation with the internet down, and a green padlock in an
// UNMODIFIED browser (including Chrome on Android). Only two of those are free.
//
// No publicly trusted CA can ever issue a certificate for `vulos.local`, for a
// bare RFC1918 address, or for any other name that is not globally unique in
// the public DNS. `.local` is reserved by RFC 6762 §3, and the CA/Browser Forum
// prohibited "Internal Names" and "Reserved IP Addresses" outright — the
// Forum's own Internal Names guidance states that "the issuance of certificates
// with a reserved IP address or internal server name is prohibited" as of
// 2015-11-11, and that "On October 1, 2016, all publicly trusted SSL/TLS
// certificates with an internal name or reserved IP address will be revoked
// and/or blocked by browser software". An Internal Name is defined there as a
// name that "cannot be verified as globally unique within the public DNS ...
// because it does not end with a Top Level Domain registered in IANA's Root
// Zone Database" — `.local` is exactly that, and is additionally reserved.
//
// So a padlock on `vulos.local`, offline, is reachable by exactly one route: a
// CA the devices trust because the OWNER installed it. That is the accepted
// cost (one root certificate installed once per device), and this package's
// entire job is to make that root as small a concession as it can possibly be.
//
// # The two properties that make the concession small
//
//  1. THE CA PRIVATE KEY DOES NOT LIVE ON THE BOX. This package is a library
//     for an operator-run tool ([vulos-lanca]) and/or a control plane. A CA
//     sitting on the machine it certifies buys nothing: whoever owns the box
//     owns the CA. Issuance happens elsewhere; the box only ever receives a
//     signed leaf. See [Root.IssueFromCSR] — the box keeps its private key and
//     sends only a CSR, so the CA never holds box key material either.
//
//  2. THE ROOT IS NAME-CONSTRAINED. X.509 permittedSubtrees (RFC 5280 §4.2.1.10)
//     limit this root to `.local`, `lan.vulos.org`, and private/link-local IP
//     space. A stolen CA key therefore cannot mint a working certificate for
//     `google.com` on any verifier that enforces the extension. This is what
//     separates "one root the owner controls" from "~150 public roots" — the
//     public roots are unconstrained by construction.
//
// Property 2 is a claim about OTHER PEOPLE'S CODE, and a name constraint that
// is silently ignored is worse than none, because it invites a false sense of
// safety. See constraints_test.go for which verifiers are actually measured
// here and doc comments on [PermittedDNSDomains] for which are not.
package lanca

import (
	"fmt"
	"net"
	"strings"
)

// PermittedDNSDomains is the dNSName permittedSubtrees set stamped into the
// root. A verifier that enforces RFC 5280 §4.2.1.10 will reject any chain
// through this root whose leaf carries a dNSName outside these subtrees.
//
// Encoding note (portability, deliberate): the entries carry NO leading dot.
// Go's crypto/x509 treats a leading dot as "strict subdomains only" (so
// ".local" would NOT permit the bare name "local"), while RFC 5280 has no
// leading-dot form at all and OpenSSL accepts both spellings with different
// meanings. The dotless spelling means the same thing — "this name and
// everything under it" — in Go, OpenSSL and NSS alike, so it is the spelling
// that travels.
//
// `local` covers `vulos.local` and every avahi-renamed variant (`vulos-2.local`
// and friends), which matters because the SAN set is DERIVED from the names the
// box actually advertises rather than hardcoded — see [LeafRequest].
var PermittedDNSDomains = []string{
	"local",
	"lan.vulos.org",
}

// permittedIPCIDRs is the iPAddress permittedSubtrees set, as CIDR text.
// Parsed once by [PermittedIPRanges].
//
// v4: RFC 1918 private space plus RFC 3927 link-local (169.254/16), which is
// what a box lands on when there is no DHCP server — precisely the offline
// case this whole design exists to serve.
//
// v6: RFC 4193 unique-local (fc00::/7) and RFC 4291 link-local (fe80::/10).
// These are included because a name-constrained iPAddress subtree set is
// CLOSED: crypto/x509 and OpenSSL both reject any IP SAN that matches no
// permitted range, and they do not keep separate v4/v6 pools. Omitting the v6
// ranges would therefore not be "leaving v6 unconstrained", it would be
// "refusing to issue for v6 at all" — a silent breakage on a v6-only link.
//
// Every range here is unroutable on the public internet, which is the property
// that matters: a stolen key cannot mint a certificate for any address a victim
// could be MITM'd at from outside their own LAN.
var permittedIPCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
	"fc00::/7",
	"fe80::/10",
}

// PermittedIPRanges parses [permittedIPCIDRs] into the form crypto/x509 wants
// for x509.Certificate.PermittedIPRanges. It returns a fresh slice on every
// call so a caller cannot mutate the package's notion of what is permitted.
func PermittedIPRanges() []*net.IPNet {
	out := make([]*net.IPNet, 0, len(permittedIPCIDRs))
	for _, c := range permittedIPCIDRs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			// Unreachable: the literals above are constant and tested.
			panic(fmt.Sprintf("lanca: bad permitted CIDR %q: %v", c, err))
		}
		out = append(out, n)
	}
	return out
}

// CheckDNSName reports whether name falls inside [PermittedDNSDomains].
//
// This duplicates, on the ISSUING side, a check the verifier will also make.
// That duplication is the point: a leaf issued outside the constraint is not a
// security hole (every enforcing verifier rejects it) but it IS a silent
// outage, and an outage discovered by a user staring at a browser error is far
// worse than a tool that refuses up front and says why.
func CheckDNSName(name string) error {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimSuffix(n, ".")
	if n == "" {
		return fmt.Errorf("lanca: empty DNS name")
	}
	if strings.ContainsAny(n, " \t/:") {
		return fmt.Errorf("lanca: DNS name %q contains invalid characters", name)
	}
	// A wildcard leaf under a permitted subtree is still inside the subtree,
	// but this CA has no reason to mint one and a wildcard widens blast radius
	// for free. Refuse.
	if strings.Contains(n, "*") {
		return fmt.Errorf("lanca: wildcard DNS name %q is not issuable by this CA", name)
	}
	for _, d := range PermittedDNSDomains {
		if n == d || strings.HasSuffix(n, "."+d) {
			return nil
		}
	}
	return fmt.Errorf("lanca: DNS name %q is outside this CA's permitted subtrees %v — "+
		"the root is name-constrained and no verifier that enforces RFC 5280 §4.2.1.10 "+
		"would accept such a leaf", name, PermittedDNSDomains)
}

// CheckIP reports whether ip falls inside [PermittedIPRanges]. Same rationale
// as [CheckDNSName]: refuse at issuance rather than ship a leaf that verifiers
// will reject.
func CheckIP(ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("lanca: nil IP")
	}
	for _, n := range PermittedIPRanges() {
		// Compare in the same family. net.IPNet.Contains already normalises
		// v4-in-v6, but an explicit nil guard keeps a malformed IP from
		// silently matching.
		if n.Contains(ip) {
			return nil
		}
	}
	return fmt.Errorf("lanca: IP %s is outside this CA's permitted ranges %v — "+
		"this CA issues only for private and link-local address space", ip, permittedIPCIDRs)
}
