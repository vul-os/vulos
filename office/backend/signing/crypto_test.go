package signing_test

// crypto_test.go — OFFICE-44 tests: hash-chain integrity + tamper-link detection.

import (
	"testing"
	"time"

	"vulos-office/backend/signing"
)

// setupKey initialises (or reuses) the in-memory dev key for each test.
func setupKey(t *testing.T) {
	t.Helper()
	if err := signing.LoadOrGenerateKey(); err != nil {
		t.Fatalf("LoadOrGenerateKey: %v", err)
	}
}

// -------------------------------------------------------------------------
// Token round-trip
// -------------------------------------------------------------------------

func TestGenerateAndVerifyToken(t *testing.T) {
	setupKey(t)

	payload := signing.TokenPayload{
		EnvelopeID:    "env-001",
		SignerID:      "sg-01",
		FieldID:       "f-01",
		DocHashBefore: "abc123",
		DocHashAfter:  "def456",
		Timestamp:     time.Now().Unix(),
		Identity:      "alice@example.com",
	}

	token, err := signing.GenerateToken(payload)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	got, err := signing.VerifyToken(token)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}

	if got.EnvelopeID != payload.EnvelopeID {
		t.Errorf("envelope_id mismatch: %q vs %q", got.EnvelopeID, payload.EnvelopeID)
	}
	if got.SignerID != payload.SignerID {
		t.Errorf("signer_id mismatch: %q vs %q", got.SignerID, payload.SignerID)
	}
	if got.DocHashBefore != payload.DocHashBefore {
		t.Errorf("doc_hash_before mismatch: %q vs %q", got.DocHashBefore, payload.DocHashBefore)
	}
	if got.DocHashAfter != payload.DocHashAfter {
		t.Errorf("doc_hash_after mismatch: %q vs %q", got.DocHashAfter, payload.DocHashAfter)
	}
}

func TestVerifyToken_Tampered(t *testing.T) {
	setupKey(t)

	payload := signing.TokenPayload{
		EnvelopeID:   "env-002",
		SignerID:     "sg-02",
		DocHashAfter: "goodhash",
		Timestamp:    time.Now().Unix(),
	}

	token, err := signing.GenerateToken(payload)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	// Corrupt a byte deep inside the signature portion (not trailing padding).
	// Ed25519 signatures are 64 bytes → ~86 base64 chars; flip a char near the middle.
	runes := []rune(token)
	mid := len(runes)/2 + 10
	if mid >= len(runes) {
		mid = len(runes) / 2
	}
	if runes[mid] == 'A' {
		runes[mid] = 'Z'
	} else {
		runes[mid] = 'A'
	}
	tampered := string(runes)

	if _, err := signing.VerifyToken(tampered); err == nil {
		t.Error("expected error verifying tampered token, got nil")
	}
}

func TestVerifyToken_MalformedNoDot(t *testing.T) {
	setupKey(t)
	if _, err := signing.VerifyToken("nodottoken"); err == nil {
		t.Error("expected error for token without dot separator")
	}
}

// -------------------------------------------------------------------------
// Document hashing
// -------------------------------------------------------------------------

func TestHashDocument(t *testing.T) {
	h1 := signing.HashDocument([]byte("hello pdf"))
	h2 := signing.HashDocument([]byte("hello pdf"))
	h3 := signing.HashDocument([]byte("different pdf"))

	if h1 == "" {
		t.Fatal("expected non-empty hash")
	}
	if h1 != h2 {
		t.Error("same content must produce same hash")
	}
	if h1 == h3 {
		t.Error("different content must produce different hash")
	}
}

// -------------------------------------------------------------------------
// Hash-chain helpers
// -------------------------------------------------------------------------

func makeEvent(id, envelopeID, action, prevHash string, ts time.Time) signing.AuditChainInput {
	return signing.AuditChainInput{
		ID:            id,
		EnvelopeID:    envelopeID,
		Action:        action,
		Timestamp:     ts,
		PrevEventHash: prevHash,
	}
}

