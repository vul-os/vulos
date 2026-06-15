package passkeys

// login_test.go — LOGINISO-01 + LOGINISO-02 unit tests.
//
// Tests:
//   - TestLoginService_BeginLoginUnknownUser — BeginLogin for non-existent user errors.
//   - TestLoginService_FinishLoginUnknownUser — FinishLogin for non-existent user errors.
//   - TestLoginService_RegistrationRoundTrip — register passkey, then login → session issued.
//   - TestQRLogin_ApproveAndPoll — happy-path: begin → approve → poll (LOGINISO-02).
//   - TestQRLogin_Expiry — challenge TTL enforced on Approve + Poll.
//   - TestQRLogin_SingleUse — second Approve on the same challenge is rejected.
//   - TestQRLogin_PollUnknown — polling an unknown challenge returns ErrQRChallengeNotFound.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	wa "github.com/go-webauthn/webauthn/webauthn"

	"vulos/backend/services/auth"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func newTestAuthStore(t *testing.T) *auth.Store {
	t.Helper()
	dir, err := os.MkdirTemp("", "auth-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dbDir := filepath.Join(dir, "db")
	os.MkdirAll(dbDir, 0700)
	store, err := auth.NewStore(dbDir)
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	return store
}

func newTestLoginService(t *testing.T) (*LoginService, *auth.Store) {
	t.Helper()
	svc := newTestService(t)
	store := newTestAuthStore(t)
	return NewLoginService(svc, store), store
}

// sealAndStoreKey directly seals a freshly-generated credential into the
// passkeys vault for userID and returns the private key + credID so the test
// can build a valid assertion response.
func sealAndStoreKey(t *testing.T, svc *Service, userID string) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	credID := make([]byte, 16)
	rand.Read(credID)

	cred := &wa.Credential{
		ID:              credID,
		PublicKey:       marshalCOSEKey(t, privKey),
		AttestationType: "none",
		Flags:           wa.CredentialFlags{UserPresent: true},
	}
	if err := svc.au12SealAndStore(userID, cred); err != nil {
		t.Fatalf("au12SealAndStore: %v", err)
	}
	return privKey, credID
}

// ─── LOGINISO-01 tests ────────────────────────────────────────────────────────

// TestLoginService_BeginLoginUnknownUser asserts that BeginLogin returns an
// error when the username does not exist in the auth store.
func TestLoginService_BeginLoginUnknownUser(t *testing.T) {
	ls, _ := newTestLoginService(t)
	_, _, err := ls.BeginLogin("ghost-user")
	if err == nil {
		t.Fatal("BeginLogin for unknown user should have returned an error")
	}
}

// TestLoginService_FinishLoginUnknownUser asserts that FinishLogin returns an
// error when the username does not exist in the auth store.
func TestLoginService_FinishLoginUnknownUser(t *testing.T) {
	ls, _ := newTestLoginService(t)
	_, err := ls.FinishLogin("ghost-user", []byte(`{}`), []byte(`{"token":"x"}`))
	if err == nil {
		t.Fatal("FinishLogin for unknown user should have returned an error")
	}
}

// TestLoginService_RegistrationRoundTrip verifies that:
//  1. A user is created in the auth store.
//  2. A passkey credential is seeded into the vault.
//  3. BeginLogin + FinishLogin produces a valid auth.Session.
func TestLoginService_RegistrationRoundTrip(t *testing.T) {
	ls, store := newTestLoginService(t)
	username := "pk-login-user-1"

	// Create the user in auth store.
	user, err := store.Register(username, "test-pw-1234", username)
	if err != nil {
		t.Fatalf("Register user %q: %v", username, err)
	}

	// Seal a credential into the vault for this user so assertion can succeed.
	privKey, credID := sealAndStoreKey(t, ls.svc, user.ID)

	// ── Begin login ──────────────────────────────────────────────────────────
	challenge, sessionData, err := ls.BeginLogin(username)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if len(challenge) == 0 || len(sessionData) == 0 {
		t.Fatal("BeginLogin returned empty challenge or sessionData")
	}

	var opts struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(challenge, &opts); err != nil {
		t.Fatalf("unmarshal challenge: %v", err)
	}
	rawChallenge, err := base64.RawURLEncoding.DecodeString(opts.PublicKey.Challenge)
	if err != nil {
		t.Fatalf("decode challenge: %v", err)
	}

	// Build a valid assertion response using the private key we seeded.
	assertResp := buildAssertionResponse(t, credID, privKey, rawChallenge,
		ls.svc.rpID, ls.svc.origins[0], 1)

	// ── Finish login ─────────────────────────────────────────────────────────
	sess, err := ls.FinishLogin(username, assertResp, sessionData)
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if sess == nil {
		t.Fatal("FinishLogin returned nil session")
	}
	if sess.Token == "" {
		t.Fatal("session token is empty")
	}
	if sess.UserID != user.ID {
		t.Fatalf("session.UserID = %q, want %q", sess.UserID, user.ID)
	}

	// Verify the session token is valid in the auth store.
	storedSess, ok := store.ValidateToken(sess.Token)
	if !ok {
		t.Fatal("session token not valid in auth store after FinishLogin")
	}
	if storedSess.UserID != user.ID {
		t.Fatalf("stored session.UserID = %q, want %q", storedSess.UserID, user.ID)
	}
}

