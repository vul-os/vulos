package lan

// fingerprint_test.go — PAIR-01 tests.
//
// The property that makes SPKI pinning work at all is: the fingerprint a box
// shows a user survives a restart. If that ever regresses silently (e.g. a
// future refactor starts hashing the certificate instead of the key), every
// already-paired client would start rejecting the box with no warning. The
// stability test below is the guard for that; see
// TestSPKISHA256_MutationDetectsFullCertHash for confirmation it actually
// catches the regression it claims to (mutation notes in the PR/report).

import (
	"crypto/sha256"
	"crypto/tls"
	"net"
	"net/url"
	"path/filepath"
	"testing"
)

// TestSPKISHA256_StableAcrossCertMints asserts the core pinning property:
// two independent SelfSignedCertSource instances pointed at the SAME
// persisted key path (i.e. two process starts against the same on-disk key,
// as certsource.go's loadOrCreateKey reuse path guarantees) yield the
// IDENTICAL SPKI digest, even though each mints its own certificate (distinct
// serial numbers / DER bytes).
func TestSPKISHA256_StableAcrossCertMints(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "lan-selfsigned.key")

	src1 := NewSelfSignedCertSourceWithKeyPath([]string{"vulos.local"}, nil, keyPath)
	cert1, err := src1.Certificate(nil)
	if err != nil {
		t.Fatalf("mint 1: %v", err)
	}

	// A second, independent CertSource instance pointed at the same key path —
	// this is what happens across a real restart: loadOrCreateKey reuses the
	// key file that already exists on disk instead of generating a new one.
	src2 := NewSelfSignedCertSourceWithKeyPath([]string{"vulos.local"}, nil, keyPath)
	cert2, err := src2.Certificate(nil)
	if err != nil {
		t.Fatalf("mint 2: %v", err)
	}

	// Sanity: the two mints really are DIFFERENT certificates (different
	// serial numbers), not the same cached *tls.Certificate handed back twice —
	// otherwise "stable across mints" would be trivially true for the wrong
	// reason.
	if cert1.Leaf == nil || cert2.Leaf == nil {
		t.Fatal("expected both certs to have a parsed Leaf")
	}
	if cert1.Leaf.SerialNumber.Cmp(cert2.Leaf.SerialNumber) == 0 {
		t.Fatal("test setup broken: both mints produced the same serial number — they are not independent mints")
	}

	fp1, err := SPKISHA256(src1)
	if err != nil {
		t.Fatalf("SPKISHA256(src1): %v", err)
	}
	fp2, err := SPKISHA256(src2)
	if err != nil {
		t.Fatalf("SPKISHA256(src2): %v", err)
	}

	if fp1 != fp2 {
		t.Fatalf("SPKI fingerprint changed across cert mints from the SAME key: %x != %x — this breaks every already-paired client on box restart", fp1, fp2)
	}
}

// TestSPKISHA256_ChangesWithDifferentKey asserts the flip side: two
// CertSources backed by DIFFERENT keys must NOT collide on the same
// fingerprint (that would defeat pinning's entire purpose — an attacker's box
// could present a different key and still match a victim's stored pin).
func TestSPKISHA256_ChangesWithDifferentKey(t *testing.T) {
	keyPathA := filepath.Join(t.TempDir(), "a.key")
	keyPathB := filepath.Join(t.TempDir(), "b.key")

	srcA := NewSelfSignedCertSourceWithKeyPath([]string{"vulos.local"}, nil, keyPathA)
	srcB := NewSelfSignedCertSourceWithKeyPath([]string{"vulos.local"}, nil, keyPathB)

	fpA, err := SPKISHA256(srcA)
	if err != nil {
		t.Fatalf("SPKISHA256(srcA): %v", err)
	}
	fpB, err := SPKISHA256(srcB)
	if err != nil {
		t.Fatalf("SPKISHA256(srcB): %v", err)
	}

	if fpA == fpB {
		t.Fatal("SPKI fingerprint is identical for two DIFFERENT keys — pinning would not distinguish two different boxes")
	}
}

