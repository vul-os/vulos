package main

// lan_pairing.go — PAIR-01: box-side LAN certificate pairing surfaces.
//
// Not to be confused with routes_pairing.go's /api/pairing/* (add-a-device
// account pairing via device keys) — this file is about pinning the box's
// LAN TLS certificate for native clients (clients/core/), a different
// mechanism with a different trust anchor. Route path (/api/lan/pairing) and
// Go identifiers here are namespaced accordingly.
//
// Native clients (clients/core/) pin a box's TLS certificate by its SPKI
// SHA-256 digest on first pairing, but until now there was no way for a user
// to actually learn that fingerprint — so the pin was unusable end to end.
// This file gives an operator two ways to get it:
//
//   - `vulos -print-pairing` (see main.go's flag wiring): a one-shot CLI
//     print, for a user at the console or over SSH.
//   - GET /api/lan/pairing (registerLANPairingRoutes, below): the same payload
//     over HTTP, for the in-browser pairing flow. Session-gated like any
//     other authenticated OS route (see registerLANPairingRoutes for why).
//
// Both paths share buildPairingInfo so they can never drift.

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"

	"vulos/backend/internal/config"
	"vulos/backend/internal/lan"
)

// lanServiceRef is a race-free holder for the LAN service, plus the two
// callbacks the LAN certificate source reads its SANs from.
//
// WHY A HOLDER. The certificate source is built BEFORE lan.New runs (it is an
// input to it), but the authoritative name set and LAN IP live ON the service.
// The callbacks fire from inside the TLS handshake, on arbitrary goroutines, so
// reading a bare package-level *lan.Service would be a data race. This holder
// takes the lock and falls back to the config-derived name set for handshakes
// that arrive before the service is up.
type lanServiceRef struct {
	instanceID string
	hostname   string

	mu  sync.RWMutex
	svc *lan.Service
}

func newLANServiceRef(instanceID, hostname string) *lanServiceRef {
	return &lanServiceRef{instanceID: instanceID, hostname: hostname}
}

func (r *lanServiceRef) set(s *lan.Service) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.svc = s
}

func (r *lanServiceRef) get() *lan.Service {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.svc
}

// names returns the box's CURRENT name set — from the running service when it
// exists (so a live rename is reflected immediately), otherwise derived from
// the startup config.
func (r *lanServiceRef) names() lan.NameSet {
	if s := r.get(); s != nil {
		return s.Names()
	}
	return lan.NewNameSet(r.instanceID, r.hostname)
}

// certDNSNames is the DNS SAN callback for the LAN certificate source.
func (r *lanServiceRef) certDNSNames() []string { return r.names().DNSNames }

// certIPs is the IP SAN callback for the LAN certificate source.
//
// The box's LAN IP has to be a SAN because https://<lan-ip> is the ONLY
// address that works on a client which cannot resolve .local — Chrome on
// Android is the case that forced this (see roadmap/LAN-NAME-RESOLUTION.md).
// Without it that fallback raises a NAME MISMATCH on top of the unknown-issuer
// warning: two errors where there should be one, and on some Chrome builds the
// mismatch is the harder one to click past.
//
// 127.0.0.1 is included so the same certificate also covers a loopback visit
// from the box's own kiosk browser.
func (r *lanServiceRef) certIPs() []net.IP {
	ips := []net.IP{net.IPv4(127, 0, 0, 1)}
	if ip := lan.DetectLANIP(); ip != nil && !ip.IsLoopback() {
		ips = append(ips, ip)
	}
	return ips
}

// lanPairingCertSource builds the CertSource used to fingerprint this box for
// pairing purposes. It resolves the LAN cert/key paths EXACTLY the way the
// VULOS_LAN_ENABLE block in main() does (DefaultCertPath/DefaultKeyPath,
// overridable via VULOS_LAN_CERT/VULOS_LAN_KEY) so the fingerprint shown to a
// user always matches the certificate the LAN HTTPS listener actually serves
// — whether that is the persisted self-signed fallback (certsource.go) or an
// externally issued cert dropped at those paths (LANCERT-01).
//
// Constructing an independent CertSource here (rather than sharing the one
// the LAN listener itself uses) is safe and deliberate: when no external cert
// is present, SelfSignedCertSource always loads the SAME on-disk key
// (certsource.go's loadOrCreateKey), so two independent instances derive the
// identical SPKI even though each mints its own certificate DER. This also
// means -print-pairing and /api/lan/pairing work even when VULOS_LAN_ENABLE
// is unset — the persisted key (and thus the pin) exists independently of
// whether the LAN HTTPS listener is currently running.
func lanPairingCertSource(instanceID string) lan.CertSource {
	certPath, keyPath := lan.DefaultCertPath, lan.DefaultKeyPath
	if v := os.Getenv("VULOS_LAN_CERT"); v != "" {
		certPath = v
	}
	if v := os.Getenv("VULOS_LAN_KEY"); v != "" {
		keyPath = v
	}
	// Same derivation as the VULOS_LAN_ENABLE block in main(): lan.NewNameSet
	// is the single source of truth for the box's names, so the fingerprint
	// printed here always belongs to a certificate carrying the same SANs the
	// LAN listener serves. Hostname is read from the environment the same way
	// config does, because -print-pairing can run without a loaded Config.
	hostname := os.Getenv("VULOS_HOSTNAME")
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	ref := newLANServiceRef(instanceID, hostname)
	return lan.LoadDynamicCertSource(certPath, keyPath, ref.certDNSNames, ref.certIPs)
}

