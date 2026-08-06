// Package core is the shared security core for every Vulos native client.
//
// # Why this package exists
//
// The desktop, Android and iOS clients all need the same three things: find a
// box, decide whether to trust it, and talk to it over an authenticated
// channel. Implementing that three times — once in Kotlin, once in Swift, once
// in whatever the desktop shell is written in — would guarantee they drift.
// Divergent duplicate implementations are this project's recurring defect
// class, so the rule here is that the security-critical logic exists ONCE, in
// Go, and every shell consumes it:
//
//	clients/desktop  — Wails (Go), imports this package directly
//	clients/android  — Kotlin shell, consumes this via gomobile (.aar)
//	clients/ios      — Swift shell, consumes this via gomobile (.xcframework)
//
// This is the same shape Tailscale uses for exactly the same reason.
//
// # The trust model
//
// A box on a home network cannot get a publicly-trusted certificate without
// dragging in public DNS and an ACME issuer — which reintroduces exactly the
// external dependency Vulos exists to avoid. So native clients do not use the
// public CA system at all.
//
// Instead the box presents a STABLE self-signed certificate, and the client
// pins its public key on first pairing (trust on first use):
//
//  1. The client discovers the box on the LAN over mDNS.
//  2. The user pairs once — scanning a QR code shown by the box, which carries
//     the box's public-key fingerprint.
//  3. The client stores that pin.
//  4. Every later connection is authenticated against the pin.
//
// This is STRONGER than public-CA TLS, not a compromise for lacking it: it
// removes roughly 150 root certificate authorities from the trust path, so a
// compromised or coerced CA cannot mint a certificate for someone's box. It
// also needs no DNS, no internet, and no issuer — a box and a phone on an
// isolated network can establish a mutually authenticated channel with nothing
// else present.
//
// The window of exposure is the first pairing, which is why the fingerprint is
// confirmed out of band (a QR code on the box's own screen) rather than
// accepted silently. This is the SSH and Syncthing model.
//
// # Status
//
// Implemented: SPKI fingerprinting, pin-verified TLSConfig/Client, the
// vulos://pair payload codec, Pair (dial-verify-then-store), a FileStore
// reference Store, and Discover.
//
// Discover has a real limitation worth calling out: the box side
// (backend/internal/lan) advertises a fixed, non-per-box mDNS hostname
// ("vulos.local") via a hostname-resolution mDNS server, not a DNS-SD service
// type with PTR/SRV/TXT records. There is nothing to "browse" — Discover
// resolves that one name. On a LAN with more than one Vulos box this can only
// ever find "a" box named vulos.local, not distinguish between several. That
// is a property of the current wire format, not something invented in this
// client.
//
// Any function that still cannot do its job (e.g. a platform Store shim that
// hasn't been ported) should return ErrNotImplemented rather than a zero
// value — a pinning routine that quietly returns "trusted" is far more
// dangerous than one that is obviously absent.
package core
