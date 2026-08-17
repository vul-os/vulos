package lan

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The LAN certificate used to be minted ONCE per process behind a sync.Once.
// Two things move underneath it and both produced a user-visible TLS error:
//
//   - DHCP moving the box: the IP SAN (the universal no-mDNS fallback, and the
//     only address Chrome on Android can use) would name the old address.
//   - Renaming the box via POST /api/identity/hostname: the cert would name
//     only the old name.
//
// These guards pin the re-mint, and — just as important — pin that re-minting
// does NOT rotate the key, because every native-client pin and every browser
// "accept this certificate" exception is anchored to the SPKI.

func leafOf(t *testing.T, src CertSource) *x509.Certificate {
	t.Helper()
	c, err := src.Certificate(nil)
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	if c.Leaf != nil {
		return c.Leaf
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return leaf
}

func spkiOf(t *testing.T, src CertSource) string {
	t.Helper()
	leaf := leafOf(t, src)
	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// TestSelfSignedRemintsWhenTheLANIPChanges is the DHCP guard.
func TestSelfSignedRemintsWhenTheLANIPChanges(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "lan.key")

	var mu sync.Mutex
	ip := net.IPv4(192, 168, 1, 50)
	src := NewDynamicSelfSignedCertSource(
		func() []string { return []string{"vulos.local"} },
		func() []net.IP {
			mu.Lock()
			defer mu.Unlock()
			return []net.IP{ip}
		},
		keyPath,
	)

	first := leafOf(t, src)
	if !hasIP(first, "192.168.1.50") {
		t.Fatalf("first cert IP SANs = %v, want 192.168.1.50", first.IPAddresses)
	}

	// DHCP moves the box.
	mu.Lock()
	ip = net.IPv4(192, 168, 1, 77)
	mu.Unlock()
	forceSANRecheck(src)

	second := leafOf(t, src)
	if !hasIP(second, "192.168.1.77") {
		t.Fatalf("after the IP changed the cert still carries %v — a browser on https://192.168.1.77 gets NAME MISMATCH on top of the unknown-issuer warning", second.IPAddresses)
	}
	if hasIP(second, "192.168.1.50") {
		t.Errorf("the stale address is still a SAN: %v", second.IPAddresses)
	}
}

// TestSelfSignedRemintsWhenTheNameChanges is the live-rename guard.
func TestSelfSignedRemintsWhenTheNameChanges(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "lan.key")

	var mu sync.Mutex
	names := []string{"vulos.local"}
	src := NewDynamicSelfSignedCertSource(
		func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), names...)
		},
		func() []net.IP { return []net.IP{net.IPv4(192, 168, 1, 50)} },
		keyPath,
	)

	if got := leafOf(t, src).DNSNames; !equalStrings(got, []string{"vulos.local"}) {
		t.Fatalf("first cert DNS SANs = %v", got)
	}

	mu.Lock()
	names = []string{"study.local", "vulos-k3n7q2.local"}
	mu.Unlock()
	forceSANRecheck(src)

	got := leafOf(t, src).DNSNames
	if !equalStrings(got, []string{"study.local", "vulos-k3n7q2.local"}) {
		t.Fatalf("after the rename the cert DNS SANs are %v — renaming the box would leave it answering to a name its own certificate never mentions", got)
	}
}

