// vumail_test.go — unit tests for VUMAIL-02.
//
// Tests cover:
//   - Keypair generation and round-trip encryption of the private key.
//   - Identity unlock (decrypt private key with passphrase).
//   - encryptBody / decryptBody round-trip.
//   - signEnvelope / verifyEnvelope.
//   - Send constructs a signed+encrypted envelope (stub relay server).
//   - Receive decrypts and stores in mailbox (in-memory store).
package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ─── Keypair generation ───────────────────────────────────────────────────────

func TestGenerateIdentity(t *testing.T) {
	id, err := GenerateIdentity("alice@vulos.org", "passphrase1")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if id.Address != "alice@vulos.org" {
		t.Errorf("address = %q, want %q", id.Address, "alice@vulos.org")
	}
	if id.PublicKeyB64 == "" {
		t.Error("PublicKeyB64 is empty")
	}
	if id.PrivateKeyEnc == "" {
		t.Error("PrivateKeyEnc is empty")
	}
	if id.priv == nil {
		t.Error("priv is nil after GenerateIdentity")
	}
	pub, err := id.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	if len(pub) != 32 {
		t.Errorf("public key length = %d, want 32", len(pub))
	}
}

// ─── Encrypt / Unlock round-trip ─────────────────────────────────────────────

func TestIdentityEncryptionRoundTrip(t *testing.T) {
	const pass = "super-secret-passphrase"
	id, err := GenerateIdentity("bob@vulos.org", pass)
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	originalPub, _ := id.PublicKey()
	originalPriv := make([]byte, len(id.priv))
	copy(originalPriv, id.priv)

	// Simulate a reload by zeroing the in-memory private key.
	id.priv = nil

	if err := id.Unlock(pass); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if id.priv == nil {
		t.Fatal("priv is nil after Unlock")
	}
	if string(id.priv) != string(originalPriv) {
		t.Error("unlocked private key does not match original")
	}
	unlockedPub, _ := id.PublicKey()
	if string(originalPub) != string(unlockedPub) {
		t.Error("public key changed after unlock")
	}
}

func TestIdentityUnlockWrongPassphrase(t *testing.T) {
	id, err := GenerateIdentity("carol@vulos.org", "correct")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	id.priv = nil
	if err := id.Unlock("wrong"); err == nil {
		t.Fatal("expected error unlocking with wrong passphrase, got nil")
	}
}

// ─── encryptBody / decryptBody round-trip ────────────────────────────────────

func TestBodyEncryptionRoundTrip(t *testing.T) {
	id, err := GenerateIdentity("dave@vulos.org", "pw")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	pub, err := id.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}

	plaintext := []byte("Hello, Dave! This is a secret message.")

	nonce, ct, err := encryptBody(plaintext, pub)
	if err != nil {
		t.Fatalf("encryptBody: %v", err)
	}
	if len(ct) == 0 {
		t.Fatal("ciphertext is empty")
	}

	got, err := decryptBody(ct, nonce, pub, id.priv)
	if err != nil {
		t.Fatalf("decryptBody: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("decrypted = %q, want %q", got, plaintext)
	}
}

func TestBodyEncryptionDifferentNonceEachTime(t *testing.T) {
	id, _ := GenerateIdentity("eve@vulos.org", "pw")
	pub, _ := id.PublicKey()
	plain := []byte("same message")

	n1, ct1, _ := encryptBody(plain, pub)
	n2, ct2, _ := encryptBody(plain, pub)

	if string(n1) == string(n2) {
		t.Error("nonces should differ across calls")
	}
	if string(ct1) == string(ct2) {
		t.Error("ciphertexts should differ across calls (probabilistic encryption)")
	}
}

// ─── Sign / Verify envelope ───────────────────────────────────────────────────

func TestEnvelopeSignVerify(t *testing.T) {
	sender, err := GenerateIdentity("sender@vulos.org", "pw")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}

	env := &mailEnvelope{
		Version:   1,
		From:      sender.Address,
		To:        "recipient@vulos.org",
		Subject:   "Test",
		Timestamp: "2026-01-01T00:00:00Z",
	}

	if err := signEnvelope(env, sender.priv); err != nil {
		t.Fatalf("signEnvelope: %v", err)
	}
	if env.Signature == "" {
		t.Fatal("Signature is empty after signing")
	}

	senderPub, _ := sender.PublicKey()
	if err := verifyEnvelope(env, senderPub); err != nil {
		t.Fatalf("verifyEnvelope: %v", err)
	}
}

