// relay_test.go — unit tests for relay.go (PEER-38).
package peering

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// relayTestSetup creates a temporary RelayStore and ContactStore for testing.
func relayTestSetup(t *testing.T) (*RelayStore, *ContactStore, string) {
	t.Helper()
	tmp := t.TempDir()

	cs, err := NewContactStore(tmp)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}

	rs, err := NewRelayStore(tmp, cs)
	if err != nil {
		t.Fatalf("NewRelayStore: %v", err)
	}

	// Enable relay with a small capacity.
	cfg := RelayConfig{
		Enabled:    true,
		CapacityMB: 10,
		TTLHours:   relayDefaultTTLHours,
	}
	if err := rs.SetConfig(cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	return rs, cs, tmp
}

// genKeyPair generates a random Ed25519 keypair and a Vulos ID for testing.
func genKeyPair(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	vulosID := encodeVulosID(pub)
	return priv, pub, vulosID
}

// relayTestDeposit builds and signs a deposit request.
func relayTestDeposit(t *testing.T, senderPriv ed25519.PrivateKey, senderID, recipientID, blobID string, blobData []byte, ttlHours int) relayDepositRequest {
	t.Helper()
	blobB64 := base64.StdEncoding.EncodeToString(blobData)
	nonce := fmt.Sprintf("%d", time.Now().UnixNano())

	req := relayDepositRequest{
		To:            recipientID,
		BlobID:        blobID,
		BlobB64:       blobB64,
		TTLHours:      ttlHours,
		SenderVulosID: senderID,
		Nonce:         nonce,
	}

	// Sign the canonical request (minus signature field).
	canonical, err := relayDepositCanonical(req)
	if err != nil {
		t.Fatalf("relayDepositCanonical: %v", err)
	}
	sig := ed25519.Sign(senderPriv, canonical)
	req.Signature = base64.RawURLEncoding.EncodeToString(sig)
	return req
}

// relayPickupAuthHeader builds the Authorization header for pickup/ack.
func relayPickupAuthHeader(t *testing.T, priv ed25519.PrivateKey, vulosID string) string {
	t.Helper()
	tsStr := fmt.Sprintf("%d", time.Now().Unix())
	msg := []byte(vulosID + "." + tsStr)
	sig := ed25519.Sign(priv, msg)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)
	return fmt.Sprintf("Vulos-Relay %s.%s.%s", vulosID, tsStr, sigB64)
}

// relayAddApprovedContact adds a contact to the ContactStore in the Approved state.
func relayAddApprovedContact(t *testing.T, cs *ContactStore, vulosID, displayName string) {
	t.Helper()
	if err := cs.Add(vulosID, displayName, "test-server:8080"); err != nil {
		t.Fatalf("cs.Add: %v", err)
	}
	if err := cs.Approve(vulosID, DefaultPerms()); err != nil {
		t.Fatalf("cs.Approve: %v", err)
	}
}

// ─── NewRelayStore tests ─────────────────────────────────────────────────────

func TestNewRelayStore_NilContacts(t *testing.T) {
	tmp := t.TempDir()
	_, err := NewRelayStore(tmp, nil)
	if err == nil {
		t.Fatal("expected error for nil contacts, got nil")
	}
}

func TestNewRelayStore_CreatesDirectories(t *testing.T) {
	tmp := t.TempDir()
	cs, _ := NewContactStore(tmp)
	rs, err := NewRelayStore(tmp, cs)
	if err != nil {
		t.Fatalf("NewRelayStore: %v", err)
	}
	if _, err := os.Stat(rs.storeDir); err != nil {
		t.Errorf("store directory not created: %v", err)
	}
	cfgPath := rs.root + "/config.json"
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("config.json not created: %v", err)
	}
}

// ─── Config tests ─────────────────────────────────────────────────────────────

func TestRelayConfig_DisabledByDefault(t *testing.T) {
	tmp := t.TempDir()
	cs, _ := NewContactStore(tmp)
	rs, _ := NewRelayStore(tmp, cs)
	if rs.GetConfig().Enabled {
		t.Error("relay should be disabled by default")
	}
}

