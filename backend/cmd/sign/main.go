// vulos-sign — offline PKI tooling for the Vulos two-level signing chain.
//
// # Architecture summary
//
// Vulos uses a root-signs-intermediate PKI (see roadmap/SIGNING.md):
//
//	Offline ROOT key  (air-gapped / HSM — NEVER brought online for routine releases)
//	   │  signs (offline, occasionally)
//	   ▼
//	RELEASE-key certificate  (fetched by device alongside stable.json)
//	   │  signs (per release)
//	   ▼
//	os-core.squashfs.sig  /  stable.json.sig
//
// The root public key is baked into the OS image as the trust anchor (SEED-TRUST.md).
// Devices validate the root-signed release cert before trusting any image the
// release key signed.  The release key is the only key used online.
//
// # Security model
//
//   - The ROOT private key file must live on an air-gapped machine or inside an HSM.
//     It is read by this tool only when issuing a new release cert.
//     Never copy a root private key to an internet-connected machine.
//   - The RELEASE private key is used for routine per-release signing.
//     Compromise is recoverable: issue a new release cert from the root key and
//     bump min_epoch in the manifest.
//   - All key files written by gen-key are JSON with hex-encoded keys.
//     They carry no passphrase protection — OS-level permissions or HSM custody
//     are the expected protection mechanisms.
//
// # Subcommands
//
//	gen-key               Generate an Ed25519 keypair and write it to disk.
//	                      Use for the offline root key and the online release key.
//	issue-release-cert    Offline root operation: root key signs a release cert.
//	sign-image            Sign an OS image payload with the release key.
//	sign-manifest         Sign a stable.json manifest payload with the release key.
//	export-anchor         Write a ROOT public key out as trust-anchor.pub.
//	sign-registry         Sign every registry.json entry with the release key.
//	verify-registry       Verify registry.json against the anchor + release cert
//	                      (public keys only — this is what CI runs).
//	publish-feed          Append a signed entry to registry-feed.json recording
//	                      this publication of registry.json (phase 1, additive —
//	                      see backend/services/appnet/feed.go).
//	verify-feed           Verify registry-feed.json's hash chain + signed head
//	                      against the anchor + release cert.
//
// # Exit codes
//
//	0  success
//	1  usage error
//	2  operation error
package main

