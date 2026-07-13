package main

// routes_stream_webauthn_test.go -- WAVE-46 HTTP coverage for the AUTH-13 stream
// input-gate re-auth endpoints (routes_stream_webauthn.go):
//
//	POST /api/stream/webauthn-begin   → challenge + session_data
//	POST /api/stream/webauthn-assert  → lift the input gate (handleAssertion*)
//
// These drive the REAL StreamVerifier (go-webauthn) through the REAL handlers
// with a spec-correct virtual authenticator, and assert the gate actually flips.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vulos/backend/services/auth"
	"vulos/backend/services/devicekey"
	"vulos/backend/services/passkeys"
	"vulos/backend/services/stream"
)

func newStreamWATestMux(t *testing.T) (*http.ServeMux, *stream.Pool, *passkeys.Service) {
	t.Helper()
	t.Setenv("VULOS_RPID", pkTestRPID)
	t.Setenv("VULOS_ORIGIN", pkTestOrigin)

	ks, err := devicekey.Open(t.TempDir())
	if err != nil {
		t.Fatalf("devicekey.Open: %v", err)
	}
	t.Cleanup(func() { _ = ks.Close() })

	svc := passkeys.New(t.TempDir(), ks)
	sv := passkeys.NewStreamVerifier(svc)
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	pool := stream.NewPool()
	pool.SetWebAuthnVerifier(sv)

	mux := http.NewServeMux()
	registerStreamWebAuthnRoutes(mux, pool, store, sv)
	return mux, pool, svc
}

// streamPost issues a raw-body POST with X-User-ID (query params in path).
func streamPost(t *testing.T, mux *http.ServeMux, path, userID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// enrollStreamUser registers a passkey for userID via the Service and returns the
// authenticator.
func enrollStreamUser(t *testing.T, svc *passkeys.Service, userID string) *pkAuthenticator {
	t.Helper()
	va := newPKAuthenticator(t, pkTestRPID, pkTestOrigin)
	challenge, sessionData, err := svc.BeginRegistration(userID, "Stream User")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if _, err := svc.FinishRegistration(userID, va.attestation(t, pkChallengeFrom(t, challenge)), sessionData); err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	return va
}

// TestStreamWebAuthn_BeginRequiresAuthAndSession covers the begin-endpoint guards.
func TestStreamWebAuthn_BeginRequiresAuthAndSession(t *testing.T) {
	mux, pool, svc := newStreamWATestMux(t)
	userID := "sw-guard"
	enrollStreamUser(t, svc, userID)
	pool.RegisterGatedSessionForTest("sess-guard", userID)

	// missing ?id → 400
	rec := streamPost(t, mux, "/api/stream/webauthn-begin", userID, nil)
	if rec.Code != 400 {
		t.Errorf("begin no id: got %d want 400", rec.Code)
	}
	// unknown session → 404
	rec = streamPost(t, mux, "/api/stream/webauthn-begin?id=nope", userID, nil)
	if rec.Code != 404 {
		t.Errorf("begin unknown session: got %d want 404", rec.Code)
	}
	// no X-User-ID → 401
	rec = streamPost(t, mux, "/api/stream/webauthn-begin?id=sess-guard", "", nil)
	if rec.Code != 401 {
		t.Errorf("begin no auth: got %d want 401", rec.Code)
	}
}

// TestStreamWebAuthn_AssertLiftsGate is the AUTH-13 happy path over HTTP: a valid
// assertion via /webauthn-assert flips the session's inputGated flag to false.
func TestStreamWebAuthn_AssertLiftsGate(t *testing.T) {
	mux, pool, svc := newStreamWATestMux(t)
	userID := "sw-lift"
	va := enrollStreamUser(t, svc, userID)
	sess := pool.RegisterGatedSessionForTest("sess-lift", userID)

	if !sess.IsInputGatedForTest() {
		t.Fatal("precondition: session should start gated")
	}

	// begin
	rec := streamPost(t, mux, "/api/stream/webauthn-begin?id=sess-lift", userID, nil)
	if rec.Code != 200 {
		t.Fatalf("webauthn-begin: got %d body=%s", rec.Code, rec.Body)
	}
	var begin struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &begin)

	// assert with a valid assertion envelope
	va.signCount++
	envelope := map[string]any{
		"session_data":       begin.SessionData,
		"assertion_response": va.assertion(t, pkAssertOpts{challenge: pkChallengeFrom(t, begin.Challenge)}),
	}
	body, _ := json.Marshal(envelope)
	rec = streamPost(t, mux, "/api/stream/webauthn-assert?id=sess-lift", userID, body)
	if rec.Code != 200 {
		t.Fatalf("webauthn-assert: got %d body=%s", rec.Code, rec.Body)
	}
	if sess.IsInputGatedForTest() {
		t.Fatal("AUTH-13 REGRESSION: gate still active after a valid assertion")
	}
}

