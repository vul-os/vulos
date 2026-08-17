package lanca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"net"
	"sort"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// The constraint set itself.
//
// EVERY expectation below is transcribed by hand from the DESIGN (the operator
// installs a root that may only ever speak for `.local`, `lan.vulos.org`, and
// private/link-local IP space) — never read back out of PermittedDNSDomains or
// permittedIPCIDRs. A test that derives its expectation from the symbol under
// test proves the code equals itself and nothing else.
// ---------------------------------------------------------------------------

func TestPermittedDNSDomainsAreExactlyTheIntendedSubtrees(t *testing.T) {
	// Hand-typed. If someone widens the CA to another subtree, this fails and
	// they must justify it HERE rather than silently inherit trust for it.
	//
	// This test has already earned its keep once: adding `lan` and `home.arpa`
	// for internal/lan's router-DHCP names turned it red, which is the correct
	// behaviour — widening a trust boundary should require a deliberate edit to
	// a hand-typed list, not ride along with an unrelated change.
	//
	// Every entry must be a name that CANNOT resolve on the public internet.
	// That is the property that makes a stolen CA key harmless outside the
	// owner's LAN, and it is the bar any future addition has to clear.
	//   local        RFC 6762 reserved
	//   home.arpa    RFC 8375 reserved
	//   lan          de-facto private TLD, undelegated (see the caveat on
	//                PermittedDNSDomains — remove it if ICANN ever delegates)
	//   lan.vulos.org  a real domain, but this subtree is ours
	want := []string{"home.arpa", "lan", "lan.vulos.org", "local"}

	got := append([]string(nil), PermittedDNSDomains...)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("permitted DNS subtree count = %d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("permitted DNS subtrees = %v, want %v", got, want)
		}
	}
	// Leading dots would change the meaning in Go (strict subdomains only) and
	// are not RFC 5280 syntax. Assert the spelling, not just the set.
	for _, d := range PermittedDNSDomains {
		if len(d) > 0 && d[0] == '.' {
			t.Fatalf("permitted DNS subtree %q has a leading dot; the dotless spelling is the portable one", d)
		}
	}
}

func TestPermittedIPRangesAreExactlyPrivateAndLinkLocalSpace(t *testing.T) {
	// Hand-typed from RFC 1918 / RFC 3927 / RFC 4193 / RFC 4291.
	want := []string{
		"10.0.0.0/8",
		"169.254.0.0/16",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"fc00::/7",
		"fe80::/10",
	}
	got := SortedPermittedCIDRs()
	if len(got) != len(want) {
		t.Fatalf("permitted CIDR count = %d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("permitted CIDRs = %v, want %v", got, want)
		}
	}
}

// TestCheckIPRejectsEveryPublicAddressWeCanThinkOf pins the property that
// matters — no publicly routable address is issuable — against literals that
// have nothing to do with the CIDR list.
func TestCheckIPRejectsPublicAddresses(t *testing.T) {
	public := []string{
		"8.8.8.8",         // Google DNS
		"1.1.1.1",         // Cloudflare
		"142.250.72.14",   // a google.com address
		"172.32.0.1",      // just OUTSIDE 172.16/12 — the classic off-by-one
		"172.15.255.255",  // just BELOW 172.16/12
		"11.0.0.1",        // just outside 10/8
		"192.169.0.1",     // just outside 192.168/16
		"169.253.0.1",     // just outside 169.254/16
		"2606:4700::1111", // Cloudflare v6
		"fd00::1",         // NOTE: this IS inside fc00::/7 and must NOT be here
	}
	// fd00::1 is genuinely permitted (ULA). Drop it from the "public" list and
	// assert it separately, so the list above stays a readable catalogue.
	public = public[:len(public)-1]

	for _, s := range public {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test bug: %q did not parse", s)
		}
		if err := CheckIP(ip); err == nil {
			t.Errorf("CheckIP(%s) allowed a publicly routable address", s)
		}
	}

	private := []string{
		"10.1.2.3", "172.16.0.1", "172.31.255.254", "192.168.1.50",
		"169.254.10.20", "fd00::1", "fe80::1",
	}
	for _, s := range private {
		ip := net.ParseIP(s)
		if err := CheckIP(ip); err != nil {
			t.Errorf("CheckIP(%s) rejected an address the box legitimately uses: %v", s, err)
		}
	}
}

