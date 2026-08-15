package cluster

// nodearch.go — ARCH-SYNC: a node advertises its CPU architecture.
//
// See roadmap/ARCH-PLACEMENT.md. The short version: Vulos ships amd64 and arm64
// images, 17 of the 119 catalogued desktop apps are published for x86_64 only,
// and the product promise is that a user's instances are one OS rather than a
// fleet. When an app genuinely cannot be installed on one instance, the OS owes
// the user an explanation naming the instance where it does work. It could not
// produce that sentence, because NodeMeta carried no architecture — or any other
// capability.
//
// Scope discipline: this file adds ADVERTISEMENT only. It deliberately does not
// add scheduling, placement, filtering, or a "best node" query, because none of
// those are buildable today (nothing can run an app on one node and surface it
// on another) and a placement API with no placement behind it is exactly the
// kind of thing this project keeps mistaking for shipped work.

import "runtime"

// nodeArch returns this node's CPU architecture for advertisement to peers.
//
// The value is runtime.GOARCH — the architecture of THIS BINARY, which is the
// only thing that can be known for certain and the only thing that cannot be
// spoofed by configuration. It is never read from the environment, a request
// header, or a peer's claim about us.
//
// ── On spelling ──────────────────────────────────────────────────────────────
//
// Three schemes are in play across the codebase and mixing them silently matches
// nothing: Debian (amd64/arm64), Flatpak and uname (x86_64/aarch64), and Go's
// GOARCH (amd64/arm64). appnet.NormalizeArch is the single place a foreign
// spelling becomes a Vulos one, and every comparison must route through it.
//
// This function does NOT call it, and that is deliberate rather than an
// oversight: `cluster` is imported by cmd/server, joinsync, sync and firstboot,
// and reaching into appnet for a two-entry lookup would couple the cluster
// substrate to the app layer for no benefit. GOARCH already emits the Debian
// spelling for both architectures Vulos publishes, so the value written here is
// directly comparable to RegistryEntry.Arch.
//
// The obligation this creates is on the CONSUMER, and it is stated here so it
// is not forgotten: anything comparing NodeMeta.Arch against registry data must
// pass BOTH sides through appnet.NormalizeArch. GOARCH agreeing with Debian is
// true for amd64 and arm64 and NOT true in general — GOARCH "arm" is Debian
// "armhf", GOARCH "386" is Debian "i386". A future Vulos image on either of
// those would silently stop matching if a consumer compared raw strings.
func nodeArch() string {
	return runtime.GOARCH
}