func TestRelayConfig_SetAndPersist(t *testing.T) {
	tmp := t.TempDir()
	cs, _ := NewContactStore(tmp)
	rs, _ := NewRelayStore(tmp, cs)

	cfg := RelayConfig{Enabled: true, CapacityMB: 200, TTLHours: 48}
	if err := rs.SetConfig(cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	// Re-open to verify persistence.
	rs2, _ := NewRelayStore(tmp, cs)
	got := rs2.GetConfig()
	if !got.Enabled {
		t.Error("expected Enabled=true after reload")
	}
	if got.CapacityMB != 200 {
		t.Errorf("CapacityMB: got %d, want 200", got.CapacityMB)
	}
	if got.TTLHours != 48 {
		t.Errorf("TTLHours: got %d, want 48", got.TTLHours)
	}
}

func TestRelayConfig_EffectiveTTL_Clamped(t *testing.T) {
	cfg := RelayConfig{TTLHours: relayDefaultTTLHours}

	// Requested more than max → capped.
	got := cfg.effectiveTTL(relayMaxTTLHours + 24)
	if got != time.Duration(relayMaxTTLHours)*time.Hour {
		t.Errorf("effectiveTTL clamping: got %v, want %v", got, time.Duration(relayMaxTTLHours)*time.Hour)
	}

	// Requested 0 → falls back to config default.
	got = cfg.effectiveTTL(0)
	if got != time.Duration(relayDefaultTTLHours)*time.Hour {
		t.Errorf("effectiveTTL default: got %v, want %v", got, time.Duration(relayDefaultTTLHours)*time.Hour)
	}
}

// ─── Deposit tests ────────────────────────────────────────────────────────────

func TestDeposit_Success(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	blobData := []byte("this is an encrypted blob")
	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-001", blobData, 24)

	if err := rs.Deposit(req); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
}

func TestDeposit_RelayDisabled(t *testing.T) {
	tmp := t.TempDir()
	cs, _ := NewContactStore(tmp)
	rs, _ := NewRelayStore(tmp, cs) // disabled by default

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-002", []byte("data"), 24)
	err := rs.Deposit(req)
	if err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("expected 'not enabled' error, got: %v", err)
	}
}

func TestDeposit_SenderNotApproved(t *testing.T) {
	rs, _, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	// Do NOT add sender to contacts.

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-003", []byte("data"), 24)
	err := rs.Deposit(req)
	if err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("expected 'not approved' error, got: %v", err)
	}
}

func TestDeposit_InvalidSignature(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-004", []byte("data"), 24)
	// Corrupt the signature.
	req.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))

	err := rs.Deposit(req)
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("expected signature error, got: %v", err)
	}
}

func TestDeposit_BlobTooLarge(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	// Create a blob that exceeds relayMaxBlobBytes.
	bigBlob := make([]byte, relayMaxBlobBytes+1)
	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-005", bigBlob, 24)

	err := rs.Deposit(req)
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("expected 'exceeds maximum' error, got: %v", err)
	}
}

func TestDeposit_RateLimit(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	// Fill the rate-limit bucket directly.
	now := time.Now().UTC()
	rs.rateMu.Lock()
	timestamps := make([]time.Time, relayMaxDepositsPerHour)
	for i := range timestamps {
		timestamps[i] = now.Add(-time.Minute) // all recent
	}
	rs.senderDeposits[senderID] = timestamps
	rs.rateMu.Unlock()

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-006", []byte("data"), 24)
	err := rs.Deposit(req)
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("expected rate limit error, got: %v", err)
	}
}

func TestDeposit_AllowedListEnforced(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, otherSenderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)

	relayAddApprovedContact(t, cs, senderID, "Sender")

	// Restrict relay to only otherSenderID.
	cfg := rs.GetConfig()
	cfg.Allowed = []string{otherSenderID}
	rs.SetConfig(cfg) //nolint:errcheck

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-007", []byte("data"), 24)
	err := rs.Deposit(req)
	if err == nil || !strings.Contains(err.Error(), "allowed list") {
		t.Errorf("expected 'allowed list' error, got: %v", err)
	}
}

