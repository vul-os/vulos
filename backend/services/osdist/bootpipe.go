// Package osdist — bootpipe.go (NETB-02)
//
// A "boot-pipe manifest" describes every artifact that iPXE must fetch and
// signature-verify before executing, covering the full netboot payload:
//
//	boot-pipe.json       — this manifest (also signature-verified via imgverify)
//	boot-pipe.json.sig   — detached Ed25519 signature over the manifest
//	kernel               — Linux kernel image
//	kernel.sig           — detached Ed25519 signature
//	initramfs.img        — initramfs cpio archive
//	initramfs.img.sig    — detached Ed25519 signature
//
// The cloud side (or a self-hosted boot server) generates and serves these
// files.  iPXE fetches boot-pipe.json, runs imgverify against the baked trust
// anchor, then uses the listed URLs to fetch + verify each artifact in turn.
//
// Verification uses SIGN-01/SIGN-02 (backend/services/signing) — no new crypto.
package osdist

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"

	"vulos/backend/services/signing"
)

// ─── Sentinel errors ─────────────────────────────────────────────────────────

// ErrBootPipeMalformed is returned when the manifest JSON cannot be parsed or
// is structurally incomplete (missing required fields).
var ErrBootPipeMalformed = errors.New("bootpipe: malformed manifest")

// ErrBootPipeBadSignature is returned when the Ed25519 signature over the
// manifest's canonical bytes does not verify against the supplied anchor key.
var ErrBootPipeBadSignature = errors.New("bootpipe: bad signature")

// ─── Schema ───────────────────────────────────────────────────────────────────

// ArtifactEntry describes a single fetched artifact: its download URL, the URL
// of the detached .sig file, and the signing algorithm.
//
// All fields are required; the verifier rejects entries with empty values.
type ArtifactEntry struct {
	// Name is a short human-readable identifier (e.g. "kernel", "initramfs").
	// Used as the imgfetch/imgverify image name in the iPXE script.
	Name string `json:"name"`

	// URL is the absolute HTTP(S) URL for the artifact.
	URL string `json:"url"`

	// SigURL is the absolute HTTP(S) URL for the detached .sig file.
	// The .sig must be in the Vulos detached-signature format (FORMAT.md).
	SigURL string `json:"sig_url"`

	// Algorithm is the signing algorithm; always "ed25519" for this version.
	Algorithm string `json:"algorithm"`
}

// BootPipeManifest is the in-memory representation of a boot-pipe.json file.
//
// The manifest is signed by the release key (SIGN-03) and verified by iPXE
// imgverify against the trust anchor baked into the stick image (NETB-02).
// The Go side (this package) signs and verifies the manifest for the cloud
// serving path and for unit tests; it does not replace iPXE imgverify.
//
// Example JSON:
//
//	{
//	  "version":   1,
//	  "channel":   "stable",
//	  "artifacts": [
//	    { "name":"kernel",    "url":"http://boot.vulos.org/v08/kernel",
//	                          "sig_url":"http://boot.vulos.org/v08/kernel.sig",
//	                          "algorithm":"ed25519" },
//	    { "name":"initramfs", "url":"http://boot.vulos.org/v08/initramfs.img",
//	                          "sig_url":"http://boot.vulos.org/v08/initramfs.img.sig",
//	                          "algorithm":"ed25519" }
//	  ]
//	}
type BootPipeManifest struct {
	// Version is the manifest format version; always 1 for this release.
	Version int `json:"version"`

	// Channel identifies the OS update channel, e.g. "stable" or "edge".
	Channel string `json:"channel"`

	// Artifacts is the ordered list of artifacts iPXE must fetch and verify.
	// Order is significant: iPXE processes them in the listed order.
	Artifacts []ArtifactEntry `json:"artifacts"`
}

// ─── Canonical bytes ─────────────────────────────────────────────────────────

