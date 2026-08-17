package lan

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// mdnsHostname is the zero-config name a single box on a LAN answers to.
//
// It is NOT the only name advertised any more, and it is no longer claimed
// unconditionally: see names.go for the full derivation and mdns.go for the
// conflict probe that stops two boxes both answering for it.
const mdnsHostname = GenericHostname + mdnsSuffix

// lanDomainSuffix is the suffix the local DNS responder is authoritative for.
// The full name is box.<id>.lan.vulos.org — resolvable even with public DNS
// down because the box answers it directly on the LAN.
const lanDomainSuffix = "lan.vulos.org"

// BoxHostname returns the LAN hostname for a box with the given instance id,
// e.g. box.01h.....lan.vulos.org. The id is lower-cased; an empty id yields a
// stable fallback so the name is always well-formed.
func BoxHostname(instanceID string) string {
	id := strings.ToLower(strings.TrimSpace(instanceID))
	if id == "" {
		id = "unknown"
	}
	return fmt.Sprintf("box.%s.%s", id, lanDomainSuffix)
}

// Config configures the LAN reachability Service.
type Config struct {
	// InstanceID is the box's stable ULID; it forms the box.<id>.lan.vulos.org name.
	InstanceID string

	// Hostname is the owner-chosen box name (config.Hostname / VULOS_HOSTNAME /
	// the identity store). Empty or unusable falls back to
	// lan.DefaultHostname(InstanceID), which is per-instance and therefore does
	// not collide with a sibling box. See names.go.
	Hostname string

	// CertSource supplies the TLS cert+key for the HTTPS listener. Required.
	CertSource CertSource

	// Handler is the OS HTTP handler served over HTTPS on the LAN. Required.
	Handler http.Handler

	// LANIP overrides the auto-detected LAN IP advertised over mDNS and returned
	// by the DNS responder. Leave nil to auto-detect.
	LANIP net.IP

	// HTTPSAddr is the listen address for the LAN HTTPS server.
	// Defaults to ":443"; tests inject an ephemeral address like "127.0.0.1:0".
	HTTPSAddr string

	// DNSAddr is the listen address for the UDP DNS responder.
	// Defaults to ":53"; tests inject "127.0.0.1:0".
	DNSAddr string

	// DisableMDNS skips mDNS advertisement (e.g. in CI where multicast is unavailable).
	DisableMDNS bool

	// DisableDNS skips constructing and starting the local DNS responder
	// entirely — no socket is opened on DNSAddr (default :53) at all. This is
	// independent of HTTPS/mDNS: both keep working normally with DNS disabled,
	// including the self-signed TLS fallback (secure-context status depends
	// only on the scheme, not on how the box's name was resolved).
	//
	// Defaults to false (DNS responder starts) so existing VULOS_LAN_ENABLE=1
	// deployments see no behaviour change; see cmd/server/main.go's
	// VULOS_LAN_DNS_DISABLE handling for the operator-facing knob.
	DisableDNS bool
}

// Service is the box-side LAN reachability layer: mDNS advertisement, a local
// DNS responder, and an HTTPS listener — all working without any cloud round-trip.
type Service struct {
	cfg      Config
	hostname string // box.<id>.lan.vulos.org
	lanIP    net.IP

	names NameSet // every name this box answers to; the single source of truth

	mu      sync.Mutex
	mdns    *mdnsAdvertiser
	dns     *dnsResponder
	httpSrv *http.Server
	httpLn  net.Listener
	started bool
}

// New builds a LAN Service from cfg. It resolves the box hostname and LAN IP up
// front (so they are observable before Start) but binds no sockets.
func New(cfg Config) (*Service, error) {
	if cfg.CertSource == nil {
		return nil, errors.New("lan: CertSource is required")
	}
	if cfg.Handler == nil {
		return nil, errors.New("lan: Handler is required")
	}
	if cfg.HTTPSAddr == "" {
		cfg.HTTPSAddr = ":443"
	}
	if cfg.DNSAddr == "" {
		cfg.DNSAddr = ":53"
	}

	lanIP := cfg.LANIP
	if lanIP == nil {
		lanIP = detectLANIPWaiting()
	}

	return &Service{
		cfg:      cfg,
		hostname: BoxHostname(cfg.InstanceID),
		lanIP:    lanIP,
		names:    NewNameSet(cfg.InstanceID, cfg.Hostname),
	}, nil
}

