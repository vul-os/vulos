// Command vulos-verify-sig is the tiny fail-closed verifier the initramfs calls
// to authenticate a raw file against the PINNED trust anchor before trusting it.
//
// It exists so scripts/initramfs/vulos-live can bind the dm-verity root hash
// (os-core.roothash) to the baked Ed25519 trust anchor.  Without this, a
// netboot payload's roothash is unauthenticated and an attacker who controls
// the (plain-HTTP) transport can substitute image + roothash together and
// defeat dm-verity — the exact MITM-substitutes-payload threat (THREAT-MODEL #3).
//
// Usage:
//
//	vulos-verify-sig <anchor.pub> <file> <file.sig>
//
// Exit codes:
//
//	0  signature verifies against the pinned anchor
//	1  ANY failure (missing/invalid anchor, missing/invalid sig, mismatch)
//
// The command NEVER exits 0 on doubt — callers treat a non-zero exit as a hard
// boot halt.  There is no CA-bundle fallback: the pinned anchor is the sole gate.
package main

import (
	"fmt"
	"os"

	"vulos/backend/services/signing"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: vulos-verify-sig <anchor.pub> <file> <file.sig>")
		os.Exit(2)
	}
	anchorPath, filePath, sigPath := os.Args[1], os.Args[2], os.Args[3]

	// Load the PINNED anchor.  Missing/malformed ⇒ fail closed.
	anchor, err := signing.LoadAnchor(anchorPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vulos-verify-sig: load anchor: %v\n", err)
		os.Exit(1)
	}

	payload, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vulos-verify-sig: read file: %v\n", err)
		os.Exit(1)
	}

	sigData, err := os.ReadFile(sigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vulos-verify-sig: read sig: %v\n", err)
		os.Exit(1)
	}

	parsed, err := signing.ParseSig(sigData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vulos-verify-sig: parse sig: %v\n", err)
		os.Exit(1)
	}

	if !signing.Verify(anchor, payload, parsed.SigBytes) {
		fmt.Fprintf(os.Stderr, "vulos-verify-sig: signature INVALID for %q against pinned anchor\n", filePath)
		os.Exit(1)
	}

	// Success — silent, exit 0.
	os.Exit(0)
}
