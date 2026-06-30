// relay_attest_test.go — unit tests for relay_attest.go (PEER-39).
package peering

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── AttestError ─────────────────────────────────────────────────────────────

func TestAttestError_ErrorString(t *testing.T) {
	e := &AttestError{Code: "test-code", Msg: "something went wrong"}
	if !strings.Contains(e.Error(), "test-code") {
		t.Errorf("expected error to contain code, got: %s", e.Error())
	}
	if !strings.Contains(e.Error(), "something went wrong") {
		t.Errorf("expected error to contain message, got: %s", e.Error())
	}
}

func TestAttestError_WithCause(t *testing.T) {
	inner := &AttestError{Code: "inner", Msg: "inner error"}
	outer := attestErr("outer", "outer error", inner)
	if outer.Unwrap() != inner {
		t.Errorf("Unwrap() should return inner error")
	}
	if !strings.Contains(outer.Error(), "inner error") {
		t.Errorf("outer error should mention cause, got: %s", outer.Error())
	}
}

// ─── AttestVerifyRelay: basic field checks ───────────────────────────────────

func TestAttestVerifyRelay_MissingProvider(t *testing.T) {
	doc := AttestDoc{
		RelayVulaID: "vula:ed25519:test",
		IssuedAt:    time.Now(),
	}
	err := AttestVerifyRelay(doc, AttestPolicy{})
	assertAttestCode(t, err, "missing-provider")
}

func TestAttestVerifyRelay_ProviderMismatch(t *testing.T) {
	doc := AttestDoc{
		Provider:    AttestProviderNoop,
		RelayVulaID: "vula:ed25519:test",
		IssuedAt:    time.Now(),
	}
	err := AttestVerifyRelay(doc, AttestPolicy{Provider: AttestProviderNitro})
	assertAttestCode(t, err, "provider-mismatch")
}

func TestAttestVerifyRelay_MissingRelayID(t *testing.T) {
	doc := AttestDoc{
		Provider: AttestProviderNoop,
		IssuedAt: time.Now(),
	}
	err := AttestVerifyRelay(doc, AttestPolicy{Provider: AttestProviderNoop})
	assertAttestCode(t, err, "missing-relay-id")
}

func TestAttestVerifyRelay_MissingIssuedAt(t *testing.T) {
	doc := AttestDoc{
		Provider:    AttestProviderNoop,
		RelayVulaID: "vula:ed25519:test",
	}
	err := AttestVerifyRelay(doc, AttestPolicy{Provider: AttestProviderNoop})
	assertAttestCode(t, err, "missing-issued-at")
}

func TestAttestVerifyRelay_DocExpired(t *testing.T) {
	doc := AttestDoc{
		Provider:    AttestProviderNoop,
		RelayVulaID: "vula:ed25519:test",
		IssuedAt:    time.Now().Add(-2 * time.Hour),
	}
	policy := AttestPolicy{Provider: AttestProviderNoop, MaxAge: time.Hour}
	err := AttestVerifyRelay(doc, policy)
	assertAttestCode(t, err, "doc-expired")
}

func TestAttestVerifyRelay_DocFutureDated(t *testing.T) {
	doc := AttestDoc{
		Provider:    AttestProviderNoop,
		RelayVulaID: "vula:ed25519:test",
		IssuedAt:    time.Now().Add(2 * time.Hour),
	}
	policy := AttestPolicy{Provider: AttestProviderNoop, MaxAge: time.Hour}
	err := AttestVerifyRelay(doc, policy)
	assertAttestCode(t, err, "doc-expired")
}

func TestAttestVerifyRelay_UnknownProvider(t *testing.T) {
	doc := AttestDoc{
		Provider:    "unknown-provider",
		RelayVulaID: "vula:ed25519:test",
		IssuedAt:    time.Now(),
	}
	// Pin the (unregistered) provider so we reach the verifier-lookup step.
	err := AttestVerifyRelay(doc, AttestPolicy{Provider: "unknown-provider"})
	assertAttestCode(t, err, "unknown-provider")
}

