// Command vulos-verify-sig is the fail-closed verifier the initramfs calls to
// prove that the dm-verity ROOT HASH it is about to trust came from the release
// key — and not merely that it came with the image.
//
// # Why this exists
//
// dm-verity binds an image to a root hash.  Nothing in that binds the root hash
// to anyone.  An attacker who can substitute BOTH the squashfs and the
// os-core.roothash beside it defeats dm-verity completely: the kernel dutifully
// verifies every block of the attacker's image against the attacker's hash tree
// and reports success.  The root hash is only a security statement once it is
// signed, and this is the program that checks the signature.
//
// # The chain — the one the rest of this repository already agrees on
//
//	pinned anchor  (/etc/vulos/trust-anchor.pub, baked by SEED-01)
//	  └─ release cert (root signature, expiry, epoch floor)
//	       └─ release public key
//	            └─ canonical(ImagePayload)          ← what the release key signed
//	                 └─ bound to THESE artifacts by:
//	                      payload.roothash == the bytes of the roothash file
//	                                          veritysetup is about to be handed
//	                      payload.size     == the size of the squashfs it opens
//
// This is deliberately the identical shape to the netboot installer (c1fba1b0),
// the OTA updater (7d1101af) and cmd/init's pre-pivot gate, and it is
// implemented by CALLING that code (backend/cmd/verify) rather than restating
// it.  A previous version of this command verified a RAW-BYTE signature made by
// the ANCHOR key directly.  Nothing in this repository produces such a
// signature and nothing was going to — `cmd/sign sign-image` signs
// canonical(ImagePayload) with the RELEASE key — so it was a fourth,
// unreachable verifier disagreeing with the three real ones, in exactly the way
// c1fba1b0 and 7d1101af describe.  Its own tests passed because its fixture
// fabricated the shape production cannot emit.
//
// # The bundle it reads
//
// A signature over an ImagePayload covers a NAME, a SIZE and a ROOT HASH.  It
// does not cover an image, and it cannot be checked at all without the payload
// it was made over — which is why c1fba1b0 made stable.json MANDATORY beside
// stable.json.sig.  The same rule applies here, but the initramfs has nowhere to
// put a second file: the installer stages exactly three names into a slot
// (os-core.hashtree, os-core.roothash, os-core.roothash.sig — see
// stageVerityArtifacts in backend/services/installer/netboot_verity.go), and
// that code is not ours to change.
//
// So the manifest travels INSIDE the .sig, as one additional keyed line:
//
//	vulos-sig-v1
//	algorithm: ed25519
//	key-id: release-2026-08
//	sig: <base64 Ed25519 over canonical(ImagePayload)>
//	payload: <base64 of the ImagePayload JSON document>
//
// Nothing about what a signature COVERS changes: the signed bytes are still
// canonical(ImagePayload) and nothing else.  signing.ParseSig ignores keyed
// lines it does not know, so the plain output of `cmd/sign sign-image` is a
// valid prefix of this file and every existing parser reads it unchanged.
// scripts/verity/sign-roothash.sh is the offline ceremony that produces it.
//
// The payload document itself is NOT trusted as bytes — it is parsed and
// RE-CANONICALISED before the signature is checked, so its formatting, key
// order and any unknown fields are irrelevant.  This mirrors how cmd/init
// treats /etc/vulos/stable.json.
//
// # Usage
//
//	vulos-verify-sig -anchor <trust-anchor.pub> -cert <release-cert.json> \
//	                 -roothash <os-core.roothash> -bundle <os-core.roothash.sig> \
//	                 -image <os-core.squashfs> [-epoch-floor N]
//
// Exit codes:
//
//	0  the release key signed a payload naming exactly this root hash and image
//	1  ANY failure (missing/invalid anchor, cert, bundle, payload, signature,
//	   or a payload that does not describe these artifacts)
//	2  usage error
//
// It NEVER exits 0 on doubt.  scripts/initramfs/vulos-live treats a non-zero
// exit as a hard boot halt.
//
// # What this does NOT establish
//
// Rollback.  An attacker holding an OLDER, genuinely release-signed
// (image, roothash, bundle) triple can substitute all three and this gate will
// accept it, because every one of them verifies.  That is what the epoch floor
// is for, and enforcing it needs a floor that RISES — persistent, writable
// state.  The initramfs has none: $rootmnt is mounted `ro` at this point and
// signing.NewEpochStore CREATES its file, so opening a store here would either
// fail or, worse, silently start every boot at floor 0.  -epoch-floor is the
// seam for a caller that can supply one; the moving floor is maintained on the
// OTA path (services/osdist, which has a writable root) and by cmd/init.
// Stating the limit is the point — a gate that implies more than it checks is
// how this area accumulated three disagreeing verifiers in the first place.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	verify "vulos/backend/cmd/verify"
)

// payloadField is the keyed line in the bundle that carries the base64 of the
// ImagePayload JSON document the release key signed.
const payloadField = "payload"

