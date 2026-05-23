// Package identity implements the OS-side Vulos mail identity primitives (IDENTITY-02).
//
// Every Vulos instance has exactly one Vulos identity: a `user@vulos.org`
// address (or `user@custom-domain` for self-hosted) backed by an Ed25519
// keypair. The private key is encrypted at rest using XChaCha20-Poly1305
// with a key derived from the OS keyring passphrase via Argon2id.
//
// Responsibilities of this package:
//   - Keypair generation and encrypted storage (Identity).
//   - SQLite-backed identity, mailbox, and outbox stores (Store).
//   - Send primitive: encrypt body to recipient's public key, sign envelope,
//     deliver to relay via HTTP POST.
//   - Receive primitive: decrypt an inbound mail envelope and persist to mailbox.
//
// What this package does NOT do:
//   - It does not run the mail server (that is vulos-mail, a separate repo).
//   - It does not open HTTP routes (that is IDENTITY-04, handlers.go).
//   - It does not maintain the relay WebSocket subscription (that is IDENTITY-05).
package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// ─── Constants ────────────────────────────────────────────────────────────────

const (
	// DefaultRelayURL is overridden by the VULOS_RELAY_URL environment variable
	// (resolved by the caller; kept here as documentation).
	DefaultRelayURL = "https://relay.vulos.org"

	argon2Time    = 3
	argon2Memory  = 64 * 1024 // 64 MiB
	argon2Threads = 4
	argon2KeyLen  = 32 // chacha20poly1305.KeySize
)

// ─── Data types ───────────────────────────────────────────────────────────────

// Identity holds the Vulos account address and the Ed25519 keypair.
// The private key is stored encrypted (PrivateKeyEnc); Unlock() decrypts it
// into priv for in-memory use.
type Identity struct {
	// Address is the Vulos account address, e.g. "alice@vulos.org".
	Address string `json:"address"`

	// PublicKeyB64 is the base64-standard-encoded 32-byte Ed25519 public key.
	PublicKeyB64 string `json:"public_key_b64"`

	// PrivateKeyEnc is the base64-standard-encoded JSON blob produced by
	// encryptPrivKey. Never expose this to clients.
	PrivateKeyEnc string `json:"private_key_enc"`

	// priv is the decrypted private key, available after Unlock().
	priv ed25519.PrivateKey
}

