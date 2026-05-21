// Package signing provides deterministic canonical-byte serialisation for
// Vulos OS manifests (stable.json) and Ed25519-based detached-signature
// helpers.  No key custody, no key generation — callers supply keys.
//
// See FORMAT.md in this package for the full specification.
package signing

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ─── Canonical bytes ──────────────────────────────────────────────────────────

// Canonical serialises v to deterministic JSON:
//   - map keys sorted lexicographically at every level of nesting
//   - no insignificant whitespace (no indentation, no trailing newline)
//   - the output is byte-stable across runs regardless of Go map-iteration order
//
// v must be JSON-marshallable.  The function is intentionally strict: it
// re-encodes through the canonical encoder so even a pre-encoded []byte value
// would be canonical.
func Canonical(v any) ([]byte, error) {
	// Marshal to intermediate form first so we can walk the generic structure.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("signing: canonical marshal: %w", err)
	}
	return canonicalBytes(json.RawMessage(raw))
}

// canonicalBytes recursively re-encodes a json.RawMessage with sorted keys.
func canonicalBytes(raw json.RawMessage) ([]byte, error) {
	// Decode into the most general form.
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // preserve number precision
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("signing: decode: %w", err)
	}
	return encodeCanonical(v)
}

// encodeCanonical recursively encodes a Go value into canonical JSON.
func encodeCanonical(v any) ([]byte, error) {
	switch val := v.(type) {
	case map[string]any:
		return encodeObject(val)
	case []any:
		return encodeArray(val)
	case json.Number:
		// Preserve the exact numeric representation; no transformation.
		return []byte(val.String()), nil
	case string:
		return json.Marshal(val)
	case bool:
		return json.Marshal(val)
	case nil:
		return []byte("null"), nil
	default:
		return json.Marshal(val)
	}
}

func encodeObject(m map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyBytes, err := json.Marshal(k)
		if err != nil {
			return nil, fmt.Errorf("signing: marshal key %q: %w", k, err)
		}
		buf.Write(keyBytes)
		buf.WriteByte(':')
		valBytes, err := encodeCanonical(m[k])
		if err != nil {
			return nil, fmt.Errorf("signing: marshal value for key %q: %w", k, err)
		}
		buf.Write(valBytes)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func encodeArray(a []any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, elem := range a {
		if i > 0 {
			buf.WriteByte(',')
		}
		b, err := encodeCanonical(elem)
		if err != nil {
			return nil, fmt.Errorf("signing: marshal array element %d: %w", i, err)
		}
		buf.Write(b)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

// ─── Sign / Verify ────────────────────────────────────────────────────────────

// Sign produces an Ed25519 signature over canonicalBytes using priv.
// canonicalBytes must be the output of Canonical — deterministic, sorted JSON.
// The caller is responsible for zeroing priv after use if key hygiene matters.
func Sign(priv ed25519.PrivateKey, canonicalBytes []byte) []byte {
	return ed25519.Sign(priv, canonicalBytes)
}

// Verify reports whether sig is a valid Ed25519 signature by pub over
// canonicalBytes.  Returns false (not a panic) on any mismatch or length error.
func Verify(pub ed25519.PublicKey, canonicalBytes, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, canonicalBytes, sig)
}

// ─── Detached-signature format ────────────────────────────────────────────────

// AlgorithmID is the algorithm identifier embedded in .sig files.
const AlgorithmID = "ed25519"

// sigFilePrefix is the first line of every .sig file.
const sigFilePrefix = "vulos-sig-v1"

// Signature is the in-memory representation of a Vulos detached .sig file.
type Signature struct {
	// Algorithm is always "ed25519" for this version.
	Algorithm string
	// KeyID is an opaque, human-readable identifier for the signing key
	// (e.g. a hex-encoded public key fingerprint or a label like
	// "release-2026-05").  Not validated during Verify; informational only.
	KeyID string
	// SigBytes is the raw 64-byte Ed25519 signature.
	SigBytes []byte
}

// MarshalSig serialises a Signature to the canonical .sig text format:
//
//	vulos-sig-v1
//	algorithm: ed25519
//	key-id: <keyID>
//	sig: <base64(sigBytes)>
//
// The format is newline-terminated and contains no other whitespace.
func MarshalSig(s Signature) ([]byte, error) {
	if s.Algorithm != AlgorithmID {
		return nil, fmt.Errorf("signing: unsupported algorithm %q (want %q)", s.Algorithm, AlgorithmID)
	}
	if len(s.SigBytes) != ed25519.SignatureSize {
		return nil, fmt.Errorf("signing: sig must be %d bytes, got %d", ed25519.SignatureSize, len(s.SigBytes))
	}
	// key-id may not contain newlines.
	if strings.ContainsAny(s.KeyID, "\r\n") {
		return nil, errors.New("signing: key-id must not contain newlines")
	}
	encoded := base64.StdEncoding.EncodeToString(s.SigBytes)
	out := fmt.Sprintf("%s\nalgorithm: %s\nkey-id: %s\nsig: %s\n",
		sigFilePrefix, s.Algorithm, s.KeyID, encoded)
	return []byte(out), nil
}

// ParseSig deserialises a .sig file produced by MarshalSig.
// It returns an error if the format is unrecognised or any field is missing.
func ParseSig(data []byte) (Signature, error) {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 4 {
		return Signature{}, errors.New("signing: .sig file too short")
	}
	if lines[0] != sigFilePrefix {
		return Signature{}, fmt.Errorf("signing: unrecognised .sig header %q", lines[0])
	}

	fields := make(map[string]string, 3)
	for _, line := range lines[1:] {
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		fields[key] = val
	}

	algo, ok := fields["algorithm"]
	if !ok {
		return Signature{}, errors.New("signing: missing 'algorithm' field")
	}
	if algo != AlgorithmID {
		return Signature{}, fmt.Errorf("signing: unsupported algorithm %q", algo)
	}
	keyID, ok := fields["key-id"]
	if !ok {
		return Signature{}, errors.New("signing: missing 'key-id' field")
	}
	sigB64, ok := fields["sig"]
	if !ok {
		return Signature{}, errors.New("signing: missing 'sig' field")
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return Signature{}, fmt.Errorf("signing: base64 decode sig: %w", err)
	}
	if len(sigBytes) != ed25519.SignatureSize {
		return Signature{}, fmt.Errorf("signing: decoded sig has %d bytes, want %d",
			len(sigBytes), ed25519.SignatureSize)
	}
	return Signature{
		Algorithm: algo,
		KeyID:     keyID,
		SigBytes:  sigBytes,
	}, nil
}