func main() {
	fs := flag.NewFlagSet("vulos-verify-sig", flag.ContinueOnError)
	anchorPath := fs.String("anchor", verify.DefaultAnchorPath, "pinned trust-anchor public key (SEED-01)")
	certPath := fs.String("cert", "/etc/vulos/release-cert.json", "root-signed release-key certificate")
	roothashPath := fs.String("roothash", "", "the os-core.roothash file veritysetup will be handed")
	bundlePath := fs.String("bundle", "", "the os-core.roothash.sig signed-roothash bundle")
	imagePath := fs.String("image", "", "the squashfs the root hash describes")
	epochFloor := fs.Int64("epoch-floor", 0, "minimum trusted epoch the release cert must meet")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if *roothashPath == "" || *bundlePath == "" || *imagePath == "" {
		fmt.Fprintln(os.Stderr, "usage: vulos-verify-sig -anchor <pub> -cert <cert.json> "+
			"-roothash <file> -bundle <file.sig> -image <file.squashfs> [-epoch-floor N]")
		os.Exit(2)
	}

	if err := run(*anchorPath, *certPath, *roothashPath, *bundlePath, *imagePath, *epochFloor); err != nil {
		fmt.Fprintf(os.Stderr, "vulos-verify-sig: %v\n", err)
		os.Exit(1)
	}
	// Success — silent, exit 0.
}

func run(anchorPath, certPath, roothashPath, bundlePath, imagePath string, epochFloor int64) error {
	payload, err := readBundlePayload(bundlePath)
	if err != nil {
		return err
	}

	// The root hash the initramfs is about to hand to veritysetup, read from the
	// SAME file veritysetup reads.  Reading the payload's copy instead would
	// compare a value with itself — the defect RootHashBinder exists to prevent
	// (see backend/cmd/verify/verifier.go).
	wantHash, err := readRootHashFile(roothashPath)
	if err != nil {
		return err
	}

	st, err := os.Stat(imagePath)
	if err != nil {
		return fmt.Errorf("stat image %q: %w", imagePath, err)
	}
	imageSize := st.Size()

	cfg := verify.SquashfsVerifyConfig{
		AnchorPath:         anchorPath,
		CertPath:           certPath,
		SquashfsPath:       imagePath,
		SigPath:            bundlePath,
		ImagePayloadForSig: payload,
		EpochFloor:         epochFloor,
		BindRootHash: func(signedRootHash string) error {
			if !strings.EqualFold(strings.TrimSpace(signedRootHash), wantHash) {
				return fmt.Errorf("the release key signed root hash %q, but %s holds %q",
					signedRootHash, roothashPath, wantHash)
			}
			// Size is the second half of the binding, for the same reason
			// services/osdist checks it: it is cheap, and an image of a
			// different length cannot be the one the payload describes.
			if payload.Size != imageSize {
				return fmt.Errorf("the release key signed size %d, but %s is %d bytes",
					payload.Size, imagePath, imageSize)
			}
			return nil
		},
	}
	return verify.VerifySquashfsBeforePivot(cfg)
}

// readBundlePayload extracts and parses the ImagePayload manifest carried in the
// bundle's `payload:` line.
//
// A bundle with no payload line is REFUSED rather than treated as unsigned: it
// is the plain output of `cmd/sign sign-image`, a real signature whose subject
// is simply absent, and there is no honest way to check it.  Guessing the
// payload from the local artifacts would make the signature verify against
// whatever an attacker put on the disk, which is worse than no check at all.
func readBundlePayload(bundlePath string) (verify.ImagePayload, error) {
	var zero verify.ImagePayload

	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return zero, fmt.Errorf("read bundle %q: %w", bundlePath, err)
	}

	encoded := ""
	for _, line := range strings.Split(string(data), "\n") {
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		if strings.TrimSpace(line[:colon]) == payloadField {
			encoded = strings.TrimSpace(line[colon+1:])
			break
		}
	}
	if encoded == "" {
		return zero, fmt.Errorf("bundle %q carries no %q line — it is a bare sign-image "+
			"signature with no manifest, and an ImagePayload signature cannot be checked "+
			"without the payload it was made over (see scripts/verity/sign-roothash.sh)",
			bundlePath, payloadField)
	}

	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return zero, fmt.Errorf("bundle %q: decode %s: %w", bundlePath, payloadField, err)
	}

	// Strict parsing: an unknown field means this bundle was made against a
	// payload shape this verifier does not implement, and the canonical bytes it
	// re-derives would silently omit it.  Refuse rather than verify a signature
	// over a document we only partly understand.
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var payload verify.ImagePayload
	if err := dec.Decode(&payload); err != nil {
		return zero, fmt.Errorf("bundle %q: parse %s manifest: %w", bundlePath, payloadField, err)
	}
	if payload.RootHash == "" {
		return zero, fmt.Errorf("bundle %q: manifest names no root hash", bundlePath)
	}
	return payload, nil
}

// readRootHashFile reads the hex root hash exactly as the initramfs does.
//
// The shape is asserted, not assumed.  A garbled root hash is a failure this
// project has already paid for once: an ANSI-coloured build transcript landed in
// stable.json's roothash field and the kernel panicked at boot.  Here it would
// be worse — a value that cannot match anything would look like tampering.
func readRootHashFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read roothash %q: %w", path, err)
	}
	h := strings.ToLower(strings.TrimSpace(string(raw)))
	if h == "" {
		return "", fmt.Errorf("roothash %q is empty", path)
	}
	if len(h) != 64 || strings.TrimLeft(h, "0123456789abcdef") != "" {
		return "", fmt.Errorf("roothash %q is not a 64-char lowercase hex dm-verity root hash", path)
	}
	return h, nil
}