func TestAttestVerifyRelay_EmptyPolicyRejected(t *testing.T) {
	// DEFAULT-DENY: an empty policy (no pinned provider) must be rejected even
	// for a structurally complete document.
	doc := AttestDoc{
		Provider:    AttestProviderNoop,
		RelayVulaID: "vula:ed25519:test",
		IssuedAt:    time.Now(),
	}
	err := AttestVerifyRelay(doc, AttestPolicy{})
	assertAttestCode(t, err, "empty-policy")
}

func TestAttestVerifyRelay_NoopVerifierRejected(t *testing.T) {
	// Even with the provider pinned, the (now fail-closed, unregistered) noop
	// provider must not yield a pass: it is not in the default registry.
	doc := AttestDoc{
		Provider:    AttestProviderNoop,
		RelayVulaID: "vula:ed25519:test",
		IssuedAt:    time.Now(),
	}
	err := AttestVerifyRelay(doc, AttestPolicy{Provider: AttestProviderNoop})
	assertAttestCode(t, err, "unknown-provider")
}

func TestAttestVerifyRelay_NoMaxAge_OldDocAccepted(t *testing.T) {
	// Zero MaxAge means no age enforcement: an old doc passes the age gate and
	// reaches (and is accepted by) a registered, passing verifier.
	const okProvider AttestProvider = "test-nomaxage-provider-39"
	AttestRegisterVerifier(okProvider, attestVerifierFunc(func(_ AttestDoc, _ AttestPolicy) error {
		return nil
	}))
	doc := AttestDoc{
		Provider:    okProvider,
		RelayVulaID: "vula:ed25519:test",
		IssuedAt:    time.Now().Add(-365 * 24 * time.Hour),
	}
	if err := AttestVerifyRelay(doc, AttestPolicy{Provider: okProvider}); err != nil {
		t.Fatalf("expected old doc to be accepted when MaxAge=0, got: %v", err)
	}
}

// ─── AttestRegisterVerifier ───────────────────────────────────────────────────

func TestAttestRegisterVerifier_Custom(t *testing.T) {
	const customProvider AttestProvider = "test-custom-provider-39"

	called := false
	AttestRegisterVerifier(customProvider, attestVerifierFunc(func(_ AttestDoc, _ AttestPolicy) error {
		called = true
		return nil
	}))

	doc := AttestDoc{
		Provider:    customProvider,
		RelayVulaID: "vula:ed25519:test",
		IssuedAt:    time.Now(),
	}
	if err := AttestVerifyRelay(doc, AttestPolicy{Provider: customProvider}); err != nil {
		t.Fatalf("custom verifier: unexpected error: %v", err)
	}
	if !called {
		t.Fatal("custom verifier was not called")
	}
}

func TestAttestRegisterVerifier_CustomReject(t *testing.T) {
	const badProvider AttestProvider = "test-reject-provider-39"

	AttestRegisterVerifier(badProvider, attestVerifierFunc(func(_ AttestDoc, _ AttestPolicy) error {
		return attestErr("custom-reject", "always reject", nil)
	}))

	doc := AttestDoc{
		Provider:    badProvider,
		RelayVulaID: "vula:ed25519:test",
		IssuedAt:    time.Now(),
	}
	err := AttestVerifyRelay(doc, AttestPolicy{Provider: badProvider})
	assertAttestCode(t, err, "custom-reject")
}

// ─── AttestNitroVerifier: PCR checks ─────────────────────────────────────────

func TestAttestNitroVerifier_WrongProviderPanics(t *testing.T) {
	v := AttestNitroVerifier{}
	doc := AttestDoc{Provider: AttestProviderNoop}
	err := v.Verify(doc, AttestPolicy{})
	assertAttestCode(t, err, "wrong-provider")
}

