// Package devicekey provides a KeyStore abstraction for device-level key operations.
// Two backends are supported:
//   - TPM backend: uses a hardware TPM via /dev/tpmrm0 (github.com/google/go-tpm/tpm2).
//   - Software backend: AES-256-GCM encrypted key file under ~/.vulos/auth/tpm/ when no
//     hardware TPM is present.
//
// Callers should use Open() which probes for hardware and returns the appropriate
// implementation transparently.
package devicekey

import (
	"crypto"
	"crypto/ecdsa"
	"fmt"
	"os"
	"vulos/backend/internal/datadir"
)

// BackendType identifies which key-storage backend is in use.
type BackendType string

const (
	BackendHardware BackendType = "hardware" // real TPM via /dev/tpmrm0
	BackendSoftware BackendType = "software" // AES-256-GCM encrypted key file
)

// Status describes the current state of the key store.
type Status struct {
	Backend    BackendType `json:"backend"`     // "hardware" or "software"
	Available  bool        `json:"available"`   // always true after Open()
	DevicePath string      `json:"device_path"` // e.g. /dev/tpmrm0 or ""
	KeyDir     string      `json:"key_dir"`     // directory holding key material
	DeviceID   string      `json:"device_id"`   // stable identity (public key fingerprint)

	// Revoked reports whether the CURRENTLY ACTIVE identity key is known-revoked
	// by the process-wide revocation checker installed via SetRevocationChecker
	// (see revocation.go). This is a LOCAL observability signal only — a box
	// cannot un-compromise itself by ignoring this flag; real enforcement happens
	// at every REMOTE verifier via IsDeviceKeyRevoked / VerifyDeviceSignatureChecked.
	// Always false when no checker is installed (fail-open only in the sense that
	// an entirely unwired subsystem reports nothing — matches the isVulosIDRevoked
	// convention in services/peering).
	Revoked bool `json:"revoked"`
}

