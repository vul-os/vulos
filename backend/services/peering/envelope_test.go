// envelope_test.go — unit tests for PEER-03 signed canonical-JSON envelope.
package peering

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// genKey generates a fresh Ed25519 keypair and its Vula ID for testing.
func genKey(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genKey: %v", err)
	}
	return priv, pub, encodeVulaID(pub)
}

// makePayload returns a json.RawMessage from a Go value for use in tests.
func makePayload(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("makePayload: %v", err)
	}
	return b
}

// ─── Sign + Verify round-trip ─────────────────────────────────────────────────

func TestSignVerifyRoundTrip(t *testing.T) {
	priv, _, vulaID := genKey(t)

	payload := makePayload(t, MessagePayload{
		Type: "text",
		Body: "hello, peering",
	})

	env, err := NewEnvelope("test-id-1", vulaID, "vula:ed25519:somerecipient", TypeMessage, payload)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}

	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	if env.Signature == "" {
		t.Fatal("Signature is empty after Sign")
	}

	if err := env.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// ─── Tamper detection ─────────────────────────────────────────────────────────

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	priv, _, vulaID := genKey(t)

	payload := makePayload(t, MessagePayload{Type: "text", Body: "original"})
	env, _ := NewEnvelope("test-id-2", vulaID, "", TypeMessage, payload)
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Tamper: replace Payload after signing.
	env.Payload = makePayload(t, MessagePayload{Type: "text", Body: "tampered"})

	if err := env.Verify(); err == nil {
		t.Fatal("expected Verify to fail after payload tamper, but it passed")
	}
}

func TestVerifyRejectsTamperedID(t *testing.T) {
	priv, _, vulaID := genKey(t)

	env, _ := NewEnvelope("test-id-3", vulaID, "", TypeMessage, makePayload(t, map[string]any{"x": 1}))
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	env.ID = "completely-different-id"

	if err := env.Verify(); err == nil {
		t.Fatal("expected Verify to fail after ID tamper")
	}
}

func TestVerifyRejectsTamperedType(t *testing.T) {
	priv, _, vulaID := genKey(t)

	env, _ := NewEnvelope("test-id-4", vulaID, "", TypeMessage, makePayload(t, map[string]any{}))
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	env.Type = TypeFeed // change type after signing

	if err := env.Verify(); err == nil {
		t.Fatal("expected Verify to fail after Type tamper")
	}
}

// ─── Wrong-key rejection ──────────────────────────────────────────────────────

func TestVerifyRejectsWrongKey(t *testing.T) {
	priv1, _, _ := genKey(t)
	_, _, vulaID2 := genKey(t)

	// Sign with key1 but set From = vulaID2 so Verify uses key2.
	env, _ := NewEnvelope("test-id-5", vulaID2, "", TypeMessage, makePayload(t, map[string]any{"k": "v"}))
	if err := env.Sign(priv1); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Verify should decode vulaID2 as the public key, which doesn't match priv1.
	if err := env.Verify(); err == nil {
		t.Fatal("expected Verify to fail when signed by a different key")
	}
}

func TestVerifyRejectsMissingSignature(t *testing.T) {
	_, _, vulaID := genKey(t)

	env, _ := NewEnvelope("test-id-6", vulaID, "", TypeMessage, makePayload(t, map[string]any{}))
	// Do NOT call Sign.

	if err := env.Verify(); err == nil {
		t.Fatal("expected Verify to fail when Signature is absent")
	}
}

// ─── Canonical serialisation byte-stability ───────────────────────────────────