func TestHashEvent_Deterministic(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ev := makeEvent("id-1", "env-x", "created", "", ts)
	h1, err := signing.HashEvent(ev)
	if err != nil {
		t.Fatalf("HashEvent: %v", err)
	}
	h2, err := signing.HashEvent(ev)
	if err != nil {
		t.Fatalf("HashEvent: %v", err)
	}
	if h1 != h2 {
		t.Error("HashEvent must be deterministic for same input")
	}
}

func TestVerifyChain_Valid(t *testing.T) {
	ts := time.Now().UTC()

	// Build three-event chain.
	ev0 := makeEvent("id-0", "env-1", "created", "", ts)
	h0, _ := signing.HashEvent(ev0)

	ev1 := makeEvent("id-1", "env-1", "viewed", h0, ts.Add(time.Second))
	h1, _ := signing.HashEvent(ev1)

	ev2 := makeEvent("id-2", "env-1", "signed", h1, ts.Add(2*time.Second))

	chain := []signing.AuditChainInput{ev0, ev1, ev2}
	if err := signing.VerifyChain(chain); err != nil {
		t.Fatalf("VerifyChain valid chain: %v", err)
	}
}

func TestVerifyChain_TamperedMiddleLink(t *testing.T) {
	ts := time.Now().UTC()

	ev0 := makeEvent("id-0", "env-2", "created", "", ts)
	h0, _ := signing.HashEvent(ev0)

	ev1 := makeEvent("id-1", "env-2", "viewed", h0, ts.Add(time.Second))
	h1, _ := signing.HashEvent(ev1)

	// Tamper: ev2 claims ev1's hash but ev1 was altered.
	ev1Tampered := ev1
	ev1Tampered.Identity = "injected" // mutate after hashing

	h1Tampered, _ := signing.HashEvent(ev1Tampered)
	_ = h1Tampered

	// ev2 still points to the original h1, but the chain holds ev1Tampered.
	ev2 := makeEvent("id-2", "env-2", "signed", h1, ts.Add(2*time.Second))

	// Chain with the tampered middle event.
	chain := []signing.AuditChainInput{ev0, ev1Tampered, ev2}

	// ev1Tampered.PrevEventHash == h0 (still correct), so chain up to ev1 passes.
	// ev2.PrevEventHash == h1 (the original), but hash(ev1Tampered) != h1.
	// VerifyChain builds prevHash from hashing the actual events so it will
	// detect the mismatch at ev2.
	if err := signing.VerifyChain(chain); err == nil {
		t.Error("expected VerifyChain to detect tampered middle link, got nil")
	}
}

func TestVerifyChain_BrokenPrevHashPointer(t *testing.T) {
	ts := time.Now().UTC()

	ev0 := makeEvent("id-0", "env-3", "created", "", ts)
	// ev1 claims a wrong prev_event_hash value from the start.
	ev1 := makeEvent("id-1", "env-3", "viewed", "wronghash", ts.Add(time.Second))

	chain := []signing.AuditChainInput{ev0, ev1}
	if err := signing.VerifyChain(chain); err == nil {
		t.Error("expected VerifyChain to detect wrong prev_event_hash, got nil")
	}
}

func TestVerifyChain_Empty(t *testing.T) {
	if err := signing.VerifyChain(nil); err != nil {
		t.Errorf("empty chain should be valid: %v", err)
	}
}

func TestVerifyChain_SingleEvent(t *testing.T) {
	ev := makeEvent("id-0", "env-4", "created", "", time.Now().UTC())
	if err := signing.VerifyChain([]signing.AuditChainInput{ev}); err != nil {
		t.Fatalf("single-event chain should be valid: %v", err)
	}
}

func TestLatestEventHash_Empty(t *testing.T) {
	h, err := signing.LatestEventHash(nil)
	if err != nil {
		t.Fatalf("LatestEventHash empty: %v", err)
	}
	if h != "" {
		t.Errorf("expected empty hash for empty chain, got %q", h)
	}
}

func TestLatestEventHash_NonEmpty(t *testing.T) {
	setupKey(t)
	ts := time.Now().UTC()
	ev := makeEvent("id-0", "env-5", "created", "", ts)
	h, err := signing.LatestEventHash([]signing.AuditChainInput{ev})
	if err != nil {
		t.Fatalf("LatestEventHash: %v", err)
	}
	if h == "" {
		t.Error("expected non-empty hash")
	}
}