// lanPairingHTTPSAddr resolves the LAN HTTPS listener's configured address
// the same way the VULOS_LAN_ENABLE block does, so PairingAddr's port matches
// whatever the box is actually (or would actually be) listening on.
func lanPairingHTTPSAddr() string {
	if v := os.Getenv("VULOS_LAN_HTTPS_ADDR"); v != "" {
		return v
	}
	return ":443"
}

// pairingInfo bundles everything a user needs to pair a native client against
// this box.
type pairingInfo struct {
	Name    string `json:"name"`     // box name shown to the user (VULOS_HOSTNAME)
	Addr    string `json:"addr"`     // host:port a client should dial
	SPKI    string `json:"spki"`     // base64 SHA-256 of the SPKI — the pin value itself
	SPKIHex string `json:"spki_hex"` // human-readable colon-hex form, for reading aloud
	Payload string `json:"payload"`  // full vulos://pair?... URI (QR code contents)
}

// buildPairingInfo computes the pairing payload for cfg's box identity using
// certSrc's current certificate. Both -print-pairing and
// GET /api/lan/pairing call this so they can never disagree.
func buildPairingInfo(cfg *config.Config, certSrc lan.CertSource) (pairingInfo, error) {
	addr := lan.PairingAddr(lanPairingHTTPSAddr())

	spkiB64, err := lan.SPKIFingerprintBase64(certSrc)
	if err != nil {
		return pairingInfo{}, fmt.Errorf("compute SPKI fingerprint: %w", err)
	}
	spkiHex, err := lan.SPKIFingerprintHex(certSrc)
	if err != nil {
		return pairingInfo{}, fmt.Errorf("compute SPKI fingerprint (hex): %w", err)
	}

	return pairingInfo{
		Name:    cfg.Hostname,
		Addr:    addr,
		SPKI:    spkiB64,
		SPKIHex: spkiHex,
		Payload: lan.BuildPairingURI(cfg.Hostname, addr, spkiB64),
	}, nil
}

// runPrintPairingCLI implements `vulos -print-pairing`: it prints the box's
// LAN pairing payload to stdout and returns a process exit code. Meant to be
// run by an operator at the console or over SSH — there is no other way for a
// user to learn the fingerprint a native client needs to pin.
func runPrintPairingCLI(cfg *config.Config) int {
	certSrc := lanPairingCertSource(cfg.InstanceID)
	info, err := buildPairingInfo(cfg, certSrc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "print-pairing: %v\n", err)
		return 1
	}
	fmt.Printf("Box name:      %s\n", info.Name)
	fmt.Printf("LAN address:   %s\n", info.Addr)
	fmt.Printf("Fingerprint:   %s\n", info.SPKIHex)
	fmt.Printf("Pairing code:\n%s\n", info.Payload)
	return 0
}

// registerLANPairingRoutes exposes buildPairingInfo over HTTP for the in-browser
// pairing flow (a QR code / copyable code rendered by the OS shell's Settings
// UI).
//
// SECURITY: this route is deliberately NOT added to auth's publicPaths
// (services/auth/handlers.go's exhaustive allow-list, enforced by
// backend/services/security_test.go's SEC-HARD-08), so the OS session
// middleware gates it exactly like any other authenticated route — a request
// with no valid session / vk_ bearer / AT10 admin bearer gets 401 before this
// handler ever runs. The SPKI fingerprint itself is not secret (any TLS
// client on the LAN observes it in the handshake), but this endpoint also
// discloses the box's name and internal LAN address, and an unauthenticated
// pairing surface would let an attacker get a victim's client to pin THEIR
// box instead of the real one — so it stays behind the same session gate as
// every other non-public OS API route.
func registerLANPairingRoutes(mux *http.ServeMux, cfg *config.Config, certSrc lan.CertSource) {
	mux.HandleFunc("GET /api/lan/pairing", func(w http.ResponseWriter, r *http.Request) {
		info, err := buildPairingInfo(cfg, certSrc)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, info)
	})
}
