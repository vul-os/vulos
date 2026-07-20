// security_test.go — peering-specific security regression tests.
//
// These tests guard the hardening invariants documented in SECURITY-OSS.md §5
// (Peering) and must fail if any of the following properties regress:
//
//	PEER-SEC-01  Envelope with bad/corrupted signature rejected
//	PEER-SEC-02  Relay: deposit with forged signature rejected
//	PEER-SEC-03  Relay: pickup with stale timestamp rejected (replay window)
//	PEER-SEC-04  Relay: audience mismatch — wrong recipient rejected
//	PEER-SEC-05  Relay: unknown (unapproved) sender rejected
//	PEER-SEC-06  Inbound middleware: valid sig from unapproved sender → 403
//	PEER-SEC-07  Envelope: absent signature field → Verify returns error
//	PEER-SEC-08  Envelope: tampered payload invalidates valid signature
//	PEER-SEC-09  Envelope: tampered type invalidates valid signature
package peering

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// secGenKey generates an Ed25519 keypair and Vula ID for security tests.
func secGenKey(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("secGenKey: %v", err)
	}
	return priv, pub, encodeVulaID(pub)
}

// secMakeRelayStore creates a RelayStore backed by a temp dir with relay enabled.
func secMakeRelayStore(t *testing.T) (*RelayStore, *ContactStore) {
	t.Helper()
	dir := t.TempDir()
	cs, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("secMakeRelayStore NewContactStore: %v", err)
	}
	rs, err := NewRelayStore(dir, cs)
	if err != nil {
		t.Fatalf("secMakeRelayStore NewRelayStore: %v", err)
	}
	if err := rs.SetConfig(RelayConfig{Enabled: true, CapacityMB: 10, TTLHours: 72}); err != nil {
		t.Fatalf("secMakeRelayStore SetConfig: %v", err)
	}
	return rs, cs
}

// secAddApproved adds a contact to cs in the Approved state.
func secAddApproved(t *testing.T, cs *ContactStore, vulaID, name string) {
	t.Helper()
	if err := cs.Add(vulaID, name, "server:8080"); err != nil {
		t.Fatalf("secAddApproved Add(%s): %v", vulaID, err)
	}
	if err := cs.Approve(vulaID, DefaultPerms()); err != nil {
		t.Fatalf("secAddApproved Approve(%s): %v", vulaID, err)
	}
}

// secSignedDeposit builds a valid signed relayDepositRequest.
func secSignedDeposit(t *testing.T, senderPriv ed25519.PrivateKey, senderID, recipientID, blobID string, blob []byte) relayDepositRequest {
	t.Helper()
	blobB64 := base64.StdEncoding.EncodeToString(blob)
	nonce := fmt.Sprintf("%d", time.Now().UnixNano())
	req := relayDepositRequest{
		To:           recipientID,
		BlobID:       blobID,
		BlobB64:      blobB64,
		TTLHours:     24,
		SenderVulaID: senderID,
		Nonce:        nonce,
	}
	canonical, err := relayDepositCanonical(req)
	if err != nil {
		t.Fatalf("secSignedDeposit canonical: %v", err)
	}
	sig := ed25519.Sign(senderPriv, canonical)
	req.Signature = base64.RawURLEncoding.EncodeToString(sig)
	return req
}

// ─── PEER-SEC-01: Envelope bad signature rejected ─────────────────────────────

// TestSEC_Envelope_BadSigRejected verifies that a corrupted signature causes
// Verify to return a non-nil error.
// Guards: SECURITY-OSS.md §5 — Ed25519 sig verified before forwarding.
func TestSEC_Envelope_BadSigRejected(t *testing.T) {
	priv, _, vulaID := secGenKey(t)

	env, err := NewEnvelope("sec-bad-sig-01", vulaID, "vula:other", TypeMessage,
		json.RawMessage(`{"type":"text","body":"hello"}`))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Corrupt the signature — replace with random bytes of the same length.
	badSig := make([]byte, ed25519.SignatureSize)
	rand.Read(badSig)
	env.Signature = base64.RawURLEncoding.EncodeToString(badSig)

	if err := env.Verify(); err == nil {
		t.Fatal("PEER-SEC-01 REGRESSION: Envelope.Verify returned nil for a corrupted signature — message forgery undetected")
	}
}

