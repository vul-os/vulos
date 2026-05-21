package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

// DefaultAnchorPath is the canonical in-image path where embed-anchor.sh
// (SEED-01) installs the baked trust-anchor public key.
const DefaultAnchorPath = "/etc/vulos/trust-anchor.pub"

// LoadAnchor reads and parses the Ed25519 public key at path.
//
// The file must contain a single line: the raw 32-byte Ed25519 public key
// encoded as standard Base64 (RFC 4648).  This matches the format written by
// scripts/seed/embed-anchor.sh (SEED-01).
//
// Any error (missing file, wrong length, malformed Base64) is returned as a
// non-nil error; the function never returns a partial or zero-length key.
func LoadAnchor(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("signing: load anchor %q: %w", path, err)
	}
	line := strings.TrimSpace(string(data))
	if line == "" {
		return nil, fmt.Errorf("signing: anchor file %q is empty", path)
	}
	raw, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		return nil, fmt.Errorf("signing: anchor file %q: base64 decode: %w", path, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("signing: anchor file %q: decoded key is %d bytes, want %d",
			path, len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// VerifyWithAnchor loads the trust-anchor public key from anchorPath and
// verifies that sig is a valid Ed25519 signature over canonicalBytes.
//
// It returns a non-nil error on every failure path:
//   - anchor file missing or unreadable
//   - anchor key malformed or wrong length
//   - canonicalBytes or sig are nil/empty
//   - signature does not verify
//
// The function is fail-closed: it never returns nil when anything is wrong,
// and it never panics.
func VerifyWithAnchor(anchorPath string, canonicalBytes, sig []byte) error {
	if len(canonicalBytes) == 0 {
		return fmt.Errorf("signing: VerifyWithAnchor: canonicalBytes must not be empty")
	}
	if len(sig) == 0 {
		return fmt.Errorf("signing: VerifyWithAnchor: sig must not be empty")
	}

	pub, err := LoadAnchor(anchorPath)
	if err != nil {
		return err
	}

	if !Verify(pub, canonicalBytes, sig) {
		return fmt.Errorf("signing: VerifyWithAnchor: signature verification failed")
	}
	return nil
}
