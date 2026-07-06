package main

// routes_passkeys_test.go -- WAVE-46 HTTP round-trip coverage for the AUTH-12
// passkey management routes (/api/passkeys/*). Drives the REAL passkeys.Service
// through the REAL registered handlers with a spec-correct virtual authenticator.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vulos/backend/services/auth"
	"vulos/backend/services/devicekey"
	"vulos/backend/services/passkeys"
)

const (
	pkTestRPID   = "localhost"
	pkTestOrigin = "http://localhost:8080"
)

// newPasskeysTestMux builds a mux with the passkey management routes wired to a
// real Service backed by a software devicekey KeyStore in a temp dir.
func newPasskeysTestMux(t *testing.T) (*http.ServeMux, *passkeys.Service) {
	t.Helper()
	t.Setenv("VULOS_RPID", pkTestRPID)
	t.Setenv("VULOS_ORIGIN", pkTestOrigin)

	ks, err := devicekey.Open(t.TempDir())
	if err != nil {
		t.Fatalf("devicekey.Open: %v", err)
	}
	t.Cleanup(func() { _ = ks.Close() })

	svc := passkeys.New(t.TempDir(), ks)
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}

	mux := http.NewServeMux()
	registerPasskeysRoutes(mux, svc, store)
	return mux, svc
}

func pkDoJSON(t *testing.T, mux *http.ServeMux, method, path, userID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestPasskeyRoutes_Unauthenticated: every route requires X-User-ID.
func TestPasskeyRoutes_Unauthenticated(t *testing.T) {
	mux, _ := newPasskeysTestMux(t)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/passkeys/register/begin"},
		{"POST", "/api/passkeys/register/finish"},
		{"POST", "/api/passkeys/assert/begin"},
		{"POST", "/api/passkeys/assert/finish"},
		{"GET", "/api/passkeys"},
		{"DELETE", "/api/passkeys/some-id"},
	} {
		rec := pkDoJSON(t, mux, tc.method, tc.path, "", map[string]any{})
		if rec.Code != 401 {
			t.Errorf("%s %s without X-User-ID: got %d want 401", tc.method, tc.path, rec.Code)
		}
	}
}

// TestPasskeyRoutes_RegisterAssertRoundTrip drives register begin/finish then
// assert begin/finish over HTTP, then lists + deletes the credential.
func TestPasskeyRoutes_RegisterAssertRoundTrip(t *testing.T) {
	mux, _ := newPasskeysTestMux(t)
	userID := "route-user"
	va := newPKAuthenticator(t, pkTestRPID, pkTestOrigin)

	// --- register begin ---
	rec := pkDoJSON(t, mux, "POST", "/api/passkeys/register/begin", userID, map[string]string{"display_name": "Route User"})
	if rec.Code != 200 {
		t.Fatalf("register/begin: got %d body=%s", rec.Code, rec.Body)
	}
	var beginResp struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &beginResp); err != nil {
		t.Fatalf("parse register/begin: %v", err)
	}
	if beginResp.SessionData == "" {
		t.Fatal("register/begin: empty session_data")
	}

	// --- register finish ---
	rec = pkDoJSON(t, mux, "POST", "/api/passkeys/register/finish", userID, map[string]any{
		"session_data":         beginResp.SessionData,
		"attestation_response": va.attestation(t, pkChallengeFrom(t, beginResp.Challenge)),
	})
	if rec.Code != 200 {
		t.Fatalf("register/finish: got %d body=%s", rec.Code, rec.Body)
	}
	var finResp struct {
		CredentialID string `json:"credential_id"`
	}
	json.Unmarshal(rec.Body.Bytes(), &finResp)
	if finResp.CredentialID == "" {
		t.Fatal("register/finish: empty credential_id")
	}

	// --- list shows the credential ---
	rec = pkDoJSON(t, mux, "GET", "/api/passkeys", userID, nil)
	if rec.Code != 200 {
		t.Fatalf("list: got %d", rec.Code)
	}
	var creds []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &creds)
	if len(creds) != 1 {
		t.Fatalf("list: got %d creds want 1", len(creds))
	}

	// --- assert begin ---
	rec = pkDoJSON(t, mux, "POST", "/api/passkeys/assert/begin", userID, nil)
	if rec.Code != 200 {
		t.Fatalf("assert/begin: got %d body=%s", rec.Code, rec.Body)
	}
	var aBegin struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &aBegin)

	// --- assert finish (advance counter so it verifies) ---
	va.signCount++
	rec = pkDoJSON(t, mux, "POST", "/api/passkeys/assert/finish", userID, map[string]any{
		"session_data":       aBegin.SessionData,
		"assertion_response": va.assertion(t, pkAssertOpts{challenge: pkChallengeFrom(t, aBegin.Challenge)}),
	})
	if rec.Code != 200 {
		t.Fatalf("assert/finish: got %d body=%s", rec.Code, rec.Body)
	}
	var verified struct {
		Verified bool `json:"verified"`
	}
	json.Unmarshal(rec.Body.Bytes(), &verified)
	if !verified.Verified {
		t.Fatal("assert/finish: verified=false")
	}

	// --- delete ---
	rec = pkDoJSON(t, mux, "DELETE", "/api/passkeys/"+finResp.CredentialID, userID, nil)
	if rec.Code != 200 {
		t.Fatalf("delete: got %d body=%s", rec.Code, rec.Body)
	}
	rec = pkDoJSON(t, mux, "GET", "/api/passkeys", userID, nil)
	json.Unmarshal(rec.Body.Bytes(), &creds)
	if len(creds) != 0 {
		// re-parse into fresh slice
		var after []map[string]any
		json.Unmarshal(rec.Body.Bytes(), &after)
		if len(after) != 0 {
			t.Fatalf("after delete: got %d creds want 0", len(after))
		}
	}
}