// KeyStore is the core abstraction for TPM/software-keystore operations.
// All implementations must be safe for concurrent use.
type KeyStore interface {
	// Seal encrypts plaintext so that it can only be unsealed by this device.
	// The returned ciphertext is opaque and should be treated as a blob.
	Seal(plaintext []byte) (ciphertext []byte, err error)

	// Unseal decrypts ciphertext that was produced by Seal on this device.
	Unseal(ciphertext []byte) (plaintext []byte, err error)

	// Sign produces a signature over digest using the device identity key.
	// The hash parameter identifies the algorithm used to produce digest
	// (e.g. crypto.SHA256). Implementations use ECDSA P-256.
	//
	// SELF FAIL-CLOSED (see revocation.go's ErrActiveKeyRevoked): if the
	// CURRENTLY ACTIVE identity key's fingerprint is known-revoked by the
	// process-wide checker (SetRevocationChecker), Sign returns
	// ErrActiveKeyRevoked and mints nothing — a revoked key cannot be used to
	// authenticate anywhere in this process, not merely at remote verifiers
	// that happen to have pulled the revocation. This is independent of, and
	// in addition to, VerifyDeviceSignatureChecked's remote-side rejection.
	Sign(digest []byte, hash crypto.Hash) (signature []byte, err error)

	// DeviceIdentity returns the stable public key bytes (PKIX DER) that
	// uniquely identify this device. The same bytes are returned across
	// restarts as long as the key material has not been rotated.
	DeviceIdentity() (pubKeyDER []byte, err error)

	// Status returns a human/machine-readable description of the backend.
	Status() Status

	// Close releases any held resources (TPM file descriptor, etc.).
	Close() error

	// Rotate performs a NORMAL (self-authorized) device-key rotation: it
	// generates a fresh identity key, signs a RotationCert binding the CURRENT
	// (old) device key to the new one using the OLD key — proving possession
	// of the key being rotated away from — then installs the new key as the
	// active identity. The previous public key is retained internally with a
	// grace-window expiry so a signature made just before the rotation still
	// verifies for a short period (mirrors FABRIC-KEY-01's overlap window).
	// reason is a short human-readable note recorded on the returned cert.
	//
	// This is the ONLY exported way to rotate a device identity. There is no
	// exported way to force a rotation without the old key's signature — see
	// forceInstallIdentity and BreakGlassRotate in rotation.go for the
	// quorum-gated alternative used when the old key is lost or compromised.
	//
	// SELF FAIL-CLOSED: like Sign, Rotate refuses (ErrActiveKeyRevoked) when
	// the CURRENT active key is already revoked — signing with a revoked key
	// proves nothing, so a box in that state must use BreakGlassRotate.
	Rotate(reason string) (*RotationCert, error)

	// forceInstallIdentity is the MECHANICAL-ONLY primitive behind break-glass
	// rotation. It installs newPriv as the active identity key WITHOUT
	// requiring any signature from the previous (possibly compromised) key,
	// and returns the OLD public key (PKIX DER) so the caller can build the
	// RotationCert.
	//
	// COMPARE-AND-SWAP: expectedOldPubDER is the active-key PKIX DER the caller
	// verified the quorum against. The install is atomic under the store lock:
	// if the CURRENTLY active key no longer equals expectedOldPubDER (e.g. a
	// concurrent Rotate swapped it between the caller's DeviceIdentity() read
	// and this call), it returns ErrActiveKeyChanged and installs nothing —
	// closing the TOCTOU that would otherwise bind the cert's OldPubKey to a
	// key the quorum never signed over (an unauditable cert) or revoke a
	// superseded key. The returned oldPubKeyDER therefore always equals
	// expectedOldPubDER on success.
	//
	// Deliberately UNEXPORTED: in Go, a type outside this package cannot
	// implement an interface that has an unexported method, so KeyStore can
	// only ever be satisfied by types defined in package devicekey (today:
	// softwareStore and tpmStore) — and only code inside this package can call
	// the method at all. The SOLE call site is BreakGlassRotate (rotation.go),
	// which requires a successful fleetid.VerifyQuorum from a quorum of OTHER
	// boxes BEFORE this is ever invoked. There is structurally no path — from
	// this package, from cmd/server, or from any other package — to
	// force-rotate a device identity without EITHER the old key's signature
	// (Rotate) OR a verified peer quorum (BreakGlassRotate). This is the
	// compile-time enforcement of the hard rule: a box can never self-authorize
	// its own break-glass rotation.
	forceInstallIdentity(newPriv *ecdsa.PrivateKey, expectedOldPubDER []byte) (oldPubKeyDER []byte, err error)
}

// defaultTPMPath is the Linux resource-managed TPM character device.
const defaultTPMPath = "/dev/tpmrm0"

// defaultKeyDir returns ~/.vulos/auth/tpm/ creating it if needed.
func defaultKeyDir() (string, error) {
	dir := datadir.Join("auth", "tpm")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("devicekey: cannot create key dir %q: %w", dir, err)
	}
	return dir, nil
}

// Open probes for a hardware TPM at /dev/tpmrm0. If the device is present and
// openable it returns a TPM-backed KeyStore; otherwise it returns a software
// KeyStore whose keys are stored under ~/.vulos/auth/tpm/.
//
// keyDir may be "" to use the default (~/.vulos/auth/tpm/).
func Open(keyDir string) (KeyStore, error) {
	if keyDir == "" {
		var err error
		keyDir, err = defaultKeyDir()
		if err != nil {
			return nil, err
		}
	} else {
		if err := os.MkdirAll(keyDir, 0700); err != nil {
			return nil, fmt.Errorf("devicekey: cannot create key dir %q: %w", keyDir, err)
		}
	}

	if ks, err := openTPM(keyDir); err == nil {
		return ks, nil
	}
	return openSoftware(keyDir)
}
