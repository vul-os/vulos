// webauthn_wave42_test.go -- end-to-end Finish* ceremony coverage (WAVE-42).
//
// Wave-40 flagged FinishWebAuthnRegistration / FinishWebAuthnLogin /
// FinishWebAuthnLoginNoSession / storeWebAuthnCredential at 0% coverage: they
// require a real WebAuthn attestation object + a valid ECDSA assertion signature
// that go-webauthn cryptographically verifies, which cannot be faked without an
// authenticator. This file drives them through the software virtualAuthenticator
// (see webauthn_virtual_authenticator_test.go) over a test RP/origin.
//
// Covered (server-side, cryptographically real):
//   - Registration Begin -> attestation -> Finish stores credential
//     (credential id / public key / sign-count / aaguid persisted).
//   - Login Begin -> assertion -> Finish succeeds and issues a session.
//   - FinishWebAuthnLoginNoSession (passkey-as-2FA) happy path.
//   - Rejections: tampered signature, wrong/replayed challenge, sign-count
//     regression (cloned authenticator), wrong origin, unknown credential.
//
// NOT covered (still browser-only): navigator.credentials.{create,get}, user
// gestures/biometrics, real hardware attestation certificate chains, and the
// route-handler layer that persists SessionData between Begin and Finish.
package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

// waTestRPID / waTestOrigin match setupWebAuthnEnv so the virtual authenticator's
// rpIDHash and clientData origin verify against the same RP the Store uses.
const (
	waTestRPID    = "localhost"
	waTestOrigin  = "http://localhost:5173"
	waWrongOrigin = "https://evil.example.com"
)

// sessionChallenge adapts a *webauthn.SessionData to the challenge() interface
// the virtual authenticator consumes. SessionData.Challenge is the base64url
// challenge string that must be echoed verbatim in clientDataJSON.
type sessionChallenge struct{ s *webauthn.SessionData }

func (c sessionChallenge) challenge() string { return c.s.Challenge }

// finishRequest wraps a JSON body in an *http.Request the Finish* methods parse.
func finishRequest(body []byte) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, waTestOrigin, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// registerViaVirtualAuthenticator runs Begin -> attestation -> Finish and
// returns the stored credential plus the authenticator (for later login).
func registerViaVirtualAuthenticator(t *testing.T, st *Store, u *User, name string) (*virtualAuthenticator, *WebAuthnCredential) {
	t.Helper()
	ctx := context.Background()

	_, session, err := st.BeginWebAuthnRegistration(ctx, u)
	if err != nil {
		t.Fatalf("BeginWebAuthnRegistration: %v", err)
	}

	va := newVirtualAuthenticator(t, waTestRPID, waTestOrigin)
	body := va.makeAttestationResponse(t, sessionChallenge{session}, "")

	cred, err := st.FinishWebAuthnRegistration(ctx, u, *session, finishRequest(body), name)
	if err != nil {
		t.Fatalf("FinishWebAuthnRegistration: %v", err)
	}
	return va, cred
}

// ─────────────────────────────────────────────────────────────────────────────
// Registration: end-to-end
// ─────────────────────────────────────────────────────────────────────────────

func TestWebAuthnE2E_Registration_StoresCredential(t *testing.T) {
	setupWebAuthnEnv(t)
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "e2ereg@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	va, cred := registerViaVirtualAuthenticator(t, st, u, "My Passkey")

	// The returned record must carry the credential ID the authenticator minted.
	wantCredID := base64.RawURLEncoding.EncodeToString(va.credID)
	if cred.CredentialID != wantCredID {
		t.Errorf("stored credential_id: got %q, want %q", cred.CredentialID, wantCredID)
	}
	if cred.FriendlyName != "My Passkey" {
		t.Errorf("friendly_name: got %q", cred.FriendlyName)
	}
	if cred.AAGUID == "" {
		t.Error("aaguid should be persisted (non-empty) from attestation authData")
	}

	// storeWebAuthnCredential must have persisted id/public_key/sign_count.
	var (
		pubKey    []byte
		signCount uint32
		dbCredID  string
	)
	if err := st.db.QueryRowContext(ctx,
		st.db.Rebind(`SELECT credential_id, public_key, sign_count FROM webauthn_credentials WHERE user_id = ?`),
		u.ID,
	).Scan(&dbCredID, &pubKey, &signCount); err != nil {
		t.Fatalf("query stored credential: %v", err)
	}
	if dbCredID != wantCredID {
		t.Errorf("db credential_id mismatch: got %q", dbCredID)
	}
	if len(pubKey) == 0 {
		t.Error("public_key must be persisted (CBOR COSE key)")
	}
	// Registration authData started the counter at 1.
	if signCount != 1 {
		t.Errorf("persisted sign_count: got %d, want 1", signCount)
	}

	// And it must be visible via the public listing.
	creds, err := st.ListWebAuthnCredentials(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListWebAuthnCredentials: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("expected 1 listed credential, got %d", len(creds))
	}
}