// ─── PEER-SEC-07: Absent signature rejected ───────────────────────────────────

// TestSEC_Envelope_AbsentSignatureRejected verifies that an envelope with no
// Signature field fails Verify.
// Guards: SECURITY-OSS.md §5 — absent signature is not treated as valid.
func TestSEC_Envelope_AbsentSignatureRejected(t *testing.T) {
	_, _, vulaID := secGenKey(t)

	env, err := NewEnvelope("sec-no-sig-01", vulaID, "", TypeMessage, json.RawMessage(`null`))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	// Do NOT sign.

	if err := env.Verify(); err == nil {
		t.Fatal("PEER-SEC-07 REGRESSION: Envelope.Verify returned nil for an unsigned envelope")
	}
}

// ─── PEER-SEC-08: Tampered payload rejected ───────────────────────────────────

// TestSEC_Envelope_TamperedPayloadRejected verifies that modifying the Payload
// after signing invalidates the signature.
// Guards: SECURITY-OSS.md §5 — canonical-JSON covers all fields incl. payload.
func TestSEC_Envelope_TamperedPayloadRejected(t *testing.T) {
	priv, _, vulaID := secGenKey(t)

	env, _ := NewEnvelope("sec-tamper-payload", vulaID, "", TypeMessage,
		json.RawMessage(`{"type":"text","body":"original"}`))
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Tamper.
	env.Payload = json.RawMessage(`{"type":"text","body":"TAMPERED"}`)

	if err := env.Verify(); err == nil {
		t.Fatal("PEER-SEC-08 REGRESSION: Envelope.Verify returned nil after payload tamper — message content can be altered")
	}
}

// ─── PEER-SEC-09: Tampered type rejected ─────────────────────────────────────

// TestSEC_Envelope_TamperedTypeRejected verifies that changing the Type field
// after signing invalidates the signature.
// Guards: SECURITY-OSS.md §5 — type escalation (e.g. message → signaling) blocked.
func TestSEC_Envelope_TamperedTypeRejected(t *testing.T) {
	priv, _, vulaID := secGenKey(t)

	env, _ := NewEnvelope("sec-tamper-type", vulaID, "", TypeMessage, json.RawMessage(`null`))
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Escalate type.
	env.Type = TypeSignaling

	if err := env.Verify(); err == nil {
		t.Fatal("PEER-SEC-09 REGRESSION: Envelope.Verify returned nil after type tamper — type escalation possible")
	}
}

// ─── PEER-SEC-02: Relay deposit forged signature rejected ─────────────────────

// TestSEC_Relay_DepositForgeSigRejected verifies that a deposit with a
// random/forged signature is refused even when the sender is in contacts.
// Guards: SECURITY-OSS.md §5 — relay verifies Ed25519 sig on every deposit.
func TestSEC_Relay_DepositForgeSigRejected(t *testing.T) {
	rs, cs := secMakeRelayStore(t)

	senderPriv, _, senderID := secGenKey(t)
	_, _, recipientID := secGenKey(t)
	secAddApproved(t, cs, senderID, "Sender")

	req := secSignedDeposit(t, senderPriv, senderID, recipientID, "forge-sig-blob-01", []byte("data"))

	// Replace valid signature with garbage.
	bad := make([]byte, ed25519.SignatureSize)
	rand.Read(bad)
	req.Signature = base64.RawURLEncoding.EncodeToString(bad)

	if err := rs.Deposit(req); err == nil {
		t.Fatal("PEER-SEC-02 REGRESSION: relay accepted deposit with forged signature — attacker can relay on behalf of any identity")
	}
}

