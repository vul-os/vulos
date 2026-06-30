// relay_attest_freshness_test.go — freshness/replay hardening for the Nitro
// verifier (Item 4). The freshness window and certificate validity instant are
// taken from the SIGNED NSM timestamp (never the attacker-controllable, unsigned
// doc.IssuedAt), a strict default max-age applies when the policy pins none, and a
// caller-supplied challenge nonce is bound into the signed document.
package peering

import (
	"testing"
	"time"
)

// freshNSM is goodNSM with an explicit signed timestamp and nonce so freshness and
// replay binding can be exercised independently of doc.IssuedAt.
func freshNSM(pcr0 []byte, userData string, signedAt time.Time, nonce []byte) nsmAttestationDoc {
	n := goodNSM(pcr0, userData)
	n.Timestamp = uint64(signedAt.UnixMilli())
	n.Nonce = nonce
	return n
}

// TestNitroCOSE_BackdatedSignedTimestampRejected proves the freshness check uses
// the SIGNED timestamp: a document whose unsigned doc.IssuedAt is "now" but whose
// signed NSM timestamp is an hour old is rejected even though every signature and
// PCR is valid. This is the replay/backdate attack the unsigned-freshness bug
// allowed.
func TestNitroCOSE_BackdatedSignedTimestampRejected(t *testing.T) {
	leaf, cabundle, rootPEM := nitroChain(t)
	pcr0 := []byte{0xde, 0xad, 0xbe, 0xef}
	staleSigned := time.Now().Add(-1 * time.Hour)
	raw := buildSignedNitroDoc(t, leaf, cabundle, freshNSM(pcr0, relayID, staleSigned, nil), nil)

	doc := AttestDoc{
		Provider:    AttestProviderNitro,
		RelayVulaID: relayID,
		IssuedAt:    time.Now(), // attacker presents a FRESH unsigned issued_at
		RawDocument: raw,
	}
	// No MaxAge pinned → strict default (attestDefaultMaxAge) applies.
	policy := AttestPolicy{Provider: AttestProviderNitro, TrustedRootPEM: rootPEM, ExpectedPCRs: map[string]string{"0": "deadbeef"}}
	assertAttestCode(t, AttestVerifyRelay(doc, policy), "stale-attestation")
}

// TestNitroCOSE_MissingSignedTimestampRejected proves a document with no signed
// timestamp is fail-closed (freshness cannot be established).
func TestNitroCOSE_MissingSignedTimestampRejected(t *testing.T) {
	leaf, cabundle, rootPEM := nitroChain(t)
	nsm := goodNSM([]byte{0xde, 0xad, 0xbe, 0xef}, relayID)
	nsm.Timestamp = 0
	raw := buildSignedNitroDoc(t, leaf, cabundle, nsm, nil)
	doc := AttestDoc{Provider: AttestProviderNitro, RelayVulaID: relayID, IssuedAt: time.Now(), RawDocument: raw}
	policy := AttestPolicy{Provider: AttestProviderNitro, TrustedRootPEM: rootPEM, ExpectedPCRs: map[string]string{"0": "deadbeef"}}
	assertAttestCode(t, AttestVerifyRelay(doc, policy), "missing-timestamp")
}

// TestNitroCOSE_ReplayedNonceRejected proves the caller-supplied challenge is
// bound: a captured-but-valid document carrying challenge A is rejected when the
// verifier supplies challenge B.
func TestNitroCOSE_ReplayedNonceRejected(t *testing.T) {
	leaf, cabundle, rootPEM := nitroChain(t)
	pcr0 := []byte{0xde, 0xad, 0xbe, 0xef}
	captured := []byte("challenge-A")
	raw := buildSignedNitroDoc(t, leaf, cabundle, freshNSM(pcr0, relayID, time.Now(), captured), nil)

	doc := AttestDoc{Provider: AttestProviderNitro, RelayVulaID: relayID, IssuedAt: time.Now(), RawDocument: raw}
	policy := AttestPolicy{
		Provider:       AttestProviderNitro,
		TrustedRootPEM: rootPEM,
		ExpectedPCRs:   map[string]string{"0": "deadbeef"},
		Nonce:          []byte("challenge-B"), // verifier issued a DIFFERENT challenge
	}
	assertAttestCode(t, AttestVerifyRelay(doc, policy), "nonce-mismatch")
}

// TestNitroCOSE_FreshNonceBoundAccepted proves a fresh document with a matching
// signed timestamp and matching challenge nonce is accepted.
func TestNitroCOSE_FreshNonceBoundAccepted(t *testing.T) {
	leaf, cabundle, rootPEM := nitroChain(t)
	pcr0 := []byte{0xde, 0xad, 0xbe, 0xef}
	challenge := []byte("challenge-XYZ")
	raw := buildSignedNitroDoc(t, leaf, cabundle, freshNSM(pcr0, relayID, time.Now(), challenge), nil)

	doc := AttestDoc{Provider: AttestProviderNitro, RelayVulaID: relayID, IssuedAt: time.Now(), RawDocument: raw}
	policy := AttestPolicy{
		Provider:       AttestProviderNitro,
		TrustedRootPEM: rootPEM,
		ExpectedPCRs:   map[string]string{"0": "deadbeef"},
		Nonce:          challenge,
	}
	if err := AttestVerifyRelay(doc, policy); err != nil {
		t.Fatalf("fresh, nonce-bound Nitro doc rejected: %v", err)
	}
}
