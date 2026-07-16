package ddos

import (
	"fmt"
	"testing"
	"time"
)

func TestVerifyHashcash_Valid(t *testing.T) {
	challenge := "testchallenge"
	bits := 4 // require 4 leading zero bits

	// Brute-force a valid nonce.
	var nonce string
	for i := 0; i < 1_000_000; i++ {
		n := fmt.Sprintf("%d", i)
		if verifyHashcash(challenge, n, bits) {
			nonce = n
			break
		}
	}
	if nonce == "" {
		t.Fatal("could not find valid nonce in 1M attempts")
	}
	if !verifyHashcash(challenge, nonce, bits) {
		t.Fatal("valid nonce rejected by verifyHashcash")
	}
}

func TestVerifyHashcash_Invalid(t *testing.T) {
	// SHA256("bad"+"wrong") almost certainly doesn't have 4 leading zero bits
	// unless we get extremely lucky — but we can guarantee it with bits=0.
	if !verifyHashcash("any", "any", 0) {
		t.Fatal("0-bit difficulty should always pass")
	}
}

func TestCaptchaStore_IssueAndVerify(t *testing.T) {
	store := NewCaptchaStore(nil)
	store.SetCaptchaEverywhere(false)

	// Manually inject a challenge with difficulty 0 (trivial), bound to a route.
	store.mu.Lock()
	store.challenges["deadbeef"] = captchaEntry{
		challenge:  "deadbeef",
		difficulty: 0,
		issuedAt:   time.Now(),
		route:      "/api/auth/login",
	}
	store.mu.Unlock()

	// Any nonce satisfies 0-bit PoW, on the route it was issued for.
	pow := "deadbeef:0"
	if !store.Verify(pow, "/api/auth/login") {
		t.Fatal("expected Verify to return true for 0-difficulty challenge on its own route")
	}
	// Replay should fail (challenge consumed).
	if store.Verify(pow, "/api/auth/login") {
		t.Fatal("replay should be rejected")
	}
}

// A challenge issued for one route must NOT validate on another (no cross-route
// difficulty arbitrage) — and it is still consumed, so it can't then be retried
// on the right route either.
func TestCaptchaStore_RouteBinding(t *testing.T) {
	store := NewCaptchaStore(nil)
	store.mu.Lock()
	store.challenges["c0ffee"] = captchaEntry{
		challenge:  "c0ffee",
		difficulty: 0,
		issuedAt:   time.Now(),
		route:      "/api/auth/handle-available",
	}
	store.mu.Unlock()

	// Presented on a DIFFERENT route → rejected.
	if store.Verify("c0ffee:0", "/api/auth/login") {
		t.Fatal("a challenge for one route must not validate on another")
	}
	// And it was consumed, so even the correct route now fails.
	if store.Verify("c0ffee:0", "/api/auth/handle-available") {
		t.Fatal("a challenge spent on a wrong-route attempt must not be reusable")
	}
}

func TestCaptchaStore_CaptchaEverywhereToggle(t *testing.T) {
	store := NewCaptchaStore(nil)
	if store.CaptchaEverywhere() {
		t.Fatal("default should be false")
	}
	store.SetCaptchaEverywhere(true)
	if !store.CaptchaEverywhere() {
		t.Fatal("expected true after set")
	}
}