import (
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"vulos/backend/services/osdist"
	"vulos/backend/services/signing"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "gen-key":
		cmdGenKey(os.Args[2:])
	case "issue-release-cert":
		cmdIssueReleaseCert(os.Args[2:])
	case "sign-image":
		cmdSignImage(os.Args[2:])
	case "sign-manifest":
		cmdSignManifest(os.Args[2:])
	case "export-anchor":
		cmdExportAnchor(os.Args[2:])
	case "sign-registry":
		cmdSignRegistry(os.Args[2:])
	case "verify-registry":
		cmdVerifyRegistry(os.Args[2:])
	case "publish-feed":
		cmdPublishFeed(os.Args[2:])
	case "verify-feed":
		cmdVerifyFeed(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "vulos-sign: unknown subcommand %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `vulos-sign — Vulos offline PKI tooling

SECURITY NOTE: The root private key must be kept on an air-gapped machine or
inside an HSM. It is read only when issuing a new release cert. Never copy the
root private key to an internet-connected system.

Subcommands:

  gen-key
      Generate an Ed25519 keypair and write two files to disk:
        -out-priv  path for the private-key JSON file (default: key.priv.json)
        -out-pub   path for the public-key JSON file  (default: key.pub.json)
        -dev-seed  DEV ONLY: derive the key deterministically from a published
                   seed string instead of the CSPRNG.  The resulting key is NOT
                   secret — anyone with the seed can reproduce it.  Used only to
                   regenerate the repo's checked-in dev keys (make dev-keys).
      The private key file must be protected by OS permissions or HSM custody.
      Label the output clearly (e.g. root.priv.json vs release.priv.json).

  export-anchor
      Convert a ROOT public-key JSON file into the single-line base64
      trust-anchor.pub format that the OS image bakes in.
        -pub  path to the ROOT public-key JSON file
        -out  output path (default: trust-anchor.pub)

  sign-registry  [RELEASE-KEY OPERATION]
      Sign every entry in registry.json with the release key and rewrite the
      file in place, then re-verify the result before exiting.
        -release-priv  path to release private-key JSON file
        -registry      path to registry.json (default: registry.json)

  verify-registry
      Verify every entry in registry.json against the trust anchor (and, if
      given, the root-signed release cert that authorises the signing key).
      Needs no private key — this is the check CI runs.
        -anchor    path to trust-anchor public key (default: `+signing.DefaultAnchorPath+`)
        -cert      path to release cert JSON (omit for the single-key model)
        -registry  path to registry.json (default: registry.json)

  publish-feed  [RELEASE-KEY OPERATION]
      Append a signed entry to registry-feed.json recording this publication
      of registry.json (anti-rollback distribution, phase 1 — additive, does
      not change registry.json or install-time verification). A no-op if
      registry.json is unchanged since the last publish.
        -release-priv  path to release private-key JSON file
        -registry      path to registry.json (default: registry.json)
        -feed          path to registry-feed.json (default: registry-feed.json)

  verify-feed
      Verify registry-feed.json's hash chain and signed head against the
      trust anchor (and, if given, the release cert). Needs no private key.
        -anchor  path to trust-anchor public key (default: `+signing.DefaultAnchorPath+`)
        -cert    path to release cert JSON (omit for the single-key model)
        -feed    path to registry-feed.json (default: registry-feed.json)

  issue-release-cert  [OFFLINE ROOT OPERATION]
      Root key signs a release-key certificate.
      Must be run on the air-gapped machine that holds the root private key.
        -root-priv    path to root private-key JSON file
        -release-pub  path to release public-key JSON file
        -key-id       human-readable key label (e.g. "release-2026-05")
        -not-after    certificate expiry in RFC 3339 (e.g. 2027-01-01T00:00:00Z)
        -min-epoch    minimum trusted epoch (non-negative integer)
        -out          path to write the release-cert JSON file (default: release.cert.json)

  sign-image
      Sign an OS image payload with the release key.
        -release-priv  path to release private-key JSON file
        -key-id        key label to embed in the .sig file
        -path          artifact path (e.g. os/v08/os-core.squashfs)
        -roothash      dm-verity root hash (hex)
        -size          image file size in bytes
        -min-epoch     epoch to embed in the signed payload
        -released-at   release timestamp RFC 3339 (default: now)
        -out           path to write the .sig file (default: image.sig)

  sign-manifest
      Sign a stable.json manifest with the release key.
        -release-priv  path to release private-key JSON file
        -key-id        key label to embed in the .sig file
        -channel       distribution channel (e.g. stable)
        -latest        version string (e.g. v08)
        -path          artifact path in the bucket
        -roothash      dm-verity root hash (hex)
        -size          image file size in bytes
        -min-epoch     epoch to embed in the signed manifest
        -released-at   release timestamp RFC 3339 (default: now)
        -security      mark this release as fixing a security defect. Raises the
                       owner's OS-update banner and fires ONE priority
                       notification per new version. Requires -severity.
        -severity      `+strings.Join(osdist.Severities, "|")+` — required with
                       -security, and refused without it.
        -notes         short single-line release note shown to the box owner in
                       Settings → OS Update, and used as the security
                       notification's body. At most `+strconv.Itoa(osdist.MaxNotesBytes)+` bytes, printable
                       text only, and NO LINKS ("://" / "www."): the only place
                       an update note may send the owner is the button the box
                       draws itself.
        -out           path to write the .sig file (default: manifest.sig)

      -security/-severity/-notes are INSIDE the signed surface. Omitting all
      three produces the byte-identical seven-key document earlier releases
      signed, so every signature already issued keeps verifying.

`)
}

// ─── gen-key ─────────────────────────────────────────────────────────────────

func cmdGenKey(args []string) {
	fs := flag.NewFlagSet("gen-key", flag.ExitOnError)
	outPriv := fs.String("out-priv", "key.priv.json", "output path for private-key JSON file")
	outPub := fs.String("out-pub", "key.pub.json", "output path for public-key JSON file")
	devSeed := fs.String("dev-seed", "", "DEV ONLY: derive the key from this published seed instead of the CSPRNG")
	_ = fs.Parse(args)

	var (
		pub  ed25519.PublicKey
		priv ed25519.PrivateKey
		err  error
	)
	if *devSeed != "" {
		// Deterministic, reproducible, and therefore NOT secret. This exists so
		// that a fresh clone can regenerate the repo's dev keys and verify the
		// committed registry signatures offline. signing.RefuseDevKeyInProd
		// makes sure the result can never be trusted by a production box.
		pub, priv = signing.DeriveDevKey(*devSeed)
		fmt.Fprintf(os.Stderr,
			"gen-key: WARNING: -dev-seed %q derives a NON-SECRET key. Never use it for a real release.\n",
			*devSeed)
	} else if pub, priv, err = generateKey(); err != nil {
		fatalf(2, "gen-key: generate: %v", err)
	}

	privData, err := MarshalPrivateKey(priv)
	if err != nil {
		fatalf(2, "gen-key: marshal private key: %v", err)
	}
	pubData, err := MarshalPublicKey(pub)
	if err != nil {
		fatalf(2, "gen-key: marshal public key: %v", err)
	}

	mustWriteFile(*outPriv, privData, 0600)
	mustWriteFile(*outPub, pubData, 0644)

	fmt.Printf("gen-key: wrote private key to %s\n", *outPriv)
	fmt.Printf("gen-key: wrote public key  to %s\n", *outPub)
	fmt.Println()
	fmt.Println("IMPORTANT: Protect the private key file using OS permissions or HSM custody.")
	fmt.Println("If this is a root key, store it on an air-gapped machine and never copy it online.")
}

// ─── issue-release-cert ───────────────────────────────────────────────────────

func cmdIssueReleaseCert(args []string) {
	fs := flag.NewFlagSet("issue-release-cert", flag.ExitOnError)
	rootPrivPath := fs.String("root-priv", "", "path to root private-key JSON file [OFFLINE ROOT OPERATION]")
	releasePubPath := fs.String("release-pub", "", "path to release public-key JSON file")
	keyID := fs.String("key-id", "", "human-readable key label (e.g. release-2026-05)")
	notAfterStr := fs.String("not-after", "", "certificate expiry RFC 3339 (e.g. 2027-01-01T00:00:00Z)")
	minEpochStr := fs.String("min-epoch", "0", "minimum trusted epoch (non-negative integer)")
	outPath := fs.String("out", "release.cert.json", "output path for the release-cert JSON file")
	_ = fs.Parse(args)

	if *rootPrivPath == "" {
		fatalf(1, "issue-release-cert: -root-priv is required")
	}
	if *releasePubPath == "" {
		fatalf(1, "issue-release-cert: -release-pub is required")
	}
	if *keyID == "" {
		fatalf(1, "issue-release-cert: -key-id is required")
	}
	if *notAfterStr == "" {
		fatalf(1, "issue-release-cert: -not-after is required")
	}

	notAfter, err := time.Parse(time.RFC3339, *notAfterStr)
	if err != nil {
		fatalf(1, "issue-release-cert: invalid -not-after %q: %v", *notAfterStr, err)
	}
	minEpoch, err := strconv.ParseInt(*minEpochStr, 10, 64)
	if err != nil || minEpoch < 0 {
		fatalf(1, "issue-release-cert: invalid -min-epoch %q: must be a non-negative integer", *minEpochStr)
	}

	rootPriv, err := readPrivateKey(*rootPrivPath)
	if err != nil {
		fatalf(2, "issue-release-cert: read root private key: %v", err)
	}
	releasePub, err := readPublicKey(*releasePubPath)
	if err != nil {
		fatalf(2, "issue-release-cert: read release public key: %v", err)
	}

	cert, err := IssueReleaseCert(rootPriv, releasePub, *keyID, notAfter, minEpoch)
	if err != nil {
		fatalf(2, "issue-release-cert: %v", err)
	}

	data, err := json.MarshalIndent(cert, "", "  ")
	if err != nil {
		fatalf(2, "issue-release-cert: marshal cert: %v", err)
	}
	mustWriteFile(*outPath, append(data, '\n'), 0644)

	fmt.Printf("issue-release-cert: wrote release cert to %s\n", *outPath)
	fmt.Printf("  key_id:     %s\n", cert.KeyID)
	fmt.Printf("  not_after:  %s\n", cert.NotAfter)
	fmt.Printf("  min_epoch:  %d\n", cert.MinEpoch)
}

// ─── sign-image ───────────────────────────────────────────────────────────────

func cmdSignImage(args []string) {
	fs := flag.NewFlagSet("sign-image", flag.ExitOnError)
	releasePrivPath := fs.String("release-priv", "", "path to release private-key JSON file")
	keyID := fs.String("key-id", "", "key label to embed in the .sig file")
	path := fs.String("path", "", "artifact path (e.g. os/v08/os-core.squashfs)")
	roothash := fs.String("roothash", "", "dm-verity root hash (hex)")
	sizeStr := fs.String("size", "0", "image file size in bytes")
	minEpochStr := fs.String("min-epoch", "0", "epoch to embed in the signed payload")
	releasedAt := fs.String("released-at", "", "release timestamp RFC 3339 (default: now)")
	outPath := fs.String("out", "image.sig", "output path for the .sig file")
	_ = fs.Parse(args)

	if *releasePrivPath == "" {
		fatalf(1, "sign-image: -release-priv is required")
	}
	if *keyID == "" {
		fatalf(1, "sign-image: -key-id is required")
	}
	if *path == "" {
		fatalf(1, "sign-image: -path is required")
	}
	if *roothash == "" {
		fatalf(1, "sign-image: -roothash is required")
	}

	size, err := strconv.ParseInt(*sizeStr, 10, 64)
	if err != nil {
		fatalf(1, "sign-image: invalid -size %q: %v", *sizeStr, err)
	}
	minEpoch, err := strconv.ParseInt(*minEpochStr, 10, 64)
	if err != nil || minEpoch < 0 {
		fatalf(1, "sign-image: invalid -min-epoch %q: must be a non-negative integer", *minEpochStr)
	}
	ts := *releasedAt
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339)
	} else if _, err := time.Parse(time.RFC3339, ts); err != nil {
		fatalf(1, "sign-image: invalid -released-at %q: %v", ts, err)
	}

	releasePriv, err := readPrivateKey(*releasePrivPath)
	if err != nil {
		fatalf(2, "sign-image: read release private key: %v", err)
	}

	payload := ImagePayload{
		Path:       *path,
		RootHash:   *roothash,
		Size:       size,
		MinEpoch:   minEpoch,
		ReleasedAt: ts,
	}

	sig, err := SignImage(releasePriv, *keyID, payload)
	if err != nil {
		fatalf(2, "sign-image: %v", err)
	}

	data, err := signing.MarshalSig(sig)
	if err != nil {
		fatalf(2, "sign-image: marshal sig: %v", err)
	}
	mustWriteFile(*outPath, data, 0644)

	fmt.Printf("sign-image: wrote .sig to %s\n", *outPath)
	fmt.Printf("  key-id: %s\n", sig.KeyID)
	fmt.Printf("  path:   %s\n", payload.Path)
}

// ─── sign-manifest ────────────────────────────────────────────────────────────

func cmdSignManifest(args []string) {
	fs := flag.NewFlagSet("sign-manifest", flag.ExitOnError)
	releasePrivPath := fs.String("release-priv", "", "path to release private-key JSON file")
	keyID := fs.String("key-id", "", "key label to embed in the .sig file")
	channel := fs.String("channel", "stable", "distribution channel")
	latest := fs.String("latest", "", "version string (e.g. v08)")
	path := fs.String("path", "", "artifact path in the bucket")
	roothash := fs.String("roothash", "", "dm-verity root hash (hex)")
	sizeStr := fs.String("size", "0", "image file size in bytes")
	minEpochStr := fs.String("min-epoch", "0", "epoch to embed in the signed manifest")
	releasedAt := fs.String("released-at", "", "release timestamp RFC 3339 (default: now)")
	isSecurity := fs.Bool("security", false, "this release fixes a security defect (requires -severity)")
	severity := fs.String("severity", "", "severity of the fixed defect: "+strings.Join(osdist.Severities, "|"))
	notes := fs.String("notes", "", "short single-line link-free release note shown to the box owner")
	outPath := fs.String("out", "manifest.sig", "output path for the .sig file")
	_ = fs.Parse(args)

	if *releasePrivPath == "" {
		fatalf(1, "sign-manifest: -release-priv is required")
	}
	if *keyID == "" {
		fatalf(1, "sign-manifest: -key-id is required")
	}
	if *latest == "" {
		fatalf(1, "sign-manifest: -latest is required")
	}
	if *path == "" {
		fatalf(1, "sign-manifest: -path is required")
	}
	if *roothash == "" {
		fatalf(1, "sign-manifest: -roothash is required")
	}

	size, err := strconv.ParseInt(*sizeStr, 10, 64)
	if err != nil {
		fatalf(1, "sign-manifest: invalid -size %q: %v", *sizeStr, err)
	}
	minEpoch, err := strconv.ParseInt(*minEpochStr, 10, 64)
	if err != nil || minEpoch < 0 {
		fatalf(1, "sign-manifest: invalid -min-epoch %q: must be a non-negative integer", *minEpochStr)
	}
	ts := *releasedAt
	if ts == "" {
		ts = time.Now().UTC().Format(time.RFC3339)
	} else if _, err := time.Parse(time.RFC3339, ts); err != nil {
		fatalf(1, "sign-manifest: invalid -released-at %q: %v", ts, err)
	}

	// The release-severity surface is checked HERE, before the key file is even
	// read, so a bad -severity/-notes is a usage error (exit 1) at the ceremony
	// rather than an artifact that every box in the field refuses.
	// SignManifest re-checks it — this is the friendly message, that is the gate.
	if err := osdist.ValidateSecuritySurface(*isSecurity, *severity, *notes); err != nil {
		fatalf(1, "sign-manifest: %v", err)
	}

	releasePriv, err := readPrivateKey(*releasePrivPath)
	if err != nil {
		fatalf(2, "sign-manifest: read release private key: %v", err)
	}

	payload := ManifestPayload{
		Channel:    *channel,
		Latest:     *latest,
		MinEpoch:   minEpoch,
		Path:       *path,
		ReleasedAt: ts,
		RootHash:   *roothash,
		Size:       size,
		IsSecurity: *isSecurity,
		Severity:   *severity,
		Notes:      *notes,
	}

	sig, err := SignManifest(releasePriv, *keyID, payload)
	if err != nil {
		fatalf(2, "sign-manifest: %v", err)
	}

	data, err := signing.MarshalSig(sig)
	if err != nil {
		fatalf(2, "sign-manifest: marshal sig: %v", err)
	}
	mustWriteFile(*outPath, data, 0644)

	fmt.Printf("sign-manifest: wrote .sig to %s\n", *outPath)
	fmt.Printf("  key-id:  %s\n", sig.KeyID)
	fmt.Printf("  channel: %s\n", payload.Channel)
	fmt.Printf("  latest:  %s\n", payload.Latest)
	fmt.Printf("  epoch:   %d\n", payload.MinEpoch)
	if payload.IsSecurity {
		fmt.Printf("  SECURITY RELEASE (severity %s) — boxes will notify their owner once\n", payload.Severity)
	}
	if payload.Notes != "" {
		fmt.Printf("  notes:   %s\n", payload.Notes)
	}
}

// ─── I/O helpers ─────────────────────────────────────────────────────────────

func readPrivateKey(path string) ([]byte, error) {
	// Returns ed25519.PrivateKey via ParsePrivateKey; read the raw bytes here.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	priv, err := ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return priv, nil
}

func readPublicKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	pub, err := ParsePublicKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return pub, nil
}

func mustWriteFile(path string, data []byte, perm os.FileMode) {
	if err := os.WriteFile(path, data, perm); err != nil {
		fatalf(2, "write %s: %v", path, err)
	}
}

func fatalf(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "vulos-sign: "+format+"\n", args...)
	os.Exit(code)
}