// TestSelfSignedRemintKeepsSPKI is the pinning-safety guard, and it is the
// reason re-minting is acceptable at all: SPKI pins (clients/core/pair.go) and
// browser certificate exceptions are anchored to the KEY, and loadOrCreateKey
// persists it. If a re-mint ever rotated the key, every paired native client
// would break silently on the next handshake.
func TestSelfSignedRemintKeepsSPKI(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "lan.key")

	var mu sync.Mutex
	ip := net.IPv4(192, 168, 1, 50)
	names := []string{"vulos.local"}
	src := NewDynamicSelfSignedCertSource(
		func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), names...)
		},
		func() []net.IP {
			mu.Lock()
			defer mu.Unlock()
			return []net.IP{ip}
		},
		keyPath,
	)

	before := spkiOf(t, src)
	serialBefore := leafOf(t, src).SerialNumber.String()

	mu.Lock()
	ip = net.IPv4(10, 0, 0, 4)
	names = []string{"study.local"}
	mu.Unlock()
	forceSANRecheck(src)

	after := spkiOf(t, src)
	serialAfter := leafOf(t, src).SerialNumber.String()

	if before != after {
		t.Fatalf("re-minting ROTATED the key: SPKI %s -> %s. Every paired native client's pin and every browser exception just broke.", before, after)
	}
	if serialBefore == serialAfter {
		t.Fatalf("the certificate was not actually re-minted (serial %s unchanged) — the SPKI check above proves nothing", serialBefore)
	}

	// A brand-new source over the same persisted key must also agree, which is
	// what makes -print-pairing's independently constructed CertSource match
	// the one the listener serves.
	other := NewDynamicSelfSignedCertSource(
		func() []string { return []string{"anything.local"} },
		func() []net.IP { return []net.IP{net.IPv4(172, 16, 0, 9)} },
		keyPath,
	)
	if got := spkiOf(t, other); got != before {
		t.Fatalf("a second source over the same persisted key derived a different SPKI (%s vs %s)", got, before)
	}
}

// TestSelfSignedDoesNotRemintWhenNothingChanged: Certificate() runs on every
// handshake. Re-minting each time would put an ECDSA keygen-and-sign in the
// handshake path and hand every connection a different serial.
func TestSelfSignedDoesNotRemintWhenNothingChanged(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "lan.key")
	src := NewDynamicSelfSignedCertSource(
		func() []string { return []string{"vulos.local"} },
		func() []net.IP { return []net.IP{net.IPv4(192, 168, 1, 50)} },
		keyPath,
	)
	first := leafOf(t, src).SerialNumber.String()
	for i := 0; i < 25; i++ {
		forceSANRecheck(src) // even with the throttle defeated
		if got := leafOf(t, src).SerialNumber.String(); got != first {
			t.Fatalf("certificate was re-minted on handshake %d with no SAN change (serial %s -> %s)", i, first, got)
		}
	}
}

// TestSelfSignedThrottlesSANLookups: the IP provider does a UDP socket dial, so
// it must not run on every single handshake.
func TestSelfSignedThrottlesSANLookups(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "lan.key")
	var calls int
	src := NewDynamicSelfSignedCertSource(
		func() []string { return []string{"vulos.local"} },
		func() []net.IP {
			calls++
			return []net.IP{net.IPv4(192, 168, 1, 50)}
		},
		keyPath,
	)
	for i := 0; i < 50; i++ {
		if _, err := src.Certificate(&tls.ClientHelloInfo{}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("the IP provider ran %d times across 50 handshakes, want 1 (sanRecheckInterval is %s)", calls, sanRecheckInterval)
	}
}

// TestStaticSelfSignedStillWorks: the non-dynamic constructor is used by tests
// and by callers that genuinely have fixed SANs; replacing sync.Once must not
// have changed its behaviour.
func TestStaticSelfSignedStillWorks(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "lan.key")
	src := NewSelfSignedCertSourceWithKeyPath([]string{"vulos.local"}, []net.IP{net.IPv4(192, 168, 1, 50)}, keyPath)
	first := leafOf(t, src)
	if !equalStrings(first.DNSNames, []string{"vulos.local"}) {
		t.Fatalf("DNS SANs = %v", first.DNSNames)
	}
	if !hasIP(first, "192.168.1.50") {
		t.Fatalf("IP SANs = %v", first.IPAddresses)
	}
	for i := 0; i < 5; i++ {
		if got := leafOf(t, src).SerialNumber.String(); got != first.SerialNumber.String() {
			t.Fatalf("a static source re-minted (serial changed) on call %d", i)
		}
	}
}

func hasIP(c *x509.Certificate, want string) bool {
	for _, ip := range c.IPAddresses {
		if ip.String() == want {
			return true
		}
	}
	return false
}

// forceSANRecheck defeats the sanRecheckInterval throttle so a test does not
// have to sleep for it. It reaches into the source deliberately: the throttle
// is a performance property (pinned by TestSelfSignedThrottlesSANLookups), not
// the behaviour under test here.
func forceSANRecheck(s *SelfSignedCertSource) {
	s.mu.Lock()
	s.lastCheck = time.Now().Add(-2 * sanRecheckInterval)
	s.mu.Unlock()
}
