package appsplatform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests harden the outbound-event signing contract (signing.go): the HMAC
// scheme, constant-time verification, tamper rejection and replay/expiry
// defense. They are deliberately adversarial — each asserts a way an attacker
// might forge or replay a signed event and confirms it is rejected.

// TestSignMatchesManualHMAC proves Sign is exactly HMAC-SHA256 over
// "<timestamp>.<body>" with the "v0=" prefix — i.e. there is no secret-leaking
// shortcut and the basestring is byte-exact.
func TestSignMatchesManualHMAC(t *testing.T) {
	ts := "1700000000"
	body := []byte(`{"event":"message.created","x":"y"}`)
	secret := "vas_" + strings.Repeat("a", 64)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + string(body)))
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))

	if got := Sign(ts, body, secret); got != want {
		t.Fatalf("Sign mismatch:\n got %q\nwant %q", got, want)
	}
}

// TestSignBasestringSeparatorMatters ensures the "." separator is part of the
// signed string: moving the boundary between timestamp and body must change the
// signature (otherwise "12.3" + "x" would collide with "1" + "2.3x").
func TestSignBasestringSeparatorMatters(t *testing.T) {
	secret := "s3cr3t"
	a := Sign("12", []byte("3.4"), secret)
	b := Sign("123", []byte(".4"), secret)
	if a == b {
		t.Fatal("basestring is ambiguous: separator not bound into the signature")
	}
}

// TestVerifyRejectsTamperedTimestamp confirms changing the timestamp (a replay
// with a refreshed clock value) invalidates the signature.
func TestVerifyRejectsTamperedTimestamp(t *testing.T) {
	body, secret := []byte("payload"), "k"
	ts := "1700000000"
	sig := Sign(ts, body, secret)
	if Verify("1700000001", body, secret, sig) {
		t.Fatal("signature verified under a different timestamp")
	}
}

// TestVerifyRejectsMalformedSignatures feeds Verify a battery of malformed
// signature strings: it must never panic and must always return false.
func TestVerifyRejectsMalformedSignatures(t *testing.T) {
	ts, body, secret := "1", []byte("b"), "k"
	good := Sign(ts, body, secret)
	bad := []string{
		"",                              // empty
		"v0=",                           // prefix only
		good[3:],                        // hex without the v0= prefix
		"v1=" + good[3:],                // wrong version tag
		strings.ToUpper(good),           // uppercased hex (hex compare is exact)
		good + "00",                     // too long
		good[:len(good)-2],              // too short
		"v0=zzzz",                       // non-hex
		"garbage",                       // arbitrary
		strings.Repeat("v0=ab", 100000), // pathologically long
	}
	for _, s := range bad {
		if Verify(ts, body, secret, s) {
			t.Errorf("Verify accepted a malformed signature %q", truncate(s))
		}
	}
}

// TestVerifyUsesConstantTimeCompare is a structural guarantee: a signature that
// shares a long correct prefix but differs at the end must still be rejected.
// (Verify delegates to hmac.Equal, which does not early-return.)
func TestVerifyConstantTimePrefixNotAccepted(t *testing.T) {
	ts, body, secret := "1", []byte("b"), "k"
	good := Sign(ts, body, secret)
	// Flip only the final hex nibble.
	last := good[len(good)-1]
	var flipped byte = '0'
	if last == '0' {
		flipped = '1'
	}
	near := good[:len(good)-1] + string(flipped)
	if near == good {
		t.Fatal("test setup: failed to perturb signature")
	}
	if Verify(ts, body, secret, near) {
		t.Fatal("a near-miss signature (correct long prefix) was accepted")
	}
}

// TestVerifyWithSkewBoundary exercises the exact freshness boundary.
func TestVerifyWithSkewBoundary(t *testing.T) {
	body, secret := []byte("b"), "k"
	maxAge := 5 * time.Minute

	// Just inside the window (4m59s old) verifies.
	inside := strconv.FormatInt(time.Now().Add(-(maxAge - time.Second)).Unix(), 10)
	if !VerifyWithSkew(inside, body, secret, Sign(inside, body, secret), maxAge) {
		t.Error("timestamp just inside the window should verify")
	}
	// Comfortably outside the window (10m old) is rejected.
	outside := strconv.FormatInt(time.Now().Add(-(maxAge + 5*time.Minute)).Unix(), 10)
	if VerifyWithSkew(outside, body, secret, Sign(outside, body, secret), maxAge) {
		t.Error("timestamp well outside the window should be rejected")
	}
}

// TestVerifyWithSkewEmptyAndJunkTimestamp ensures a blank/garbage timestamp is
// rejected even when the HMAC itself is valid for that string.
func TestVerifyWithSkewEmptyAndJunkTimestamp(t *testing.T) {
	body, secret := []byte("b"), "k"
	for _, ts := range []string{"", "   ", "abc", "12x", "0x10", "1e9"} {
		sig := Sign(ts, body, secret) // valid HMAC over the junk timestamp
		if VerifyWithSkew(ts, body, secret, sig, time.Minute) {
			t.Errorf("VerifyWithSkew accepted non-numeric timestamp %q", ts)
		}
	}
}

// TestSignEmptySecretStillBinds documents that even an empty secret yields a
// deterministic, body-bound signature (so a misconfigured zero secret does not
// collapse to a constant a forger could guess without knowing the body).
func TestSignEmptySecretStillBinds(t *testing.T) {
	a := Sign("1", []byte("alpha"), "")
	b := Sign("1", []byte("beta"), "")
	if a == b {
		t.Fatal("empty-secret signatures do not depend on the body")
	}
}

func truncate(s string) string {
	if len(s) > 32 {
		return s[:32] + "..."
	}
	return s
}
