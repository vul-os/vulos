package integrations

// devicecert.go — INTEG-SEC-01 owner-attested device-cert verification.
//
// The control-plane enrollment subsystem (internal/enroll) issues a management
// DeviceCert when a fleet owner approves a device-authorization grant in-browser
// — binding the device's ed25519 public key to its ULID and owning account, and
// signed by the management CA. That approval is the attestation: it eliminates
// the trust-on-first-use race of the device-key registry.
//
// A box presents {pubKey, cert} + a fresh signature on the mint request; the CP
// verifies the cert against the CA public key (no registry, no shared secret)
// and then verifies the per-request signature against the presented pubKey.

import (
	"crypto/ed25519"
	"encoding/base64"
)

// DeviceCertPayload reconstructs the exact bytes the management CA signs for a
// device cert. It MUST match the signer (cmd/server envCertSigner and
// enroll.MemCertSigner): "vulos-device-cert:v1:<b64url(pubKey)>:<account>:<ulid>".
func DeviceCertPayload(pubKey []byte, accountID, ulid string) string {
	return "vulos-device-cert:v1:" +
		base64.RawURLEncoding.EncodeToString(pubKey) + ":" +
		accountID + ":" + ulid
}

// VerifyDeviceCert reports whether certSig is a valid management-CA signature
// binding (pubKey, accountID, ulid). caPub is the ed25519 CA public key.
func VerifyDeviceCert(caPub, pubKey []byte, accountID, ulid string, certSig []byte) bool {
	if len(caPub) != ed25519.PublicKeySize || len(pubKey) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(caPub), []byte(DeviceCertPayload(pubKey, accountID, ulid)), certSig)
}

// VerifyEd25519Sig verifies a device's per-request signature over message
// (ed25519 signs the message directly — no external pre-hash).
func VerifyEd25519Sig(pubKey []byte, message string, sig []byte) bool {
	if len(pubKey) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pubKey), []byte(message), sig)
}
