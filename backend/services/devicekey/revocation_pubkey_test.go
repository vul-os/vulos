package devicekey

import (
	"crypto/x509"
	"testing"
	"time"

	"vulos/backend/services/fleetid"
)

// targetDeviceKey returns a fresh, arbitrary ECDSA device public key (PKIX DER)
// standing in for a COMPROMISED secondary device — one whose private key the
// operator does not control, so it can only be removed by fleet quorum.
func targetDeviceKey(t *testing.T) []byte {
	t.Helper()
	priv, err := GenerateCandidateKey()
	if err != nil {
		t.Fatalf("GenerateCandidateKey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	return der
}

func TestBreakGlassRevokePubKey_ValidQuorumRevokesEnforcesPropagates(t *testing.T) {
	resetRevocationChecker(t)
	store := newTestRevocationStore(t)
	targetDER := targetDeviceKey(t)
	fp := Fingerprint(targetDER)

	subject := newFleetBox(t)
	v1 := newFleetBox(t)
	v2 := newFleetBox(t)
	roster := newTestRoster(subject, v1, v2)

	now := time.Now()
	requestID := "remove-stolen-phone"
	payloadHash := revocationBreakGlassPayloadHash(requestID, fp)
	certs := []fleetid.VouchCert{
		vouchFor(t, v1, subject.vulosID, payloadHash, now),
		vouchFor(t, v2, subject.vulosID, payloadHash, now),
	}

	cert, err := BreakGlassRevokePubKey(store, targetDER, "phone stolen", subject.vulosID, requestID, certs, roster, fleetid.MinThreshold, now)
	if err != nil {
		t.Fatalf("BreakGlassRevokePubKey: %v", err)
	}
	if cert.Fingerprint != fp {
		t.Fatalf("cert fingerprint = %q, want %q", cert.Fingerprint, fp)
	}
	if !store.IsRevoked(fp) {
		t.Fatal("target device key was not recorded as revoked")
	}

	// ENFORCED: once the process-wide checker is wired to the store, any remote
	// verifier rejects the compromised key — it is locked out.
	SetRevocationChecker(store.IsRevoked)
	if !IsDeviceKeyRevoked(fp) {
		t.Fatal("IsDeviceKeyRevoked should report the revoked device once wired")
	}
	if err := VerifyDeviceSignatureChecked(targetDER, make([]byte, 32), make([]byte, 64)); err == nil {
		t.Fatal("VerifyDeviceSignatureChecked must reject a signature from the revoked device")
	}

	// PROPAGATED: a peer box that pulls the cert (self-verifying via the same
	// quorum) merges it and locks the device out too.
	peer := newTestRevocationStore(t)
	if err := peer.MergeVerified(cert, roster, fleetid.MinThreshold, now); err != nil {
		t.Fatalf("peer MergeVerified: %v", err)
	}
	if !peer.IsRevoked(fp) {
		t.Fatal("peer did not learn the revocation via propagation")
	}
}

func TestBreakGlassRevokePubKey_InsufficientQuorumRejected(t *testing.T) {
	store := newTestRevocationStore(t)
	targetDER := targetDeviceKey(t)
	fp := Fingerprint(targetDER)

	subject := newFleetBox(t)
	v1 := newFleetBox(t)
	roster := newTestRoster(subject, v1)

	now := time.Now()
	requestID := "remove-attempt"
	payloadHash := revocationBreakGlassPayloadHash(requestID, fp)
	certs := []fleetid.VouchCert{
		vouchFor(t, v1, subject.vulosID, payloadHash, now), // only ONE voucher
	}

	if _, err := BreakGlassRevokePubKey(store, targetDER, "reason", subject.vulosID, requestID, certs, roster, fleetid.MinThreshold, now); err == nil {
		t.Fatal("expected removal to fail with insufficient quorum")
	}
	if store.IsRevoked(fp) {
		t.Fatal("device must not be revoked when quorum was insufficient")
	}
}

func TestBreakGlassRevokePubKey_SelfVouchNeverCounts(t *testing.T) {
	store := newTestRevocationStore(t)
	targetDER := targetDeviceKey(t)
	fp := Fingerprint(targetDER)

	subject := newFleetBox(t)
	roster := newTestRoster(subject)

	now := time.Now()
	requestID := "self-authorise-attempt"
	payloadHash := revocationBreakGlassPayloadHash(requestID, fp)
	selfVouch := vouchFor(t, subject, subject.vulosID, payloadHash, now)
	certs := []fleetid.VouchCert{selfVouch, selfVouch}

	if _, err := BreakGlassRevokePubKey(store, targetDER, "reason", subject.vulosID, requestID, certs, roster, fleetid.MinThreshold, now); err == nil {
		t.Fatal("expected removal to reject a self-vouched 'quorum'")
	}
	if store.IsRevoked(fp) {
		t.Fatal("device must not be revoked from a self-vouched quorum")
	}
}

func TestBreakGlassRevokePubKey_MalformedInputsRejected(t *testing.T) {
	store := newTestRevocationStore(t)
	subject := newFleetBox(t)
	roster := newTestRoster(subject)
	now := time.Now()

	if _, err := BreakGlassRevokePubKey(nil, targetDeviceKey(t), "r", subject.vulosID, "req", nil, roster, 2, now); err == nil {
		t.Fatal("nil store must be rejected")
	}
	if _, err := BreakGlassRevokePubKey(store, nil, "r", subject.vulosID, "req", nil, roster, 2, now); err == nil {
		t.Fatal("empty target pubkey must be rejected")
	}
	if _, err := BreakGlassRevokePubKey(store, []byte("not a pkix key"), "r", subject.vulosID, "req", nil, roster, 2, now); err == nil {
		t.Fatal("un-parseable target pubkey must be rejected")
	}
}
