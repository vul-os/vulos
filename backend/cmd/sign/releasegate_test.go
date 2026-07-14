package main

// releasegate_test.go — REGISTRY-SIGN-03: `verify-registry -require-prod-keys`
// is the build-halt. The repo ships a DEV keypair so a fresh clone verifies real
// signatures with no flags; the danger is shipping it. A box refuses dev keys at
// runtime, but by then the image is already in someone's hands — so the release
// build refuses to produce it, the way netboot halts without os-core.roothash.sig.

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"vulos/backend/services/signing"
)

// TestReleaseGate_RefusesDevAnchorKey — the release must not ship a registry
// signed by the published-seed dev ROOT key.
func TestReleaseGate_RefusesDevAnchorKey(t *testing.T) {
	devRoot, _ := signing.DeriveDevKey(signing.DevRootSeed)

	err := releaseKeyGate(devRoot, true, "registry.json")
	if err == nil {
		t.Fatal("release gate PASSED a registry signed by the dev root key")
	}
	if !strings.Contains(err.Error(), "REFUSING TO RELEASE") {
		t.Errorf("refusal should be unmissable, got: %v", err)
	}
	if !strings.Contains(err.Error(), "KEY-CEREMONY") {
		t.Errorf("refusal should tell the founder how to fix it, got: %v", err)
	}
}

// TestReleaseGate_RefusesDevReleaseKey — and not just the root: the dev RELEASE
// key is what actually signs the committed entries, so it is the one most likely
// to slip through.
func TestReleaseGate_RefusesDevReleaseKey(t *testing.T) {
	devRelease, _ := signing.DeriveDevKey(signing.DevReleaseSeed)

	if err := releaseKeyGate(devRelease, true, "registry.json"); err == nil {
		t.Fatal("release gate PASSED a registry signed by the dev release key")
	}
}

// TestReleaseGate_AllowsRealKey — a key from a real ceremony releases cleanly.
// Without this the gate could "pass" by refusing everything.
func TestReleaseGate_AllowsRealKey(t *testing.T) {
	realKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	if err := releaseKeyGate(realKey, true, "registry.json"); err != nil {
		t.Fatalf("release gate refused a real (non-dev) production key: %v", err)
	}
}

// TestReleaseGate_DevKeyAllowedWhenNotReleasing — `make verify-registry` (no
// -require-prod-keys) must still work on a dev clone, or every contributor is
// blocked. The gate is about releases, not about local development.
func TestReleaseGate_DevKeyAllowedWhenNotReleasing(t *testing.T) {
	devRelease, _ := signing.DeriveDevKey(signing.DevReleaseSeed)

	if err := releaseKeyGate(devRelease, false, "registry.json"); err != nil {
		t.Fatalf("plain verify-registry refused the dev key on a dev clone: %v", err)
	}
}
