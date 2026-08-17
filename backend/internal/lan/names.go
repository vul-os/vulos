package lan

import (
	"fmt"
	"strings"
)

// names.go — THE single source of truth for every name a box answers to.
//
// WHY THIS FILE EXISTS (measured 2026-08-17).
//
// Before it, the mDNS name and the certificate's SAN list were two independent
// hard-coded literals:
//
//	internal/lan/lan.go:  const mdnsHostname = "vulos.local"
//	cmd/server/main.go:   lan.LoadCertSource(..., []string{"vulos.local", lanHost}, nil)
//	cmd/server/lan_pairing.go:58: the same literal again
//
// Three declarations of the same fact is the suite's dominant defect shape, and
// it produced three separate live defects:
//
//  1. TWO BOXES COLLIDE. Every box ships /etc/hostname = "vulos" and every box
//     advertised the identical "vulos.local". pion/mdns does no RFC 6762 §8
//     probing at all, so BOTH boxes answer and the client takes whichever reply
//     arrives first. Measured with two containers running this package's real
//     advertiser on one link, ten lookups from a third host:
//
//     172.31.99.6 / .5 / .5 / .5 / .5 / .5 / .5 / .5 / .6 / .5
//
//     Both certs carry vulos.local, so TLS succeeds either way and the user is
//     never told they reached the wrong box. Under the standing "every instance
//     is almost a direct clone" directive the two boxes then look identical.
//     On the bare-metal path avahi-daemon runs too and DOES probe — measured:
//     "Host name conflict, retrying with vulos-2" — and vulos-2.local was in
//     nobody's SAN list, so that path gives a name-mismatch TLS error instead.
//
//  2. RENAMING THE BOX BROKE TLS. POST /api/identity/hostname lets an owner
//     choose a name, but the SAN list was a literal "vulos.local", so the moment
//     the chosen name took effect the box answered to a name its own certificate
//     never mentioned.
//
//  3. NO IP SANs AT ALL. LoadCertSource was called with a nil IP list, so the
//     universal no-mDNS fallback — typing https://192.168.1.50 — raised a NAME
//     MISMATCH on top of the unknown-issuer warning: two errors where there
//     should be one.
//
// Everything downstream now derives from NewNameSet. Adding a name in one place
// adds it to the advertisement, the certificate and the pairing payload at once,
// and they cannot disagree.

// mdnsSuffix is the multicast-DNS domain. Only names under it are advertised
// over mDNS.
const mdnsSuffix = ".local"

// routerSuffixes are the non-.local domains a home router commonly appends to a
// DHCP client's option-12 hostname, so that a device with the router's search
// domain configured reaches the box by its short name.
//
// ".lan" is OpenWrt's historical default local domain and the most widespread
// vendor choice. ".home.arpa" is the RFC 8375 reserved name for residential
// networks and what newer OpenWrt/OpenThread deployments use.
//
// These cost nothing but SAN bytes on a self-signed leaf and they turn the
// router-DHCP path (see roadmap/LAN-NAME-RESOLUTION.md) into one warning
// instead of two. They are NOT advertised by us — the router publishes them or
// nobody does. We only make sure the certificate does not become the reason
// that path fails. Which routers actually do this is UNVERIFIED here; see the
// roadmap doc.
var routerSuffixes = []string{".lan", ".home.arpa"}

// GenericHostname is the zero-config name a single box on a LAN answers to.
// It is deliberately memorable and deliberately NOT unique — see NameSet.MDNS
// for how a second box on the same link avoids claiming it.
const GenericHostname = "vulos"

// shortIDLen is how many trailing ULID characters form the per-box suffix.
// The instance id is a 26-char ULID whose last 16 characters are the random
// half (internal/ulid), so six trailing characters are six random Crockford
// base32 symbols: 32^6 ≈ 1.07e9 combinations. That is overwhelming for the
// handful of boxes on one household LAN, and short enough that an owner can
// read "vulos-k3n7q2" off a screen and type it into a browser.
const shortIDLen = 6

// ShortID renders the per-box suffix used in the default hostname. The ULID's
// Crockford alphabet ("0123456789ABCDEFGHJKMNPQRSTVWXYZ") is a strict subset of
// the RFC-1123 label alphabet once lower-cased, so the result is always a legal
// DNS label fragment. A short or empty id is padded rather than rejected so the
// name is always well-formed.
func ShortID(instanceID string) string {
	id := strings.ToLower(strings.TrimSpace(instanceID))
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	clean := b.String()
	if len(clean) > shortIDLen {
		clean = clean[len(clean)-shortIDLen:]
	}
	for len(clean) < shortIDLen {
		clean += "0"
	}
	return clean
}

