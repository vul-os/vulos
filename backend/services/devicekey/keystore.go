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
	Sign(digest []byte, hash crypto.Hash) (signature []byte, err error)

	// DeviceIdentity returns the stable public key bytes (PKIX DER) that
	// uniquely identify this device. The same bytes are returned across
	// restarts as long as the key material has not been rotated.
	DeviceIdentity() (pubKeyDER []byte, err error)

	// Status returns a human/machine-readable description of the backend.
	Status() Status

	// Close releases any held resources (TPM file descriptor, etc.).
	Close() error
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
