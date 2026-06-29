package peering

// relay_nonce_test.go — nonce deduplication tests for the Deposit path.
//
// Coverage:
//   - Same nonce submitted twice → second deposit rejected with "duplicate nonce" error.
//   - Distinct nonces from the same sender → both accepted.
//   - Nonce eviction: backdating a seen-nonce entry past nonceDedupeWindow causes
//     the next deposit with the same nonce to be accepted (TTL eviction works).
//   - Hard-cap eviction: filling the map to nonceMaxEntries causes the oldest
//     entry to be dropped so new ones can be added.

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// TestDeposit_NonceDedup_SameNonceRejected verifies that a replay of the same
// signed deposit request (identical nonce) is rejected on the second attempt.
func TestDeposit_NonceDedup_SameNonceRejected(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "nonce-blob-1", []byte("payload"), 24)

	// First deposit must succeed.
	if err := rs.Deposit(req); err != nil {
		t.Fatalf("first deposit: %v", err)
	}

	// Second deposit with the same nonce (different blob_id to rule out blob
	// idempotent-overwrite as the source of any error) must be rejected.
	req2 := req
	req2.BlobID = "nonce-blob-1-retry"
	canonical2, err := relayDepositCanonical(req2)
	if err != nil {
		t.Fatalf("relayDepositCanonical: %v", err)
	}
	sig2 := ed25519.Sign(senderPriv, canonical2)
	req2.Signature = base64.RawURLEncoding.EncodeToString(sig2)

	err = rs.Deposit(req2)
	if err == nil {
		t.Fatal("expected duplicate-nonce error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate nonce") {
		t.Errorf("expected 'duplicate nonce' in error, got: %v", err)
	}
}

// TestDeposit_NonceDedup_DistinctNoncesAccepted verifies that two deposits
// from the same sender with distinct nonces are both accepted.
func TestDeposit_NonceDedup_DistinctNoncesAccepted(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	// relayTestDeposit generates a fresh nonce each call (time.Now().UnixNano()).
	req1 := relayTestDeposit(t, senderPriv, senderID, recipientID, "nonce-blob-2a", []byte("payload-a"), 24)
	req2 := relayTestDeposit(t, senderPriv, senderID, recipientID, "nonce-blob-2b", []byte("payload-b"), 24)

	if req1.Nonce == req2.Nonce {
		t.Skip("nonces collided — skipping (extremely unlikely)")
	}

	if err := rs.Deposit(req1); err != nil {
		t.Fatalf("deposit 1: %v", err)
	}
	if err := rs.Deposit(req2); err != nil {
		t.Fatalf("deposit 2 (distinct nonce): %v", err)
	}
}

// TestDeposit_NonceDedup_Eviction verifies that a nonce entry backdated past
// nonceDedupeWindow is evicted by the lazy TTL sweep, allowing the same nonce
// to be re-used after the window expires.
func TestDeposit_NonceDedup_Eviction(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "nonce-evict-1", []byte("payload"), 24)

	// First deposit succeeds and records the nonce.
	if err := rs.Deposit(req); err != nil {
		t.Fatalf("first deposit: %v", err)
	}

	// Backdate the nonce entry to simulate TTL expiry.
	rs.rateMu.Lock()
	key := senderID + "\x00" + req.Nonce
	rs.seenNonces[key] = time.Now().UTC().Add(-(nonceDedupeWindow + time.Minute))
	rs.rateMu.Unlock()

	// A second deposit with the same nonce should now succeed (entry evicted).
	req2 := req
	req2.BlobID = "nonce-evict-2"
	canonical2, err := relayDepositCanonical(req2)
	if err != nil {
		t.Fatalf("relayDepositCanonical: %v", err)
	}
	sig2 := ed25519.Sign(senderPriv, canonical2)
	req2.Signature = base64.RawURLEncoding.EncodeToString(sig2)

	if err := rs.Deposit(req2); err != nil {
		t.Errorf("expected evicted nonce to be accepted, got: %v", err)
	}
}

// TestDeposit_NonceDedup_HardCap verifies that when the nonce map reaches
// nonceMaxEntries the oldest entry is evicted to make room for the new one.
func TestDeposit_NonceDedup_HardCap(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	// Populate seenNonces to just below the hard cap, all with a future-ish
	// timestamp so they won't be evicted by the TTL sweep.
	rs.rateMu.Lock()
	base := time.Now().UTC().Add(-30 * time.Minute) // within window
	const oldestKey = "dummy\x00oldest"
	rs.seenNonces[oldestKey] = base.Add(-time.Second) // oldest entry
	for i := 1; i < nonceMaxEntries; i++ {
		k := "dummy\x00filler" + string(rune(i))
		rs.seenNonces[k] = base
	}
	rs.rateMu.Unlock()

	// Verify map is at the cap.
	rs.rateMu.Lock()
	size := len(rs.seenNonces)
	rs.rateMu.Unlock()
	if size != nonceMaxEntries {
		t.Fatalf("pre-condition: seenNonces has %d entries, want %d", size, nonceMaxEntries)
	}

	// A new deposit should trigger hard-cap eviction and then succeed.
	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "cap-blob-1", []byte("data"), 24)
	if err := rs.Deposit(req); err != nil {
		t.Fatalf("deposit after hard-cap eviction: %v", err)
	}

	// The oldest entry should have been evicted.
	rs.rateMu.Lock()
	_, oldestStillPresent := rs.seenNonces[oldestKey]
	rs.rateMu.Unlock()
	if oldestStillPresent {
		t.Error("expected oldest nonce entry to be evicted by hard-cap, but it is still present")
	}
}
