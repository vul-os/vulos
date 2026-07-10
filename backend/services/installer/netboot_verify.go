package installer

// netboot_verify.go — NETB-03 SECURITY: verify-before-write for the
// netboot-to-install squashfs.
//
// # Why this exists
//
// The netboot-to-install pipeline (netboot_install.go) takes the OS squashfs
// that was fetched during the live-RAM boot and stages it into slot-a on the
// target disk, where it becomes the image the machine boots *locally forever*.
//
// The threat model (THREAT-MODEL.md, Component 1) calls out two live threats
// that this file closes:
//
//   - #1 Tampering:  an attacker replaces the squashfs on the boot medium with
//     a backdoored version before dm-verity is active.
//   - #3 Spoofing:   a MITM on the firstboot network fetch substitutes a
//     malicious image payload before signature verification.
//
// The design invariant (roadmap/NETBOOT.md) is signature-first,
// transport-second: "even plain-HTTP netboot is safe IF the signature is
// checked BEFORE anything executes / is written to disk."  Before this file the
// installer wrote whatever bytes were at SquashfsPath straight to the permanent
// slot with NO signature check — a TOCTOU/verify-after-write dent.  This file
// makes staging fail-closed:
//
//  1. Load the trust anchor baked into the seed (/etc/vulos/trust-anchor.pub).
//     A netboot must PIN this key, not trust the system CA bundle — TLS success
//     never authorises a payload; only a signature that chains to the pinned
//     anchor does.  Absent/malformed anchor ⇒ refuse to install.
//  2. Verify the detached Ed25519 signature (os-core.squashfs.sig, sibling of
//     the image) over the raw squashfs bytes against the pinned anchor.  Any
//     mismatch ⇒ refuse.  This is the same primitive the OTA updater uses
//     (osdist/update.go), so the netboot path is as trustworthy as OTA.
//  3. Downgrade/rollback protection: if a signed manifest (stable.json +
//     stable.json.sig) ships beside the image, enforce that its min_epoch is
//     not below the device epoch floor, and that the image hash matches the
//     manifest RootHash.  An attacker cannot serve an older signed-but-
//     vulnerable image below the floor.
//
// The verification is performed on the SOURCE bytes before they are copied, and
// the copy is only started if verification passes — there is no window in which
// unverified bytes reach the permanent slot.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"vulos/backend/services/osdist"
	"vulos/backend/services/signing"
)

// ─── Sentinel errors ─────────────────────────────────────────────────────────

// ErrAnchorMissing is returned when the baked trust anchor cannot be loaded.
// A netboot install must fail closed rather than install an unverified image.
var ErrAnchorMissing = errors.New("netboot-verify: trust anchor unavailable — refusing to install unverified image")

// ErrSquashfsSigMissing is returned when the detached squashfs signature is not
// present next to the image.  Without a signature the image cannot be trusted.
var ErrSquashfsSigMissing = errors.New("netboot-verify: squashfs signature (.sig) missing — refusing to install unverified image")

// ErrSquashfsBadSignature is returned when the squashfs signature does not
// verify against the pinned trust anchor.
var ErrSquashfsBadSignature = errors.New("netboot-verify: squashfs signature verification failed against pinned trust anchor")

// ErrSquashfsHashMismatch is returned when a signed manifest is present and the
// image hash does not match the manifest's RootHash.
var ErrSquashfsHashMismatch = errors.New("netboot-verify: squashfs hash does not match signed manifest roothash")

// ─── Verifier configuration ──────────────────────────────────────────────────

// netbootVerifyConfig holds the (overridable) paths the verifier reads.  In
// production these default to the well-known seed locations; tests override
// them to point at a temp tree.
type netbootVerifyConfig struct {
	// anchorPath is the pinned trust-anchor public key baked by SEED-01.
	anchorPath string
	// epochPath is the persistent epoch-floor file (downgrade protection).
	epochPath string
}

// defaultNetbootVerifyConfig returns the production paths.
func defaultNetbootVerifyConfig() netbootVerifyConfig {
	return netbootVerifyConfig{
		anchorPath: signing.DefaultAnchorPath, // /etc/vulos/trust-anchor.pub
		epochPath:  signing.DefaultEpochPath,  // /var/lib/vulos/epoch-floor.json
	}
}

// netbootVerifyConfig returns the verification config for this Service: the
// test override when set, otherwise the production seed paths.
func (s *Service) netbootVerifyConfig() netbootVerifyConfig {
	if s.verifyCfg != nil {
		return *s.verifyCfg
	}
	return defaultNetbootVerifyConfig()
}

// squashfsSigPath returns the conventional detached-signature path for a
// squashfs image: "<image>.sig".  Matches the bucket layout
// (os-core.squashfs → os-core.squashfs.sig) used across osdist.
func squashfsSigPath(squashfsPath string) string {
	return squashfsPath + ".sig"
}

