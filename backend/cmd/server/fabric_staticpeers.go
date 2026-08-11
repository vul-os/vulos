package main

import (
	"net"
	"net/url"
	"strings"
)

// staticPeerIsLocal reports whether a hand-configured VULOS_FABRIC_PEERS entry
// names an address on the operator's own network.
//
// # Why this check exists
//
// A peer from VULOS_FABRIC_PEERS is constructed with WAN=false, so fabric dials
// it with the LAN client: InsecureSkipVerify, no SSRF guard, and the shared
// fabric secret in an X-Fabric-Auth header. That is the right transport for the
// case the setting was added for — two of your own boxes on one wired network
// that mDNS cannot bridge because multicast does not cross a subnet or a VLAN.
//
// It is the wrong transport for a public address. FABRIC-SSRF-01 spells out why
// for rendezvous-resolved peers: reaching an untrusted address with a client
// that skips certificate verification lets a network MITM collect the fabric
// secret. Nothing about that reasoning depends on where the address came from,
// and a hand-typed hostname is not more trustworthy than a relay's answer — it
// is just less obviously untrusted.
//
// Marking such a peer WAN instead is not the fix. A WAN peer with no pinned
// Ed25519 key is SKIPPED (see MULTI-INSTANCE.md "Beyond the LAN"), so the
// operator would get silence — the exact symptom VULOS_FABRIC_PEERS was added to
// end. Refusing loudly tells them something they can act on.
//
// A hostname is treated as NOT local: it cannot be classified without resolving
// it, resolution can change under us, and the safe answer for an address we
// cannot vouch for is the one that does not hand out the secret.
func staticPeerIsLocal(base string) bool {
	raw := strings.TrimSpace(base)
	if raw == "" {
		return false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// A name, not an address — unclassifiable here. Fail closed.
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