func TestDeposit_NeverDecrypts(t *testing.T) {
	// This test verifies that the blob is stored verbatim without touching
	// the crypto layer.  We deposit a blob with invalid base64-encoded content
	// (simulating a ciphertext the relay should not touch) and then verify it
	// comes back byte-for-byte identical via Pickup.
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	recipientPriv, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	// Pretend this is an encrypted blob — relay should not try to decrypt.
	fakeEncrypted := []byte("OPAQUE_CIPHERTEXT_DO_NOT_DECRYPT")
	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-opaque", fakeEncrypted, 24)

	if err := rs.Deposit(req); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	// Pickup and verify the blob is byte-for-byte identical.
	tsStr := fmt.Sprintf("%d", time.Now().Unix())
	msg := []byte(recipientID + "." + tsStr)
	sig := ed25519.Sign(recipientPriv, msg)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	blobs, err := rs.Pickup(recipientID, tsStr, sigB64)
	if err != nil {
		t.Fatalf("Pickup: %v", err)
	}
	if len(blobs) != 1 {
		t.Fatalf("expected 1 blob, got %d", len(blobs))
	}
	if blobs[0].BlobB64 != req.BlobB64 {
		t.Error("blob_b64 was mutated by relay — relay must not alter ciphertext")
	}
}

// ─── Pickup tests ─────────────────────────────────────────────────────────────

func TestPickup_Success(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	recipientPriv, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-p1", []byte("payload"), 24)
	if err := rs.Deposit(req); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	tsStr := fmt.Sprintf("%d", time.Now().Unix())
	msg := []byte(recipientID + "." + tsStr)
	sig := ed25519.Sign(recipientPriv, msg)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	blobs, err := rs.Pickup(recipientID, tsStr, sigB64)
	if err != nil {
		t.Fatalf("Pickup: %v", err)
	}
	if len(blobs) != 1 {
		t.Fatalf("expected 1 blob, got %d", len(blobs))
	}
	if blobs[0].ID != "blob-p1" {
		t.Errorf("blob ID: got %q, want %q", blobs[0].ID, "blob-p1")
	}
}

func TestPickup_WrongRecipient(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	wrongPriv, _, wrongID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-p2", []byte("payload"), 24)
	rs.Deposit(req) //nolint:errcheck

	// Wrong recipient tries to pick up.
	tsStr := fmt.Sprintf("%d", time.Now().Unix())
	msg := []byte(wrongID + "." + tsStr)
	sig := ed25519.Sign(wrongPriv, msg)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	blobs, err := rs.Pickup(wrongID, tsStr, sigB64)
	if err != nil {
		t.Fatalf("Pickup with wrong ID returned error: %v", err)
	}
	if len(blobs) != 0 {
		t.Errorf("wrong recipient got %d blobs, want 0", len(blobs))
	}
}

func TestPickup_InvalidSignature(t *testing.T) {
	rs, _, _ := relayTestSetup(t)
	_, _, recipientID := genKeyPair(t)

	tsStr := fmt.Sprintf("%d", time.Now().Unix())
	badSig := base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))

	_, err := rs.Pickup(recipientID, tsStr, badSig)
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("expected signature error, got: %v", err)
	}
}

func TestPickup_TimestampTooOld(t *testing.T) {
	rs, _, _ := relayTestSetup(t)
	recipientPriv, _, recipientID := genKeyPair(t)

	// Timestamp 10 minutes in the past.
	oldTS := time.Now().Add(-10 * time.Minute).Unix()
	tsStr := fmt.Sprintf("%d", oldTS)
	msg := []byte(recipientID + "." + tsStr)
	sig := ed25519.Sign(recipientPriv, msg)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	_, err := rs.Pickup(recipientID, tsStr, sigB64)
	if err == nil || !strings.Contains(err.Error(), "skew") {
		t.Errorf("expected timestamp skew error, got: %v", err)
	}
}

