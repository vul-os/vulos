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
// slot with NO signature check — a TOCTOU/verify-after-write dent.
//
// # The scheme this file verifies, and the one it used to verify
//
// This file previously checked a detached signature made by the ROOT ANCHOR
// over the RAW IMAGE BYTES.  Nothing in this repository has ever produced such
// a signature, and nothing was ever going to:
//
//	cmd/sign sign-image     → RELEASE key over canonical JSON of an ImagePayload
//	                          {path, roothash, size, min_epoch, released_at}
//	cmd/sign sign-manifest  → RELEASE key over canonical JSON of a ManifestPayload
//	cmd/verify + cmd/init   → verify exactly that, after validating the release
//	                          cert against the pinned root anchor
//
// So the two ends disagreed on BOTH the key and the bytes.  The consequence was
// not a hole — it was a wall: verifyNetbootSquashfs cannot return nil for any
// artifact this project can sign, so a real netboot install aborts at the
// verify-squashfs step every time.  It has never been observed because a real
// netboot has never been run (docs/ARCHITECTURE.md); the QEMU harness got past
// it only because its E2E test fabricated a root-key raw-bytes signature, i.e.
// it tested a shape that production can never produce.  osdist/update.go:352,455
// (the OTA updater) has the identical mismatch and is NOT fixed here — it is
// outside this file's remit and is reported separately.
//
// What is verified now is the chain cmd/sign actually emits, in the order
// cmd/verify.VerifySquashfsBeforePivot and cmd/init.verifyOSBeforeBoot use:
//
//  1. Load the trust anchor baked into the seed (/etc/vulos/trust-anchor.pub).
//     A netboot must PIN this key, not trust the system CA bundle — TLS success
//     never authorises a payload.  Absent/malformed anchor ⇒ refuse.
//  2. Load the release-key certificate and validate it against that anchor;
//     enforce cert.MinEpoch >= the device epoch floor, and RAISE that floor to
//     the cert's min_epoch.  The release key is trusted only for as long as the
//     offline root says it is — see "Why the floor is raised here" below.
//  3. Verify stable.json against stable.json.sig with the RELEASE key, and
//     enforce the epoch floor over the manifest as well.  The manifest is
//     MANDATORY here — see "Why the manifest is no longer optional" below.
//  4. Verify <image>.sig as a RELEASE-key signature over canonical(ImagePayload),
//     the payload being the manifest's own fields.
//  5. BIND that payload to the bytes on this medium: the image's dm-verity root
//     hash must equal ImagePayload.RootHash, and its byte length must equal
//     ImagePayload.Size.
//
// # Why the manifest is no longer optional
//
// An ImagePayload signature covers a NAME, a SIZE and a ROOT HASH — it does not
// cover the image.  Verifying it in isolation proves that someone with the
// release key once described an artifact; it says nothing about the bytes in
// front of us.  The binding is step 5, and step 5 needs the manifest's fields to
// reconstruct the payload in the first place.  A raw-bytes signature bound
// itself; this one does not, so the medium must carry the manifest that names
// what it should be, or there is nothing to check.
//
// # Why the floor is raised here, and why an unopenable store is fatal
//
// The epoch floor is the device's memory of which release keys the offline root
// has retired (roadmap/SIGNING.md § Minimum Trusted Epoch).  This file used to
// READ it and never raise it, which is the defect 8846d61c fixed on the OTA
// path: nothing anywhere moved the floor, so it sat at 0 for the life of the
// device, every min_epoch >= 0 passed, and issuing a release cert with a higher
// -min-epoch revoked nothing at all.  signing.EpochStore.RaiseFromReleaseCert
// is now called as soon as the ROOT-SIGNED cert validates — and only from the
// cert, never from the release-key-signed manifest below, because an attacker
// holding a compromised release key could otherwise publish min_epoch=MaxInt64
// and permanently brick the install path (the floor never falls).
//
// The raise happens BEFORE the manifest is verified, on purpose: the authority
// for it is the root signature on the cert alone.  Making it contingent on a
// later, separately-signed artifact would let a hostile medium hold the floor
// down by pairing a good cert with a broken manifest and then replaying the
// retired cert.  It also means the manifest is checked against the RAISED floor,
// which enforces cmd/sign's rule that a manifest's min_epoch is at least its
// cert's.
//
// A floor store that will not open is FATAL here.  It used to fall back to
// floor 0, and floor 0 is not "no opinion" — it is "accept every epoch any root
// has ever retired", i.e. the exact downgrade this file exists to refuse, chosen
// silently on behalf of the operator.  Three considerations settle it:
//
//   - Where this runs.  The netboot payload is attacker-reachable in transit
//     (THREAT-MODEL.md #3), and every other input on this path — anchor, cert,
//     manifest, verity root hash — is already refused rather than downgraded
//     when it is missing.  A store that is unreadable or holds unparseable JSON
//     is an anomaly on the one path where anomalies are cheapest to attack.
//   - What the failure means.  NewEpochStore creates the file at floor 0 when it
//     is simply ABSENT, so a genuinely new device does not reach the error path
//     at all; reaching it means the directory is unwritable or the record is
//     corrupt.  A corrupt record is precisely how an attacker with local write
//     access would try to reset the floor, and answering that by adopting floor
//     0 would grant them exactly what they asked for.
//   - What it costs.  The failure is loud and recoverable — the operator is at
//     the machine, running an installer, and can be told which file to fix.  The
//     alternative failure mode is silent and permanent: an image signed by a
//     retired key installed onto the disk the box then boots forever.
//
// # Why a missing dm-verity root hash is fatal here
//
// Step 5's root-hash half is the only thing tying the signature to the image
// content, and it can only be computed by VERITY-03's resolveVerityArtifacts
// (which runs `veritysetup verify` over the real image).  Where that returns no
// artifacts there is no binding left except Size, and "a file of the right
// length" is not authentication.  On the netboot path — the one where the
// payload is attacker-reachable in transit, and where the initramfs hook's own
// policy is strictest — that is refused rather than downgraded.  Note this is
// why the pipeline resolves verity BEFORE it verifies the signature; the two
// steps were independent when the signature covered raw bytes, and are not now.
//
// The verification is performed on the SOURCE artifacts before anything is
// copied, and the copy only starts if it passes — there is no window in which
// unverified bytes reach the permanent slot.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	// cmd/verify is a library that happens to live under cmd/ ("Package verify
	// implements per-boot-stage signature verification"); cmd/init imports it
	// the same way.  Importing the ImagePayload definition rather than copying
	// it is deliberate: a third, independently-maintained copy of the signing
	// surface is exactly how the two ends came to disagree in the first place.
	"vulos/backend/cmd/verify"
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
// verify against the release key certified by the pinned trust anchor.
var ErrSquashfsBadSignature = errors.New("netboot-verify: squashfs signature verification failed against the certified release key")

