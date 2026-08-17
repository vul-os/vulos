package lanca

import (
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
)

// NewCSR builds a PEM certificate-signing request over an EXISTING private key.
//
// This is the box side of the issuance path and it is the reason SPKI pinning
// survives re-issuance: the box never generates a new key to get a new
// certificate. It signs a CSR with the key `loadOrCreateKey` already persisted,
// so every certificate this CA ever issues for the box carries the same
// SubjectPublicKeyInfo that native clients pinned at first pairing.
//
// The CSR carries no SANs. The issuer ignores requester-supplied names anyway
// (see [Root.IssueFromCSR]), so including them would only invite the mistake of
// believing they mattered. The CSR's job is proof of possession plus transport
// of the public key, nothing more.
func NewCSR(key crypto.Signer, commonName string) ([]byte, error) {
	if key == nil {
		return nil, fmt.Errorf("lanca: NewCSR needs a signer")
	}
	tmpl := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, fmt.Errorf("lanca: create CSR: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// SPKISHA256 returns the base64 SHA-256 of a certificate's
// SubjectPublicKeyInfo — the exact value native clients pin, in the exact
// encoding the rest of the codebase and the `openssl ... | openssl dgst -sha256
// -binary | base64` convention use.
//
// Provided here so a test (or an operator) can assert that a re-issued
// certificate carries the SAME pin as the one it replaces, rather than assuming
// it.
func SPKISHA256(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}
