package stream

import (
	"errors"
	"testing"
)

// fakeVerifier is a controllable A13_WebAuthnVerifier for tests.
type fakeVerifier struct{ accept bool }

func (f fakeVerifier) Verify(_ string, _ []byte) error {
	if f.accept {
		return nil
	}
	return errors.New("rejected")
}

// TestShouldGateInput_ArmsOnlyWithVerifierOrStrict is the wave-34 AUTH-13
// regression. The gate must be armed when it can actually be lifted (a real
// verifier is wired) OR the operator opted into strict fail-closed gating. It
// must NOT be armed when neither holds — arming with only the reject-all stub
// would permanently block legitimate input.
func TestShouldGateInput_ArmsOnlyWithVerifierOrStrict(t *testing.T) {
	// No verifier, strict off → do NOT gate (would brick input).
	p := NewPool()
	if p.shouldGateInput() {
		t.Error("no verifier + strict off: gate armed (would permanently block input)")
	}

	// Real verifier wired, strict off → gate.
	p2 := NewPool()
	p2.SetWebAuthnVerifier(fakeVerifier{accept: true})
	if !p2.shouldGateInput() {
		t.Error("verifier wired: gate should be armed")
	}

	// No verifier, strict ON → gate (explicit fail-closed opt-in).
	p3 := NewPool()
	p3.SetStrictInputGate(true)
	if !p3.shouldGateInput() {
		t.Error("strict gating on: gate should be armed even without a verifier")
	}
}

// TestRequireAssertion_LiftsGateOnValidAssertion proves the armed gate can be
// lifted end-to-end: a registered gated session with inputGated=true is flipped
// to false only when the verifier accepts the assertion.
func TestRequireAssertion_LiftsGateOnValidAssertion(t *testing.T) {
	sess := &Session{ID: "sess-arm-1", OwnerID: "user-1"}
	sess.inputGated = true
	saWebauthn_register(sess.ID, "user-1")
	t.Cleanup(func() { saWebauthn_unregister(sess.ID) })

	// A rejecting verifier must NOT lift the gate.
	if err := RequireAssertion(sess, "user-1", []byte("assertion"), fakeVerifier{accept: false}); err == nil {
		t.Fatal("expected rejection error from failing verifier")
	}
	sess.mu.Lock()
	stillGated := sess.inputGated
	sess.mu.Unlock()
	if !stillGated {
		t.Fatal("gate lifted despite rejected assertion (fail-open)")
	}

	// An accepting verifier lifts the gate.
	if err := RequireAssertion(sess, "user-1", []byte("assertion"), fakeVerifier{accept: true}); err != nil {
		t.Fatalf("valid assertion: %v", err)
	}
	sess.mu.Lock()
	lifted := !sess.inputGated
	sess.mu.Unlock()
	if !lifted {
		t.Fatal("gate not lifted after valid assertion")
	}
}

// TestRequireAssertion_NoGateIsNotSuccess pins the AUTH-13 honesty contract: a
// session with no gate has verified no passkey, so RequireAssertion must report
// ErrNoGate rather than the nil (== "assertion verified") it used to return.
func TestRequireAssertion_NoGateIsNotSuccess(t *testing.T) {
	sess := &Session{ID: "sess-nogate", OwnerID: "user-1"}

	err := RequireAssertion(sess, "user-1", []byte("assertion"), fakeVerifier{accept: false})
	if err == nil {
		t.Fatal("ungated session reported a successful assertion that never happened")
	}
	if !errors.Is(err, ErrNoGate) {
		t.Fatalf("got %v, want ErrNoGate", err)
	}

	// Not even an accepting verifier turns "no gate" into a verified assertion:
	// the verifier is never consulted, so nothing was proven.
	if err := RequireAssertion(sess, "user-1", []byte("assertion"), fakeVerifier{accept: true}); !errors.Is(err, ErrNoGate) {
		t.Fatalf("got %v, want ErrNoGate", err)
	}
}

// TestRequireAssertion_RejectsNonOwner pins that only the session's owner may
// lift its gate, and that an unauthenticated caller may not try at all.
func TestRequireAssertion_RejectsNonOwner(t *testing.T) {
	sess := &Session{ID: "sess-owner", OwnerID: "alice"}
	sess.inputGated = true
	saWebauthn_register(sess.ID, "alice")
	t.Cleanup(func() { saWebauthn_unregister(sess.ID) })

	// Mallory's assertion would satisfy the verifier, but she does not own the
	// session — the gate must hold and the verifier must never be reached.
	if err := RequireAssertion(sess, "mallory", []byte("assertion"), fakeVerifier{accept: true}); !errors.Is(err, ErrNotSessionOwner) {
		t.Fatalf("non-owner: got %v, want ErrNotSessionOwner", err)
	}
	if err := RequireAssertion(sess, "", []byte("assertion"), fakeVerifier{accept: true}); !errors.Is(err, ErrNotSessionOwner) {
		t.Fatalf("anonymous caller: got %v, want ErrNotSessionOwner", err)
	}
	sess.mu.Lock()
	stillGated := sess.inputGated
	sess.mu.Unlock()
	if !stillGated {
		t.Fatal("AUTH-13 REGRESSION: gate lifted for a caller who does not own the session")
	}
}

// TestRequireAssertion_RejectsCallerMismatchedWithGate covers the case where the
// caller passes the session-level owner check but the gate names a different
// user (e.g. an unowned session whose gate was armed for someone else).
func TestRequireAssertion_RejectsCallerMismatchedWithGate(t *testing.T) {
	sess := &Session{ID: "sess-unowned"}
	sess.inputGated = true
	saWebauthn_register(sess.ID, "alice")
	t.Cleanup(func() { saWebauthn_unregister(sess.ID) })

	if err := RequireAssertion(sess, "mallory", []byte("assertion"), fakeVerifier{accept: true}); !errors.Is(err, ErrNotSessionOwner) {
		t.Fatalf("gate-owner mismatch: got %v, want ErrNotSessionOwner", err)
	}
	sess.mu.Lock()
	stillGated := sess.inputGated
	sess.mu.Unlock()
	if !stillGated {
		t.Fatal("AUTH-13 REGRESSION: gate lifted by a user the gate was not armed for")
	}
}

// TestStubVerifier_RejectsAll documents that the default stub verifier rejects
// everything — the reason we must not arm the gate without a real verifier.
func TestStubVerifier_RejectsAll(t *testing.T) {
	if err := (A13_stubVerifier{}).Verify("u", []byte("x")); err == nil {
		t.Error("stub verifier accepted an assertion; it must reject all")
	}
}