// ErrSquashfsHashMismatch is returned when the signed ImagePayload does not
// describe the image on this medium — its dm-verity root hash or its byte
// length differs.  This is the only thing binding the signature to the bytes.
var ErrSquashfsHashMismatch = errors.New("netboot-verify: signed image payload does not describe this image")

// ErrReleaseCertMissing is returned when the release-key certificate cannot be
// read.  Without it the release key is unknown and nothing can be verified.
var ErrReleaseCertMissing = errors.New("netboot-verify: release certificate unavailable — refusing to install unverified image")

// ErrReleaseCertInvalid is returned when the release certificate does not
// validate against the pinned anchor, has expired, or is below the epoch floor.
var ErrReleaseCertInvalid = errors.New("netboot-verify: release certificate rejected")

// ErrManifestMissing is returned when stable.json / stable.json.sig do not ship
// beside the image.  They are mandatory: the ImagePayload the release key signed
// cannot be reconstructed without them (see the file comment).
var ErrManifestMissing = errors.New("netboot-verify: signed manifest (stable.json + stable.json.sig) missing — the image signature cannot be reconstructed or bound")

// ErrVerityRootHashUnknown is returned when the medium carries no validated
// dm-verity root hash, leaving the signature unbound to the image bytes.
var ErrVerityRootHashUnknown = errors.New("netboot-verify: no validated dm-verity root hash for this image — the signature cannot be bound to its bytes")

// ErrEpochFloorUnavailable is returned when the persistent epoch floor cannot be
// opened or updated.  It is fatal rather than a fallback to floor 0: floor 0
// accepts every retired release key, and choosing it silently would hand an
// attacker the downgrade this file exists to refuse.  See the file comment.
var ErrEpochFloorUnavailable = errors.New("netboot-verify: epoch floor unavailable — refusing to install without rollback protection")

// ─── Verifier configuration ──────────────────────────────────────────────────

// netbootVerifyConfig holds the (overridable) paths the verifier reads.  In
// production these default to the well-known seed locations; tests override
// them to point at a temp tree.
type netbootVerifyConfig struct {
	// anchorPath is the pinned trust-anchor public key baked by SEED-01.
	anchorPath string
	// epochPath is the persistent epoch-floor file (downgrade protection).
	epochPath string
	// certPath is the seed copy of the release-key certificate, used when the
	// medium does not ship one beside the image.  Same path cmd/init reads.
	certPath string
}