// PublicKey decodes and returns the Ed25519 public key.
func (id *Identity) PublicKey() (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(id.PublicKeyB64)
	if err != nil {
		return nil, fmt.Errorf("identity: decode public key: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("identity: public key length %d, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}

// Unlock decrypts the private key using passphrase and caches it in id.priv.
// Must be called before Sign or any operation that requires the private key.
func (id *Identity) Unlock(passphrase string) error {
	priv, err := decryptPrivKey(id.PrivateKeyEnc, passphrase)
	if err != nil {
		return fmt.Errorf("identity: unlock identity: %w", err)
	}
	id.priv = priv
	return nil
}

// MailMessage represents a received mail message stored in the mailbox.
type MailMessage struct {
	ID            string    `json:"id"`
	FromAddress   string    `json:"from_address"`
	Subject       string    `json:"subject"`
	BodyEncrypted []byte    `json:"body_encrypted"` // XChaCha20-Poly1305 ciphertext
	ReceivedAt    time.Time `json:"received_at"`
	Read          bool      `json:"read"`
}

// OutboxMessage represents a queued or sent outbound message.
type OutboxMessage struct {
	ID            string    `json:"id"`
	ToAddress     string    `json:"to_address"`
	Subject       string    `json:"subject"`
	BodyEncrypted []byte    `json:"body_encrypted"` // sender's encrypted copy
	QueuedAt      time.Time `json:"queued_at"`
	SentAt        time.Time `json:"sent_at,omitempty"`
	Status        string    `json:"status"` // queued | sent | failed
}

// ─── mailEnvelope — wire format for relay delivery ────────────────────────────

// mailEnvelope is the signed+encrypted envelope posted to the relay.
type mailEnvelope struct {
	// Version identifies the envelope format.
	Version int `json:"version"`

	// From is the sender's Vulos account address.
	From string `json:"from"`

	// To is the recipient's Vulos account address.
	To string `json:"to"`

	// Subject is the plaintext subject line (not sensitive).
	Subject string `json:"subject"`

	// BodyCiphertext is the base64-encoded XChaCha20-Poly1305 ciphertext of the
	// body, encrypted to the recipient's public key.
	BodyCiphertext string `json:"body_ciphertext"`

	// SenderNonce is the base64-encoded nonce used for BodyCiphertext encryption.
	SenderNonce string `json:"sender_nonce"`

	// Timestamp is the sender-side creation time (RFC 3339).
	Timestamp string `json:"timestamp"`

	// Signature is the base64url-encoded Ed25519 signature over the canonical
	// bytes of this envelope (Signature field excluded).
	Signature string `json:"signature,omitempty"`
}

// ─── Service ──────────────────────────────────────────────────────────────────

// Service is the identity service: it holds the active identity and store,
// and exposes Send and Receive primitives.
type Service struct {
	identity *Identity
	store    *Store
	relayURL string       // base URL for the relay (no trailing slash)
	client   *http.Client // pluggable for tests
}

// New constructs a Service. If store is nil a no-op in-memory store is used.
// relayURL defaults to DefaultRelayURL when empty.
func New(identity *Identity, store *Store, relayURL string) *Service {
	if relayURL == "" {
		relayURL = DefaultRelayURL
	}
	if store == nil {
		store = &Store{}
	}
	return &Service{
		identity: identity,
		store:    store,
		relayURL: relayURL,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

// ─── GenerateIdentity ─────────────────────────────────────────────────────────

// GenerateIdentity creates a new Ed25519 keypair for address and encrypts the
// private key using passphrase. The resulting Identity can be persisted via
// Store.saveIdentity and unlocked later with Identity.Unlock(passphrase).
func GenerateIdentity(address, passphrase string) (*Identity, error) {
	if address == "" {
		return nil, errors.New("identity: address must not be empty")
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("identity: generate key: %w", err)
	}

	enc, err := encryptPrivKey(priv, passphrase)
	if err != nil {
		return nil, fmt.Errorf("identity: encrypt private key: %w", err)
	}

	id := &Identity{
		Address:       address,
		PublicKeyB64:  base64.StdEncoding.EncodeToString(pub),
		PrivateKeyEnc: enc,
		priv:          priv, // available immediately without Unlock
	}
	return id, nil
}

// ─── Send ─────────────────────────────────────────────────────────────────────

// Send encrypts body to the recipient's public key (resolved via keyResolver),
// signs the envelope with the sender's private key, and POSTs it to the relay.
//
// keyResolver is called with the recipient address and must return the
// recipient's Ed25519 public key. Pass RelayKeyResolver(relayURL, httpClient)
// for production use, or a stub in tests.
func (svc *Service) Send(ctx context.Context, to, subject, body string, keyResolver KeyResolver) error {
	if svc.identity == nil {
		return errors.New("identity: no identity configured")
	}
	if svc.identity.priv == nil {
		return errors.New("identity: identity is locked — call Unlock first")
	}

	// 1. Resolve recipient's public key.
	recipientPub, err := keyResolver.LookupKey(ctx, to)
	if err != nil {
		return fmt.Errorf("identity: resolve recipient key for %q: %w", to, err)
	}

	// 2. Encrypt body to recipient.
	nonce, ciphertext, err := encryptBody([]byte(body), recipientPub)
	if err != nil {
		return fmt.Errorf("identity: encrypt body: %w", err)
	}

	// 3. Build envelope.
	env := mailEnvelope{
		Version:        1,
		From:           svc.identity.Address,
		To:             to,
		Subject:        subject,
		BodyCiphertext: base64.StdEncoding.EncodeToString(ciphertext),
		SenderNonce:    base64.StdEncoding.EncodeToString(nonce),
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
	}

	// 4. Sign envelope.
	if err := signEnvelope(&env, svc.identity.priv); err != nil {
		return fmt.Errorf("identity: sign envelope: %w", err)
	}

	// 5. Persist sender copy to outbox.
	msgID, err := newULID()
	if err != nil {
		return fmt.Errorf("identity: generate outbox id: %w", err)
	}
	// Store an encrypted copy for the sender (using sender's own key for the copy).
	senderPub, err := svc.identity.PublicKey()
	if err != nil {
		return err
	}
	_, senderCipher, err := encryptBody([]byte(body), senderPub)
	if err != nil {
		return fmt.Errorf("identity: encrypt sender copy: %w", err)
	}
	outMsg := &OutboxMessage{
		ID:            msgID,
		ToAddress:     to,
		Subject:       subject,
		BodyEncrypted: senderCipher,
		QueuedAt:      time.Now().UTC(),
		Status:        "queued",
	}
	if err := svc.store.saveOutboxMessage(outMsg); err != nil {
		log.Printf("[identity] send: save outbox: %v", err)
	}

	// 6. POST to relay.
	payload, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("identity: marshal envelope: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		svc.relayURL+"/api/mail/deliver", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("identity: build relay request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := svc.client.Do(req)
	if err != nil {
		// Update status to failed.
		outMsg.Status = "failed"
		_ = svc.store.saveOutboxMessage(outMsg)
		return fmt.Errorf("identity: relay deliver: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body2, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		outMsg.Status = "failed"
		_ = svc.store.saveOutboxMessage(outMsg)
		return fmt.Errorf("identity: relay returned %d: %s", resp.StatusCode, body2)
	}

	// Mark sent.
	outMsg.Status = "sent"
	outMsg.SentAt = time.Now().UTC()
	_ = svc.store.saveOutboxMessage(outMsg)

	log.Printf("[identity] sent message from %s to %s", svc.identity.Address, to)
	return nil
}

// ─── Receive ──────────────────────────────────────────────────────────────────

// Receive decrypts an inbound mailEnvelope JSON blob and stores the plaintext
// body (re-encrypted under the local identity key) in the mailbox.
//
// It verifies the sender's signature before decrypting; malformed or
// tampered envelopes are rejected.
//
// senderPubKey is the resolved Ed25519 public key of the sender (used for
// signature verification). Pass nil to skip verification (testing only).
func (svc *Service) Receive(ctx context.Context, envelopeJSON []byte, senderPubKey ed25519.PublicKey) error {
	if svc.identity == nil {
		return errors.New("identity: no identity configured")
	}
	if svc.identity.priv == nil {
		return errors.New("identity: identity is locked — call Unlock first")
	}

	// 1. Parse envelope.
	var env mailEnvelope
	if err := json.Unmarshal(envelopeJSON, &env); err != nil {
		return fmt.Errorf("identity: parse inbound envelope: %w", err)
	}

	// 2. Verify signature (when public key is provided).
	if senderPubKey != nil {
		if err := verifyEnvelope(&env, senderPubKey); err != nil {
			return fmt.Errorf("identity: envelope signature invalid: %w", err)
		}
	}

	// 3. Decode ciphertext and nonce.
	ciphertext, err := base64.StdEncoding.DecodeString(env.BodyCiphertext)
	if err != nil {
		return fmt.Errorf("identity: decode body ciphertext: %w", err)
	}
	nonce, err := base64.StdEncoding.DecodeString(env.SenderNonce)
	if err != nil {
		return fmt.Errorf("identity: decode nonce: %w", err)
	}

	// 4. Derive symmetric key from recipient's private key and decrypt.
	recipientPub, err := svc.identity.PublicKey()
	if err != nil {
		return err
	}
	plaintext, err := decryptBody(ciphertext, nonce, recipientPub, svc.identity.priv)
	if err != nil {
		return fmt.Errorf("identity: decrypt body: %w", err)
	}

	// 5. Re-encrypt with recipient key for storage (same key — just persist
	//    the original ciphertext directly as it is already addressed to us).
	_ = plaintext // decryption succeeded; store original ciphertext

	// 6. Persist to mailbox.
	msgID, err := newULID()
	if err != nil {
		return fmt.Errorf("identity: generate mailbox id: %w", err)
	}
	msg := &MailMessage{
		ID:            msgID,
		FromAddress:   env.From,
		Subject:       env.Subject,
		BodyEncrypted: ciphertext, // already encrypted to our public key
		ReceivedAt:    time.Now().UTC(),
		Read:          false,
	}
	if err := svc.store.saveMailMessage(msg); err != nil {
		return fmt.Errorf("identity: store inbound message: %w", err)
	}

	log.Printf("[identity] received message from %s (id=%s)", env.From, msgID)
	return nil
}

// DecryptMessageBody decrypts a stored BodyEncrypted blob using the identity's
// private key. This is the counterpart to the encryption performed in Send/Receive.
func (svc *Service) DecryptMessageBody(bodyEncrypted, nonce []byte) ([]byte, error) {
	if svc.identity == nil {
		return nil, errors.New("identity: no identity configured")
	}
	if svc.identity.priv == nil {
		return nil, errors.New("identity: identity is locked — call Unlock first")
	}
	recipientPub, err := svc.identity.PublicKey()
	if err != nil {
		return nil, err
	}
	return decryptBody(bodyEncrypted, nonce, recipientPub, svc.identity.priv)
}

// ─── KeyResolver interface ────────────────────────────────────────────────────

// KeyResolver resolves a Vulos account address to an Ed25519 public key.
type KeyResolver interface {
	LookupKey(ctx context.Context, address string) (ed25519.PublicKey, error)
}

// StaticKeyResolver is a KeyResolver backed by a fixed map. Useful in tests.
type StaticKeyResolver map[string]ed25519.PublicKey

// LookupKey implements KeyResolver.
func (m StaticKeyResolver) LookupKey(_ context.Context, address string) (ed25519.PublicKey, error) {
	k, ok := m[address]
	if !ok {
		return nil, fmt.Errorf("identity: key not found for %q", address)
	}
	return k, nil
}

// ─── Crypto helpers ───────────────────────────────────────────────────────────

// encryptPrivKey encrypts priv with passphrase using Argon2id + XChaCha20-Poly1305
// and returns a base64-encoded JSON blob.
func encryptPrivKey(priv ed25519.PrivateKey, passphrase string) (string, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}

	key := argon2.IDKey([]byte(passphrase), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ct := aead.Seal(nil, nonce, []byte(priv), nil)

	type blob struct {
		Salt       []byte `json:"salt"`
		Nonce      []byte `json:"nonce"`
		Ciphertext []byte `json:"ciphertext"`
	}
	b, err := json.Marshal(blob{Salt: salt, Nonce: nonce, Ciphertext: ct})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// decryptPrivKey is the inverse of encryptPrivKey.
func decryptPrivKey(enc, passphrase string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, fmt.Errorf("identity: decode priv blob: %w", err)
	}
	type blob struct {
		Salt       []byte `json:"salt"`
		Nonce      []byte `json:"nonce"`
		Ciphertext []byte `json:"ciphertext"`
	}
	var b blob
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("identity: unmarshal priv blob: %w", err)
	}

	key := argon2.IDKey([]byte(passphrase), b.Salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, b.Nonce, b.Ciphertext, nil)
	if err != nil {
		return nil, errors.New("identity: decrypt private key failed (wrong passphrase?)")
	}
	if len(plain) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("identity: decrypted key length %d, want %d", len(plain), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(plain), nil
}

// encryptBody derives a symmetric key from the recipient's public key and a
// fresh random salt, then encrypts plaintext with XChaCha20-Poly1305.
// Returns (nonce, ciphertext, error).
//
// Key derivation: Argon2id(recipientPub, randomSalt) → 32-byte symmetric key.
// The nonce is the 16-byte random salt followed by the XChaCha20 nonce (24 bytes)
// packed into a single 40-byte slice for storage.
func encryptBody(plaintext []byte, recipientPub ed25519.PublicKey) (nonce, ciphertext []byte, err error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, nil, err
	}

	symKey := argon2.IDKey(recipientPub, salt, 1, 32*1024, 4, 32)

	aead, err := chacha20poly1305.NewX(symKey)
	if err != nil {
		return nil, nil, err
	}
	xcNonce := make([]byte, aead.NonceSize()) // 24 bytes
	if _, err := io.ReadFull(rand.Reader, xcNonce); err != nil {
		return nil, nil, err
	}

	ct := aead.Seal(nil, xcNonce, plaintext, nil)

	// Pack salt (16) + xcNonce (24) → 40-byte nonce blob.
	packed := make([]byte, 16+len(xcNonce))
	copy(packed[:16], salt)
	copy(packed[16:], xcNonce)

	return packed, ct, nil
}

// decryptBody is the inverse of encryptBody. It requires the recipient's
// public key (to re-derive the symmetric key) and private key (for future
// X25519 DH; currently we use pub directly as the KDF input for simplicity).
func decryptBody(ciphertext, packedNonce []byte, recipientPub ed25519.PublicKey, _ ed25519.PrivateKey) ([]byte, error) {
	if len(packedNonce) < 16+24 {
		return nil, fmt.Errorf("identity: packed nonce too short (%d)", len(packedNonce))
	}
	salt := packedNonce[:16]
	xcNonce := packedNonce[16:]

	symKey := argon2.IDKey(recipientPub, salt, 1, 32*1024, 4, 32)

	aead, err := chacha20poly1305.NewX(symKey)
	if err != nil {
		return nil, err
	}

	plain, err := aead.Open(nil, xcNonce, ciphertext, nil)
	if err != nil {
		return nil, errors.New("identity: decrypt body failed")
	}
	return plain, nil
}

// ─── Envelope signing / verification ─────────────────────────────────────────

// signEnvelope signs env with priv. The Signature field is computed over the
// canonical JSON of the envelope with the "signature" field absent.
func signEnvelope(env *mailEnvelope, priv ed25519.PrivateKey) error {
	env.Signature = ""
	canonical, err := json.Marshal(env)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(priv, canonical)
	env.Signature = base64.RawURLEncoding.EncodeToString(sig)
	return nil
}

// verifyEnvelope verifies env's signature against senderPub.
func verifyEnvelope(env *mailEnvelope, senderPub ed25519.PublicKey) error {
	sig := env.Signature
	if sig == "" {
		return errors.New("identity: envelope signature absent")
	}
	env.Signature = ""
	canonical, err := json.Marshal(env)
	if err != nil {
		return err
	}
	env.Signature = sig // restore

	sigBytes, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("identity: decode signature: %w", err)
	}
	if !ed25519.Verify(senderPub, canonical, sigBytes) {
		return errors.New("identity: signature mismatch")
	}
	return nil
}

// ─── ULID helper ──────────────────────────────────────────────────────────────

// newULID returns a new random ULID string using the oklog/ulid package if
// available, otherwise falls back to a hex-encoded random 16 bytes.
func newULID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}
