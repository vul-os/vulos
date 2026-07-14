package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

// issue is the happy-path ceremony: root certifies release.
func issue(t *testing.T, notAfter time.Time) (rootPub ed25519.PublicKey, releasePub ed25519.PublicKey, cert ReleaseCert) {
	t.Helper()
	rootPub, rootPriv := testKey(t)
	releasePub, _ = testKey(t)
	cert, err := IssueReleaseCert(rootPriv, releasePub, "release-test", notAfter, 3)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}
	return rootPub, releasePub, cert
}

func TestReleaseCert_RoundTrip(t *testing.T) {
	rootPub, releasePub, cert := issue(t, time.Now().Add(time.Hour))

	got, err := ReleaseKeyFromCert(rootPub, cert)
	if err != nil {
		t.Fatalf("ReleaseKeyFromCert: %v", err)
	}
	if !got.Equal(releasePub) {
		t.Fatal("release key from cert does not match the key that was certified")
	}
	if cert.MinEpoch != 3 {
		t.Errorf("MinEpoch: got %d, want 3", cert.MinEpoch)
	}
}

// The cert must survive a trip through JSON — it is read from disk on the box.
func TestReleaseCert_JSONRoundTrip(t *testing.T) {
	rootPub, releasePub, cert := issue(t, time.Now().Add(time.Hour))

	data, err := json.Marshal(cert)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseReleaseCert(data)
	if err != nil {
		t.Fatalf("ParseReleaseCert: %v", err)
	}
	got, err := ReleaseKeyFromCert(rootPub, parsed)
	if err != nil {
		t.Fatalf("ReleaseKeyFromCert after JSON round-trip: %v", err)
	}
	if !got.Equal(releasePub) {
		t.Fatal("release key changed across a JSON round-trip")
	}
}

func TestReleaseCert_RejectsExpired(t *testing.T) {
	rootPub, _, cert := issue(t, time.Now().Add(-time.Minute))

	if err := ValidateReleaseCert(rootPub, cert); err == nil {
		t.Fatal("an expired cert validated")
	} else if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected an expiry error, got: %v", err)
	}
}

func TestReleaseCert_RejectsForeignRoot(t *testing.T) {
	_, _, cert := issue(t, time.Now().Add(time.Hour))
	otherRootPub, _ := testKey(t)

	if err := ValidateReleaseCert(otherRootPub, cert); err == nil {
		t.Fatal("a cert validated against a root that never signed it")
	}
}

// Every field the root signs must be tamper-evident — otherwise an attacker
// could extend NotAfter or swap the release key inside a valid-looking cert.
func TestReleaseCert_RejectsTamperedFields(t *testing.T) {
	otherPub, _ := testKey(t)

	cases := map[string]func(c *ReleaseCert){
		"release_pubkey": func(c *ReleaseCert) { c.ReleasePubKey = encodeHex(otherPub) },
		"key_id":         func(c *ReleaseCert) { c.KeyID = "attacker" },
		"not_after":      func(c *ReleaseCert) { c.NotAfter = "2999-01-01T00:00:00Z" },
		"min_epoch":      func(c *ReleaseCert) { c.MinEpoch = 999 },
	}
	for field, tamper := range cases {
		t.Run(field, func(t *testing.T) {
			rootPub, _, cert := issue(t, time.Now().Add(time.Hour))
			tamper(&cert)
			if err := ValidateReleaseCert(rootPub, cert); err == nil {
				t.Fatalf("tampering with %q was not detected", field)
			}
		})
	}
}

func TestParseReleaseCert_RejectsMissingFields(t *testing.T) {
	// A cert with no root_sig must not parse into a zero-signature cert that
	// some later check might wave through.
	if _, err := ParseReleaseCert([]byte(`{"release_pubkey":"ab","key_id":"k","not_after":"2030-01-01T00:00:00Z"}`)); err == nil {
		t.Fatal("a cert with no root_sig parsed successfully")
	}
}

// TestDevKeys_PinnedConstantsMatchSeeds guards the invariant the whole dev-key
// story rests on: the pubkeys pinned in devanchor.go really are the keys the
// published seeds derive, so RefuseDevKeyInProd cannot be silently bypassed by
// a seed change that nobody re-pinned.
func TestDevKeys_PinnedConstantsMatchSeeds(t *testing.T) {
	rootPub, _ := DeriveDevKey(DevRootSeed)
	releasePub, _ := DeriveDevKey(DevReleaseSeed)

	if got := strings.TrimSpace(EncodeAnchor(rootPub)); got != DevAnchorPubB64 {
		t.Errorf("DevAnchorPubB64 is stale: seed derives %s, constant says %s", got, DevAnchorPubB64)
	}
	if got := strings.TrimSpace(EncodeAnchor(releasePub)); got != DevReleasePubB64 {
		t.Errorf("DevReleasePubB64 is stale: seed derives %s, constant says %s", got, DevReleasePubB64)
	}

	if !IsDevKey(rootPub) || !IsDevKey(releasePub) {
		t.Fatal("IsDevKey does not recognise the keys its own seeds derive")
	}

	realPub, _ := testKey(t)
	if IsDevKey(realPub) {
		t.Fatal("IsDevKey flagged a freshly generated key as a dev key")
	}
}

func TestRefuseDevKeyInProd(t *testing.T) {
	devPub, _ := DeriveDevKey(DevRootSeed)
	realPub, _ := testKey(t)

	if err := RefuseDevKeyInProd(devPub, true); err == nil {
		t.Fatal("the dev key was permitted in prod")
	}
	if err := RefuseDevKeyInProd(devPub, false); err != nil {
		t.Fatalf("the dev key must be usable outside prod: %v", err)
	}
	if err := RefuseDevKeyInProd(realPub, true); err != nil {
		t.Fatalf("a real key must be permitted in prod: %v", err)
	}
}

// TestEncodeAnchor_RoundTrip pins the wire format: what EncodeAnchor writes,
// LoadAnchor must read back byte-identically.
func TestEncodeAnchor_RoundTrip(t *testing.T) {
	pub, _ := testKey(t)

	path := t.TempDir() + "/trust-anchor.pub"
	if err := writeFile(path, EncodeAnchor(pub)); err != nil {
		t.Fatalf("write anchor: %v", err)
	}
	got, err := LoadAnchor(path)
	if err != nil {
		t.Fatalf("LoadAnchor: %v", err)
	}
	if !got.Equal(pub) {
		t.Fatal("anchor did not survive an EncodeAnchor → LoadAnchor round-trip")
	}
}

// ─── small helpers ────────────────────────────────────────────────────────────

func encodeHex(pub ed25519.PublicKey) string { return hex.EncodeToString(pub) }

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0644) }
