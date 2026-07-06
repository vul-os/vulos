package passkeys

// stream_verifier_security_test.go -- WAVE-46 coverage for the AUTH-13 stream
// input-gate assertion path (services/stream/webauthn.go handleAssertion* ->
// StreamVerifier.Verify -> Service.FinishAssertion -> go-webauthn RP verify).
//
// The pre-existing services/stream/webauthn_gate_test.go only used a *fake*
// verifier that returns nil/err on command -- it never exercised a real
// cryptographic assertion nor any of the security rejections. These tests drive
// the REAL StreamVerifier with a spec-correct virtual authenticator, so a valid
// assertion lifts the gate and every documented rejection is enforced by the
// library, not by test doubles.

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// streamEnvelope builds the {session_data, assertion_response} JSON the stream
// assert endpoint hands to StreamVerifier.Verify.
func streamEnvelope(t *testing.T, sessionData, assertionResp []byte) []byte {
	t.Helper()
	env := map[string]json.RawMessage{
		"session_data":       mustJSONString(t, string(sessionData)),
		"assertion_response": assertionResp,
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("streamEnvelope: %v", err)
	}
	return b
}

func mustJSONString(t *testing.T, s string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("mustJSONString: %v", err)
	}
	return b
}

// enrolled sets up a service, a virtual authenticator, an enrolled credential,
// and returns them for the stream-assertion cases.
func enrolled(t *testing.T) (*Service, *StreamVerifier, *virtualAuthenticator, string) {
	t.Helper()
	svc := newTestService(t)
	sv := NewStreamVerifier(svc)
	va := newVirtualAuthenticator(t, svc)
	userID := "stream-user"
	va.register(t, svc, userID, "Stream User")
	return svc, sv, va, userID
}

// TestStreamAssertion_ValidLiftsGate is the happy path: a real assertion over a
// fresh Begin challenge verifies through StreamVerifier.Verify (nil error =
// gate would be lifted).
func TestStreamAssertion_ValidLiftsGate(t *testing.T) {
	svc, sv, va, userID := enrolled(t)

	challenge, sessionData, err := sv.BeginStreamAssertion(userID)
	if err != nil {
		t.Fatalf("BeginStreamAssertion: %v", err)
	}
	// Sign-count must advance past the enrolled value or go-webauthn flags a clone.
	va.signCount++
	resp := va.makeAssertionResponse(t, vaAssertionOpts{challengeB64: challengeFrom(t, challenge)})

	if err := sv.Verify(userID, streamEnvelope(t, sessionData, resp)); err != nil {
		t.Fatalf("AUTH-13: valid assertion rejected (gate would never lift): %v", err)
	}
	_ = svc
}

// TestStreamAssertion_TamperedSignatureRejected: flipping a signature byte must
// fail the ECDSA verify.
func TestStreamAssertion_TamperedSignatureRejected(t *testing.T) {
	_, sv, va, userID := enrolled(t)
	challenge, sessionData, _ := sv.BeginStreamAssertion(userID)
	va.signCount++
	resp := va.makeAssertionResponse(t, vaAssertionOpts{
		challengeB64: challengeFrom(t, challenge),
		tamperSig:    true,
	})
	if err := sv.Verify(userID, streamEnvelope(t, sessionData, resp)); err == nil {
		t.Fatal("AUTH-13 REGRESSION: tampered assertion signature accepted")
	}
}

// TestStreamAssertion_WrongChallengeRejected: signing a challenge that isn't the
// one Begin issued must fail (challenge binding).
func TestStreamAssertion_WrongChallengeRejected(t *testing.T) {
	_, sv, va, userID := enrolled(t)
	_, sessionData, _ := sv.BeginStreamAssertion(userID)
	va.signCount++
	bogus := base64.RawURLEncoding.EncodeToString([]byte("this-is-not-the-server-challenge!"))
	resp := va.makeAssertionResponse(t, vaAssertionOpts{challengeB64: bogus})
	if err := sv.Verify(userID, streamEnvelope(t, sessionData, resp)); err == nil {
		t.Fatal("AUTH-13 REGRESSION: assertion over a wrong challenge accepted")
	}
}

