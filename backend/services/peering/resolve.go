// resolve.go — the single peer-reachability seam (CONSOLIDATION B-0).
//
// resolvePeerBaseURL is the one place that turns a peer's identity + last-known
// server address into the base URL that PeerClient.Post delivers to. It is the
// consolidation seam described in docs/CONSOLIDATION.md §B-0: introduced as a
// behavioral NO-OP (it returns exactly today's "https://<contact.Server>") so it
// can later be flipped to prefer a verified-direct endpoint, then the peer's
// relay-tunnel URL, then fall back to contact.Server — WITHOUT touching any live
// delivery call site again.
//
// It replaces PEER-40's dormant in-memory multi-endpoint registry as the single
// reachability primitive for box→box (server-to-server) delivery. There is no
// second race-delivery path: durability is the outbox (outbox.go), exactly-once
// is receiver-side idempotency (idempotency.go), and the transport is a single
// signed PeerClient.Post to the resolved base URL.
package peering

import "strings"

// resolvePeerBaseURL returns the base URL to deliver an envelope to the peer
// identified by toVulaID, whose last-known server address is server (the value
// stored in contact.Server / outbox item.PeerServer).
//
// B-0 (this revision) is a PASS-THROUGH: it returns "https://<server>", exactly
// what the call sites computed inline before. toVulaID is accepted now so the
// signature is stable when B-2 flips the body to consult verified-direct + the
// relay tunnel by identity; today it is unused.
//
// Contract:
//   - server is a bare "host" or "host:port" (no scheme); the result carries the
//     https:// scheme, matching the pre-B-0 inline "https://" + contact.Server.
//   - a server that already includes a scheme is returned unchanged (defensive;
//     the live call sites never pass one).
//   - an empty server yields an empty string (the caller already guards this;
//     e.g. outbox.go skips items with an empty PeerServer).
func resolvePeerBaseURL(toVulaID, server string) string {
	_ = toVulaID // reserved for B-2 (verified-direct → relay-tunnel resolution)
	if server == "" {
		return ""
	}
	if strings.Contains(server, "://") {
		return server
	}
	return "https://" + server
}