// ─── PEER-SEC-05: Unknown (unapproved) sender rejected ────────────────────────

// TestSEC_Relay_UnknownSenderRejected verifies that a validly-signed deposit
// from a sender not in the contacts store (no mutual trust) is refused.
// Guards: SECURITY-OSS.md §5 — mutual-trust check on relay deposit.
func TestSEC_Relay_UnknownSenderRejected(t *testing.T) {
	rs, _ := secMakeRelayStore(t)
	// Intentionally do NOT add senderID to contacts.

	senderPriv, _, senderID := secGenKey(t)
	_, _, recipientID := secGenKey(t)

	req := secSignedDeposit(t, senderPriv, senderID, recipientID, "unknown-sender-blob-01", []byte("data"))

	if err := rs.Deposit(req); err == nil {
		t.Fatal("PEER-SEC-05 REGRESSION: relay accepted deposit from unapproved sender — open relay, anyone can deposit")
	}
}

// ─── PEER-SEC-03: Stale timestamp (replay window) rejected ────────────────────

// TestSEC_Relay_StalePickupTimestampRejected verifies that a pickup
// authentication token with a timestamp outside ±5 min is rejected.
// Guards: SECURITY-OSS.md §5 — timestamp tolerance ±5 min prevents replay.
func TestSEC_Relay_StalePickupTimestampRejected(t *testing.T) {
	rs, _ := secMakeRelayStore(t)

	priv, _, vulaID := secGenKey(t)

	// Build a pickup auth with a timestamp 10 minutes in the past.
	staleTS := fmt.Sprintf("%d", time.Now().Add(-10*time.Minute).Unix())
	msg := []byte(vulaID + "." + staleTS)
	staleSig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, msg))

	_, err := rs.Pickup(vulaID, staleTS, staleSig)
	if err == nil {
		t.Fatal("PEER-SEC-03 REGRESSION: relay accepted pickup with 10-minute stale timestamp — indefinite replay possible")
	}
}

// TestSEC_Relay_FutureTimestampRejected verifies that a timestamp 10 minutes
// in the future is also rejected (prevents pre-generated replay tokens).
func TestSEC_Relay_FutureTimestampRejected(t *testing.T) {
	rs, _ := secMakeRelayStore(t)

	priv, _, vulaID := secGenKey(t)

	futureTS := fmt.Sprintf("%d", time.Now().Add(10*time.Minute).Unix())
	msg := []byte(vulaID + "." + futureTS)
	futureSig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, msg))

	_, err := rs.Pickup(vulaID, futureTS, futureSig)
	if err == nil {
		t.Fatal("PEER-SEC-03 REGRESSION: relay accepted pickup with 10-minute future timestamp — pre-issued replay tokens possible")
	}
}

// ─── PEER-SEC-04: Audience mismatch rejected ──────────────────────────────────

// TestSEC_Relay_AudienceMismatchRejected verifies that recipientB cannot pick
// up blobs destined for recipientA even if they present recipientA's Vula ID
// but sign with their own key (sig mismatch = 401).
// Guards: SECURITY-OSS.md §5 — mutual-auth on pickup, cross-user data blocked.
func TestSEC_Relay_AudienceMismatchRejected(t *testing.T) {
	rs, cs := secMakeRelayStore(t)

	senderPriv, _, senderID := secGenKey(t)
	_, _, recipIDA := secGenKey(t)

	secAddApproved(t, cs, senderID, "Sender")

	// Deposit a blob for recipientA.
	req := secSignedDeposit(t, senderPriv, senderID, recipIDA, "audience-blob-sec-01", []byte("confidential"))
	if err := rs.Deposit(req); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	// RecipientB tries to pick up by presenting recipientA's ID but signing
	// with their own (B's) private key.  Since the sig is verified against the
	// public key embedded in recipIDA, it must fail.
	privB, _, _ := secGenKey(t)
	tsStr := fmt.Sprintf("%d", time.Now().Unix())
	msgForA := []byte(recipIDA + "." + tsStr)
	sigByB := base64.RawURLEncoding.EncodeToString(ed25519.Sign(privB, msgForA))

	_, err := rs.Pickup(recipIDA, tsStr, sigByB)
	if err == nil {
		t.Fatal("PEER-SEC-04 REGRESSION: relay allowed pickup by wrong key for recipient ID — cross-user blob access possible")
	}
}

