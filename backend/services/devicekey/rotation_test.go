package devicekey

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"testing"
	"time"

	"vulos/backend/services/fleetid"
	"vulos/backend/services/peering"
)

// ─── Test fixtures: a tiny fleetid.Roster, mirroring fleetid's own test style ──

// fleetBox is a peer box's fleet-fabric identity (Ed25519), independent of any
// device's own ECDSA identity key — exactly the separation BreakGlassRotate
// relies on (quorum is about the FLEET identity, not the device key being
// replaced).
type fleetBox struct {
	priv    ed25519.PrivateKey
	vulosID string
}

func newFleetBox(t *testing.T) fleetBox {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return fleetBox{priv: priv, vulosID: peering.EncodeVulosID(pub)}
}

type testRoster struct {
	members map[string]ed25519.PublicKey
}

func newTestRoster(boxes ...fleetBox) *testRoster {
	r := &testRoster{members: map[string]ed25519.PublicKey{}}
	for _, b := range boxes {
		r.members[b.vulosID] = b.priv.Public().(ed25519.PublicKey)
	}
	return r
}

func (r *testRoster) IsRostered(vulosID string) (ed25519.PublicKey, bool) {
	pub, ok := r.members[vulosID]
	return pub, ok
}

func vouchFor(t *testing.T, voucher fleetBox, subjectID string, payloadHash []byte, now time.Time) fleetid.VouchCert {
	t.Helper()
	c, err := fleetid.NewVouchCert(voucher.priv, fleetid.ActionIdentityRecovery, subjectID, payloadHash, now)
	if err != nil {
		t.Fatalf("NewVouchCert: %v", err)
	}
	return *c
}

// ─── Normal (self-authorized) rotation ──────────────────────────────────────────

func TestRotate_Self_SignsOldToNewAndVerifies(t *testing.T) {
	ks := newTestSoftwareStore(t)

	oldPub, err := ks.DeviceIdentity()
	if err != nil {
		t.Fatalf("DeviceIdentity: %v", err)
	}

	cert, err := ks.Rotate("routine rotation")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if cert.Method != RotationMethodSelf {
		t.Fatalf("Method = %q, want %q", cert.Method, RotationMethodSelf)
	}

	// The cert must verify (self path needs no roster/threshold).
	if err := VerifyRotationCert(cert, nil, 0, time.Now()); err != nil {
		t.Fatalf("VerifyRotationCert: %v", err)
	}

	// Identity must have actually changed.
	newPub, err := ks.DeviceIdentity()
	if err != nil {
		t.Fatalf("DeviceIdentity after rotate: %v", err)
	}
	if bytes.Equal(oldPub, newPub) {
		t.Fatal("device identity did not change after Rotate")
	}

	// Previous identity should be retrievable within the grace window.
	var pip PreviousIdentityProvider = ks // compile-time proof softwareStore implements it
	prevPub, expiresAt := pip.PreviousIdentity()
	if !bytes.Equal(prevPub, oldPub) {
		t.Error("PreviousIdentity did not return the pre-rotation key")
	}
	if !expiresAt.After(time.Now()) {
		t.Error("PreviousIdentity grace expiry should be in the future")
	}
}

func TestRotate_TamperedCertFailsVerification(t *testing.T) {
	ks := newTestSoftwareStore(t)
	cert, err := ks.Rotate("reason")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	cert.Reason = "tampered" // mutate a signed field
	if err := VerifyRotationCert(cert, nil, 0, time.Now()); err == nil {
		t.Fatal("expected verification failure for a tampered cert")
	}
}

// ─── Break-glass rotation ───────────────────────────────────────────────────────

func TestBreakGlassRotate_ValidQuorumSucceeds(t *testing.T) {
	ks := newTestSoftwareStore(t)
	oldPub, _ := ks.DeviceIdentity()

	subject := newFleetBox(t)
	voucher1 := newFleetBox(t)
	voucher2 := newFleetBox(t)
	roster := newTestRoster(subject, voucher1, voucher2)

	candidate, err := GenerateCandidateKey()
	if err != nil {
		t.Fatalf("GenerateCandidateKey: %v", err)
	}
	candidateDER, _ := x509.MarshalPKIXPublicKey(&candidate.PublicKey)

	now := time.Now()
	requestID := "recover-box-1"
	payloadHash := BreakGlassPayloadHash(requestID, oldPub, candidateDER)

	certs := []fleetid.VouchCert{
		vouchFor(t, voucher1, subject.vulosID, payloadHash, now),
		vouchFor(t, voucher2, subject.vulosID, payloadHash, now),
	}

	cert, err := BreakGlassRotate(ks, candidate, "compromised key", subject.vulosID, requestID, certs, roster, fleetid.MinThreshold, now)
	if err != nil {
		t.Fatalf("BreakGlassRotate: %v", err)
	}
	if cert.Method != RotationMethodBreakGlass {
		t.Fatalf("Method = %q, want %q", cert.Method, RotationMethodBreakGlass)
	}
	if err := VerifyRotationCert(cert, roster, fleetid.MinThreshold, now); err != nil {
		t.Fatalf("VerifyRotationCert: %v", err)
	}

	newPub, _ := ks.DeviceIdentity()
	if !bytes.Equal(newPub, candidateDER) {
		t.Error("device identity was not installed to the candidate key")
	}
}

