package passkeys

// passkeys_l1_test.go — SECAUDIT2 L-1 regression: bind-check before consume.
//
// The vulnerability: au12ConsumeSession previously deleted the pending session
// BEFORE checking the userID binding. A wrong-user caller who knew (or guessed)
// the session token could permanently destroy the legitimate owner's in-flight
// ceremony, causing a denial-of-service without being able to complete it.
//
// Fix invariant: on a userID mismatch the session must NOT be removed so the
// real owner can still successfully complete their ceremony.

import (
	"encoding/json"
	"testing"
	"time"

	wa "github.com/go-webauthn/webauthn/webauthn"
)

// stubWASessionData returns a minimal wa.SessionData value. The ceremony
// protocol validation is exercised by other tests; here we only need a
// non-zero value to populate the session map.
func stubWASessionData() wa.SessionData {
	return wa.SessionData{
		Challenge:        "dGVzdC1jaGFsbGVuZ2U", // base64url of "test-challenge"
		UserID:           []byte("l1-owner"),
		UserVerification: "preferred",
	}
}

// TestL1_WrongUserFinishRegistration_PreservesSession asserts that a
// FinishRegistration call with the wrong userID:
//
//	(a) returns an error, and
//	(b) does NOT delete the pending ceremony so the real owner can still
//	    complete it.
func TestL1_WrongUserFinishRegistration_PreservesSession(t *testing.T) {
	svc := newTestService(t)
	owner := "l1-owner"
	attacker := "l1-attacker"

	// Begin a legitimate registration for the owner.
	_, sessionData, err := svc.BeginRegistration(owner, "L1 Owner")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}

	// Extract the token so we can verify session existence after the attack.
	tok, err := au12ExtractToken(sessionData)
	if err != nil {
		t.Fatalf("au12ExtractToken: %v", err)
	}

	// Attacker calls FinishRegistration with the owner's sessionData but their
	// own userID. This must return an error.
	_, err = svc.FinishRegistration(attacker, []byte(`{}`), sessionData)
	if err == nil {
		t.Fatal("SECAUDIT2 L-1 REGRESSION: FinishRegistration with wrong userID " +
			"succeeded — session binding broken.")
	}

	// The owner's pending ceremony must still be present in the session map
	// so the real owner can complete their registration.
	svc.mu.Lock()
	_, stillPresent := svc.sessions[tok]
	svc.mu.Unlock()

	if !stillPresent {
		t.Fatal("SECAUDIT2 L-1 REGRESSION: wrong-user FinishRegistration call " +
			"deleted the pending session — the legitimate owner's in-flight " +
			"registration ceremony was destroyed (availability DoS).")
	}
}

// TestL1_WrongUserFinishAssertion_PreservesSession asserts that a
// FinishAssertion call with the wrong userID:
//
//	(a) returns an error (verified=false), and
//	(b) does NOT delete the pending ceremony session.
func TestL1_WrongUserFinishAssertion_PreservesSession(t *testing.T) {
	svc := newTestService(t)
	owner := "l1-assert-owner"
	attacker := "l1-assert-attacker"

	// Inject an assertion-style session directly (bypassing BeginAssertion so
	// we do not need the owner to have a credential on disk for this path test).
	tok := au12RandToken()
	svc.mu.Lock()
	svc.sessions[tok] = &au12Session{
		Data:      stubWASessionData(),
		UserID:    owner,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	svc.mu.Unlock()

	assertionSessionData, err := json.Marshal(map[string]string{"token": tok})
	if err != nil {
		t.Fatalf("marshal session data: %v", err)
	}

	// Attacker calls FinishAssertion with owner's session token but wrong userID.
	verified, err := svc.FinishAssertion(attacker, []byte(`{}`), assertionSessionData)
	if err == nil || verified {
		t.Fatal("SECAUDIT2 L-1 REGRESSION: FinishAssertion with wrong userID " +
			"succeeded — session binding broken.")
	}

	// The owner's pending session must still be in the map.
	svc.mu.Lock()
	_, stillPresent := svc.sessions[tok]
	svc.mu.Unlock()

	if !stillPresent {
		t.Fatal("SECAUDIT2 L-1 REGRESSION: wrong-user FinishAssertion call " +
			"deleted the pending session — the legitimate owner's in-flight " +
			"assertion ceremony was destroyed (availability DoS).")
	}
}