func TestAttestNitroVerifier_FailsClosedEvenWithMatchingPCRs(t *testing.T) {
	// SECURITY: PCRs on the doc are self-asserted (no verified COSE signature
	// covers them). The Nitro verifier must reject even when they "match" the
	// policy — otherwise a malicious relay simply sets matching PCRs.
	v := AttestNitroVerifier{}
	doc := AttestDoc{
		Provider:    AttestProviderNitro,
		RelayVulaID: "vula:ed25519:test",
		IssuedAt:    time.Now(),
		PCRs: map[string]string{
			"0": "aabbccdd",
			"1": "11223344",
		},
	}
	policy := AttestPolicy{
		ExpectedPCRs: map[string]string{
			"0": "AABBCCDD",
			"1": "11223344",
		},
	}
	err := v.Verify(doc, policy)
	assertAttestCode(t, err, "cose-unverified")
}

func TestAttestNitroVerifier_FailsClosed_NoPCRsNoCertChain(t *testing.T) {
	// With nothing to check the verifier must still fail closed — it never
	// returns nil until COSE_Sign1 verification is implemented.
	v := AttestNitroVerifier{}
	doc := AttestDoc{
		Provider:    AttestProviderNitro,
		RelayVulaID: "vula:ed25519:test",
		IssuedAt:    time.Now(),
	}
	err := v.Verify(doc, AttestPolicy{})
	assertAttestCode(t, err, "cose-unverified")
}

// The PCR-comparison helper is retained for use once PCRs are read from the
// signed NSM payload; verify its matching logic directly.
func TestAttestCheckPCRs_Missing(t *testing.T) {
	err := attestCheckPCRs(
		map[string]string{"0": "aabbccdd"},
		map[string]string{"0": "aabbccdd", "8": "deadbeef"},
	)
	assertAttestCode(t, err, "pcr-missing")
}

func TestAttestCheckPCRs_Mismatch(t *testing.T) {
	err := attestCheckPCRs(
		map[string]string{"0": "aabbccdd"},
		map[string]string{"0": "deadbeef"},
	)
	assertAttestCode(t, err, "pcr-mismatch")
}

func TestAttestCheckPCRs_MatchCaseInsensitive(t *testing.T) {
	err := attestCheckPCRs(
		map[string]string{"0": "aabbccdd", "1": "11223344"},
		map[string]string{"0": "AABBCCDD", "1": "11223344"},
	)
	if err != nil {
		t.Fatalf("expected case-insensitive PCR match, got: %v", err)
	}
}

// ─── AttestStore ─────────────────────────────────────────────────────────────

func TestAttestStore_EmptyOnNew(t *testing.T) {
	as := NewAttestStore()
	_, ok := as.Get()
	if ok {
		t.Fatal("expected empty store, got a document")
	}
}

func TestAttestStore_SetAndGet(t *testing.T) {
	as := NewAttestStore()
	doc := AttestDoc{
		Provider:    AttestProviderNoop,
		RelayVulaID: "vula:ed25519:relay1",
		IssuedAt:    time.Now().UTC().Truncate(time.Second),
	}
	if err := as.Set(doc); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok := as.Get()
	if !ok {
		t.Fatal("expected document after Set")
	}
	if got.RelayVulaID != doc.RelayVulaID {
		t.Errorf("RelayVulaID: got %q want %q", got.RelayVulaID, doc.RelayVulaID)
	}
	if got.Provider != doc.Provider {
		t.Errorf("Provider: got %q want %q", got.Provider, doc.Provider)
	}
}

