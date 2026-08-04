# Vulos native clients

Installable clients for the machines people actually use, talking to a box they
own. This directory was `mobile/` until it stopped being only about phones.

| Directory | Platform | Status |
|---|---|---|
| [`core/`](core/) | shared Go security core | **scaffold** — API defined, nothing implemented |
| [`android/`](android/) | Android (Kotlin, WebView shell) | shipping |
| `desktop/` | Windows, macOS, Linux (Wails) | not started |
| `ios/` | iOS (Swift shell) | not started |

## Why a shared core

Every client needs the same three things: find a box, decide whether to trust
it, and talk to it over an authenticated channel. Writing that once per platform
would guarantee the three copies drift — and divergent duplicate implementations
are this project's recurring defect class.

So the security-critical logic lives once, in Go, in [`core/`](core/), and each
shell consumes it: the desktop shell imports it directly, Android via a
gomobile `.aar`, iOS via a gomobile `.xcframework`. The UI in every shell is the
same React shell the browser serves, so the interface doesn't fork either.

Tailscale uses this shape for the same reason.

## Why native clients at all

A browser cannot be made to trust a box on your LAN without either a public
certificate authority (which needs public DNS and an external issuer — the
dependency Vulos exists to avoid) or a click-through warning on every device.

Worse, a browser on `http://192.168.1.50` is not a *secure context*, so
`crypto.subtle` is undefined and the security-critical modules in `src/lib/`
cannot run at all. See `src/lib/secureContext.js`.

A native client sidesteps both problems: it owns its TLS stack, so it can pin
the box's key directly — no CA, no DNS, no warning — and its webview is a
secure context regardless of how it reaches the box.

## Trust model

The box presents a stable self-signed certificate. The client pins its public
key at first pairing, confirmed out of band by scanning a QR code the box
displays. Every later connection is authenticated against that pin.

This is stronger than public-CA TLS rather than a substitute for it: it removes
roughly 150 root authorities from the trust path, so no CA can mint a
certificate for your box. It also works on a network with no internet at all.

Full rationale in [`core/doc.go`](core/doc.go).