// defaultReleaseCertPath is the seed location of the release certificate.  It
// matches cmd/init's verityReleaseCertPath so a box and its installer trust the
// same file rather than two policies that can drift apart.
const defaultReleaseCertPath = "/etc/vulos/release-cert.json"

// releaseCertSiblingName is the name a release certificate takes when it ships
// WITH the payload.  Preferred over the seed copy, and safe to prefer: the cert
// is worthless unless it validates against the pinned anchor, and serving it
// beside the image is what lets a release key be rotated without re-flashing
// every device's seed.
const releaseCertSiblingName = "release-cert.json"

// defaultNetbootVerifyConfig returns the production paths.
func defaultNetbootVerifyConfig() netbootVerifyConfig {
	return netbootVerifyConfig{
		anchorPath: signing.DefaultAnchorPath, // /etc/vulos/trust-anchor.pub
		epochPath:  signing.DefaultEpochPath,  // /var/lib/vulos/epoch-floor.json
		certPath:   defaultReleaseCertPath,    // /etc/vulos/release-cert.json
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
// staged to disk.  It is fail-closed: a missing or invalid anchor, release
// certificate, manifest, or image signature — or a signed payload that does not
// describe this image — returns a non-nil error and the caller must NOT stage.
//
// verityRootHash is the dm-verity root hash VERITY-03 already validated against
// this exact image (resolveVerityArtifacts ran `veritysetup verify` over it).
// It is what binds the signature to the bytes; an empty value is fatal, because
// what remains would authenticate a description rather than an image.
func (s *Service) verifyNetbootSquashfs(squashfsPath string, cfg netbootVerifyConfig, verityRootHash string) error {
	// 1. Pin: load the baked trust anchor.  No CA-bundle fallback — the pinned
	//    key is the sole gate.
	anchor, err := signing.LoadAnchor(cfg.anchorPath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAnchorMissing, err)
	}

	// The epoch floor gates BOTH the certificate and the manifest, and is RAISED
	// by the root-signed cert in step 2.  A store that will not open is fatal —
	// falling back to zero would accept every retired release key.  A brand-new
	// device does not land here: NewEpochStore creates the record at floor 0 when
	// it is merely absent.
	es, err := signing.NewEpochStore(cfg.epochPath)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrEpochFloorUnavailable, cfg.epochPath, err)
	}

	// 2. The release key, the offline root's statement about it, and the floor
	//    that statement leaves in force.
	releasePub, floor, err := s.certifiedReleaseKey(squashfsPath, cfg, anchor, es)
	if err != nil {
		return err
	}

	// 3. The signed manifest.  Mandatory: it carries the fields the release key
	//    signed, so without it the ImagePayload cannot even be reconstructed.
	//    ParseAndVerify re-derives the canonical bytes, checks the release-key
	//    signature over them, and enforces the epoch floor.
	manifestPath, manifestSigPath := squashfsManifestPaths(squashfsPath)
	manifestData, mErr := os.ReadFile(manifestPath)
	manifestSigData, msErr := os.ReadFile(manifestSigPath)
	if mErr != nil || msErr != nil {
		return fmt.Errorf("%w (looked for %s and %s)", ErrManifestMissing, manifestPath, manifestSigPath)
	}
	detachedManifestSig, err := signing.ParseSig(manifestSigData)
	if err != nil {
		return fmt.Errorf("netboot-verify: parse manifest .sig: %w", err)
	}
	if _, err := osdist.ParseAndVerify(manifestData, detachedManifestSig.SigBytes, releasePub, floor); err != nil {
		// ParseAndVerify returns ErrBadSignature / ErrEpochTooLow / ErrMalformed.
		return fmt.Errorf("netboot-verify: manifest: %w", err)
	}

	// 4. The image signature, over the canonical JSON of the ImagePayload the
	//    manifest describes.  The payload is unmarshalled STRAIGHT FROM THE
	//    MANIFEST BYTES, not re-encoded from a parsed manifest: canonical() must
	//    reproduce the signer's bytes exactly, and round-tripping released_at
	//    through time.Time would not.  cmd/init does the same for the same
	//    reason.
	var payload verify.ImagePayload
	if err := json.Unmarshal(manifestData, &payload); err != nil {
		return fmt.Errorf("netboot-verify: manifest does not describe an image payload: %w", err)
	}
	if payload.RootHash == "" || payload.Path == "" {
		return fmt.Errorf("%w: manifest is missing roothash/path", ErrManifestMissing)
	}

	sigPath := squashfsSigPath(squashfsPath)
	sigData, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSquashfsSigMissing, err)
	}
	detached, err := signing.ParseSig(sigData)
	if err != nil {
		return fmt.Errorf("netboot-verify: parse squashfs .sig: %w", err)
	}
	canonical, err := signing.Canonical(payload)
	if err != nil {
		return fmt.Errorf("netboot-verify: canonical image payload: %w", err)
	}
	if !signing.Verify(releasePub, canonical, detached.SigBytes) {
		return ErrSquashfsBadSignature
	}

	// 5. Bind the signed description to the bytes on this medium.
	return bindPayloadToImage(payload, squashfsPath, verityRootHash)
}

