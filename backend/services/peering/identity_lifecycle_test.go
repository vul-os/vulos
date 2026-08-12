package peering

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// lcKeypair returns a fresh Ed25519 keypair and its base58 Vula ID.
func lcKeypair(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return priv, encodeVulosID(pub)
}

// ── Rotation ──────────────────────────────────────────────────────────────────

func TestRotationCert_RoundTripVerify(t *testing.T) {
	oldPriv, oldID := lcKeypair(t)
	_, newID := lcKeypair(t)

	cert, err := NewRotationCert(oldPriv, oldID, newID)
	if err != nil {
		t.Fatalf("NewRotationCert: %v", err)
	}
	if err := cert.Verify(); err != nil {
		t.Fatalf("rotation cert should verify: %v", err)
	}
	// Tamper: changing NewVulosID must break the signature.
	bad := *cert
	_, otherID := lcKeypair(t)
	bad.NewVulosID = otherID
	if err := bad.Verify(); err == nil {
		t.Fatal("tampered rotation cert verified; must fail")
	}
}

func TestRotationCert_WrongSignerRejected(t *testing.T) {
	_, oldID := lcKeypair(t)
	attackerPriv, _ := lcKeypair(t)
	_, newID := lcKeypair(t)
	// Attacker tries to sign a rotation for an id they don't control.
	if _, err := NewRotationCert(attackerPriv, oldID, newID); err == nil {
		t.Fatal("rotation cert with mismatched signer key must be rejected at build")
	}
}

func TestResolveCurrentKey_RotationChain(t *testing.T) {
	priv0, id0 := lcKeypair(t)
	priv1, id1 := lcKeypair(t)
	_, id2 := lcKeypair(t)

	r1, _ := NewRotationCert(priv0, id0, id1)
	r2, _ := NewRotationCert(priv1, id1, id2)
	chain := []LifecycleLink{{Rotation: r1}, {Rotation: r2}}

	got, err := ResolveCurrentKey(id0, "", chain)
	if err != nil {
		t.Fatalf("resolve chain: %v", err)
	}
	if got != id2 {
		t.Fatalf("resolved head = %q, want %q", got, id2)
	}
}

func TestResolveCurrentKey_BrokenChainFailsClosed(t *testing.T) {
	priv0, id0 := lcKeypair(t)
	_, id1 := lcKeypair(t)
	_, id2 := lcKeypair(t)
	// Second link does not chain from id1 (signed by id0 for an unrelated prev).
	r1, _ := NewRotationCert(priv0, id0, id1)
	stray, strayID := lcKeypair(t)
	r2, _ := NewRotationCert(stray, strayID, id2) // prev = strayID, not id1
	chain := []LifecycleLink{{Rotation: r1}, {Rotation: r2}}
	if _, err := ResolveCurrentKey(id0, "", chain); err == nil {
		t.Fatal("broken chain must fail closed")
	}
}

// ── Revocation ────────────────────────────────────────────────────────────────

func TestRevocationCert_SelfRevoke(t *testing.T) {
	priv, id := lcKeypair(t)
	cert, err := NewRevocationCert(priv, id, id, "compromised")
	if err != nil {
		t.Fatalf("NewRevocationCert: %v", err)
	}
	// Self-revocation verifies with no anchors needed.
	if err := cert.Verify(nil); err != nil {
		t.Fatalf("self-revocation should verify: %v", err)
	}
}

func TestRevocationCert_AnchorRevoke(t *testing.T) {
	_, id := lcKeypair(t)
	anchorPriv, anchorID := lcKeypair(t)
	cert, err := NewRevocationCert(anchorPriv, id, anchorID, "lost-key")
	if err != nil {
		t.Fatalf("NewRevocationCert anchor: %v", err)
	}
	// Without trusting the anchor, the revocation is unauthorized.
	if err := cert.Verify(nil); err == nil {
		t.Fatal("anchor revocation must be rejected when anchor not trusted")
	}
	// With the anchor trusted, it verifies.
	if err := cert.Verify(map[string]bool{anchorID: true}); err != nil {
		t.Fatalf("anchor revocation should verify when trusted: %v", err)
	}
}

func TestRevocationCert_StrangerCannotRevoke(t *testing.T) {
	_, id := lcKeypair(t)
	strangerPriv, strangerID := lcKeypair(t)
	cert, _ := NewRevocationCert(strangerPriv, id, strangerID, "malicious")
	// Stranger is neither the identity nor a trusted anchor.
	if err := cert.Verify(nil); err == nil {
		t.Fatal("stranger revocation must be rejected")
	}
}

// ── Recovery (account-anchored) ───────────────────────────────────────────────