// TestStreamWebAuthn_AssertTamperedKeepsGate: a tampered assertion must be
// rejected (403) and the gate must remain active (fail-closed).
func TestStreamWebAuthn_AssertTamperedKeepsGate(t *testing.T) {
	mux, pool, svc := newStreamWATestMux(t)
	userID := "sw-tamper"
	va := enrollStreamUser(t, svc, userID)
	sess := pool.RegisterGatedSessionForTest("sess-tamper", userID)

	rec := streamPost(t, mux, "/api/stream/webauthn-begin?id=sess-tamper", userID, nil)
	var begin struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &begin)

	va.signCount++
	envelope := map[string]any{
		"session_data":       begin.SessionData,
		"assertion_response": va.assertion(t, pkAssertOpts{challenge: pkChallengeFrom(t, begin.Challenge), tamper: true}),
	}
	body, _ := json.Marshal(envelope)
	rec = streamPost(t, mux, "/api/stream/webauthn-assert?id=sess-tamper", userID, body)
	if rec.Code != 403 {
		t.Fatalf("tampered assert: got %d want 403 body=%s", rec.Code, rec.Body)
	}
	if !sess.IsInputGatedForTest() {
		t.Fatal("AUTH-13 REGRESSION: gate lifted by a rejected assertion (fail-open)")
	}
}

// TestStreamWebAuthn_AssertSignCountRegressionKeepsGate: a cloned authenticator
// (regressed sign counter) must NOT lift the gate over HTTP.
func TestStreamWebAuthn_AssertSignCountRegressionKeepsGate(t *testing.T) {
	mux, pool, svc := newStreamWATestMux(t)
	userID := "sw-clone"
	va := enrollStreamUser(t, svc, userID)
	sess := pool.RegisterGatedSessionForTest("sess-clone", userID)

	// First: advance the stored counter to 5 with a valid assertion.
	rec := streamPost(t, mux, "/api/stream/webauthn-begin?id=sess-clone", userID, nil)
	var b1 struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &b1)
	sc5 := uint32(5)
	env1, _ := json.Marshal(map[string]any{
		"session_data":       b1.SessionData,
		"assertion_response": va.assertion(t, pkAssertOpts{challenge: pkChallengeFrom(t, b1.Challenge), signCount: &sc5}),
	})
	if rec = streamPost(t, mux, "/api/stream/webauthn-assert?id=sess-clone", userID, env1); rec.Code != 200 {
		t.Fatalf("counter=5 assert: got %d body=%s", rec.Code, rec.Body)
	}
	// Re-arm the gate for the clone attempt.
	sess2 := pool.RegisterGatedSessionForTest("sess-clone2", userID)

	// Clone replays with counter=3 (< persisted 5) → must be rejected, gate holds.
	rec = streamPost(t, mux, "/api/stream/webauthn-begin?id=sess-clone2", userID, nil)
	var b2 struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &b2)
	sc3 := uint32(3)
	env2, _ := json.Marshal(map[string]any{
		"session_data":       b2.SessionData,
		"assertion_response": va.assertion(t, pkAssertOpts{challenge: pkChallengeFrom(t, b2.Challenge), signCount: &sc3}),
	})
	rec = streamPost(t, mux, "/api/stream/webauthn-assert?id=sess-clone2", userID, env2)
	if rec.Code == 200 {
		t.Fatalf("AUTH-13 REGRESSION: cloned-authenticator assertion accepted over HTTP: %s", rec.Body)
	}
	if !sess2.IsInputGatedForTest() {
		t.Fatal("AUTH-13 REGRESSION: gate lifted by a cloned-authenticator assertion")
	}
	_ = sess
}

// TestStreamWebAuthn_AssertJSONBodyVariant covers the alternate assert shape
// where no ?id= is given and the body is {"id":..., "assertion":<base64url>} —
// the handleAssertionJSON path. The assertion field is the base64url of the
// {session_data, assertion_response} envelope bytes.
func TestStreamWebAuthn_AssertJSONBodyVariant(t *testing.T) {
	mux, pool, svc := newStreamWATestMux(t)
	userID := "sw-json"
	va := enrollStreamUser(t, svc, userID)
	sess := pool.RegisterGatedSessionForTest("sess-json", userID)

	rec := streamPost(t, mux, "/api/stream/webauthn-begin?id=sess-json", userID, nil)
	var begin struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &begin)

	va.signCount++
	envelope, _ := json.Marshal(map[string]any{
		"session_data":       begin.SessionData,
		"assertion_response": va.assertion(t, pkAssertOpts{challenge: pkChallengeFrom(t, begin.Challenge)}),
	})
	// Wrap the envelope bytes as base64url inside the {id, assertion} JSON body,
	// with NO ?id= query param so the handler takes the JSON-body branch.
	body, _ := json.Marshal(map[string]string{
		"id":        "sess-json",
		"assertion": base64.RawURLEncoding.EncodeToString(envelope),
	})
	rec = streamPost(t, mux, "/api/stream/webauthn-assert", userID, body)
	if rec.Code != 200 {
		t.Fatalf("json-body assert: got %d body=%s", rec.Code, rec.Body)
	}
	if sess.IsInputGatedForTest() {
		t.Fatal("AUTH-13: gate still active after valid json-body assertion")
	}
}