// Names returns the box's derived name set: the same values that produced the
// mDNS advertisement and that must produce the certificate's SANs.
func (s *Service) Names() NameSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.names
}

// AdvertisedNames returns the ".local" names the box is ACTUALLY answering for
// right now — which can be a subset of Names().MDNS when a sibling box already
// claimed one of them. Nil when mDNS is off or failed to start.
func (s *Service) AdvertisedNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mdns.Names()
}

// SetHostname renames the box live: it re-derives the name set and restarts the
// mDNS advertisement so the new name resolves on the LAN immediately, with no
// reboot.
//
// It returns the new NameSet so the caller can report exactly which names took
// effect, and an error if the name is not a usable DNS label.
//
// It deliberately does NOT touch the certificate: the CertSource owns that, and
// wiring it here would give the cert two owners. Callers pass a
// NameSetCertSource (certsource.go) whose SANs are re-derived from this same
// Service, so the cert follows the rename on its next mint. See
// cmd/server/routes_identity.go for the live-rename endpoint that uses both.
func (s *Service) SetHostname(name string) (NameSet, error) {
	clean := SanitizeHostname(name)
	if clean == "" {
		return NameSet{}, fmt.Errorf("lan: %q is not a valid box name (1-63 chars of a-z, 0-9 and -, not starting or ending with -)", name)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.names = NewNameSet(s.cfg.InstanceID, clean)
	if !s.started || s.cfg.DisableMDNS {
		return s.names, nil
	}

	// Restart the advertisement so the new name is claimed and the old one is
	// released. A rename that leaves the daemon advertising the OLD name is the
	// silent no-op this project keeps finding, so the restart is not optional.
	if s.mdns != nil {
		_ = s.mdns.Close()
		s.mdns = nil
	}
	m, err := newMDNSAdvertiser(s.lanIP, s.names.MDNS)
	if err != nil {
		log.Printf("[lan] mDNS re-advertisement after rename to %q failed: %v", clean, err)
		return s.names, nil
	}
	s.mdns = m
	log.Printf("[lan] renamed to %q; now advertising %v over mDNS", clean, m.Names())
	return s.names, nil
}

// Hostname returns the box's LAN hostname (box.<id>.lan.vulos.org).
func (s *Service) Hostname() string { return s.hostname }

// HTTPSAddr returns the actual address the HTTPS listener is bound to. This is
// only meaningful after Start (and is how tests learn the ephemeral port).
func (s *Service) HTTPSAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpLn != nil {
		return s.httpLn.Addr().String()
	}
	return s.cfg.HTTPSAddr
}

// DNSAddr returns the actual UDP address the DNS responder is bound to.
func (s *Service) DNSAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dns != nil {
		return s.dns.Addr()
	}
	return s.cfg.DNSAddr
}