func TestAttestStore_Set_MissingProvider(t *testing.T) {
	as := NewAttestStore()
	err := as.Set(AttestDoc{
		RelayVulaID: "vula:ed25519:relay1",
		IssuedAt:    time.Now(),
	})
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestAttestStore_Set_MissingRelayID(t *testing.T) {
	as := NewAttestStore()
	err := as.Set(AttestDoc{
		Provider: AttestProviderNoop,
		IssuedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("expected error for missing relay_vula_id")
	}
}

func TestAttestStore_Set_MissingIssuedAt(t *testing.T) {
	as := NewAttestStore()
	err := as.Set(AttestDoc{
		Provider:    AttestProviderNoop,
		RelayVulaID: "vula:ed25519:relay1",
	})
	if err == nil {
		t.Fatal("expected error for zero issued_at")
	}
}

func TestAttestStore_ImmutableOnGet(t *testing.T) {
	as := NewAttestStore()
	doc := AttestDoc{
		Provider:    AttestProviderNoop,
		RelayVulaID: "vula:ed25519:relay1",
		IssuedAt:    time.Now(),
	}
	if err := as.Set(doc); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got1, _ := as.Get()
	got1.RelayVulaID = "mutated"
	got2, _ := as.Get()
	if got2.RelayVulaID == "mutated" {
		t.Fatal("Get returned a mutable reference to internal state")
	}
}

// ─── HTTP handler: GET /api/peering/relay/attest ─────────────────────────────

func TestHandleGetAttest_NoDocYet(t *testing.T) {
	as := NewAttestStore()
	mux := http.NewServeMux()
	RegisterAttestHandlers(mux, as)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/relay/attest", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestHandleGetAttest_ReturnsDoc(t *testing.T) {
	as := NewAttestStore()
	doc := AttestDoc{
		Provider:    AttestProviderNoop,
		RelayVulaID: "vula:ed25519:relay-http-test",
		IssuedAt:    time.Now().UTC(),
	}
	if err := as.Set(doc); err != nil {
		t.Fatalf("Set: %v", err)
	}

	mux := http.NewServeMux()
	RegisterAttestHandlers(mux, as)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/relay/attest", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	var got AttestDoc
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.RelayVulaID != doc.RelayVulaID {
		t.Errorf("RelayVulaID: got %q want %q", got.RelayVulaID, doc.RelayVulaID)
	}
	if got.Provider != doc.Provider {
		t.Errorf("Provider: got %q want %q", got.Provider, doc.Provider)
	}
}

func TestRegisterAttestHandlers_NilMux_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil mux")
		}
	}()
	RegisterAttestHandlers(nil, NewAttestStore())
}

func TestRegisterAttestHandlers_NilStore_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil store")
		}
	}()
	RegisterAttestHandlers(http.NewServeMux(), nil)
}

// ─── AttestFetchAndVerifyWithClient ───────────────────────────────────────────

func TestAttestFetchAndVerifyWithClient_Success(t *testing.T) {
	// Register a deliberate passing verifier under a dedicated provider so the
	// fetch+verify happy path can be exercised (the default registry is
	// default-deny and ships no "accept anything" verifier).
	const fetchProvider AttestProvider = "test-fetch-provider-39"
	AttestRegisterVerifier(fetchProvider, attestVerifierFunc(func(_ AttestDoc, _ AttestPolicy) error {
		return nil
	}))
	doc := AttestDoc{
		Provider:    fetchProvider,
		RelayVulaID: "vula:ed25519:fetch-test-relay",
		IssuedAt:    time.Now().UTC(),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(doc) //nolint:errcheck
	}))
	defer srv.Close()

	got, err := AttestFetchAndVerifyWithClient(srv.Client(), srv.URL, AttestPolicy{Provider: fetchProvider})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.RelayVulaID != doc.RelayVulaID {
		t.Errorf("RelayVulaID: got %q want %q", got.RelayVulaID, doc.RelayVulaID)
	}
}

func TestAttestFetchAndVerifyWithClient_BadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := AttestFetchAndVerifyWithClient(srv.Client(), srv.URL, AttestPolicy{})
	assertAttestCode(t, err, "fetch-bad-status")
}

func TestAttestFetchAndVerifyWithClient_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not-json")) //nolint:errcheck
	}))
	defer srv.Close()

	_, err := AttestFetchAndVerifyWithClient(srv.Client(), srv.URL, AttestPolicy{})
	assertAttestCode(t, err, "decode-failed")
}

func TestAttestFetchAndVerifyWithClient_NilClient(t *testing.T) {
	_, err := AttestFetchAndVerifyWithClient(nil, "http://example.com", AttestPolicy{})
	assertAttestCode(t, err, "nil-client")
}