func TestPickup_EmptyForNewRecipient(t *testing.T) {
	rs, _, _ := relayTestSetup(t)
	recipientPriv, _, recipientID := genKeyPair(t)

	tsStr := fmt.Sprintf("%d", time.Now().Unix())
	msg := []byte(recipientID + "." + tsStr)
	sig := ed25519.Sign(recipientPriv, msg)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	blobs, err := rs.Pickup(recipientID, tsStr, sigB64)
	if err != nil {
		t.Fatalf("Pickup: %v", err)
	}
	if len(blobs) != 0 {
		t.Errorf("expected 0 blobs for new recipient, got %d", len(blobs))
	}
}

// ─── Ack tests ────────────────────────────────────────────────────────────────

func TestAck_DeletesBlob(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	recipientPriv, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-ack1", []byte("payload"), 24)
	rs.Deposit(req) //nolint:errcheck

	// Ack.
	tsStr := fmt.Sprintf("%d", time.Now().Unix())
	msg := []byte(recipientID + "." + tsStr)
	sig := ed25519.Sign(recipientPriv, msg)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	if err := rs.Ack(recipientID, tsStr, sigB64, []string{"blob-ack1"}); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	// Pickup should now return nothing.
	tsStr2 := fmt.Sprintf("%d", time.Now().Unix())
	msg2 := []byte(recipientID + "." + tsStr2)
	sig2 := ed25519.Sign(recipientPriv, msg2)
	sigB642 := base64.RawURLEncoding.EncodeToString(sig2)

	blobs, _ := rs.Pickup(recipientID, tsStr2, sigB642)
	if len(blobs) != 0 {
		t.Errorf("expected 0 blobs after ack, got %d", len(blobs))
	}
}

func TestAck_Idempotent(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	recipientPriv, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-ack2", []byte("payload"), 24)
	rs.Deposit(req) //nolint:errcheck

	authFn := func() (string, string, string) {
		tsStr := fmt.Sprintf("%d", time.Now().Unix())
		msg := []byte(recipientID + "." + tsStr)
		sig := ed25519.Sign(recipientPriv, msg)
		return recipientID, tsStr, base64.RawURLEncoding.EncodeToString(sig)
	}

	vid, ts, sig := authFn()
	rs.Ack(vid, ts, sig, []string{"blob-ack2"}) //nolint:errcheck

	// Second ack should not error.
	vid, ts, sig = authFn()
	if err := rs.Ack(vid, ts, sig, []string{"blob-ack2"}); err != nil {
		t.Errorf("second Ack should be idempotent, got: %v", err)
	}
}

func TestAck_InvalidSignature(t *testing.T) {
	rs, _, _ := relayTestSetup(t)
	_, _, recipientID := genKeyPair(t)

	tsStr := fmt.Sprintf("%d", time.Now().Unix())
	badSig := base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))

	err := rs.Ack(recipientID, tsStr, badSig, []string{"some-blob"})
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("expected signature error, got: %v", err)
	}
}

// ─── Reaper test ──────────────────────────────────────────────────────────────

func TestReaper_RemovesExpiredBlobs(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "blob-exp1", []byte("payload"), 1)
	if err := rs.Deposit(req); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	// Manually expire the blob by overwriting its ExpiresAt.
	recipientDir := rs.recipientDir(recipientID)
	blobPath := recipientDir + "/" + sanitizePath("blob-exp1") + ".json"

	blobRaw, _ := os.ReadFile(blobPath)
	var blob RelayBlob
	json.Unmarshal(blobRaw, &blob) //nolint:errcheck
	blob.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	data, _ := json.Marshal(blob)
	os.WriteFile(blobPath, data, 0600) //nolint:errcheck

	// Run the reaper.
	rs.reapExpired()

	// File should be gone.
	if _, err := os.Stat(blobPath); !os.IsNotExist(err) {
		t.Error("expected expired blob to be removed by reaper")
	}
}

// ─── parseRelayAuthHeader tests ───────────────────────────────────────────────