// Start binds all sockets and begins serving. It returns the first hard error
// encountered while binding the HTTPS listener (the box's primary reachability
// surface). mDNS and DNS bind failures are logged but non-fatal: a box that can
// still be reached by IP is better than no box at all.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("lan: already started")
	}

	// HTTPS listener — the hard requirement.
	//
	// SECURITY (audit P1-5): like the DNS responder, the LAN HTTPS listener is
	// pinned to the detected LAN IP rather than 0.0.0.0/all interfaces when the
	// configured address uses a wildcard host. On a multi-homed/public box a
	// wildcard bind would expose the OS surface on the public interface too.
	httpsAddr := lanBindAddr(s.cfg.HTTPSAddr, s.lanIP)
	ln, err := net.Listen("tcp", httpsAddr)
	if err != nil {
		return fmt.Errorf("lan: https listen on %s: %w", httpsAddr, err)
	}
	s.httpLn = ln
	tlsLn := tls.NewListener(ln, TLSConfig(s.cfg.CertSource))
	srv := &http.Server{Handler: s.cfg.Handler}
	s.httpSrv = srv
	go func() {
		if err := srv.Serve(tlsLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[lan] https serve error: %v", err)
		}
	}()
	log.Printf("[lan] serving OS over HTTPS on %s (host=%s ip=%v)", ln.Addr(), s.hostname, s.lanIP)

	// Local DNS responder — answers box.<id>.lan.vulos.org offline.
	//
	// SECURITY (audit P1-5): bind the DNS responder to the detected LAN IP only,
	// never to 0.0.0.0/all interfaces. On a multi-homed or public box a wildcard
	// bind exposes an authoritative responder to the internet. lanBindAddr keeps
	// the configured port but pins the host to s.lanIP (unless the caller already
	// supplied an explicit host, e.g. tests using 127.0.0.1:0).
	//
	// DisableDNS (VULOS_LAN_DNS_DISABLE=1) skips this block entirely — no UDP
	// socket is opened at all, not even transiently. This is deliberately
	// independent of the HTTPS listener above and the mDNS block below: a box
	// running a DNS responder on :53 by default is an unacceptable surprise on
	// someone's home network (port conflicts with an existing router/Pi-hole
	// resolver, an unexpected authoritative-looking UDP:53 service appearing on
	// the LAN), whereas HTTPS-for-secure-context is the actual point of this
	// service and must not be held hostage to that decision.
	if s.cfg.DisableDNS {
		log.Printf("[lan] DNS responder disabled (VULOS_LAN_DNS_DISABLE=1); HTTPS/mDNS unaffected")
	} else {
		dnsAddr := lanBindAddr(s.cfg.DNSAddr, s.lanIP)
		dns, err := newDNSResponder(dnsAddr, s.hostname, s.lanIP)
		if err != nil {
			log.Printf("[lan] dns responder disabled: %v", err)
		} else {
			s.dns = dns
			dns.Start(ctx)
			log.Printf("[lan] DNS responder on %s answering %s -> %v", dns.Addr(), s.hostname, s.lanIP)
		}
	}

	// mDNS advertisement — every name in the derived set, conflict-probed.
	if !s.cfg.DisableMDNS {
		m, err := newMDNSAdvertiser(s.lanIP, s.names.MDNS)
		if err != nil {
			log.Printf("[lan] mDNS disabled: %v", err)
		} else {
			s.mdns = m
			log.Printf("[lan] advertising %v over mDNS (requested %v)", m.Names(), s.names.MDNS)
		}
	}

	s.started = true
	return nil
}

// Stop shuts down all listeners. It is safe to call even if Start failed partway.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	if s.mdns != nil {
		if err := s.mdns.Close(); err != nil {
			errs = append(errs, err)
		}
		s.mdns = nil
	}
	if s.dns != nil {
		if err := s.dns.Close(); err != nil {
			errs = append(errs, err)
		}
		s.dns = nil
	}
	if s.httpSrv != nil {
		if err := s.httpSrv.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
		s.httpSrv = nil
	}
	s.started = false
	return errors.Join(errs...)
}

// lanBindAddr returns the address a LAN listener (HTTPS or the DNS responder)
// should bind to. It keeps the port from cfgAddr but, when cfgAddr names a
// wildcard host (empty, "0.0.0.0", or "::"), it pins the host to lanIP so the
// listener is never exposed on all interfaces (audit P1-5). An explicit
// non-wildcard host in cfgAddr (e.g. "127.0.0.1:0" from tests) is preserved
// unchanged.
//
// If lanIP is nil/unusable the original cfgAddr is returned untouched so the
// caller's existing best-effort behaviour (and any error logging) is preserved.
func lanBindAddr(cfgAddr string, lanIP net.IP) string {
	host, port, err := net.SplitHostPort(cfgAddr)
	if err != nil {
		// cfgAddr had no port (or was malformed); leave it for the resolver to
		// report. Don't silently rewrite something we can't parse.
		return cfgAddr
	}
	if !isWildcardHost(host) {
		return cfgAddr // explicit host (e.g. test loopback) — respect it
	}
	if lanIP == nil {
		return cfgAddr
	}
	bindIP := lanIP
	if v4 := lanIP.To4(); v4 != nil {
		bindIP = v4
	}
	// Refuse to "pin" to loopback as if it were a LAN IP in production — but a
	// loopback lanIP only happens in isolated/CI environments where binding
	// loopback is the safest possible choice anyway, so allow it.
	return net.JoinHostPort(bindIP.String(), port)
}