// TestPasskeyRoutes_AssertFinish_TamperedRejected: a tampered assertion over the
// real handler must yield 400 (not 200/verified).
func TestPasskeyRoutes_AssertFinish_TamperedRejected(t *testing.T) {
	mux, _ := newPasskeysTestMux(t)
	userID := "route-tamper"
	va := newPKAuthenticator(t, pkTestRPID, pkTestOrigin)

	// register
	rec := pkDoJSON(t, mux, "POST", "/api/passkeys/register/begin", userID, nil)
	var rb struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &rb)
	pkDoJSON(t, mux, "POST", "/api/passkeys/register/finish", userID, map[string]any{
		"session_data":         rb.SessionData,
		"attestation_response": va.attestation(t, pkChallengeFrom(t, rb.Challenge)),
	})

	// assert begin
	rec = pkDoJSON(t, mux, "POST", "/api/passkeys/assert/begin", userID, nil)
	var ab struct {
		Challenge   json.RawMessage `json:"challenge"`
		SessionData string          `json:"session_data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &ab)

	// tampered assertion
	va.signCount++
	rec = pkDoJSON(t, mux, "POST", "/api/passkeys/assert/finish", userID, map[string]any{
		"session_data":       ab.SessionData,
		"assertion_response": va.assertion(t, pkAssertOpts{challenge: pkChallengeFrom(t, ab.Challenge), tamper: true}),
	})
	if rec.Code == 200 {
		t.Fatalf("tampered assertion accepted (got 200): %s", rec.Body)
	}
}

// TestPasskeyRoutes_FinishBadBody covers the 400 guards on the finish handlers.
func TestPasskeyRoutes_FinishBadBody(t *testing.T) {
	mux, _ := newPasskeysTestMux(t)
	userID := "route-badbody"

	// register/finish with missing fields → 400
	rec := pkDoJSON(t, mux, "POST", "/api/passkeys/register/finish", userID, map[string]any{})
	if rec.Code != 400 {
		t.Errorf("register/finish empty: got %d want 400", rec.Code)
	}
	// assert/finish with missing fields → 400
	rec = pkDoJSON(t, mux, "POST", "/api/passkeys/assert/finish", userID, map[string]any{})
	if rec.Code != 400 {
		t.Errorf("assert/finish empty: got %d want 400", rec.Code)
	}
	// assert/begin for a user with no credentials → 400
	rec = pkDoJSON(t, mux, "POST", "/api/passkeys/assert/begin", userID, nil)
	if rec.Code != 400 {
		t.Errorf("assert/begin no creds: got %d want 400", rec.Code)
	}
	// delete missing credential → 404
	rec = pkDoJSON(t, mux, "DELETE", "/api/passkeys/no-such-id", userID, nil)
	if rec.Code != 404 {
		t.Errorf("delete unknown: got %d want 404", rec.Code)
	}
}