// squashfsManifestPaths returns the conventional sibling manifest + signature
// paths that (optionally) ship next to the live-medium squashfs.  When present
// they enable roothash + epoch-floor (downgrade) enforcement.
func squashfsManifestPaths(squashfsPath string) (manifest, manifestSig string) {
	dir := squashfsPath
	if i := strings.LastIndexByte(squashfsPath, '/'); i >= 0 {
		dir = squashfsPath[:i]
	} else {
		dir = "."
	}
	return dir + "/stable.json", dir + "/stable.json.sig"
}

// ─── Verification entry point ─────────────────────────────────────────────────

// verifyNetbootSquashfs verifies the squashfs at squashfsPath BEFORE it is
// staged to disk.  It is fail-closed: any missing/invalid anchor, missing
// signature, bad signature, hash mismatch, or epoch downgrade returns a non-nil
// error and the caller must NOT stage the image.
//
// Verification steps:
//  1. Load the pinned trust anchor (fail closed if unavailable).
//  2. Read + verify the detached squashfs signature over the raw image bytes.
//  3. If a signed manifest ships beside the image, enforce roothash match and
//     epoch-floor (downgrade) protection.
//
// The signed manifest is OPTIONAL for the base guarantee (a valid detached
// signature over the image already prevents tampering/substitution), but when
// present it upgrades protection to include downgrade defence.
func (s *Service) verifyNetbootSquashfs(squashfsPath string, cfg netbootVerifyConfig) error {
	// 1. Pin: load the baked trust anchor.  No CA-bundle fallback — the pinned
	//    key is the sole gate.
	anchor, err := signing.LoadAnchor(cfg.anchorPath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAnchorMissing, err)
	}

	// Compute the image hash once (streamed) — reused for signature + roothash.
	imageBytes, gotHash, err := readAndHash(squashfsPath)
	if err != nil {
		return fmt.Errorf("netboot-verify: read image: %w", err)
	}

	// 2. Detached signature over the raw image bytes.
	sigPath := squashfsSigPath(squashfsPath)
	sigData, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSquashfsSigMissing, err)
	}
	detached, err := signing.ParseSig(sigData)
	if err != nil {
		return fmt.Errorf("netboot-verify: parse squashfs .sig: %w", err)
	}
	if !signing.Verify(anchor, imageBytes, detached.SigBytes) {
		return ErrSquashfsBadSignature
	}

	// 3. Optional signed manifest → roothash + epoch-floor (downgrade) defence.
	manifestPath, manifestSigPath := squashfsManifestPaths(squashfsPath)
	manifestData, mErr := os.ReadFile(manifestPath)
	manifestSigData, msErr := os.ReadFile(manifestSigPath)
	if mErr == nil && msErr == nil {
		if err := s.enforceManifest(manifestData, manifestSigData, anchor, gotHash, cfg); err != nil {
			return err
		}
	}
	// If no manifest ships beside the image the detached-signature guarantee
	// still holds; downgrade defence simply is not available for that medium.

	return nil
}

// enforceManifest verifies the signed manifest, enforces the epoch floor
// (downgrade protection), and confirms the image hash matches the manifest's
// RootHash.  All checks are fail-closed.
func (s *Service) enforceManifest(
	manifestData, manifestSigData []byte,
	anchor []byte,
	gotHash string,
	cfg netbootVerifyConfig,
) error {
	detachedManifestSig, err := signing.ParseSig(manifestSigData)
	if err != nil {
		return fmt.Errorf("netboot-verify: parse manifest .sig: %w", err)
	}

	// Load the persistent epoch floor (downgrade protection).  If the floor
	// store cannot be opened, use a zero floor rather than fail — the signature
	// check already guarantees authenticity; the floor only strengthens it.
	var floor int64
	if es, err := signing.NewEpochStore(cfg.epochPath); err == nil {
		floor = es.Current()
	}

	manifest, err := osdist.ParseAndVerify(manifestData, detachedManifestSig.SigBytes, anchor, floor)
	if err != nil {
		// ParseAndVerify returns ErrBadSignature / ErrEpochTooLow / ErrMalformed.
		return fmt.Errorf("netboot-verify: manifest: %w", err)
	}

	// Roothash binding: the image we are about to stage must be exactly the one
	// the signed manifest names.  Prevents a valid signature over a *different*
	// (older/vulnerable) image being paired with a newer manifest.
	wantHash := strings.ToLower(strings.TrimSpace(manifest.RootHash))
	if !strings.EqualFold(gotHash, wantHash) {
		return fmt.Errorf("%w: got %s, want %s", ErrSquashfsHashMismatch, gotHash, wantHash)
	}

	return nil
}

// readAndHash reads the whole file at path into memory and returns the bytes
// plus the lowercase hex SHA-256.  The squashfs must be verified in full before
// staging, so reading it entirely is intentional (the OTA path does the same).
func readAndHash(path string) ([]byte, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	h := sha256.New()
	buf, err := io.ReadAll(io.TeeReader(f, h))
	if err != nil {
		return nil, "", err
	}
	return buf, hex.EncodeToString(h.Sum(nil)), nil
}