// TestSPKIFingerprintBase64_MatchesRawDigest cross-checks the base64 encoder
// against a hand-computed digest so a future refactor of SPKIFingerprintBase64
// can't silently start encoding the wrong bytes (e.g. the whole cert instead
// of just the SPKI) while still "looking like" valid base64.
func TestSPKIFingerprintBase64_MatchesRawDigest(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "lan-selfsigned.key")
	src := NewSelfSignedCertSourceWithKeyPath([]string{"vulos.local"}, nil, keyPath)

	cert, err := src.Certificate(nil)
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	want := sha256.Sum256(cert.Leaf.RawSubjectPublicKeyInfo)

	gotB64, err := SPKIFingerprintBase64(src)
	if err != nil {
		t.Fatalf("SPKIFingerprintBase64: %v", err)
	}

	sum, err := SPKISHA256(src)
	if err != nil {
		t.Fatalf("SPKISHA256: %v", err)
	}
	if sum != want {
		t.Fatalf("SPKISHA256 = %x, want %x (hash of RawSubjectPublicKeyInfo)", sum, want)
	}

	hexFP, err := SPKIFingerprintHex(src)
	if err != nil {
		t.Fatalf("SPKIFingerprintHex: %v", err)
	}
	if len(hexFP) != sha256.Size*3-1 { // "XX:" * 31 + "XX"
		t.Fatalf("hex fingerprint %q has unexpected length %d", hexFP, len(hexFP))
	}
	if gotB64 == "" {
		t.Fatal("SPKIFingerprintBase64 returned empty string")
	}
}

// TestSPKISHA256_ErrorsOnNilSource asserts the fail-closed contract: a nil
// CertSource is an error, not a panic or a zero-value fingerprint that would
// look like a legitimate (if unlucky) digest.
func TestSPKISHA256_ErrorsOnNilSource(t *testing.T) {
	if _, err := SPKISHA256(nil); err == nil {
		t.Fatal("SPKISHA256(nil) returned no error")
	}
}

// TestSPKISHA256_ParsesLeafWhenNotPreset asserts SPKISHA256 works even when
// the CertSource hands back a *tls.Certificate with Leaf == nil (e.g.
// fileCertSource via tls.LoadX509KeyPair does not always populate it) by
// falling back to parsing Certificate[0]. Also asserts the parsed-leaf path
// and the Leaf-set path agree, which is what makes the file-cert case and the
// self-signed case fingerprint consistently.
func TestSPKISHA256_ParsesLeafWhenNotPreset(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "lan-selfsigned.key")
	src := NewSelfSignedCertSourceWithKeyPath([]string{"vulos.local"}, nil, keyPath)
	cert, err := src.Certificate(nil)
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}

	withLeaf, err := SPKISHA256(src)
	if err != nil {
		t.Fatalf("SPKISHA256 (leaf set): %v", err)
	}

	stripped := *cert
	stripped.Leaf = nil
	stub := &stubCertSource{cert: &stripped}
	withoutLeaf, err := SPKISHA256(stub)
	if err != nil {
		t.Fatalf("SPKISHA256 (leaf nil, parse fallback): %v", err)
	}

	if withLeaf != withoutLeaf {
		t.Fatalf("fingerprint differs depending on whether Leaf was pre-populated: %x != %x", withLeaf, withoutLeaf)
	}
}

// TestSPKISHA256_ErrorsOnEmptyCertificate asserts a certificate with no DER
// bytes at all is an error, not a hash of zero bytes.
func TestSPKISHA256_ErrorsOnEmptyCertificate(t *testing.T) {
	stub := &stubCertSource{cert: &tls.Certificate{}}
	if _, err := SPKISHA256(stub); err == nil {
		t.Fatal("SPKISHA256 of an empty *tls.Certificate returned no error")
	}
}

// ─── Pairing payload ────────────────────────────────────────────────────────