// TestStreamAssertion_ReplayedChallengeRejected: a session is single-use. Signing
// a *stale* challenge from a consumed Begin session must fail.
func TestStreamAssertion_ReplayedChallengeRejected(t *testing.T) {
	_, sv, va, userID := enrolled(t)

	// First ceremony consumes the session successfully.
	challenge1, sessionData1, _ := sv.BeginStreamAssertion(userID)
	va.signCount++
	resp1 := va.makeAssertionResponse(t, vaAssertionOpts{challengeB64: challengeFrom(t, challenge1)})
	if err := sv.Verify(userID, streamEnvelope(t, sessionData1, resp1)); err != nil {
		t.Fatalf("first assertion should verify: %v", err)
	}

	// Replay the SAME session_data + a fresh signature over the SAME challenge.
	va.signCount++
	resp2 := va.makeAssertionResponse(t, vaAssertionOpts{challengeB64: challengeFrom(t, challenge1)})
	if err := sv.Verify(userID, streamEnvelope(t, sessionData1, resp2)); err == nil {
		t.Fatal("AUTH-13 REGRESSION: a consumed assertion session was replayable")
	}
}

// TestStreamAssertion_SignCountRegressionRejected is the cloned-authenticator
// defence. After the enrolled credential's sign counter has advanced, an
// assertion presenting a LOWER (or equal) counter -- the signature a cloned
// authenticator would produce -- must be rejected by go-webauthn's monotonic
// counter check.
func TestStreamAssertion_SignCountRegressionRejected(t *testing.T) {
	_, sv, va, userID := enrolled(t)

	// Advance the stored counter with one legitimate assertion (sc=2).
	c1, sd1, _ := sv.BeginStreamAssertion(userID)
	sc2 := uint32(2)
	resp1 := va.makeAssertionResponse(t, vaAssertionOpts{
		challengeB64: challengeFrom(t, c1),
		signCount:    &sc2,
	})
	if err := sv.Verify(userID, streamEnvelope(t, sd1, resp1)); err != nil {
		t.Fatalf("counter-advancing assertion should verify: %v", err)
	}

	// Now a "clone" replays with a stale/regressed counter (sc=1 < stored 2).
	c2, sd2, _ := sv.BeginStreamAssertion(userID)
	sc1 := uint32(1)
	resp2 := va.makeAssertionResponse(t, vaAssertionOpts{
		challengeB64: challengeFrom(t, c2),
		signCount:    &sc1,
	})
	if err := sv.Verify(userID, streamEnvelope(t, sd2, resp2)); err == nil {
		t.Fatal("AUTH-13 REGRESSION: cloned-authenticator sign-count regression accepted")
	}
}

// TestStreamAssertion_WrongRPRejected: an assertion whose authenticatorData
// carries a different RP ID hash must be rejected (authData is signed, so this
// also breaks the signature).
func TestStreamAssertion_WrongRPRejected(t *testing.T) {
	_, sv, va, userID := enrolled(t)
	challenge, sessionData, _ := sv.BeginStreamAssertion(userID)
	va.signCount++
	resp := va.makeAssertionResponse(t, vaAssertionOpts{
		challengeB64: challengeFrom(t, challenge),
		wrongRPID:    "evil.example.com",
	})
	if err := sv.Verify(userID, streamEnvelope(t, sessionData, resp)); err == nil {
		t.Fatal("AUTH-13 REGRESSION: assertion with a foreign RP ID hash accepted")
	}
}

// TestStreamAssertion_WrongOriginRejected: clientDataJSON.origin must match the
// RP's configured origin.
func TestStreamAssertion_WrongOriginRejected(t *testing.T) {
	_, sv, va, userID := enrolled(t)
	challenge, sessionData, _ := sv.BeginStreamAssertion(userID)
	va.signCount++
	resp := va.makeAssertionResponse(t, vaAssertionOpts{
		challengeB64: challengeFrom(t, challenge),
		origin:       "https://phishing.example.com",
	})
	if err := sv.Verify(userID, streamEnvelope(t, sessionData, resp)); err == nil {
		t.Fatal("AUTH-13 REGRESSION: assertion from a foreign origin accepted")
	}
}

