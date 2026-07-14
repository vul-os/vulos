// registry.go — `vulos-sign sign-registry` / `verify-registry`.
//
// The App Hub registry (registry.json) is the third artifact class in the Vulos
// signing chain, alongside OS images and stable.json manifests:
//
//	offline ROOT key ──(issue-release-cert)──▶ release cert ──▶ RELEASE key
//	                                                              │
//	                        ┌─────────────────────────────────────┤
//	                        ▼                     ▼               ▼
//	                  os-core.squashfs.sig   stable.json.sig   registry.json
//	                                                           (per-entry sigs)
//
// sign-registry is a RELEASE-key operation: it runs on the maintainer's signing
// machine, never in CI.  verify-registry is the public half — it needs only the
// anchor and the cert, so CI runs it on every push to prove that every entry the
// repo ships is signed by the key the shipped anchor vouches for.
//
// See docs/KEY-CEREMONY.md.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"sort"

	"vulos/backend/services/appnet"
	"vulos/backend/services/signing"
)

// encodeB64 renders a public key in the same base64 form as trust-anchor.pub
// (without the trailing newline signing.EncodeAnchor adds for the file itself).
func encodeB64(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// ─── export-anchor ────────────────────────────────────────────────────────────

// cmdExportAnchor converts a public-key JSON file (hex) into the trust-anchor
// wire format (single-line base64) that signing.LoadAnchor reads and that
// scripts/seed/embed-anchor.sh bakes into the image.
//
// It exists so the ceremony needs nothing but this binary — no openssl, no jq,
// no hand-rolled base64 pipeline that could silently emit the wrong 32 bytes.
func cmdExportAnchor(args []string) {
	fs := flag.NewFlagSet("export-anchor", flag.ExitOnError)
	pubPath := fs.String("pub", "", "path to the ROOT public-key JSON file")
	outPath := fs.String("out", "trust-anchor.pub", "output path for the base64 anchor file")
	_ = fs.Parse(args)

	if *pubPath == "" {
		fatalf(1, "export-anchor: -pub is required")
	}
	pub, err := readPublicKey(*pubPath)
	if err != nil {
		fatalf(2, "export-anchor: %v", err)
	}
	mustWriteFile(*outPath, []byte(signing.EncodeAnchor(pub)), 0644)

	// Prove the file we just wrote parses back to the same key — an anchor that
	// LoadAnchor rejects would halt boot on every device that receives it.
	got, err := signing.LoadAnchor(*outPath)
	if err != nil {
		fatalf(2, "export-anchor: wrote %s but it does not parse: %v", *outPath, err)
	}
	if !got.Equal(ed25519.PublicKey(pub)) {
		fatalf(2, "export-anchor: round-trip mismatch writing %s", *outPath)
	}

	fmt.Printf("export-anchor: wrote %s\n", *outPath)
	fmt.Printf("  pubkey (base64): %s\n", encodeB64(pub))
}

// ─── sign-registry ────────────────────────────────────────────────────────────

func cmdSignRegistry(args []string) {
	fs := flag.NewFlagSet("sign-registry", flag.ExitOnError)
	releasePrivPath := fs.String("release-priv", "", "path to the release private-key JSON file")
	registryPath := fs.String("registry", "registry.json", "path to registry.json (rewritten in place)")
	_ = fs.Parse(args)

	if *releasePrivPath == "" {
		fatalf(1, "sign-registry: -release-priv is required")
	}

	privBytes, err := readPrivateKey(*releasePrivPath)
	if err != nil {
		fatalf(2, "sign-registry: read release private key: %v", err)
	}
	releasePriv := ed25519.PrivateKey(privBytes)

	reg, err := appnet.LoadRegistry(*registryPath)
	if err != nil {
		fatalf(2, "sign-registry: load %s: %v", *registryPath, err)
	}
	if len(reg.Apps) == 0 {
		fatalf(2, "sign-registry: %s contains no apps — refusing to write an empty registry", *registryPath)
	}

	for _, appID := range sortedAppIDs(reg) {
		if err := appnet.SignEntry(reg.Apps[appID], appID, releasePriv); err != nil {
			fatalf(2, "sign-registry: sign entry %q: %v", appID, err)
		}
	}

	if err := appnet.SaveRegistry(*registryPath, reg); err != nil {
		fatalf(2, "sign-registry: write %s: %v", *registryPath, err)
	}

	// Signing is worthless if we cannot prove the result verifies. Re-read from
	// disk and check every signature against the public half of the key we just
	// signed with — so a serialisation bug can never ship a registry that the
	// box will reject at install time.
	releasePub, ok := releasePriv.Public().(ed25519.PublicKey)
	if !ok {
		fatalf(2, "sign-registry: release key is not Ed25519")
	}
	n, err := verifyRegistryFile(*registryPath, releasePub)
	if err != nil {
		fatalf(2, "sign-registry: post-write verification FAILED: %v", err)
	}

	fmt.Printf("sign-registry: signed and verified %d entries in %s\n", n, *registryPath)
	fmt.Printf("  release pubkey (base64): %s\n", encodeB64(releasePub))
}

// ─── verify-registry ──────────────────────────────────────────────────────────

func cmdVerifyRegistry(args []string) {
	fs := flag.NewFlagSet("verify-registry", flag.ExitOnError)
	anchorPath := fs.String("anchor", signing.DefaultAnchorPath, "path to the trust-anchor public key (root)")
	certPath := fs.String("cert", "", "path to the root-signed release cert (omit for the single-key model)")
	registryPath := fs.String("registry", "registry.json", "path to registry.json")
	requireProd := fs.Bool("require-prod-keys", false,
		"fail if the trust material is the well-known DEV keypair (use in release builds)")
	_ = fs.Parse(args)

	anchor, err := signing.LoadAnchor(*anchorPath)
	if err != nil {
		fatalf(2, "verify-registry: %v", err)
	}

	// The key entries must be signed by: the release key if a cert chains it to
	// the anchor, otherwise the anchor itself.
	verifyKey := anchor
	keyDesc := fmt.Sprintf("trust anchor %s", *anchorPath)
	if *certPath != "" {
		cert, err := signing.LoadReleaseCert(*certPath)
		if err != nil {
			fatalf(2, "verify-registry: %v", err)
		}
		releasePub, err := signing.ReleaseKeyFromCert(anchor, cert)
		if err != nil {
			fatalf(2, "verify-registry: release cert %s does not chain to anchor %s: %v",
				*certPath, *anchorPath, err)
		}
		verifyKey = releasePub
		keyDesc = fmt.Sprintf("release key %q (cert %s, expires %s)", cert.KeyID, *certPath, cert.NotAfter)
	}

	n, err := verifyRegistryFile(*registryPath, verifyKey)
	if err != nil {
		fatalf(2, "verify-registry: %v", err)
	}

	fmt.Printf("verify-registry: OK — all %d entries in %s are signed by %s\n", n, *registryPath, keyDesc)

	if err := releaseKeyGate(verifyKey, *requireProd, *registryPath); err != nil {
		fatalf(2, "verify-registry: %v", err)
	}
	if signing.IsDevKey(verifyKey) {
		fmt.Fprintf(os.Stderr,
			"verify-registry: NOTE: this is the DEVELOPMENT key. A production box (VULOS_ENV=prod)\n"+
				"  will refuse it. Run the ceremony in docs/KEY-CEREMONY.md before shipping.\n")
	}
}

// releaseKeyGate refuses a dev-signed registry when the caller demands
// production keys (-require-prod-keys, used by the release workflow).
//
// A dev-signed registry verifies perfectly well — that is the point of the
// checked-in dev keypair. It just must never be what we SHIP: the dev private key
// is derived from a published seed, so anyone can sign an app for it. The runtime
// already refuses these keys in prod, which means a dev-signed image would boot
// with the App Hub dead. This gate stops us cutting that image at all, the same
// way netboot halts without os-core.roothash.sig: no founder signature, no release.
func releaseKeyGate(verifyKey ed25519.PublicKey, requireProd bool, registryPath string) error {
	if !requireProd || !signing.IsDevKey(verifyKey) {
		return nil
	}
	return fmt.Errorf("REFUSING TO RELEASE — %s is signed by the DEVELOPMENT key, whose\n"+
		"  private half is derived from a published seed (anyone can forge app signatures with it).\n"+
		"  A production box will refuse this registry and ship with the App Hub disabled.\n"+
		"  Run the offline key ceremony (docs/KEY-CEREMONY.md), install the real trust anchor at\n"+
		"  keys/trust-anchor.pub, and re-sign with: make sign-registry RELEASE_PRIV=...",
		registryPath)
}

// ─── shared ───────────────────────────────────────────────────────────────────

// verifyRegistryFile loads registryPath and verifies every entry against pub.
// It returns the number of entries verified, and fails on the FIRST bad entry —
// an unsigned or invalid entry is a hard stop, never a warning.
func verifyRegistryFile(registryPath string, pub ed25519.PublicKey) (int, error) {
	reg, err := appnet.LoadRegistry(registryPath)
	if err != nil {
		return 0, fmt.Errorf("load %s: %w", registryPath, err)
	}
	if len(reg.Apps) == 0 {
		return 0, fmt.Errorf("%s contains no apps", registryPath)
	}
	for _, appID := range sortedAppIDs(reg) {
		if err := appnet.VerifyEntrySignature(reg.Apps[appID], appID, pub); err != nil {
			return 0, fmt.Errorf("entry %q: %w", appID, err)
		}
	}
	return len(reg.Apps), nil
}

// sortedAppIDs returns the registry's app IDs in a stable order so that output
// and signing order are reproducible.
func sortedAppIDs(reg *appnet.Registry) []string {
	ids := make([]string, 0, len(reg.Apps))
	for id := range reg.Apps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