func TestEnvelopeVerifyTampered(t *testing.T) {
	sender, _ := GenerateIdentity("s@vulos.org", "pw")
	env := &mailEnvelope{
		Version:   1,
		From:      sender.Address,
		To:        "r@vulos.org",
		Subject:   "Original",
		Timestamp: "2026-01-01T00:00:00Z",
	}
	_ = signEnvelope(env, sender.priv)

	// Tamper with the subject after signing.
	env.Subject = "TAMPERED"

	senderPub, _ := sender.PublicKey()
	if err := verifyEnvelope(env, senderPub); err == nil {
		t.Fatal("expected error verifying tampered envelope, got nil")
	}
}

// ─── Send primitive ───────────────────────────────────────────────────────────

func TestSend(t *testing.T) {
	// Stub relay server — records the delivered envelope.
	var received []byte
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mail/deliver" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		buf := make([]byte, 8192)
		n, _ := r.Body.Read(buf)
		received = buf[:n]
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	sender, err := GenerateIdentity("alice@vulos.org", "pw")
	if err != nil {
		t.Fatalf("GenerateIdentity sender: %v", err)
	}
	recipient, err := GenerateIdentity("bob@vulos.org", "pw2")
	if err != nil {
		t.Fatalf("GenerateIdentity recipient: %v", err)
	}
	recipientPub, _ := recipient.PublicKey()

	svc := New(sender, &Store{}, relay.URL)
	keys := StaticKeyResolver{"bob@vulos.org": recipientPub}

	if err := svc.Send(context.Background(), "bob@vulos.org", "Hello", "Test body", keys); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(received) == 0 {
		t.Fatal("relay received no data")
	}
	var env mailEnvelope
	if err := json.Unmarshal(received, &env); err != nil {
		t.Fatalf("unmarshal relay envelope: %v", err)
	}
	if env.From != "alice@vulos.org" {
		t.Errorf("From = %q, want alice@vulos.org", env.From)
	}
	if env.To != "bob@vulos.org" {
		t.Errorf("To = %q, want bob@vulos.org", env.To)
	}
	if env.Signature == "" {
		t.Error("envelope Signature is empty")
	}
	if env.BodyCiphertext == "" {
		t.Error("envelope BodyCiphertext is empty")
	}
	senderPub, _ := sender.PublicKey()
	if err := verifyEnvelope(&env, senderPub); err != nil {
		t.Fatalf("verify sender signature: %v", err)
	}
}

func TestSendRelayError(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer relay.Close()

	sender, _ := GenerateIdentity("alice@vulos.org", "pw")
	recipient, _ := GenerateIdentity("bob@vulos.org", "pw2")
	recipientPub, _ := recipient.PublicKey()

	svc := New(sender, &Store{}, relay.URL)
	keys := StaticKeyResolver{"bob@vulos.org": recipientPub}

	if err := svc.Send(context.Background(), "bob@vulos.org", "Hi", "body", keys); err == nil {
		t.Fatal("expected error from relay 500, got nil")
	}
}

// ─── Receive primitive ────────────────────────────────────────────────────────

func TestReceive(t *testing.T) {
	sender, err := GenerateIdentity("alice@vulos.org", "pw")
	if err != nil {
		t.Fatalf("GenerateIdentity sender: %v", err)
	}
	recipient, err := GenerateIdentity("bob@vulos.org", "pw2")
	if err != nil {
		t.Fatalf("GenerateIdentity recipient: %v", err)
	}
	recipientPub, _ := recipient.PublicKey()

	body := "Hello Bob! This is a secret."
	nonce, ct, err := encryptBody([]byte(body), recipientPub)
	if err != nil {
		t.Fatalf("encryptBody: %v", err)
	}

	env := mailEnvelope{
		Version:        1,
		From:           sender.Address,
		To:             recipient.Address,
		Subject:        "Hello",
		BodyCiphertext: base64.StdEncoding.EncodeToString(ct),
		SenderNonce:    base64.StdEncoding.EncodeToString(nonce),
		Timestamp:      "2026-01-01T00:00:00Z",
	}
	if err := signEnvelope(&env, sender.priv); err != nil {
		t.Fatalf("signEnvelope: %v", err)
	}

	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}

	svc := New(recipient, &Store{}, "")

	senderPub, _ := sender.PublicKey()
	if err := svc.Receive(context.Background(), envJSON, senderPub); err != nil {
		t.Fatalf("Receive: %v", err)
	}
}