// Canonical returns the deterministic, signing-canonical JSON encoding of m.
// Delegates to signing.Canonical for consistent byte ordering across the
// Vulos signing toolchain (SIGN-01).
func (m *BootPipeManifest) Canonical() ([]byte, error) {
	b, err := signing.Canonical(m)
	if err != nil {
		return nil, fmt.Errorf("bootpipe: canonical: %w", err)
	}
	return b, nil
}

// ─── Validate ────────────────────────────────────────────────────────────────

// validate returns ErrBootPipeMalformed (wrapped) if any required field is
// missing or invalid.  Called by ParseAndVerifyBootPipe before signature check.
func (m *BootPipeManifest) validate() error {
	if m.Version != 1 {
		return fmt.Errorf("%w: version must be 1, got %d", ErrBootPipeMalformed, m.Version)
	}
	if m.Channel == "" {
		return fmt.Errorf("%w: channel is empty", ErrBootPipeMalformed)
	}
	if len(m.Artifacts) == 0 {
		return fmt.Errorf("%w: artifacts list is empty", ErrBootPipeMalformed)
	}
	seen := make(map[string]bool, len(m.Artifacts))
	for i, a := range m.Artifacts {
		if a.Name == "" {
			return fmt.Errorf("%w: artifact[%d] name is empty", ErrBootPipeMalformed, i)
		}
		if a.URL == "" {
			return fmt.Errorf("%w: artifact[%d] %q url is empty", ErrBootPipeMalformed, i, a.Name)
		}
		if a.SigURL == "" {
			return fmt.Errorf("%w: artifact[%d] %q sig_url is empty", ErrBootPipeMalformed, i, a.Name)
		}
		if a.Algorithm != signing.AlgorithmID {
			return fmt.Errorf("%w: artifact[%d] %q algorithm %q is not supported (want %q)",
				ErrBootPipeMalformed, i, a.Name, a.Algorithm, signing.AlgorithmID)
		}
		if seen[a.Name] {
			return fmt.Errorf("%w: artifact name %q is duplicated", ErrBootPipeMalformed, a.Name)
		}
		seen[a.Name] = true
	}
	return nil
}

// ─── Sign ────────────────────────────────────────────────────────────────────

// SignBootPipe signs the canonical bytes of m with priv and returns the raw
// 64-byte Ed25519 signature.  The caller serialises it into a .sig file via
// signing.MarshalSig (SIGN-01 / FORMAT.md).
//
// The private key should be the release key (SIGN-03); the matching public key
// is the trust anchor baked into the iPXE binary (SEED-01 / NETB-02).
func SignBootPipe(m *BootPipeManifest, priv ed25519.PrivateKey) ([]byte, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	canonical, err := m.Canonical()
	if err != nil {
		return nil, err
	}
	return signing.Sign(priv, canonical), nil
}

// ─── Parse + verify ──────────────────────────────────────────────────────────

// ParseAndVerifyBootPipe unmarshals data as a BootPipeManifest, validates its
// structure, and verifies sig (raw 64-byte Ed25519 signature) against
// anchorPub over the manifest's canonical bytes.
//
// Parameters:
//   - data      raw bytes of boot-pipe.json (any valid JSON encoding)
//   - sig       raw 64-byte Ed25519 signature over the canonical bytes
//   - anchorPub the trust-anchor public key (supplied by the caller;
//     SIGN-02 wires the baked anchor key in production)
//
// The function is fail-closed: any verification failure returns nil and a
// typed sentinel error (ErrBootPipeMalformed or ErrBootPipeBadSignature).
func ParseAndVerifyBootPipe(data []byte, sig []byte, anchorPub ed25519.PublicKey) (*BootPipeManifest, error) {
	// 1. Unmarshal.
	var m BootPipeManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBootPipeMalformed, err)
	}

	// 2. Structural validation.
	if err := m.validate(); err != nil {
		return nil, err
	}

	// 3. Canonical bytes for verification.
	canonical, err := m.Canonical()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBootPipeMalformed, err)
	}

	// 4. Signature verification — fail closed.
	if !signing.Verify(anchorPub, canonical, sig) {
		return nil, ErrBootPipeBadSignature
	}

	return &m, nil
}