func TestAttestFetchAndVerifyWithClient_PolicyEnforced(t *testing.T) {
	// Server returns a doc with wrong provider — policy should reject it.
	doc := AttestDoc{
		Provider:    AttestProviderNoop,
		RelayVulaID: "vula:ed25519:fetch-test-relay",
		IssuedAt:    time.Now().UTC(),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(doc) //nolint:errcheck
	}))
	defer srv.Close()

	_, err := AttestFetchAndVerifyWithClient(srv.Client(), srv.URL,
		AttestPolicy{Provider: AttestProviderNitro}) // expect Nitro, got Noop
	assertAttestCode(t, err, "provider-mismatch")
}

// ─── AttestDocumentDigest ─────────────────────────────────────────────────────

func TestAttestDocumentDigest_Empty(t *testing.T) {
	if got := AttestDocumentDigest(AttestDoc{}); got != "" {
		t.Errorf("expected empty string for empty doc, got %q", got)
	}
}

func TestAttestDocumentDigest_InvalidBase64(t *testing.T) {
	doc := AttestDoc{RawDocument: "not-valid-base64!!!"}
	if got := AttestDocumentDigest(doc); got != "" {
		t.Errorf("expected empty string for invalid base64, got %q", got)
	}
}

func TestAttestDocumentDigest_Valid(t *testing.T) {
	import_base64 := "aGVsbG8gd29ybGQ=" // base64 of "hello world"
	doc := AttestDoc{RawDocument: import_base64}
	got := AttestDocumentDigest(doc)
	// sha256("hello world") = b94d27b9...
	if !strings.HasPrefix(got, "b94d27b9") {
		t.Errorf("unexpected digest prefix: %q", got)
	}
}

// ─── Sender rejects absent attestation ───────────────────────────────────────

// TestAttestSenderRejectsAbsent verifies the core acceptance criterion: when
// a relay returns a 503 (no attestation document), the sender-side flow
// correctly fails rather than proceeding.
func TestAttestSenderRejectsAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := AttestFetchAndVerifyWithClient(srv.Client(), srv.URL, AttestPolicy{})
	if err == nil {
		t.Fatal("expected error when relay has no attestation doc, got nil")
	}
	var ae *AttestError
	if !isAttestError(err, &ae) {
		t.Fatalf("expected *AttestError, got: %T %v", err, err)
	}
}

// TestAttestSenderRejectsFailed verifies that a relay whose attestation
// document fails policy (wrong provider) is rejected.
func TestAttestSenderRejectsFailed(t *testing.T) {
	doc := AttestDoc{
		Provider:    AttestProviderNoop,
		RelayVulaID: "vula:ed25519:relay",
		IssuedAt:    time.Now(),
	}
	// Inline verification without HTTP.
	err := AttestVerifyRelay(doc, AttestPolicy{Provider: AttestProviderNitro})
	if err == nil {
		t.Fatal("expected rejection for provider mismatch")
	}
}

// ─── Test helpers ─────────────────────────────────────────────────────────────

// attestVerifierFunc allows functions to be used as [AttestVerifier] in tests.
type attestVerifierFunc func(AttestDoc, AttestPolicy) error

func (f attestVerifierFunc) Verify(doc AttestDoc, policy AttestPolicy) error {
	return f(doc, policy)
}

// assertAttestCode asserts that err is a non-nil *AttestError with the given
// code.
func assertAttestCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %q, got nil", wantCode)
	}
	var ae *AttestError
	if !isAttestError(err, &ae) {
		t.Fatalf("expected *AttestError, got %T: %v", err, err)
	}
	if ae.Code != wantCode {
		t.Errorf("expected code %q, got %q (full error: %v)", wantCode, ae.Code, ae)
	}
}

// isAttestError reports whether err (or any error in its chain) is an
// *AttestError, and stores it in out.
func isAttestError(err error, out **AttestError) bool {
	var ae *AttestError
	if errors.As(err, &ae) {
		if out != nil {
			*out = ae
		}
		return true
	}
	return false
}