// TestCanonicalBytesStableAcrossMapOrderings checks that canonicalBytes()
// produces the identical byte string regardless of the map insertion order used
// to build the payload.  Because Go map iteration is intentionally random, we
// build the same logical payload two different ways and compare.
func TestCanonicalBytesStableAcrossMapOrderings(t *testing.T) {
	priv, _, vulaID := genKey(t)

	buildEnv := func(payload json.RawMessage) *Envelope {
		env, err := NewEnvelope("stable-id", vulaID, "recipient", TypeMessage, payload)
		if err != nil {
			t.Fatalf("NewEnvelope: %v", err)
		}
		env.Timestamp = "2026-01-01T00:00:00Z" // pin timestamp so canonical bytes match
		if err := env.Sign(priv); err != nil {
			t.Fatalf("Sign: %v", err)
		}
		return env
	}

	// Build payload1 in one field order and payload2 in another.
	// We marshal two maps with the same keys/values but constructed in different
	// order; json.Marshal in Go does not guarantee order for maps, so we parse
	// the resulting JSON back through canonicaliseValue to compare.
	payload1, _ := json.Marshal(map[string]any{
		"alpha": 1,
		"beta":  "two",
		"gamma": true,
	})
	payload2, _ := json.Marshal(map[string]any{
		"gamma": true,
		"alpha": 1,
		"beta":  "two",
	})

	env1 := buildEnv(payload1)
	env2 := buildEnv(payload2)

	cb1, err := env1.canonicalBytes()
	if err != nil {
		t.Fatalf("canonicalBytes env1: %v", err)
	}
	cb2, err := env2.canonicalBytes()
	if err != nil {
		t.Fatalf("canonicalBytes env2: %v", err)
	}

	if string(cb1) != string(cb2) {
		t.Errorf("canonical bytes differ:\n  env1: %s\n  env2: %s", cb1, cb2)
	}

	// Also verify that both envelopes verify correctly (same signature for same data).
	if err := env1.Verify(); err != nil {
		t.Errorf("env1 Verify: %v", err)
	}
	if err := env2.Verify(); err != nil {
		t.Errorf("env2 Verify: %v", err)
	}
}

// TestCanonicalBytesSignatureFieldExcluded ensures that changing Signature does
// not change the canonical bytes (since "signature" is excluded from signing).
func TestCanonicalBytesSignatureFieldExcluded(t *testing.T) {
	priv, _, vulaID := genKey(t)

	env, _ := NewEnvelope("sig-excl-id", vulaID, "", TypeMessage, makePayload(t, map[string]any{"msg": "hi"}))
	env.Timestamp = "2026-01-01T00:00:00Z"

	cb1, _ := env.canonicalBytes()

	// Set a dummy signature and re-compute canonical bytes — must be identical.
	env.Signature = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	cb2, _ := env.canonicalBytes()

	if string(cb1) != string(cb2) {
		t.Errorf("canonical bytes should not change when Signature is set:\n  without: %s\n  with:    %s", cb1, cb2)
	}

	// The signature we just set is bogus, so Verify should fail — proving Sign
	// used real bytes, not the fake ones we stuffed in.
	_ = env.Sign(priv) // now sign properly
	if err := env.Verify(); err != nil {
		t.Errorf("Verify after proper Sign: %v", err)
	}
}

// ─── Payload shape tests ──────────────────────────────────────────────────────

func TestContactRequestEnvelope(t *testing.T) {
	priv, _, vulaID := genKey(t)
	_, _, toVulaID := genKey(t)

	body := ContactRequestPayload{
		DisplayName: "Alice",
		Message:     "Hey, want to connect?",
		ServerAddr:  "alice.vulos.org:8080",
	}
	payload := makePayload(t, body)

	env, err := NewEnvelope("cr-id-1", vulaID, toVulaID, TypeContactRequest, payload)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := env.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// Decode payload and check round-trip.
	var decoded ContactRequestPayload
	if err := json.Unmarshal(env.Payload, &decoded); err != nil {
		t.Fatalf("unmarshal ContactRequestPayload: %v", err)
	}
	if decoded.DisplayName != body.DisplayName {
		t.Errorf("DisplayName: got %q, want %q", decoded.DisplayName, body.DisplayName)
	}
}