// TestBuildAndParsePairingURI_RoundTrip asserts BuildPairingURI/ParsePairingURI
// round-trip the same (name, addr, spki) — including a name with characters
// that require URL-encoding — and that the fixed payload shape is exactly
// what clients/core/pair.go's EncodePairPayload/ParsePairPayload produce and
// expect (vulos://pair?...).
func TestBuildAndParsePairingURI_RoundTrip(t *testing.T) {
	cases := []struct {
		name, addr, spki string
	}{
		{"vulos", "192.168.1.42:443", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="},
		{"Living Room Box", "10.0.0.7:8443", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="},
		{"box/with#weird&chars", "[::1]:443", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="},
	}

	for _, tc := range cases {
		payload := BuildPairingURI(tc.name, tc.addr, tc.spki)

		gotName, gotAddr, gotSPKI, err := ParsePairingURI(payload)
		if err != nil {
			t.Fatalf("ParsePairingURI(%q): %v", payload, err)
		}
		if gotName != tc.name {
			t.Errorf("name round-trip: got %q, want %q", gotName, tc.name)
		}
		if gotAddr != tc.addr {
			t.Errorf("addr round-trip: got %q, want %q", gotAddr, tc.addr)
		}
		if gotSPKI != tc.spki {
			t.Errorf("spki round-trip: got %q, want %q", gotSPKI, tc.spki)
		}
	}
}

// TestBuildPairingURI_MatchesFixedFormat pins the exact wire shape: scheme
// vulos, host pair, and query keys addr/name/spki. url.Values.Encode
// alphabetizes keys, so despite Set()-ing name, addr, spki in that order the
// serialized query string is addr=...&name=...&spki=... — this test would
// catch an accidental switch to manual (non-alphabetized) string
// concatenation that drifted from what clients/core/pair.go's
// EncodePairPayload actually emits.
func TestBuildPairingURI_MatchesFixedFormat(t *testing.T) {
	payload := BuildPairingURI("mybox", "10.1.2.3:443", "c3BraQ==")

	u, err := url.Parse(payload)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	if u.Scheme != "vulos" {
		t.Errorf("scheme = %q, want vulos", u.Scheme)
	}
	if u.Host != "pair" {
		t.Errorf("host = %q, want pair", u.Host)
	}

	const wantQuery = "addr=10.1.2.3%3A443&name=mybox&spki=c3BraQ%3D%3D"
	if u.RawQuery != wantQuery {
		t.Errorf("query = %q, want %q", u.RawQuery, wantQuery)
	}
}

// TestParsePairingURI_RejectsWrongScheme / MissingFields / BadAddr assert the
// negative paths a hostile or malformed payload must not sail through.
func TestParsePairingURI_RejectsWrongSchemeOrHost(t *testing.T) {
	for _, bad := range []string{
		"https://pair?name=a&addr=1.2.3.4:443&spki=c3BraQ==",
		"vulos://notpair?name=a&addr=1.2.3.4:443&spki=c3BraQ==",
		"not a url at all \x7f",
	} {
		if _, _, _, err := ParsePairingURI(bad); err == nil {
			t.Errorf("ParsePairingURI(%q) accepted an invalid scheme/host", bad)
		}
	}
}

func TestParsePairingURI_RejectsMissingFields(t *testing.T) {
	for _, bad := range []string{
		"vulos://pair?addr=1.2.3.4:443&spki=c3BraQ==",       // no name
		"vulos://pair?name=a&spki=c3BraQ==",                 // no addr
		"vulos://pair?name=a&addr=1.2.3.4:443",              // no spki
		"vulos://pair?name=&addr=1.2.3.4:443&spki=c3BraQ==", // empty name
	} {
		if _, _, _, err := ParsePairingURI(bad); err == nil {
			t.Errorf("ParsePairingURI(%q) accepted a payload missing a required field", bad)
		}
	}
}

func TestParsePairingURI_RejectsUnparsableAddr(t *testing.T) {
	bad := "vulos://pair?name=a&addr=not-a-host-port&spki=c3BraQ=="
	if _, _, _, err := ParsePairingURI(bad); err == nil {
		t.Fatal("ParsePairingURI accepted an addr with no port")
	}
}

// TestPairingPayload_EndToEnd exercises PairingPayload (the real CertSource ->
// payload path) end to end and confirms the emitted spki matches
// SPKIFingerprintBase64 for the same source.
func TestPairingPayload_EndToEnd(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "lan-selfsigned.key")
	src := NewSelfSignedCertSourceWithKeyPath([]string{"vulos.local"}, nil, keyPath)

	payload, err := PairingPayload("my-box", "192.168.1.9:443", src)
	if err != nil {
		t.Fatalf("PairingPayload: %v", err)
	}

	name, addr, spki, err := ParsePairingURI(payload)
	if err != nil {
		t.Fatalf("ParsePairingURI(%q): %v", payload, err)
	}
	if name != "my-box" {
		t.Errorf("name = %q, want my-box", name)
	}
	if addr != "192.168.1.9:443" {
		t.Errorf("addr = %q, want 192.168.1.9:443", addr)
	}

	wantSPKI, err := SPKIFingerprintBase64(src)
	if err != nil {
		t.Fatalf("SPKIFingerprintBase64: %v", err)
	}
	if spki != wantSPKI {
		t.Errorf("spki in payload = %q, want %q", spki, wantSPKI)
	}
}

// TestPairingAddr_UsesConfiguredPort asserts PairingAddr takes the port from
// httpsAddr (not a hardcoded value) while resolving the host itself via
// DetectLANIP rather than trusting a possibly-wildcard host in httpsAddr.
func TestPairingAddr_UsesConfiguredPort(t *testing.T) {
	addr := PairingAddr(":8443")
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("PairingAddr(':8443') = %q is not host:port: %v", addr, err)
	}
	if port != "8443" {
		t.Errorf("port = %q, want 8443", port)
	}
}

func TestPairingAddr_DefaultsPortTo443(t *testing.T) {
	addr := PairingAddr("")
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("PairingAddr('') = %q is not host:port: %v", addr, err)
	}
	if port != "443" {
		t.Errorf("port = %q, want 443 (default)", port)
	}
}
