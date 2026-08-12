// relay_attest_cose_test.go — end-to-end COSE_Sign1 verification tests for the
// AWS Nitro attestation verifier (PEER-39).
//
// These tests build a CRYPTOGRAPHICALLY VALID Nitro-shaped attestation document
// from a synthetic root CA → intermediate → leaf chain (ECDSA P-384), sign the
// COSE_Sign1 envelope exactly as the AWS NSM would (ES384 over the Sig_structure),
// and assert that:
//   - a known-good document is ACCEPTED when the policy pins the matching PCRs,
//     trusted root, and provider;
//   - a document whose signed payload is TAMPERED after signing is REJECTED;
//   - a document with FORGED PCRs (re-signed but not matching the policy) is
//     REJECTED;
//   - the relay-identity binding (signed user_data ↔ relay_vulos_id) is enforced.
package peering

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// ── test PKI ──────────────────────────────────────────────────────────────────

type testCert struct {
	cert *x509.Certificate
	der  []byte
	key  *ecdsa.PrivateKey
}

// mkCert creates an ECDSA P-384 certificate. When parent is nil it is a
// self-signed CA root; otherwise it is signed by parent. isCA controls the CA
// basic constraint.
func mkCert(t *testing.T, cn string, parent *testCert, isCA bool) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	if isCA {
		tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature
	} else {
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageAny}
	}
	signerCert := tmpl
	var signerKey any = key
	if parent != nil {
		signerCert = parent.cert
		signerKey = parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatalf("create cert %s: %v", cn, err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert %s: %v", cn, err)
	}
	return &testCert{cert: parsed, der: der, key: key}
}