func TestRecoveryCert_AnchorReestablishesIdentity(t *testing.T) {
	// Original identity (key now "lost").
	_, id0 := lcKeypair(t)
	// Recovery anchor derived from the recovery seed.
	seed := make([]byte, 64)
	_, _ = rand.Read(seed)
	anchorPriv, anchorID, err := DeriveRecoveryAnchor(seed)
	if err != nil {
		t.Fatalf("DeriveRecoveryAnchor: %v", err)
	}
	// Brand-new replacement identity.
	_, id1 := lcKeypair(t)

	rec, err := NewRecoveryCert(anchorPriv, id0, id1, anchorID)
	if err != nil {
		t.Fatalf("NewRecoveryCert: %v", err)
	}
	chain := []LifecycleLink{{Recovery: rec}}

	// A peer that pinned the anchor follows the recovery to id1.
	got, err := ResolveCurrentKey(id0, anchorID, chain)
	if err != nil {
		t.Fatalf("resolve recovery: %v", err)
	}
	if got != id1 {
		t.Fatalf("recovered head = %q, want %q", got, id1)
	}

	// A peer that did NOT pin the anchor cannot follow it (fail closed).
	if _, err := ResolveCurrentKey(id0, "", chain); err == nil {
		t.Fatal("recovery without a pinned anchor must fail closed")
	}
	// A peer that pinned a DIFFERENT anchor must reject.
	_, otherAnchor := lcKeypair(t)
	if _, err := ResolveCurrentKey(id0, otherAnchor, chain); err == nil {
		t.Fatal("recovery with wrong anchor must fail closed")
	}
}

func TestDeriveRecoveryAnchor_Deterministic(t *testing.T) {
	seed := make([]byte, 64)
	_, _ = rand.Read(seed)
	_, id1, err := DeriveRecoveryAnchor(seed)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	_, id2, _ := DeriveRecoveryAnchor(seed)
	if id1 != id2 {
		t.Fatalf("anchor derivation not deterministic: %q vs %q", id1, id2)
	}
}

// ── LifecycleStore persistence + admission gate ───────────────────────────────

func TestLifecycleStore_RotateAndPersist(t *testing.T) {
	dir := t.TempDir()
	priv0, id0 := lcKeypair(t)
	_, anchorID := lcKeypair(t)

	s, err := NewLifecycleStore(dir, id0, anchorID)
	if err != nil {
		t.Fatalf("NewLifecycleStore: %v", err)
	}
	if s.Head() != id0 {
		t.Fatalf("initial head = %q, want %q", s.Head(), id0)
	}
	_, id1 := lcKeypair(t)
	if _, err := s.AppendOwnRotation(priv0, id1); err != nil {
		t.Fatalf("AppendOwnRotation: %v", err)
	}
	if s.Head() != id1 {
		t.Fatalf("head after rotation = %q, want %q", s.Head(), id1)
	}
	// Reopen from disk — chain must persist.
	s2, err := NewLifecycleStore(dir, id0, anchorID)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s2.Head() != id1 {
		t.Fatalf("persisted head = %q, want %q", s2.Head(), id1)
	}
	// The published chain must resolve to the new head.
	root, _, chain := s2.OwnChain()
	got, err := ResolveCurrentKey(root, anchorID, chain)
	if err != nil || got != id1 {
		t.Fatalf("resolve own chain = (%q,%v), want (%q,nil)", got, err, id1)
	}
}

func TestLifecycleStore_RecordRevocation_GatesAdmission(t *testing.T) {
	dir := t.TempDir()
	priv, id := lcKeypair(t)
	s, err := NewLifecycleStore(dir, id, "")
	if err != nil {
		t.Fatalf("NewLifecycleStore: %v", err)
	}
	cert, _ := NewRevocationCert(priv, id, id, "compromised")
	if err := s.RecordRevocation(cert); err != nil {
		t.Fatalf("RecordRevocation: %v", err)
	}
	if !s.IsRevoked(id) {
		t.Fatal("identity should be revoked after RecordRevocation")
	}

	// Wire the global gate and confirm InboundMiddleware rejects the revoked id.
	SetRevocationChecker(s.IsRevoked)
	t.Cleanup(func() { SetRevocationChecker(nil) })

	if !isVulosIDRevoked(id) {
		t.Fatal("global gate should report revoked")
	}

	// Build a signed envelope from the revoked identity and confirm middleware
	// rejects it with 403 even though the signature is valid. The sender is an
	// APPROVED contact, so only the revocation gate can be the cause of the 403.
	cs := makeContactStore(t)
	addApprovedContact(t, cs, id, "host:1")
	env, _ := NewEnvelope("m1", id, "vulos:ed25519:dest", TypeMessage, nil)
	if err := env.Sign(priv); err != nil {
		t.Fatalf("sign env: %v", err)
	}
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	h := InboundMiddleware(cs, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/message", bytesReader(body))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("revoked sender admission = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
}

func TestVerifyVulaSignatureChecked_RejectsRevoked(t *testing.T) {
	priv, id := lcKeypair(t)
	msg := []byte("capability-proof")
	sig := ed25519.Sign(priv, msg)
	// Not revoked → passes.
	SetRevocationChecker(func(v string) bool { return false })
	if err := VerifyVulaSignatureChecked(id, msg, sig); err != nil {
		t.Fatalf("unrevoked checked verify should pass: %v", err)
	}
	// Revoked → rejected even though signature is valid.
	SetRevocationChecker(func(v string) bool { return v == id })
	t.Cleanup(func() { SetRevocationChecker(nil) })
	if err := VerifyVulaSignatureChecked(id, msg, sig); err == nil {
		t.Fatal("revoked identity signature must be rejected")
	}
}