func TestCheckDNSNameAllowsAdvertisedLANNamesAndRefusesThePublicInternet(t *testing.T) {
	ok := []string{
		"vulos.local",
		"vulos-2.local", // the avahi-renamed second box on the same LAN
		"VULOS.LOCAL",   // case-insensitive
		"vulos.local.",  // trailing dot, as an mDNS responder may emit
		"box.abc123.lan.vulos.org",
		"local",
	}
	for _, n := range ok {
		if err := CheckDNSName(n); err != nil {
			t.Errorf("CheckDNSName(%q) rejected a name the box may legitimately advertise: %v", n, err)
		}
	}

	bad := []string{
		"google.com",
		"www.google.com",
		"vulos.local.evil.com", // suffix-confusion: contains ".local" but is not under it
		"notlocal",
		"mylocal", // must not match "local" by bare suffix
		"lan.vulos.org.evil.io",
		"vulos.org",      // the parent of the permitted subtree is NOT permitted
		"evil.vulos.org", // a sibling of lan.vulos.org is NOT permitted
		"*.local",        // wildcards refused outright
		"",
	}
	for _, n := range bad {
		if err := CheckDNSName(n); err == nil {
			t.Errorf("CheckDNSName(%q) allowed a name outside the CA's remit", n)
		}
	}
}

// ---------------------------------------------------------------------------
// Enforcement: does a REAL verifier actually honour the extension?
//
// This is the part of the design that is a claim about other people's code. A
// name constraint that is silently ignored is worse than none. These tests
// measure Go's crypto/x509 verifier, which is the one verifier this process can
// actually run. See the package's README note and the final report for which
// verifiers were NOT measured.
// ---------------------------------------------------------------------------