// TestBreakGlassRotate_InsufficientQuorumRejected is the HARD-RULE test: a
// single vouch (below MinThreshold=2) must be rejected, and — critically —
// the device identity must NOT change. This proves BreakGlassRotate never
// touches key material before quorum succeeds.
func TestBreakGlassRotate_InsufficientQuorumRejected(t *testing.T) {
	ks := newTestSoftwareStore(t)
	oldPub, _ := ks.DeviceIdentity()

	subject := newFleetBox(t)
	voucher1 := newFleetBox(t)
	roster := newTestRoster(subject, voucher1)

	candidate, err := GenerateCandidateKey()
	if err != nil {
		t.Fatalf("GenerateCandidateKey: %v", err)
	}
	candidateDER, _ := x509.MarshalPKIXPublicKey(&candidate.PublicKey)

	now := time.Now()
	requestID := "recover-box-2"
	payloadHash := BreakGlassPayloadHash(requestID, oldPub, candidateDER)

	// Only ONE vouch — below the hard floor of 2.
	certs := []fleetid.VouchCert{
		vouchFor(t, voucher1, subject.vulosID, payloadHash, now),
	}

	_, err = BreakGlassRotate(ks, candidate, "compromised key", subject.vulosID, requestID, certs, roster, fleetid.MinThreshold, now)
	if err == nil {
		t.Fatal("expected BreakGlassRotate to fail with insufficient quorum")
	}

	stillOldPub, _ := ks.DeviceIdentity()
	if !bytes.Equal(stillOldPub, oldPub) {
		t.Fatal("device identity changed despite insufficient quorum — hard rule violated")
	}
}

// TestBreakGlassRotate_SelfVouchNeverCounts proves the box being recovered
// cannot vouch for itself: a "quorum" made entirely of the subject's own
// (self-)vouches must be rejected — self-rotation of a compromised key
// WITHOUT the old key AND WITHOUT a genuine quorum of OTHER boxes is
// impossible, structurally (fleetid.VerifyQuorum's self-exclusion) and
// procedurally (identity unchanged on failure).
func TestBreakGlassRotate_SelfVouchNeverCounts(t *testing.T) {
	ks := newTestSoftwareStore(t)
	oldPub, _ := ks.DeviceIdentity()

	subject := newFleetBox(t) // the box "recovering" itself
	roster := newTestRoster(subject)

	candidate, err := GenerateCandidateKey()
	if err != nil {
		t.Fatalf("GenerateCandidateKey: %v", err)
	}
	candidateDER, _ := x509.MarshalPKIXPublicKey(&candidate.PublicKey)

	now := time.Now()
	requestID := "self-authorize-attempt"
	payloadHash := BreakGlassPayloadHash(requestID, oldPub, candidateDER)

	// The subject signs its OWN vouch for itself, twice (to also rule out
	// "just submit the same vouch N times").
	selfVouch := vouchFor(t, subject, subject.vulosID, payloadHash, now)
	certs := []fleetid.VouchCert{selfVouch, selfVouch}

	_, err = BreakGlassRotate(ks, candidate, "attempted self-authorization", subject.vulosID, requestID, certs, roster, fleetid.MinThreshold, now)
	if err == nil {
		t.Fatal("expected BreakGlassRotate to reject a self-vouched quorum")
	}

	stillOldPub, _ := ks.DeviceIdentity()
	if !bytes.Equal(stillOldPub, oldPub) {
		t.Fatal("device identity changed from a self-vouched 'quorum' — hard rule violated")
	}
}

func TestBreakGlassRotate_WrongPayloadRejected(t *testing.T) {
	ks := newTestSoftwareStore(t)
	oldPub, _ := ks.DeviceIdentity()

	subject := newFleetBox(t)
	voucher1 := newFleetBox(t)
	voucher2 := newFleetBox(t)
	roster := newTestRoster(subject, voucher1, voucher2)

	candidate, _ := GenerateCandidateKey()
	candidateDER, _ := x509.MarshalPKIXPublicKey(&candidate.PublicKey)

	now := time.Now()
	// Vouches signed over a DIFFERENT request id than the one submitted.
	wrongHash := BreakGlassPayloadHash("a-different-request", oldPub, candidateDER)
	certs := []fleetid.VouchCert{
		vouchFor(t, voucher1, subject.vulosID, wrongHash, now),
		vouchFor(t, voucher2, subject.vulosID, wrongHash, now),
	}

	_, err := BreakGlassRotate(ks, candidate, "reason", subject.vulosID, "the-real-request", certs, roster, fleetid.MinThreshold, now)
	if err == nil {
		t.Fatal("expected rejection: vouches bound to a different request id")
	}
}