// ─── PEER-SEC-06: InboundMiddleware unapproved sender rejected ────────────────

// TestSEC_InboundMiddleware_UnapprovedSenderReturns403 verifies that the
// InboundMiddleware returns 403 for a validly-signed envelope whose sender is
// not in the local contacts store.
// Guards: SECURITY-OSS.md §5 — unknown sender rejected at inbound boundary.
func TestSEC_InboundMiddleware_UnapprovedSenderReturns403(t *testing.T) {
	dir := t.TempDir()
	cs, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}

	// Intentionally do NOT add the sender to contacts.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux := http.NewServeMux()
	mux.Handle("/api/peering/inbound/", InboundMiddleware(cs, inner))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	priv, _, senderID := secGenKey(t)
	env, err := NewEnvelope("inbound-sec-01", senderID, "vula:ed25519:recipient",
		TypeMessage, json.RawMessage(`{"type":"text","body":"attack"}`))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	body, _ := json.Marshal(env)
	resp, err := http.Post(srv.URL+"/api/peering/inbound/message",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PEER-SEC-06 REGRESSION: expected 403 for unapproved sender, got %d — unknown peer accepted by inbound middleware", resp.StatusCode)
	}
}

// TestSEC_InboundMiddleware_MissingSignatureReturns401 verifies that an
// envelope with no signature field is rejected with HTTP 401.
// Guards: SECURITY-OSS.md §5 — signature required on all inbound messages.
func TestSEC_InboundMiddleware_MissingSignatureReturns401(t *testing.T) {
	dir := t.TempDir()
	cs, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux := http.NewServeMux()
	mux.Handle("/api/peering/inbound/", InboundMiddleware(cs, inner))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, _, senderID := secGenKey(t)

	// Construct an envelope but do NOT sign it.
	env := &Envelope{
		ID:        "inbound-nosig-01",
		From:      senderID,
		To:        "vula:ed25519:recipient",
		Type:      TypeMessage,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   json.RawMessage(`{"type":"text","body":"test"}`),
		// Signature intentionally omitted.
	}

	body, _ := json.Marshal(env)
	resp, err := http.Post(srv.URL+"/api/peering/inbound/message",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("PEER-SEC-06/07 REGRESSION: expected 401 for unsigned envelope, got %d", resp.StatusCode)
	}
}

// TestSEC_InboundMiddleware_ApprovedSenderPasses verifies the positive path:
// a validly-signed envelope from an approved contact is forwarded (200).
func TestSEC_InboundMiddleware_ApprovedSenderPasses(t *testing.T) {
	dir := t.TempDir()
	cs, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}

	priv, _, senderID := secGenKey(t)
	secAddApproved(t, cs, senderID, "TrustedPeer")

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux := http.NewServeMux()
	mux.Handle("/api/peering/inbound/", InboundMiddleware(cs, inner))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	env, _ := NewEnvelope("inbound-ok-01", senderID, "vula:ed25519:me",
		TypeMessage, json.RawMessage(`{"type":"text","body":"hello"}`))
	env.Sign(priv) //nolint:errcheck

	body, _ := json.Marshal(env)
	resp, err := http.Post(srv.URL+"/api/peering/inbound/message",
		"application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("positive path broken: expected 200 for approved sender, got %d", resp.StatusCode)
	}
}