func TestReceiveTamperedEnvelope(t *testing.T) {
	sender, _ := GenerateIdentity("a@vulos.org", "pw")
	recipient, _ := GenerateIdentity("b@vulos.org", "pw2")
	recipientPub, _ := recipient.PublicKey()

	nonce, ct, _ := encryptBody([]byte("secret"), recipientPub)
	env := mailEnvelope{
		Version:        1,
		From:           sender.Address,
		To:             recipient.Address,
		Subject:        "Original",
		BodyCiphertext: base64.StdEncoding.EncodeToString(ct),
		SenderNonce:    base64.StdEncoding.EncodeToString(nonce),
		Timestamp:      "2026-01-01T00:00:00Z",
	}
	_ = signEnvelope(&env, sender.priv)
	env.Subject = "TAMPERED" // invalidate signature

	envJSON, _ := json.Marshal(env)
	svc := New(recipient, &Store{}, "")
	senderPub, _ := sender.PublicKey()
	if err := svc.Receive(context.Background(), envJSON, senderPub); err == nil {
		t.Fatal("expected error for tampered envelope, got nil")
	}
}

// ─── SQLite store round-trip ──────────────────────────────────────────────────

func TestStoreSQLiteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "identity.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	// Persist identity.
	id, err := GenerateIdentity("test@vulos.org", "pw")
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := store.saveIdentity(id); err != nil {
		t.Fatalf("saveIdentity: %v", err)
	}
	loaded, err := store.loadIdentity()
	if err != nil {
		t.Fatalf("loadIdentity: %v", err)
	}
	if loaded == nil {
		t.Fatal("loadIdentity returned nil")
	}
	if loaded.Address != id.Address {
		t.Errorf("address = %q, want %q", loaded.Address, id.Address)
	}
	if loaded.PublicKeyB64 != id.PublicKeyB64 {
		t.Error("PublicKeyB64 mismatch")
	}
	if loaded.PrivateKeyEnc == "" {
		t.Error("PrivateKeyEnc is empty after round-trip")
	}

	// Persist mail message.
	msg := &MailMessage{
		ID:            "test-msg-1",
		FromAddress:   "other@vulos.org",
		Subject:       "Test subject",
		BodyEncrypted: []byte("encrypted-body"),
		ReceivedAt:    time.Now().UTC().Truncate(time.Second),
		Read:          false,
	}
	if err := store.saveMailMessage(msg); err != nil {
		t.Fatalf("saveMailMessage: %v", err)
	}
	msgs, err := store.listMailbox()
	if err != nil {
		t.Fatalf("listMailbox: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("mailbox length = %d, want 1", len(msgs))
	}
	if msgs[0].Subject != "Test subject" {
		t.Errorf("subject = %q", msgs[0].Subject)
	}

	// Persist outbox message.
	out := &OutboxMessage{
		ID:            "outbox-1",
		ToAddress:     "other@vulos.org",
		Subject:       "Outgoing",
		BodyEncrypted: []byte("enc"),
		QueuedAt:      time.Now().UTC().Truncate(time.Second),
		Status:        "queued",
	}
	if err := store.saveOutboxMessage(out); err != nil {
		t.Fatalf("saveOutboxMessage: %v", err)
	}
	outs, err := store.listOutbox()
	if err != nil {
		t.Fatalf("listOutbox: %v", err)
	}
	if len(outs) != 1 {
		t.Fatalf("outbox length = %d, want 1", len(outs))
	}
	if outs[0].Status != "queued" {
		t.Errorf("status = %q", outs[0].Status)
	}
}

func TestStoreEmptyPath(t *testing.T) {
	store, err := NewStore("")
	if err != nil {
		t.Fatalf("NewStore empty path: %v", err)
	}
	if store == nil {
		t.Fatal("store is nil")
	}
	_ = store.Close()
}

// Ensure os import is used (for t.TempDir context).
var _ = os.TempDir