// TestStreamWebAuthn_AssertUnknownSession: assert for a non-existent session → 404.
func TestStreamWebAuthn_AssertUnknownSession(t *testing.T) {
	mux, _, _ := newStreamWATestMux(t)
	rec := streamPost(t, mux, "/api/stream/webauthn-assert?id=ghost", "u", []byte(`{"session_data":"x","assertion_response":{}}`))
	if rec.Code != 404 {
		t.Fatalf("assert unknown session: got %d want 404", rec.Code)
	}
}

// TestStreamWebAuthn_AssertUngatedSessionIsNotOK pins the honesty contract of the
// endpoint: a session with no input-injection gate verified no passkey, so the
// endpoint must NOT answer 200 {"status":"ok"} — which is what it used to do,
// because RequireAssertion returned nil for the no-gate case.
func TestStreamWebAuthn_AssertUngatedSessionIsNotOK(t *testing.T) {
	mux, pool, svc := newStreamWATestMux(t)
	userID := "sw-ungated"
	enrollStreamUser(t, svc, userID)
	pool.RegisterSessionForTest("sess-ungated", userID)

	// Garbage assertion, real assertion — it does not matter: nothing is gated,
	// so nothing can be verified and the answer can never be "ok".
	rec := streamPost(t, mux, "/api/stream/webauthn-assert?id=sess-ungated", userID,
		[]byte(`{"session_data":"x","assertion_response":{}}`))
	if rec.Code == 200 {
		t.Fatalf("AUTH-13 REGRESSION: ungated session reported a verified assertion: %s", rec.Body)
	}
	if rec.Code != 409 {
		t.Fatalf("ungated assert: got %d want 409 body=%s", rec.Code, rec.Body)
	}
	var body map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	if _, isOK := body["status"]; isOK {
		t.Fatalf(`ungated assert must not report a status; got %s`, rec.Body)
	}
	if body["error"] == "" {
		t.Fatalf("ungated assert: expected an error message, got %s", rec.Body)
	}
}

// TestStreamWebAuthn_AssertRequiresSessionOwnership pins that the assert endpoint
// checks who owns the session: another user — even one holding a perfectly valid
// passkey of their own — cannot address, or lift, someone else's gate.
func TestStreamWebAuthn_AssertRequiresSessionOwnership(t *testing.T) {
	mux, pool, svc := newStreamWATestMux(t)
	owner, mallory := "sw-alice", "sw-mallory"
	enrollStreamUser(t, svc, owner)
	mva := enrollStreamUser(t, svc, mallory)
	sess := pool.RegisterGatedSessionForTest("sess-alice", owner)

	// Mallory begins an assertion against her OWN session so she holds a valid,
	// freshly-signed envelope, then aims it at Alice's gated session.
	pool.RegisterGatedSessionForTest("sess-mallory", mallory)
	rec := streamPost(t, mux, "/api/stream/webauthn-begin?id=sess-mallory", mallory, nil)
	if rec.Code != 200 {
		t.Fatalf("mallory begin: got %d body=%s", rec.Code, rec.Body)
	}
	var begin struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &begin)
	mva.signCount++
	body, _ := json.Marshal(map[string]any{
		"session_data":       begin.SessionData,
		"assertion_response": mva.assertion(t, pkAssertOpts{challenge: pkChallengeFrom(t, begin.Challenge)}),
	})

	rec = streamPost(t, mux, "/api/stream/webauthn-assert?id=sess-alice", mallory, body)
	if rec.Code != 404 {
		t.Fatalf("cross-user assert: got %d want 404 body=%s", rec.Code, rec.Body)
	}
	if !sess.IsInputGatedForTest() {
		t.Fatal("AUTH-13 REGRESSION: another user's assertion lifted the owner's input gate")
	}

	// Same for the JSON-body variant, which resolves the session from the body.
	jsonBody, _ := json.Marshal(map[string]string{
		"id":        "sess-alice",
		"assertion": base64.RawURLEncoding.EncodeToString(body),
	})
	rec = streamPost(t, mux, "/api/stream/webauthn-assert", mallory, jsonBody)
	if rec.Code != 404 {
		t.Fatalf("cross-user assert (json body): got %d want 404 body=%s", rec.Code, rec.Body)
	}
	if !sess.IsInputGatedForTest() {
		t.Fatal("AUTH-13 REGRESSION: json-body cross-user assertion lifted the owner's input gate")
	}

	// And an unauthenticated caller cannot even reach the gate.
	rec = streamPost(t, mux, "/api/stream/webauthn-assert?id=sess-alice", "", body)
	if rec.Code != 401 {
		t.Fatalf("anonymous assert: got %d want 401 body=%s", rec.Code, rec.Body)
	}
	if !sess.IsInputGatedForTest() {
		t.Fatal("AUTH-13 REGRESSION: anonymous assertion lifted the input gate")
	}
}