func TestWebAuthnE2E_Registration_WrongChallengeRejected(t *testing.T) {
	setupWebAuthnEnv(t)
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "e2eregwc@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	_, session, err := st.BeginWebAuthnRegistration(ctx, u)
	if err != nil {
		t.Fatalf("BeginWebAuthnRegistration: %v", err)
	}

	// Tamper the session challenge so clientDataJSON no longer matches what the
	// server expects (attacker replaying an old/forged create() response).
	tampered := *session
	tampered.Challenge = base64.RawURLEncoding.EncodeToString([]byte("a-totally-different-challenge!!"))

	va := newVirtualAuthenticator(t, waTestRPID, waTestOrigin)
	body := va.makeAttestationResponse(t, sessionChallenge{&tampered}, "")

	// Finish is called with the ORIGINAL server session; challenge mismatch.
	if _, err := st.FinishWebAuthnRegistration(ctx, u, *session, finishRequest(body), "x"); err == nil {
		t.Fatal("registration with mismatched challenge must be rejected")
	}
}

// TestWebAuthnE2E_Registration_DuplicateCredentialRejected drives the same
// authenticator (same credential ID) through registration twice. The DB unique
// constraint on credential_id must make the second store fail rather than
// silently overwrite -- exercising the IsUniqueViolation branch of
// storeWebAuthnCredential.
func TestWebAuthnE2E_Registration_DuplicateCredentialRejected(t *testing.T) {
	setupWebAuthnEnv(t)
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "e2edup@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	// First registration with a fixed authenticator.
	_, session1, err := st.BeginWebAuthnRegistration(ctx, u)
	if err != nil {
		t.Fatalf("BeginWebAuthnRegistration(1): %v", err)
	}
	va := newVirtualAuthenticator(t, waTestRPID, waTestOrigin)
	body1 := va.makeAttestationResponse(t, sessionChallenge{session1}, "")
	if _, err := st.FinishWebAuthnRegistration(ctx, u, *session1, finishRequest(body1), "Key"); err != nil {
		t.Fatalf("FinishWebAuthnRegistration(1): %v", err)
	}

	// Second registration re-using the SAME credential ID must fail.
	_, session2, err := st.BeginWebAuthnRegistration(ctx, u)
	if err != nil {
		t.Fatalf("BeginWebAuthnRegistration(2): %v", err)
	}
	body2 := va.makeAttestationResponse(t, sessionChallenge{session2}, "")
	if _, err := st.FinishWebAuthnRegistration(ctx, u, *session2, finishRequest(body2), "Key dup"); err == nil {
		t.Fatal("re-registering the same credential ID must be rejected (unique constraint)")
	}

	// Only the first credential should exist.
	creds, err := st.ListWebAuthnCredentials(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListWebAuthnCredentials: %v", err)
	}
	if len(creds) != 1 {
		t.Errorf("expected exactly 1 credential after duplicate attempt, got %d", len(creds))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Login: end-to-end happy path (issues a session)
// ─────────────────────────────────────────────────────────────────────────────

func TestWebAuthnE2E_Login_Succeeds(t *testing.T) {
	setupTOTPTestEnv(t) // lockout primitives share TOTP env / kek
	setupWebAuthnEnv(t)
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "e2elogin@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	va, _ := registerViaVirtualAuthenticator(t, st, u, "Login Key")

	// Advance the authenticator counter for the assertion (monotonic bump).
	va.signCount = 5

	wu, err := st.LoadWebAuthnUserForLogin(ctx, "e2elogin@example.com")
	if err != nil {
		t.Fatalf("LoadWebAuthnUserForLogin: %v", err)
	}
	_, session, err := st.BeginWebAuthnLogin(ctx, wu)
	if err != nil {
		t.Fatalf("BeginWebAuthnLogin: %v", err)
	}

	body := va.makeAssertionResponse(t, sessionChallenge{session}, assertionOpts{})
	token, err := st.FinishWebAuthnLogin(ctx, wu, *session, finishRequest(body), "127.0.0.1", "test-ua")
	if err != nil {
		t.Fatalf("FinishWebAuthnLogin: %v", err)
	}
	if token == "" {
		t.Fatal("FinishWebAuthnLogin must return a session token")
	}

	// Sign-count must have advanced to the asserted value.
	var sc uint32
	if err := st.db.QueryRowContext(ctx,
		st.db.Rebind(`SELECT sign_count FROM webauthn_credentials WHERE user_id = ?`), u.ID,
	).Scan(&sc); err != nil {
		t.Fatalf("query sign_count: %v", err)
	}
	if sc != 5 {
		t.Errorf("sign_count after login: got %d, want 5", sc)
	}
}

func TestWebAuthnE2E_LoginNoSession_Succeeds(t *testing.T) {
	setupTOTPTestEnv(t)
	setupWebAuthnEnv(t)
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "e2enosess@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	va, _ := registerViaVirtualAuthenticator(t, st, u, "2FA Key")
	va.signCount = 9

	// Passkey-as-2FA loads the user by ID (partial session), not by email.
	wu, err := st.LoadWebAuthnUserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("LoadWebAuthnUserByID: %v", err)
	}
	_, session, err := st.BeginWebAuthnLogin(ctx, wu)
	if err != nil {
		t.Fatalf("BeginWebAuthnLogin: %v", err)
	}

	body := va.makeAssertionResponse(t, sessionChallenge{session}, assertionOpts{})
	if err := st.FinishWebAuthnLoginNoSession(ctx, wu, *session, finishRequest(body)); err != nil {
		t.Fatalf("FinishWebAuthnLoginNoSession: %v", err)
	}

	var sc uint32
	if err := st.db.QueryRowContext(ctx,
		st.db.Rebind(`SELECT sign_count FROM webauthn_credentials WHERE user_id = ?`), u.ID,
	).Scan(&sc); err != nil {
		t.Fatalf("query sign_count: %v", err)
	}
	if sc != 9 {
		t.Errorf("sign_count after no-session login: got %d, want 9", sc)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Login: security-critical rejections
// ─────────────────────────────────────────────────────────────────────────────

// loginRejectionFixture registers a credential and returns a fresh login session
// plus the authenticator, ready for a negative assertion.
func loginRejectionFixture(t *testing.T, st *Store, email string) (*virtualAuthenticator, *WebAuthnUserAdapter, *webauthn.SessionData) {
	t.Helper()
	ctx := context.Background()
	u, _, err := st.Signup(ctx, email, "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	va, _ := registerViaVirtualAuthenticator(t, st, u, "Key")
	va.signCount = 5

	wu, err := st.LoadWebAuthnUserForLogin(ctx, email)
	if err != nil {
		t.Fatalf("LoadWebAuthnUserForLogin: %v", err)
	}
	_, session, err := st.BeginWebAuthnLogin(ctx, wu)
	if err != nil {
		t.Fatalf("BeginWebAuthnLogin: %v", err)
	}
	return va, wu, session
}

func TestWebAuthnE2E_Login_TamperedSignatureRejected(t *testing.T) {
	setupTOTPTestEnv(t)
	setupWebAuthnEnv(t)
	st := openTestStore(t)
	ctx := context.Background()

	va, wu, session := loginRejectionFixture(t, st, "e2etamper@example.com")
	body := va.makeAssertionResponse(t, sessionChallenge{session}, assertionOpts{tamperSig: true})

	_, err := st.FinishWebAuthnLogin(ctx, wu, *session, finishRequest(body), "127.0.0.1", "ua")
	if err == nil {
		t.Fatal("tampered assertion signature must be rejected")
	}

	// Sign-count must NOT have advanced: rejected before the counter write, so
	// it stays at the registration value (1).
	assertSignCount(t, st, wu.ID, 1)
	// A failed assertion records a failed-2FA attempt (shared lockout).
	assertFailed2FARecorded(t, st, wu.ID)
}

func TestWebAuthnE2E_Login_WrongChallengeReplayRejected(t *testing.T) {
	setupTOTPTestEnv(t)
	setupWebAuthnEnv(t)
	st := openTestStore(t)
	ctx := context.Background()

	va, wu, session := loginRejectionFixture(t, st, "e2ereplay@example.com")

	// Replay / wrong challenge: sign a stale challenge, not the one Begin issued.
	stale := base64.RawURLEncoding.EncodeToString([]byte("stale-replayed-challenge-value!!"))
	body := va.makeAssertionResponse(t, sessionChallenge{session}, assertionOpts{challengeB64: stale})

	if _, err := st.FinishWebAuthnLogin(ctx, wu, *session, finishRequest(body), "127.0.0.1", "ua"); err == nil {
		t.Fatal("assertion over a wrong/replayed challenge must be rejected")
	}
	assertSignCount(t, st, wu.ID, 1) // unchanged from registration
}

func TestWebAuthnE2E_Login_SignCountRegressionRejected(t *testing.T) {
	setupTOTPTestEnv(t)
	setupWebAuthnEnv(t)
	st := openTestStore(t)
	ctx := context.Background()

	u, _, err := st.Signup(ctx, "e2eclone@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	va, _ := registerViaVirtualAuthenticator(t, st, u, "Cloned Key")

	// 1) A legitimate login advances the stored counter to 8.
	va.signCount = 8
	wu, err := st.LoadWebAuthnUserForLogin(ctx, "e2eclone@example.com")
	if err != nil {
		t.Fatalf("LoadWebAuthnUserForLogin: %v", err)
	}
	_, session, err := st.BeginWebAuthnLogin(ctx, wu)
	if err != nil {
		t.Fatalf("BeginWebAuthnLogin: %v", err)
	}
	body := va.makeAssertionResponse(t, sessionChallenge{session}, assertionOpts{})
	if _, err := st.FinishWebAuthnLogin(ctx, wu, *session, finishRequest(body), "127.0.0.1", "ua"); err != nil {
		t.Fatalf("first login: %v", err)
	}
	assertSignCount(t, st, wu.ID, 8)

	// 2) A CLONED authenticator presents a valid signature but a LOWER counter
	//    (7 <= stored 8). Must be rejected as sign-count replay.
	wu2, err := st.LoadWebAuthnUserForLogin(ctx, "e2eclone@example.com")
	if err != nil {
		t.Fatalf("LoadWebAuthnUserForLogin(2): %v", err)
	}
	_, session2, err := st.BeginWebAuthnLogin(ctx, wu2)
	if err != nil {
		t.Fatalf("BeginWebAuthnLogin(2): %v", err)
	}
	regressed := uint32(7)
	body2 := va.makeAssertionResponse(t, sessionChallenge{session2}, assertionOpts{signCount: &regressed})

	_, err = st.FinishWebAuthnLogin(ctx, wu2, *session2, finishRequest(body2), "127.0.0.1", "ua")
	if !errors.Is(err, ErrWebAuthnSignCountReplay) {
		t.Fatalf("cloned-authenticator sign-count regression must be ErrWebAuthnSignCountReplay, got %v", err)
	}
	// Counter must remain at the legitimate 8, not roll back to 7.
	assertSignCount(t, st, wu2.ID, 8)
}

func TestWebAuthnE2E_Login_WrongOriginRejected(t *testing.T) {
	setupTOTPTestEnv(t)
	setupWebAuthnEnv(t)
	st := openTestStore(t)
	ctx := context.Background()

	va, wu, session := loginRejectionFixture(t, st, "e2eorigin@example.com")
	body := va.makeAssertionResponse(t, sessionChallenge{session}, assertionOpts{origin: waWrongOrigin})

	if _, err := st.FinishWebAuthnLogin(ctx, wu, *session, finishRequest(body), "127.0.0.1", "ua"); err == nil {
		t.Fatal("assertion from a wrong origin must be rejected")
	}
	assertSignCount(t, st, wu.ID, 1) // unchanged from registration
}

func TestWebAuthnE2E_Login_UnknownCredentialRejected(t *testing.T) {
	setupTOTPTestEnv(t)
	setupWebAuthnEnv(t)
	st := openTestStore(t)
	ctx := context.Background()

	va, wu, session := loginRejectionFixture(t, st, "e2eunknown@example.com")

	// Present a credential ID that was never registered for this user.
	unknown := make([]byte, 32)
	for i := range unknown {
		unknown[i] = 0xAB
	}
	body := va.makeAssertionResponse(t, sessionChallenge{session}, assertionOpts{overrideCredID: unknown})

	if _, err := st.FinishWebAuthnLogin(ctx, wu, *session, finishRequest(body), "127.0.0.1", "ua"); err == nil {
		t.Fatal("assertion referencing an unknown credential must be rejected")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Assertions / helpers
// ─────────────────────────────────────────────────────────────────────────────

func assertSignCount(t *testing.T, st *Store, userID string, want uint32) {
	t.Helper()
	var got uint32
	if err := st.db.QueryRowContext(context.Background(),
		st.db.Rebind(`SELECT sign_count FROM webauthn_credentials WHERE user_id = ?`), userID,
	).Scan(&got); err != nil {
		t.Fatalf("query sign_count: %v", err)
	}
	if got != want {
		t.Errorf("sign_count: got %d, want %d", got, want)
	}
}

func assertFailed2FARecorded(t *testing.T, st *Store, userID string) {
	t.Helper()
	// A single failure should not lock the account but must have been recorded;
	// four more failures should trip the shared lockout (threshold 5).
	for i := 0; i < 4; i++ {
		if err := st.RecordFailed2FA(context.Background(), userID); err != nil {
			t.Fatalf("RecordFailed2FA: %v", err)
		}
	}
	if _, locked := st.Check2FALockout(context.Background(), userID); !locked {
		t.Error("failed WebAuthn assertion should have recorded a failed-2FA attempt (shared lockout not tripped after 1+4)")
	}
}