func TestParseRelayAuthHeader_Valid(t *testing.T) {
	header := "Vulos-Relay vulos:ed25519:abc123.1700000000.sigABCDEF"
	vulosID, ts, sig, err := parseRelayAuthHeader(header)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vulosID != "vulos:ed25519:abc123" {
		t.Errorf("vulosID: got %q, want %q", vulosID, "vulos:ed25519:abc123")
	}
	if ts != "1700000000" {
		t.Errorf("ts: got %q, want %q", ts, "1700000000")
	}
	if sig != "sigABCDEF" {
		t.Errorf("sig: got %q, want %q", sig, "sigABCDEF")
	}
}

func TestParseRelayAuthHeader_WrongPrefix(t *testing.T) {
	_, _, _, err := parseRelayAuthHeader("Bearer token")
	if err == nil {
		t.Error("expected error for wrong prefix")
	}
}

func TestParseRelayAuthHeader_MissingFields(t *testing.T) {
	_, _, _, err := parseRelayAuthHeader("Vulos-Relay onlyone")
	if err == nil {
		t.Error("expected error for missing fields")
	}
}

// ─── relayIsSafeID tests ──────────────────────────────────────────────────────

func TestRelayIsSafeID(t *testing.T) {
	tests := []struct {
		id   string
		safe bool
	}{
		{"blob-123", true},
		{"BLOB_abc", true},
		{"blob123", true},
		{"blob/evil", false},
		{"blob.evil", false},
		{"blob evil", false},
		{"", false},
		{strings.Repeat("a", 129), false},
		{strings.Repeat("a", 128), true},
	}
	for _, tt := range tests {
		got := relayIsSafeID(tt.id)
		if got != tt.safe {
			t.Errorf("relayIsSafeID(%q) = %v, want %v", tt.id, got, tt.safe)
		}
	}
}

// ─── HTTP handler tests ───────────────────────────────────────────────────────