// DefaultHostname is the name a box uses when its owner has not chosen one.
//
// It is per-instance ON PURPOSE. The install wizard prefills this field, and an
// owner who simply clicks Next must not end up colliding with a box already on
// the link — which is exactly what happened while every box defaulted to the
// bare "vulos".
func DefaultHostname(instanceID string) string {
	return GenericHostname + "-" + ShortID(instanceID)
}

// SanitizeHostname normalises an owner-supplied box name to a single RFC-1123
// DNS label, or returns "" if it cannot be one.
//
// It accepts exactly what POST /api/identity/hostname's hostnameRE accepts
// after lower-casing and whitespace trimming, so the API, the advertisement and
// the certificate agree on what a name IS. Note it does NOT invent a name from
// garbage: an unusable input returns "" and the caller decides the fallback.
func SanitizeHostname(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// A caller may hand us an FQDN-ish value (e.g. an /etc/hostname holding
	// "vulos.local"); take the first label.
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	if !ValidHostnameLabel(s) {
		return ""
	}
	return s
}

// ValidHostnameLabel reports whether s is a legal RFC-1123 hostname label:
// 1–63 characters of [a-z0-9-], not starting or ending with a hyphen.
//
// Deliberately duplicated from cmd/init's validHostnameLabel rather than
// shared: cmd/init is a //go:build linux PID-1 binary that must not grow an
// import of the server's LAN package. TestValidHostnameLabelMatchesInit pins
// the two to the same truth table so they cannot drift.
func ValidHostnameLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-':
			if i == 0 || i == len(s)-1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// NameSet is every name one box answers to, derived once from its instance id
// and its owner-chosen hostname.
type NameSet struct {
	// Hostname is the box's resolved short name — the owner's choice when it is
	// usable, otherwise DefaultHostname(instanceID). Never empty, always a
	// legal DNS label.
	Hostname string

	// Unique is the per-instance name, always present even when the owner has
	// chosen something else. It is the name that is guaranteed not to collide
	// with a sibling box, so it is what an operator falls back to when two
	// boxes on one LAN want the same friendly name.
	Unique string

	// MDNS lists the ".local" names to advertise, most specific first.
	//
	// The generic "vulos.local" is LAST because it is the one that can already
	// be taken: Service.Start probes for it and drops it from the claim when a
	// sibling box answers, so first box on the link keeps the friendly name and
	// the second keeps a working unique one instead of both answering and
	// coin-flipping.
	MDNS []string

	// DNSNames is the certificate's DNS SAN list. It is a SUPERSET of MDNS: it
	// also carries the bare short names and the router-domain forms, and it
	// carries "vulos.local" whether or not this box won the generic claim, so
	// winning or losing that race never changes the certificate.
	DNSNames []string
}

// NewNameSet derives the complete name set for a box.
//
// hostname is the owner's chosen name (config.Hostname / VULOS_HOSTNAME /
// the identity store). Anything unusable — empty, whitespace, the stray
// "vulos\n" that cmd/init used to install, or a name that is not a DNS label —
// falls back to DefaultHostname(instanceID) rather than being propagated into
// certificates and mDNS records.
func NewNameSet(instanceID, hostname string) NameSet {
	unique := DefaultHostname(instanceID)

	name := SanitizeHostname(hostname)
	if name == "" {
		name = unique
	}

	ns := NameSet{Hostname: name, Unique: unique}

	// Base short names, most specific first, de-duplicated. The owner's name
	// leads; the per-instance name is always present as the collision-free
	// fallback; the generic name is last for the reason documented on MDNS.
	bases := dedupe([]string{name, unique, GenericHostname})

	for _, b := range bases {
		ns.MDNS = append(ns.MDNS, b+mdnsSuffix)
	}

	for _, b := range bases {
		ns.DNSNames = append(ns.DNSNames, b)
		ns.DNSNames = append(ns.DNSNames, b+mdnsSuffix)
		for _, suf := range routerSuffixes {
			ns.DNSNames = append(ns.DNSNames, b+suf)
		}
	}
	// box.<id>.lan.vulos.org — answered by the box's own :53 responder
	// (internal/lan/dns.go) and by the cloud's public DNS when it is reachable.
	ns.DNSNames = append(ns.DNSNames, BoxHostname(instanceID))

	return ns
}

// dedupe returns xs with empty strings and later duplicates removed, order
// preserved.
func dedupe(xs []string) []string {
	seen := make(map[string]struct{}, len(xs))
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if x == "" {
			continue
		}
		if _, ok := seen[x]; ok {
			continue
		}
		seen[x] = struct{}{}
		out = append(out, x)
	}
	return out
}

// String renders the name set for logs.
func (n NameSet) String() string {
	return fmt.Sprintf("hostname=%s unique=%s mdns=%v sans=%d", n.Hostname, n.Unique, n.MDNS, len(n.DNSNames))
}