func certPEM(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// ── COSE_Sign1 builder ────────────────────────────────────────────────────────

// buildSignedNitroDoc constructs a base64 COSE_Sign1 NSM document. mutate, when
// non-nil, is applied to the payload bytes AFTER signing (to forge a document the
// signature no longer covers). If reSign is true the (possibly mutated) payload
// is re-signed with leaf — used to model a relay that forges PCRs but re-signs
// with a key that does NOT chain to the trusted root anchor in the test.
func buildSignedNitroDoc(t *testing.T, leaf *testCert, cabundle [][]byte, nsm nsmAttestationDoc, mutate func([]byte) []byte) string {
	t.Helper()
	nsm.Certificate = leaf.der
	nsm.CABundle = cabundle
	payload, err := cbor.Marshal(nsm)
	if err != nil {
		t.Fatalf("marshal nsm: %v", err)
	}

	protected, err := cbor.Marshal(map[int]int{1: coseAlgES384})
	if err != nil {
		t.Fatalf("marshal protected: %v", err)
	}

	signPayload := payload
	sigStruct := []any{"Signature1", protected, []byte{}, signPayload}
	tbs, err := cbor.Marshal(sigStruct)
	if err != nil {
		t.Fatalf("marshal sigstruct: %v", err)
	}
	digest := sha512.Sum384(tbs)
	r, s, err := ecdsa.Sign(rand.Reader, leaf.key, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig := make([]byte, 96)
	r.FillBytes(sig[:48])
	s.FillBytes(sig[48:])

	// Apply post-signing tamper to the wire payload (signature now stale).
	wirePayload := payload
	if mutate != nil {
		wirePayload = mutate(append([]byte(nil), payload...))
	}

	coseArr := []any{protected, map[int]int{}, wirePayload, sig}
	cose, err := cbor.Marshal(coseArr)
	if err != nil {
		t.Fatalf("marshal cose: %v", err)
	}
	return base64.StdEncoding.EncodeToString(cose)
}

// goodNSM returns a populated NSM payload with the given PCR slot 0 value and
// user_data identity binding.
func goodNSM(pcr0 []byte, userData string) nsmAttestationDoc {
	return nsmAttestationDoc{
		ModuleID:  "i-0test-enc",
		Digest:    "SHA384",
		Timestamp: uint64(time.Now().UnixMilli()),
		PCRs:      map[uint64][]byte{0: pcr0, 1: {0x11, 0x22}},
		UserData:  []byte(userData),
	}
}

// nitroChain builds a root→intermediate→leaf chain and returns the leaf, the
// cabundle (root-first DER), and the trusted-root PEM.
func nitroChain(t *testing.T) (*testCert, [][]byte, string) {
	t.Helper()
	root := mkCert(t, "test-nitro-root", nil, true)
	inter := mkCert(t, "test-nitro-intermediate", root, true)
	leaf := mkCert(t, "test-nitro-leaf", inter, false)
	return leaf, [][]byte{root.der, inter.der}, certPEM(root.der)
}

// ── tests ─────────────────────────────────────────────────────────────────────

const relayID = "vulos:ed25519:relay-cose-test"

func TestNitroCOSE_KnownGoodAccepted(t *testing.T) {
	leaf, cabundle, rootPEM := nitroChain(t)
	pcr0 := []byte{0xde, 0xad, 0xbe, 0xef}
	raw := buildSignedNitroDoc(t, leaf, cabundle, goodNSM(pcr0, relayID), nil)

	doc := AttestDoc{
		Provider:     AttestProviderNitro,
		RelayVulosID: relayID,
		IssuedAt:     time.Now(),
		RawDocument:  raw,
	}
	policy := AttestPolicy{
		Provider:       AttestProviderNitro,
		TrustedRootPEM: rootPEM,
		ExpectedPCRs:   map[string]string{"0": "deadbeef"},
	}
	if err := AttestVerifyRelay(doc, policy); err != nil {
		t.Fatalf("known-good Nitro doc rejected: %v", err)
	}
}

func TestNitroCOSE_TamperedPayloadRejected(t *testing.T) {
	leaf, cabundle, rootPEM := nitroChain(t)
	pcr0 := []byte{0xde, 0xad, 0xbe, 0xef}
	// Flip a byte in the signed payload AFTER signing → signature no longer valid.
	raw := buildSignedNitroDoc(t, leaf, cabundle, goodNSM(pcr0, relayID), func(p []byte) []byte {
		// Mutate a byte that does not break CBOR structure: the last byte of
		// user_data region tends to be deep in the map; flip the final byte.
		p[len(p)-1] ^= 0xff
		return p
	})
	doc := AttestDoc{Provider: AttestProviderNitro, RelayVulosID: relayID, IssuedAt: time.Now(), RawDocument: raw}
	policy := AttestPolicy{Provider: AttestProviderNitro, TrustedRootPEM: rootPEM, ExpectedPCRs: map[string]string{"0": "deadbeef"}}
	err := AttestVerifyRelay(doc, policy)
	if err == nil {
		t.Fatal("tampered payload was accepted; must be rejected")
	}
	// Depending on whether the tamper corrupts CBOR or just content, the failure
	// is either a parse error or a signature mismatch — both are hard rejects.
	var ae *AttestError
	if !isAttestError(err, &ae) {
		t.Fatalf("expected *AttestError, got %T: %v", err, err)
	}
	if ae.Code != "bad-signature" && ae.Code != "bad-nsm-payload" && ae.Code != "bad-cose" {
		t.Fatalf("unexpected rejection code %q (want bad-signature/bad-nsm-payload/bad-cose)", ae.Code)
	}
}

func TestNitroCOSE_ForgedPCRRejected(t *testing.T) {
	// A relay re-signs a document with a PCR that does NOT match policy. The
	// signature is valid (self-consistent) but the pinned PCR check rejects it.
	leaf, cabundle, rootPEM := nitroChain(t)
	forged := []byte{0x00, 0x00, 0x00, 0x00} // not the expected deadbeef
	raw := buildSignedNitroDoc(t, leaf, cabundle, goodNSM(forged, relayID), nil)

	doc := AttestDoc{Provider: AttestProviderNitro, RelayVulosID: relayID, IssuedAt: time.Now(), RawDocument: raw}
	policy := AttestPolicy{Provider: AttestProviderNitro, TrustedRootPEM: rootPEM, ExpectedPCRs: map[string]string{"0": "deadbeef"}}
	err := AttestVerifyRelay(doc, policy)
	assertAttestCode(t, err, "pcr-mismatch")
}

func TestNitroCOSE_UntrustedRootRejected(t *testing.T) {
	// Forged document signed by an attacker chain that does NOT anchor to the
	// trusted root (here the default AWS Nitro root, since policy pins no override).
	leaf, cabundle, _ := nitroChain(t)
	raw := buildSignedNitroDoc(t, leaf, cabundle, goodNSM([]byte{0xde, 0xad, 0xbe, 0xef}, relayID), nil)
	doc := AttestDoc{Provider: AttestProviderNitro, RelayVulosID: relayID, IssuedAt: time.Now(), RawDocument: raw}
	// No TrustedRootPEM override → built-in AWS Nitro root is used → attacker
	// chain cannot anchor → reject.
	policy := AttestPolicy{Provider: AttestProviderNitro, ExpectedPCRs: map[string]string{"0": "deadbeef"}}
	err := AttestVerifyRelay(doc, policy)
	assertAttestCode(t, err, "invalid-cert-chain")
}

func TestNitroCOSE_IdentityBindingEnforced(t *testing.T) {
	leaf, cabundle, rootPEM := nitroChain(t)
	// Document is validly signed but user_data binds a DIFFERENT relay identity.
	raw := buildSignedNitroDoc(t, leaf, cabundle, goodNSM([]byte{0xde, 0xad, 0xbe, 0xef}, "vulos:ed25519:someone-else"), nil)
	doc := AttestDoc{Provider: AttestProviderNitro, RelayVulosID: relayID, IssuedAt: time.Now(), RawDocument: raw}
	policy := AttestPolicy{Provider: AttestProviderNitro, TrustedRootPEM: rootPEM, ExpectedPCRs: map[string]string{"0": "deadbeef"}}
	err := AttestVerifyRelay(doc, policy)
	assertAttestCode(t, err, "identity-mismatch")
}

func TestNitroCOSE_NoExpectedPCRsRejected(t *testing.T) {
	leaf, cabundle, rootPEM := nitroChain(t)
	raw := buildSignedNitroDoc(t, leaf, cabundle, goodNSM([]byte{0xde, 0xad, 0xbe, 0xef}, relayID), nil)
	doc := AttestDoc{Provider: AttestProviderNitro, RelayVulosID: relayID, IssuedAt: time.Now(), RawDocument: raw}
	// Policy pins the provider + trusted root but NO PCRs → default-deny.
	policy := AttestPolicy{Provider: AttestProviderNitro, TrustedRootPEM: rootPEM}
	err := AttestVerifyRelay(doc, policy)
	assertAttestCode(t, err, "no-expected-pcrs")
}

func TestNitroCOSE_MissingIdentityBindingRejected(t *testing.T) {
	leaf, cabundle, rootPEM := nitroChain(t)
	nsm := goodNSM([]byte{0xde, 0xad, 0xbe, 0xef}, "")
	nsm.UserData = nil
	raw := buildSignedNitroDoc(t, leaf, cabundle, nsm, nil)
	doc := AttestDoc{Provider: AttestProviderNitro, RelayVulosID: relayID, IssuedAt: time.Now(), RawDocument: raw}
	policy := AttestPolicy{Provider: AttestProviderNitro, TrustedRootPEM: rootPEM, ExpectedPCRs: map[string]string{"0": "deadbeef"}}
	err := AttestVerifyRelay(doc, policy)
	assertAttestCode(t, err, "missing-identity-binding")
}