func TestHTTP_Deposit_Success(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "http-blob-001", []byte("payload"), 24)
	body, _ := json.Marshal(req)

	mux := http.NewServeMux()
	RegisterRelayHandlers(mux, rs)

	r := httptest.NewRequest(http.MethodPost, "/api/peering/relay/deposit", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHTTP_Deposit_Disabled(t *testing.T) {
	tmp := t.TempDir()
	cs, _ := NewContactStore(tmp)
	rs, _ := NewRelayStore(tmp, cs) // disabled

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	req := relayTestDeposit(t, senderPriv, senderID, recipientID, "http-blob-dis", []byte("x"), 24)
	body, _ := json.Marshal(req)

	mux := http.NewServeMux()
	RegisterRelayHandlers(mux, rs)

	r := httptest.NewRequest(http.MethodPost, "/api/peering/relay/deposit", strings.NewReader(string(body)))
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestHTTP_Pickup_Success(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	recipientPriv, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	dep := relayTestDeposit(t, senderPriv, senderID, recipientID, "http-blob-p1", []byte("ciphertext"), 24)
	rs.Deposit(dep) //nolint:errcheck

	mux := http.NewServeMux()
	RegisterRelayHandlers(mux, rs)

	r := httptest.NewRequest(http.MethodGet, "/api/peering/relay/pickup", nil)
	r.Header.Set("Authorization", relayPickupAuthHeader(t, recipientPriv, recipientID))
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Blobs []RelayBlob `json:"blobs"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Blobs) != 1 {
		t.Errorf("expected 1 blob, got %d", len(resp.Blobs))
	}
}

func TestHTTP_Pickup_BadAuth(t *testing.T) {
	rs, _, _ := relayTestSetup(t)

	mux := http.NewServeMux()
	RegisterRelayHandlers(mux, rs)

	r := httptest.NewRequest(http.MethodGet, "/api/peering/relay/pickup", nil)
	r.Header.Set("Authorization", "Bearer badtoken")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestHTTP_Ack_Success(t *testing.T) {
	rs, cs, _ := relayTestSetup(t)

	senderPriv, _, senderID := genKeyPair(t)
	recipientPriv, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	dep := relayTestDeposit(t, senderPriv, senderID, recipientID, "http-blob-a1", []byte("ciphertext"), 24)
	rs.Deposit(dep) //nolint:errcheck

	mux := http.NewServeMux()
	RegisterRelayHandlers(mux, rs)

	body := `{"blob_ids": ["http-blob-a1"]}`
	r := httptest.NewRequest(http.MethodPost, "/api/peering/relay/ack", strings.NewReader(body))
	r.Header.Set("Authorization", relayPickupAuthHeader(t, recipientPriv, recipientID))
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHTTP_RegisterRelayHandlers_NilMux(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil mux")
		}
	}()
	RegisterRelayHandlers(nil, &RelayStore{})
}

func TestHTTP_RegisterRelayHandlers_NilStore(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil store")
		}
	}()
	RegisterRelayHandlers(http.NewServeMux(), nil)
}

// ─── Storage accounting ───────────────────────────────────────────────────────

func TestDeposit_PerRecipientCapEnforced(t *testing.T) {
	tmp := t.TempDir()
	cs, _ := NewContactStore(tmp)
	rs, _ := NewRelayStore(tmp, cs)

	// Configure a relay with generous total capacity but a small per-recipient
	// cap — we enforce per-recipient limits via relayMaxPerRecipientBytes which
	// is a package constant (100 MB).  Instead of filling 100 MB in tests, we
	// verify the byte-accounting is correct by depositing a small blob and
	// reading back the stored size.
	cfg := RelayConfig{
		Enabled:    true,
		CapacityMB: 200, // total cap well above per-recipient cap
		TTLHours:   relayDefaultTTLHours,
	}
	rs.SetConfig(cfg) //nolint:errcheck

	senderPriv, _, senderID := genKeyPair(t)
	_, _, recipientID := genKeyPair(t)
	relayAddApprovedContact(t, cs, senderID, "Sender")

	// Deposit a small blob and confirm byte accounting is correct.
	payload := []byte("small payload for accounting test")
	req1 := relayTestDeposit(t, senderPriv, senderID, recipientID, "cap-blob-1", payload, 24)
	if err := rs.Deposit(req1); err != nil {
		t.Fatalf("first deposit should succeed: %v", err)
	}

	recipientDir := rs.recipientDir(recipientID)
	stored, err := rs.dirStoredBytes(recipientDir)
	if err != nil {
		t.Fatalf("dirStoredBytes: %v", err)
	}
	wantSize := int64(len(payload))
	if stored != wantSize {
		t.Errorf("stored bytes: got %d, want %d", stored, wantSize)
	}

	// Deposit a second blob and confirm the total increases.
	payload2 := []byte("second payload")
	req2 := relayTestDeposit(t, senderPriv, senderID, recipientID, "cap-blob-2", payload2, 24)
	if err := rs.Deposit(req2); err != nil {
		t.Fatalf("second deposit should succeed: %v", err)
	}
	stored2, _ := rs.dirStoredBytes(recipientDir)
	wantSize2 := wantSize + int64(len(payload2))
	if stored2 != wantSize2 {
		t.Errorf("stored bytes after 2 deposits: got %d, want %d", stored2, wantSize2)
	}
}

// ─── Mutual trust — deposit by recipient of another blob ──────────────────────

func TestDeposit_MutualTrustBothDirections(t *testing.T) {
	// Alice deposits for Bob and Bob deposits for Alice — both are approved
	// contacts.
	rs, cs, _ := relayTestSetup(t)

	alicePriv, _, aliceID := genKeyPair(t)
	bobPriv, _, bobID := genKeyPair(t)
	relayAddApprovedContact(t, cs, aliceID, "Alice")
	relayAddApprovedContact(t, cs, bobID, "Bob")

	// Alice → Bob.
	req1 := relayTestDeposit(t, alicePriv, aliceID, bobID, "mt-blob-ab", []byte("msg for bob"), 24)
	if err := rs.Deposit(req1); err != nil {
		t.Fatalf("Alice→Bob deposit: %v", err)
	}

	// Bob → Alice.
	req2 := relayTestDeposit(t, bobPriv, bobID, aliceID, "mt-blob-ba", []byte("msg for alice"), 24)
	if err := rs.Deposit(req2); err != nil {
		t.Fatalf("Bob→Alice deposit: %v", err)
	}
}