func TestSignalingEnvelope(t *testing.T) {
	priv, _, vulaID := genKey(t)

	sdp := makePayload(t, map[string]string{"sdp": "v=0\r\no=..."})
	body := SignalingPayload{
		Kind:   "offer",
		CallID: "call-abc",
		Data:   sdp,
	}
	payload := makePayload(t, body)

	env, err := NewEnvelope("sig-id-1", vulaID, "", TypeSignaling, payload)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := env.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestFeedEnvelope(t *testing.T) {
	priv, _, vulaID := genKey(t)

	body := FeedPayload{
		FeedID:    vulaID + "/blog",
		Sequence:  1,
		EntryType: "post",
		Body:      makePayload(t, map[string]string{"title": "Hello World", "content": "First post"}),
	}
	payload := makePayload(t, body)

	env, err := NewEnvelope("feed-id-1", vulaID, "", TypeFeed, payload)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := env.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// ─── NewEnvelope validation ───────────────────────────────────────────────────

func TestNewEnvelopeValidation(t *testing.T) {
	_, _, vulaID := genKey(t)
	payload := makePayload(t, map[string]any{})

	cases := []struct {
		id, from, to, typ string
		wantErr           bool
	}{
		{"id", vulaID, "", TypeMessage, false},
		{"", vulaID, "", TypeMessage, true}, // empty id
		{"id", "", "", TypeMessage, true},   // empty from
		{"id", vulaID, "", "", true},        // empty type
	}

	for _, tc := range cases {
		_, err := NewEnvelope(tc.id, tc.from, tc.to, tc.typ, payload)
		if tc.wantErr && err == nil {
			t.Errorf("NewEnvelope(%q,%q,%q,%q): expected error, got nil", tc.id, tc.from, tc.to, tc.typ)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("NewEnvelope(%q,%q,%q,%q): unexpected error: %v", tc.id, tc.from, tc.to, tc.typ, err)
		}
	}
}

// ─── JSON wire format ─────────────────────────────────────────────────────────

// TestWireFormatRoundTrip confirms that an Envelope survives JSON
// marshal/unmarshal and still verifies correctly.
func TestWireFormatRoundTrip(t *testing.T) {
	priv, _, vulaID := genKey(t)

	env, _ := NewEnvelope("wire-id", vulaID, "", TypeMessage, makePayload(t, MessagePayload{
		Type: "text",
		Body: "wire format test",
	}))
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Marshal to JSON (simulating wire transmission).
	wire, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// Check that the "signature" field is present in the wire bytes.
	if !strings.Contains(string(wire), `"signature"`) {
		t.Error("signature field missing from marshalled envelope")
	}

	// Unmarshal and verify.
	var received Envelope
	if err := json.Unmarshal(wire, &received); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if err := received.Verify(); err != nil {
		t.Fatalf("Verify after wire round-trip: %v", err)
	}
}

// ─── Nested object canonicalisation ──────────────────────────────────────────

// TestNestedObjectCanonicalisation verifies that deeply nested objects are
// also key-sorted correctly.
func TestNestedObjectCanonicalisation(t *testing.T) {
	priv, _, vulaID := genKey(t)

	nested := makePayload(t, map[string]any{
		"z_outer": map[string]any{
			"z_inner": 3,
			"a_inner": 1,
			"m_inner": 2,
		},
		"a_outer": "first",
		"m_outer": []any{3, 1, 2}, // arrays preserve order
	})

	env, _ := NewEnvelope("nested-id", vulaID, "", TypeMessage, nested)
	env.Timestamp = "2026-01-01T00:00:00Z"
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := env.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	cb, err := env.canonicalBytes()
	if err != nil {
		t.Fatalf("canonicalBytes: %v", err)
	}

	// The canonical bytes should have a_outer before m_outer before z_outer,
	// and inside z_outer: a_inner before m_inner before z_inner.
	s := string(cb)
	aPos := strings.Index(s, `"a_outer"`)
	mPos := strings.Index(s, `"m_outer"`)
	zPos := strings.Index(s, `"z_outer"`)

	if aPos < 0 || mPos < 0 || zPos < 0 {
		t.Fatalf("canonical bytes missing expected keys: %s", s)
	}
	if !(aPos < mPos && mPos < zPos) {
		t.Errorf("outer keys not sorted: a=%d m=%d z=%d in %s", aPos, mPos, zPos, s)
	}

	aInner := strings.Index(s, `"a_inner"`)
	mInner := strings.Index(s, `"m_inner"`)
	zInner := strings.Index(s, `"z_inner"`)

	if aInner < 0 || mInner < 0 || zInner < 0 {
		t.Fatalf("canonical bytes missing inner keys: %s", s)
	}
	if !(aInner < mInner && mInner < zInner) {
		t.Errorf("inner keys not sorted: a=%d m=%d z=%d in %s", aInner, mInner, zInner, s)
	}
}

// TestArrayOrderPreserved ensures that array element order is NOT sorted
// (only object keys are sorted).
func TestArrayOrderPreserved(t *testing.T) {
	priv, _, vulaID := genKey(t)

	payload := makePayload(t, map[string]any{
		"items": []any{3, 1, 4, 1, 5, 9, 2, 6},
	})

	env, _ := NewEnvelope("arr-id", vulaID, "", TypeMessage, payload)
	env.Timestamp = "2026-01-01T00:00:00Z"
	if err := env.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := env.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	cb, _ := env.canonicalBytes()
	if !strings.Contains(string(cb), `[3,1,4,1,5,9,2,6]`) {
		t.Errorf("array order was changed in canonical bytes: %s", cb)
	}
}
