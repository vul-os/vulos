// Package lan provides box-side LAN reachability for a Vulos box: zero-config
// mDNS advertisement (vulos.local), a tiny authoritative DNS responder for
// box.<id>.lan.vulos.org, and an HTTPS listener that serves the OS over the LAN
// using a pluggable certificate source.
//
// # Cross-repo cert contract (FIX-LAN-PATH-CONST-01)
//
// The trusted, no-warning LAN cert is issued by an externally operated
// LAN-cert issuer via ACME DNS-01 against the hostname
// `box.<id>.lan.vulos.org`. The issuer's HTTP contract pins the on-disk
// delivery paths the OS must read from:
//
//	cert: /var/lib/vulos/tls/lan.crt
//	key : /var/lib/vulos/tls/lan.key
//
// Those paths are exported here as [DefaultCertPath] / [DefaultKeyPath] so
// that the OS server bootstrap (backend/cmd/server/main.go) and the LAN-cert
// puller (backend/internal/lan/lancert_puller.go) both reference exactly the
// same constants — no string drift between the writer (puller) and the
// reader ([LoadCertSource]).
//
// [LoadCertSource] mtime-watches these paths and hot-reloads cert+key without
// a listener restart, so a freshly-pulled cert takes effect on the next
// handshake.
package lan