// forgeLeafFor signs a leaf for arbitrary names with the root's key, bypassing
// Root.Issue's own refusal. This simulates a STOLEN CA KEY: an attacker with
// the key does not politely call our API, they call x509.CreateCertificate
// directly. If the constraint only lived in CheckDNSName, this test would pass
// verification and the whole design would be theatre.
func forgeLeafFor(t *testing.T, root *Root, dnsNames []string, ips []net.IP) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	cn := "forged"
	if len(dnsNames) > 0 {
		cn = dnsNames[0]
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, root.Cert, &key.PublicKey, root.Key)
	if err != nil {
		t.Fatalf("forging leaf: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func verifyAgainstRoot(root *Root, leaf *x509.Certificate, dnsName string) error {
	pool := x509.NewCertPool()
	pool.AddCert(root.Cert)
	_, err := leaf.Verify(x509.VerifyOptions{
		Roots:       pool,
		DNSName:     dnsName,
		CurrentTime: time.Now(),
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err
}

func TestStolenCAKeyCannotMintAWorkingCertForGoogleCom(t *testing.T) {
	root, err := NewRoot("test")
	if err != nil {
		t.Fatal(err)
	}

	// The attacker has the key and signs whatever they like.
	forged := forgeLeafFor(t, root, []string{"google.com", "www.google.com"}, nil)

	// Sanity: the signature really is from our root, i.e. the forgery is
	// well-formed and the ONLY thing that can reject it is the constraint.
	if err := forged.CheckSignatureFrom(root.Cert); err != nil {
		t.Fatalf("test bug: forged leaf is not actually signed by the root: %v", err)
	}

	if err := verifyAgainstRoot(root, forged, "google.com"); err == nil {
		t.Fatal("SECURITY: Go's verifier ACCEPTED a leaf for google.com signed by the name-constrained root — the name constraint is not being enforced")
	} else {
		t.Logf("Go crypto/x509 rejected google.com leaf as expected: %v", err)
	}
}

func TestStolenCAKeyCannotMintForAPublicIPAddress(t *testing.T) {
	root, err := NewRoot("test")
	if err != nil {
		t.Fatal(err)
	}
	forged := forgeLeafFor(t, root, []string{"vulos.local"}, []net.IP{net.ParseIP("8.8.8.8")})
	if err := verifyAgainstRoot(root, forged, "vulos.local"); err == nil {
		t.Fatal("SECURITY: verifier accepted a leaf carrying a public IP SAN (8.8.8.8) under the name-constrained root")
	}
}

func TestLegitimateLANLeafVerifiesCleanlyUnderTheSameRoot(t *testing.T) {
	// The counterpart to the two tests above: proving the constraint blocks
	// google.com is worthless if it also blocks the box. This is the control.
	root, err := NewRoot("test")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, leaf, err := root.Issue(LeafRequest{
		DNSNames:  []string{"vulos.local", "vulos-2.local"},
		IPs:       []net.IP{net.ParseIP("192.168.1.50")},
		PublicKey: &key.PublicKey,
	})
	if err != nil {
		t.Fatalf("issuing a legitimate LAN leaf failed: %v", err)
	}
	for _, name := range []string{"vulos.local", "vulos-2.local"} {
		if err := verifyAgainstRoot(root, leaf, name); err != nil {
			t.Fatalf("verify %s: %v", name, err)
		}
	}
	if err := verifyAgainstRoot(root, leaf, "192.168.1.50"); err != nil {
		t.Fatalf("verify by IP: %v", err)
	}
}

func TestNameConstraintsExtensionIsPresentAndCritical(t *testing.T) {
	root, err := NewRoot("test")
	if err != nil {
		t.Fatal(err)
	}
	// OID 2.5.29.30 = id-ce-nameConstraints (RFC 5280 §4.2.1.10). Transcribed
	// from the RFC, not read from Go's x509 package constants.
	nameConstraintsOID := asn1.ObjectIdentifier{2, 5, 29, 30}

	var found bool
	for _, ext := range root.Cert.Extensions {
		if ext.Id.Equal(nameConstraintsOID) {
			found = true
			if !ext.Critical {
				t.Fatal("nameConstraints extension is NOT marked critical — a verifier that does not understand it would be free to ignore the limits instead of rejecting the chain")
			}
		}
	}
	if !found {
		t.Fatal("root carries NO nameConstraints extension (OID 2.5.29.30) — the root is unconstrained")
	}
}

func TestRootCannotSignASubordinateCA(t *testing.T) {
	// pathLenConstraint=0. Without it, a stolen key could mint an intermediate
	// and — on verifiers with weak constraint propagation — chain around the
	// permitted subtrees.
	root, err := NewRoot("test")
	if err != nil {
		t.Fatal(err)
	}
	if !root.Cert.MaxPathLenZero || root.Cert.MaxPathLen != 0 {
		t.Fatalf("root MaxPathLen=%d MaxPathLenZero=%v, want 0/true so no intermediate can be minted",
			root.Cert.MaxPathLen, root.Cert.MaxPathLenZero)
	}

	// Actually try it: forge an intermediate, then a leaf under it.
	interKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	interTmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "forged intermediate"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	interDER, err := x509.CreateCertificate(rand.Reader, interTmpl, root.Cert, &interKey.PublicKey, root.Key)
	if err != nil {
		t.Fatalf("forging intermediate: %v", err)
	}
	inter, _ := x509.ParseCertificate(interDER)

	// The leaf under the forged intermediate is deliberately for a name the
	// name constraints ALLOW ("vulos.local"). If it were for google.com, the
	// name constraint alone would reject the chain and this half of the test
	// would prove nothing about pathLenConstraint — it would pass even with the
	// path length removed. Using a permitted name isolates pathLen as the ONLY
	// thing that can reject this chain.
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial2, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	leafTmpl := &x509.Certificate{
		SerialNumber:          serial2,
		Subject:               pkix.Name{CommonName: "vulos.local"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"vulos.local"},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, inter, &leafKey.PublicKey, interKey)
	if err != nil {
		t.Fatalf("forging leaf under intermediate: %v", err)
	}
	leaf, _ := x509.ParseCertificate(leafDER)

	roots := x509.NewCertPool()
	roots.AddCert(root.Cert)
	inters := x509.NewCertPool()
	inters.AddCert(inter)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inters,
		DNSName:       "vulos.local",
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err == nil {
		t.Fatal("SECURITY: a forged intermediate under this root produced a VALID chain — pathLenConstraint=0 is not holding, so a stolen key could mint a subordinate CA")
	} else {
		t.Logf("chain through forged intermediate rejected as expected: %v", err)
	}
}

func TestIssueRefusesNamesOutsideTheConstraintBeforeSigning(t *testing.T) {
	root, err := NewRoot("test")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	if _, _, err := root.Issue(LeafRequest{
		DNSNames:  []string{"google.com"},
		PublicKey: &key.PublicKey,
	}); err == nil {
		t.Fatal("Issue signed a leaf for google.com; the tool must refuse before the verifier has to")
	}
	if _, _, err := root.Issue(LeafRequest{
		DNSNames:  []string{"vulos.local"},
		IPs:       []net.IP{net.ParseIP("8.8.8.8")},
		PublicKey: &key.PublicKey,
	}); err == nil {
		t.Fatal("Issue signed a leaf carrying a public IP SAN")
	}
}
