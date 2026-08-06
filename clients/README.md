# Vulos native clients

Installable clients for the machines people actually use, talking to a box they
own. This directory was `mobile/` until it stopped being only about phones.

| Directory | Platform | Status |
|---|---|---|
| [`core/`](core/) | shared Go security core | implemented as a library — pinning, pairing, discovery, pin store; no shell consumes it yet |
| [`android/`](android/) | Android (Kotlin, WebView shell) | Tier 1 (installable PWA, no code in this directory) shipping; Tier 2 (the Kotlin/Gradle project here) scaffolded and deliberately deferred — see [`android/README.md`](android/README.md) |
| `desktop/` | Windows, macOS, Linux (Wails) | not started |
| `ios/` | iOS (Swift shell) | not started |

## Why a shared core

Every client needs the same three things: find a box, decide whether to trust
it, and talk to it over an authenticated channel. Writing that once per platform
would guarantee the three copies drift — and divergent duplicate implementations
are this project's recurring defect class.

So the security-critical logic lives once, in Go, in [`core/`](core/). The
design is for every shell to consume it: the desktop shell would import it
directly, Android via a gomobile `.aar`, iOS via a gomobile `.xcframework` —
with the same React shell the browser serves as the UI in every case, so the
interface doesn't fork either. Today only `core/` itself exists as a standalone
Go module; no shell in this directory wires into it yet (`android/`'s existing
WebView shell predates it and does not depend on it).

Tailscale uses this shape for the same reason.

## Why native clients at all

A browser cannot be made to trust a box on your LAN without either a public
certificate authority (which needs public DNS and an external issuer — the
dependency Vulos exists to avoid) or a click-through warning on every device.

Worse, a browser on `http://192.168.1.50` is not a *secure context*, so
`crypto.subtle` is undefined and the security-critical modules in `src/lib/`
cannot run at all. See `src/lib/secureContext.ts`.

A native client sidesteps both problems: it owns its TLS stack, so it can pin
the box's key directly — no CA, no DNS, no warning — and its webview is a
secure context regardless of how it reaches the box.

## Trust model

This is the design, implemented as a library today, not yet a pairing flow a
user can run: the box presents a stable self-signed certificate, and a client
pins its public key at first pairing, confirmed out of band by scanning a QR
code the box displays. Every later connection is authenticated against that
pin. `core/` implements the payload format and the pin/verify/store logic
(`EncodePairPayload` / `ParsePairPayload` / `Pair`), but no shell in this
directory calls it yet — there is no camera-scanning UI, and no box-side QR
display, wired to it. Until a shell exists to drive it, this pairing flow does
not run for an actual user.

This is stronger than public-CA TLS rather than a substitute for it: it removes
roughly 150 root authorities from the trust path, so no CA can mint a
certificate for your box. It also works on a network with no internet at all.

Full rationale in [`core/doc.go`](core/doc.go).