// certifiedReleaseKey resolves the release public key the payload was signed
// with, refuses to return one the pinned anchor has not certified, and returns
// the epoch floor in force after this cert has been taken into account.
//
// The certificate is taken from beside the image when the medium ships one, and
// from the seed otherwise.  Preferring the medium's copy is safe — it is inert
// until ValidateReleaseCert accepts it against the pinned anchor — and it is
// what allows a release key to be rotated without re-flashing every seed.
//
// The floor is READ before the cert is validated and RAISED after, so a cert
// carrying a min_epoch below the floor is REFUSED by this function rather than
// quietly accepted (RaiseTo is monotonic, so it would not lower the floor — but
// nothing would have refused the retired key either, and refusing is the whole
// point).  Only this root-signed cert may move the floor; see
// signing.EpochStore.RaiseFromReleaseCert for why the release-key-signed
// manifest must not.
func (s *Service) certifiedReleaseKey(
	squashfsPath string,
	cfg netbootVerifyConfig,
	anchor []byte,
	es *signing.EpochStore,
) ([]byte, int64, error) {
	floor := es.Current()

	certPath := siblingPath(squashfsPath, releaseCertSiblingName)
	if !isRegularFile(certPath) {
		certPath = cfg.certPath
	}
	certData, err := os.ReadFile(certPath)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrReleaseCertMissing, err)
	}
	cert, err := signing.ParseReleaseCert(certData)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: parse %s: %v", ErrReleaseCertInvalid, certPath, err)
	}
	if err := signing.ValidateReleaseCert(anchor, cert); err != nil {
		return nil, 0, fmt.Errorf("%w: %s does not chain to the pinned anchor: %v", ErrReleaseCertInvalid, certPath, err)
	}
	// Downgrade defence at the key level: a certificate issued before the
	// device's floor was raised must not resurrect a retired release key.
	if cert.MinEpoch < floor {
		return nil, 0, fmt.Errorf("%w: cert min_epoch %d is below the device epoch floor %d",
			ErrReleaseCertInvalid, cert.MinEpoch, floor)
	}
	releasePub, err := cert.DecodePubKey()
	if err != nil {
		return nil, 0, fmt.Errorf("%w: decode release pubkey: %v", ErrReleaseCertInvalid, err)
	}

	// Remember what the root has retired.  Fail-closed on a persistence failure:
	// installing an image while silently failing to record the revocation that
	// authorised it is the failure mode this mechanism exists to prevent, and
	// the medium's next cert would be measured against a floor that never moved.
	if err := es.RaiseFromReleaseCert(anchor, cert); err != nil {
		return nil, 0, fmt.Errorf("%w: could not record the root-signed epoch floor: %v",
			ErrEpochFloorUnavailable, err)
	}
	if cert.MinEpoch > floor {
		floor = cert.MinEpoch
	}
	return releasePub, floor, nil
}

// bindPayloadToImage is the step that makes the signature mean something about
// the image rather than about a description of one.
//
// The dm-verity root hash is the strong half: verityRootHash was produced by
// `veritysetup verify` against these exact bytes (netboot_verity.go), so an
// equal root hash means the signed payload names this image and no other.  Size
// is checked too — it is weak on its own but free, and it catches a truncated
// download before the verity tree would.
func bindPayloadToImage(payload verify.ImagePayload, squashfsPath, verityRootHash string) error {
	if strings.TrimSpace(verityRootHash) == "" {
		return fmt.Errorf("%w (signed payload names roothash %s, but nothing on this medium proves the image has it)",
			ErrVerityRootHashUnknown, payload.RootHash)
	}
	want := strings.ToLower(strings.TrimSpace(payload.RootHash))
	got := strings.ToLower(strings.TrimSpace(verityRootHash))
	if want != got {
		return fmt.Errorf("%w: signed roothash %s, image roothash %s", ErrSquashfsHashMismatch, want, got)
	}

	st, err := os.Stat(squashfsPath)
	if err != nil {
		return fmt.Errorf("netboot-verify: stat image: %w", err)
	}
	if payload.Size != 0 && st.Size() != payload.Size {
		return fmt.Errorf("%w: signed size %d, image size %d", ErrSquashfsHashMismatch, payload.Size, st.Size())
	}
	return nil
}

// siblingPath returns name resolved in the same directory as path.
func siblingPath(path, name string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i+1] + name
	}
	return name
}