// TestStreamAssertion_UnknownCredentialRejected: presenting a credential ID that
// was never enrolled must fail.
func TestStreamAssertion_UnknownCredentialRejected(t *testing.T) {
	_, sv, va, userID := enrolled(t)
	challenge, sessionData, _ := sv.BeginStreamAssertion(userID)
	va.signCount++
	unknown := make([]byte, 32)
	for i := range unknown {
		unknown[i] = 0xAB
	}
	resp := va.makeAssertionResponse(t, vaAssertionOpts{
		challengeB64:   challengeFrom(t, challenge),
		overrideCredID: unknown,
	})
	if err := sv.Verify(userID, streamEnvelope(t, sessionData, resp)); err == nil {
		t.Fatal("AUTH-13 REGRESSION: assertion for an unenrolled credential accepted")
	}
}

// TestStreamVerifier_MalformedEnvelopeRejected covers the envelope-parsing guards
// in StreamVerifier.Verify (not-JSON, missing session_data, missing response).
func TestStreamVerifier_MalformedEnvelopeRejected(t *testing.T) {
	_, sv, _, userID := enrolled(t)
	cases := map[string][]byte{
		"not JSON":             []byte("<<<not json>>>"),
		"missing session_data": []byte(`{"assertion_response":{"x":1}}`),
		"missing response":     []byte(`{"session_data":"abc"}`),
	}
	for name, body := range cases {
		if err := sv.Verify(userID, body); err == nil {
			t.Errorf("%s: expected rejection, got nil", name)
		}
	}
}

// TestFinishAssertion_PersistsCounterHighWaterMark is the direct regression for
// the WAVE-46 clone-detection bug: FinishAssertion must persist the advanced
// signature counter so a LATER ceremony that regresses the counter is rejected
// as a clone. Before the fix the stored counter never moved and clone detection
// could never fire across ceremonies.
func TestFinishAssertion_PersistsCounterHighWaterMark(t *testing.T) {
	svc := newTestService(t)
	va := newVirtualAuthenticator(t, svc)
	userID := "hwm-user"
	va.register(t, svc, userID, "HWM") // enrolled at signCount=1

	assertAt := func(sc uint32) error {
		c, sd, err := svc.BeginAssertion(userID)
		if err != nil {
			t.Fatalf("BeginAssertion: %v", err)
		}
		resp := va.makeAssertionResponse(t, vaAssertionOpts{
			challengeB64: challengeFrom(t, c),
			signCount:    &sc,
		})
		_, err = svc.FinishAssertion(userID, resp, sd)
		return err
	}

	// Advance the high-water mark to 5.
	if err := assertAt(5); err != nil {
		t.Fatalf("assertion at counter=5 should succeed: %v", err)
	}
	// A subsequent ceremony at counter=3 (< persisted 5) must be rejected as a
	// clone — proving the counter was persisted from the previous ceremony.
	if err := assertAt(3); err == nil {
		t.Fatal("WAVE-46 REGRESSION: counter regression across ceremonies accepted " +
			"(sign counter high-water mark not persisted)")
	}
	// And a further-advanced counter (7 > 5) still works.
	if err := assertAt(7); err != nil {
		t.Fatalf("assertion at counter=7 should succeed: %v", err)
	}
}

// TestStreamAssertion_CrossUserRejected: a credential enrolled for user A cannot
// be used to lift user B's gate (userID is bound into the ceremony).
func TestStreamAssertion_CrossUserRejected(t *testing.T) {
	svc, sv, va, userA := enrolled(t)

	// userB begins their own ceremony; A's authenticator answers it.
	// B has no credentials, so BeginAssertion errors -- assert that first.
	if _, _, err := sv.BeginStreamAssertion("other-user"); err == nil {
		t.Fatal("BeginStreamAssertion for a user with no credentials should error")
	}

	// A begins, but we try to verify as B: FinishAssertion loads B's (empty)
	// credential set and must reject.
	challenge, sessionData, _ := sv.BeginStreamAssertion(userA)
	va.signCount++
	resp := va.makeAssertionResponse(t, vaAssertionOpts{challengeB64: challengeFrom(t, challenge)})
	if err := sv.Verify("other-user", streamEnvelope(t, sessionData, resp)); err == nil {
		t.Fatal("AUTH-13 REGRESSION: user A's assertion lifted user B's gate")
	}
	_ = svc
}