// ─── LOGINISO-02 tests ────────────────────────────────────────────────────────

func newTestQRService(t *testing.T) (*QRLoginService, *auth.Store) {
	t.Helper()
	store := newTestAuthStore(t)
	return NewQRLoginService(store), store
}

// extractNonce parses the nonce from a QRBeginResult.QRData payload.
// qr_data = "vulos://qr-login/" + base64url(json)
func extractNonce(t *testing.T, qrData string) string {
	t.Helper()
	const prefix = "vulos://qr-login/"
	if len(qrData) <= len(prefix) {
		t.Fatalf("qr_data too short: %q", qrData)
	}
	b, err := base64.RawURLEncoding.DecodeString(qrData[len(prefix):])
	if err != nil {
		t.Fatalf("decode qr_data payload: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("unmarshal qr_data payload: %v", err)
	}
	nonce := payload["nonce"]
	if nonce == "" {
		t.Fatal("nonce missing from qr_data payload")
	}
	return nonce
}

// TestQRLogin_ApproveAndPoll verifies the happy-path: begin → approve → poll.
func TestQRLogin_ApproveAndPoll(t *testing.T) {
	qr, store := newTestQRService(t)

	user, err := store.Register("qr-approver-1", "pw-1234-extra!", "QR Approver")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	result, err := qr.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if result.ChallengeID == "" {
		t.Fatal("ChallengeID is empty")
	}
	if result.QRData == "" {
		t.Fatal("QRData is empty")
	}
	if !result.ExpiresAt.After(time.Now()) {
		t.Fatal("ExpiresAt is not in the future")
	}

	// Extract nonce from the QR payload (as the phone app would).
	nonce := extractNonce(t, result.QRData)

	// Authenticated phone approves the challenge — must supply the nonce.
	if err := qr.Approve(result.ChallengeID, user.ID, nonce); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	// Kiosk polls.
	poll, err := qr.Poll(result.ChallengeID)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !poll.Approved {
		t.Fatal("Poll.Approved should be true after approval")
	}
	if poll.SessionToken() == "" {
		t.Fatal("Poll.SessionToken() is empty after approval")
	}
	if poll.Pending || poll.Expired {
		t.Fatal("Poll should not have Pending or Expired set after approval")
	}

	// The session token must be valid in the auth store.
	sess, ok := store.ValidateToken(poll.SessionToken())
	if !ok {
		t.Fatal("session token returned from QR poll is not valid in auth store")
	}
	if sess.UserID != user.ID {
		t.Fatalf("session.UserID = %q, want %q", sess.UserID, user.ID)
	}

	// Challenge must be cleaned up: a second poll must return ErrQRChallengeNotFound.
	_, err = qr.Poll(result.ChallengeID)
	if err == nil {
		t.Fatal("second Poll after approved challenge should return an error (challenge not cleaned up)")
	}
}

// TestQRLogin_NonceMismatch verifies that Approve rejects a wrong nonce
// (QRSEC-01 — shoulder-surfer protection).
func TestQRLogin_NonceMismatch(t *testing.T) {
	qr, store := newTestQRService(t)

	user, _ := store.Register("qr-nonce-user", "pw-1234-extra!", "Nonce User")

	result, err := qr.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// Approve with the wrong nonce (simulates knowing challenge_id but not the payload).
	err = qr.Approve(result.ChallengeID, user.ID, "wrong-nonce-value")
	if err != ErrQRNonceMismatch {
		t.Fatalf("Approve with wrong nonce: got %v, want ErrQRNonceMismatch", err)
	}

	// After a failed nonce attempt the challenge must still be pending (not consumed).
	poll, err := qr.Poll(result.ChallengeID)
	if err != nil {
		t.Fatalf("Poll after nonce mismatch: %v", err)
	}
	if !poll.Pending {
		t.Fatal("challenge should still be Pending after a nonce-mismatch Approve attempt")
	}
}

// TestQRLogin_EmptyNonceRejected verifies that an empty nonce is rejected.
func TestQRLogin_EmptyNonceRejected(t *testing.T) {
	qr, store := newTestQRService(t)

	user, _ := store.Register("qr-empty-nonce", "pw-1234-extra!", "Empty Nonce")

	result, err := qr.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	err = qr.Approve(result.ChallengeID, user.ID, "")
	if err != ErrQRNonceMismatch {
		t.Fatalf("Approve with empty nonce: got %v, want ErrQRNonceMismatch", err)
	}
}

// TestQRLogin_NonceInQRPayload verifies that the nonce is present in qr_data
// and is non-empty (ensures Begin() always embeds a nonce for the phone to use).
func TestQRLogin_NonceInQRPayload(t *testing.T) {
	qr, _ := newTestQRService(t)

	result, err := qr.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	nonce := extractNonce(t, result.QRData)
	if len(nonce) < 10 {
		t.Errorf("nonce in qr_data looks too short: %q (expected cryptographic random token)", nonce)
	}

	// A second Begin() must produce a different nonce.
	result2, err := qr.Begin()
	if err != nil {
		t.Fatalf("Begin (second): %v", err)
	}
	nonce2 := extractNonce(t, result2.QRData)
	if nonce == nonce2 {
		t.Error("two successive Begin() calls returned the same nonce — nonces must be unique")
	}
}

// TestQRLogin_Expiry verifies that an expired challenge is rejected on Approve
// and returns Expired=true on Poll.
func TestQRLogin_Expiry(t *testing.T) {
	qr, store := newTestQRService(t)

	user, _ := store.Register("qr-exp-user", "pw-1234-extra!", "Exp User")

	result, err := qr.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	nonce := extractNonce(t, result.QRData)

	// Wind the expiry back to force expiration.
	qr.mu.Lock()
	ch := qr.challenges[result.ChallengeID]
	qr.mu.Unlock()

	ch.mu.Lock()
	ch.expiresAt = time.Now().Add(-time.Second)
	ch.mu.Unlock()

	// Approve must fail with ErrQRChallengeExpired.
	err = qr.Approve(result.ChallengeID, user.ID, nonce)
	if err != ErrQRChallengeExpired {
		t.Fatalf("Approve on expired challenge: got %v, want ErrQRChallengeExpired", err)
	}

	// Poll must return Expired=true.
	poll, err := qr.Poll(result.ChallengeID)
	if err != nil {
		t.Fatalf("Poll on expired challenge: %v", err)
	}
	if !poll.Expired {
		t.Fatal("Poll.Expired should be true for an expired challenge")
	}
	if poll.Approved || poll.Pending {
		t.Fatal("Poll should not be Approved or Pending for expired challenge")
	}
}

// TestQRLogin_SingleUse verifies that a challenge cannot be approved twice
// (single-use enforcement).
func TestQRLogin_SingleUse(t *testing.T) {
	qr, store := newTestQRService(t)

	user, _ := store.Register("qr-singleuse-user", "pw-1234-extra!", "Single Use")

	result, err := qr.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	nonce := extractNonce(t, result.QRData)

	// First approval must succeed.
	if err := qr.Approve(result.ChallengeID, user.ID, nonce); err != nil {
		t.Fatalf("first Approve: %v", err)
	}

	// Second approval must fail with ErrQRChallengeAlreadyUsed.
	err = qr.Approve(result.ChallengeID, user.ID, nonce)
	if err != ErrQRChallengeAlreadyUsed {
		t.Fatalf("second Approve: got %v, want ErrQRChallengeAlreadyUsed", err)
	}
}

// TestQRLogin_PollUnknown verifies that polling an unknown challenge_id returns
// ErrQRChallengeNotFound.
func TestQRLogin_PollUnknown(t *testing.T) {
	qr, _ := newTestQRService(t)

	_, err := qr.Poll("nonexistent-challenge-id-xyz")
	if err != ErrQRChallengeNotFound {
		t.Fatalf("Poll unknown id: got %v, want ErrQRChallengeNotFound", err)
	}
}