// isWildcardHost reports whether host names "all interfaces".
func isWildcardHost(host string) bool {
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return true
	}
	return false
}

// DetectLANIP returns the box's primary non-loopback IPv4 LAN address (the same
// detection the LAN Service uses to pin its listeners), falling back to
// 127.0.0.1 when nothing routable is found. It is exported so co-located
// services (e.g. the fabric P2P sync discoverer) can advertise the same IP the
// LAN listener is bound to without re-implementing detection.
func DetectLANIP() net.IP { return detectLANIP() }

// detectLANIPWaiting is detectLANIP with a bounded wait for the address to
// actually exist.
//
// WHY: the address is resolved ONCE, at construction, and the listener never
// re-binds. vulos.service starts on network.target, which fires when networking
// starts — not when DHCP has assigned an address — and dhcpcd provides nothing
// later than that (network-online.target has no provider in this image, so
// ordering on it would be inert). A box whose lease arrived a second late
// therefore detected no LAN IP, fell back to loopback, and served LAN HTTPS
// that nothing on the LAN could reach — permanently, and silently.
//
// That is not a cosmetic failure: a browser on http://<lan-ip> is not a secure
// context, so window.crypto.subtle is undefined and src/lib's master key,
// content sealing and offline auth cannot run at all. LAN HTTPS is what fixes
// that, so it binding loopback defeats its entire purpose.
//
// Observed on a QEMU boot as "HTTPS: loopback-only" on the console status
// screen. Waits up to lanIPWaitTotal, polling every lanIPWaitStep; falls back
// to whatever detectLANIP last returned (loopback) so an genuinely
// network-less box still starts rather than blocking the boot.
const (
	lanIPWaitTotal = 30 * time.Second
	lanIPWaitStep  = 500 * time.Millisecond
)

// detectLANIPFn is a seam: tests replace it to simulate an address that
// arrives late. Without it the poll loop is untestable on any machine that
// already has a LAN address — the loop would exit on the first iteration and a
// broken wait would look identical to a working one.
var detectLANIPFn = detectLANIP

func detectLANIPWaiting() net.IP {
	deadline := time.Now().Add(lanIPWaitTotal)
	for {
		ip := detectLANIPFn()
		if ip != nil && !ip.IsLoopback() {
			return ip
		}
		if time.Now().After(deadline) {
			log.Printf("[lan] no non-loopback IPv4 after %s — binding loopback; "+
				"LAN HTTPS will NOT be reachable from the LAN until restart", lanIPWaitTotal)
			return ip
		}
		time.Sleep(lanIPWaitStep)
	}
}

// detectLANIP returns the box's primary non-loopback IPv4 LAN address, falling
// back to 127.0.0.1 when nothing routable is found (so the service is still
// well-formed in an isolated/CI environment).
func detectLANIP() net.IP {
	// Prefer the address used to reach a well-known off-box target; this picks
	// the interface the kernel would route LAN/WAN traffic through without
	// actually sending anything (UDP "connect" is local-only).
	if conn, err := net.Dial("udp", "192.168.1.1:9"); err == nil {
		defer conn.Close()
		if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok && ua.IP != nil {
			if v4 := ua.IP.To4(); v4 != nil && !v4.IsLoopback() {
				return v4
			}
		}
	}

	// Fallback: first private IPv4 found across interfaces.
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		v4 := ipNet.IP.To4()
		if v4 == nil || v4.IsLoopback() {
			continue
		}
		if v4.IsPrivate() {
			return v4
		}
	}
	return net.IPv4(127, 0, 0, 1)
}
