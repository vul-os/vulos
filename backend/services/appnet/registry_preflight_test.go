package appnet

// registry_preflight_test.go — REGISTRY-SIGN-03: the boot-time trust gate.
//
// The install-time gate (registry_prodgate_test.go) proves an unsigned app
// cannot be installed. These tests prove the box does not get as far as serving
// with verification switched off: the escape hatch is a refusal to START in
// production, not a downgrade that waits until someone clicks "Install".

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"vulos/backend/services/signing"
)

// TestPreflight_InsecureInProdIsFatal — the headline. A production box that asks
// for verification to be off must refuse to start.
func TestPreflight_InsecureInProdIsFatal(t *testing.T) {
	t.Setenv("VULOS_ENV", "prod")
	t.Setenv(envRegistryInsecure, "1")
	isolateTrustSources(t)

	pf := PreflightTrust()
	if pf.Fatal == nil {
		t.Fatal("PreflightTrust did not refuse to start with VULOS_REGISTRY_INSECURE=1 in prod")
	}
	if pf.Insecure {
		t.Fatal("preflight reports Insecure=true in prod — verification was silently disabled")
	}
	if pf.Healthy() {
		t.Fatal("preflight reports healthy with verification disabled in prod")
	}
	if !strings.Contains(pf.Fatal.Error(), envRegistryInsecure) {
		t.Errorf("fatal error should name the offending variable, got: %v", pf.Fatal)
	}
}

// TestPreflight_InsecureIsFatalWhenEnvUnset — an unset VULOS_ENV means prod, so
// the hatch is closed by default. Getting this backwards is how the flag ends up
// working on real boxes.
func TestPreflight_InsecureIsFatalWhenEnvUnset(t *testing.T) {
	t.Setenv("VULOS_ENV", "")
	t.Setenv(envRegistryInsecure, "1")
	isolateTrustSources(t)

	if pf := PreflightTrust(); pf.Fatal == nil {
		t.Fatal("VULOS_REGISTRY_INSECURE=1 with VULOS_ENV unset booted — the default must be prod (refuse)")
	}
}

// TestPreflight_InsecureIsAllowedOutsideProd — the hatch still exists for
// developers; it just cannot reach production.
func TestPreflight_InsecureIsAllowedOutsideProd(t *testing.T) {
	t.Setenv("VULOS_ENV", "local")
	t.Setenv(envRegistryInsecure, "1")
	isolateTrustSources(t)

	pf := PreflightTrust()
	if pf.Fatal != nil {
		t.Fatalf("dev box refused to start with the dev-only escape hatch: %v", pf.Fatal)
	}
	if !pf.Insecure {
		t.Fatal("expected Insecure=true outside prod")
	}
	if pf.Healthy() {
		t.Fatal("skipping verification must not be reported as healthy")
	}
}

// TestPreflight_NoAnchorInProdIsDegradedNotFatal — the state a box ships in
// before the founder runs the key ceremony: verification is ON and refuses
// everything, so installs fail closed, but mail/meet keep serving.
func TestPreflight_NoAnchorInProdIsDegradedNotFatal(t *testing.T) {
	t.Setenv("VULOS_ENV", "prod")
	t.Setenv(envRegistryInsecure, "")
	t.Setenv(envRegistryPubKey, "")
	isolateTrustSources(t)

	pf := PreflightTrust()
	if pf.Fatal != nil {
		t.Fatalf("a missing anchor must not brick the whole box, only installs: %v", pf.Fatal)
	}
	if pf.Degraded == nil {
		t.Fatal("no trust anchor in prod but preflight reported no problem")
	}
	if pf.Insecure || pf.Healthy() {
		t.Fatal("no anchor must never resolve to insecure-but-healthy")
	}

	// And the install path is genuinely closed in that state.
	if _, err := TrustedKey(); err == nil {
		t.Fatal("TrustedKey succeeded with no anchor configured in prod")
	}
}

// TestPreflight_DevAnchorInProdIsDegraded — the dev keypair is derived from a
// published seed, so anyone can forge with it. A prod box must not accept it,
// which is what keeps the shipped placeholder anchor honest until the ceremony
// replaces it.
func TestPreflight_DevAnchorInProdIsDegraded(t *testing.T) {
	t.Setenv("VULOS_ENV", "prod")
	t.Setenv(envRegistryInsecure, "")
	isolateTrustSources(t)
	t.Setenv(envRegistryPubKey, signing.DevAnchorPubB64)

	pf := PreflightTrust()
	if pf.Degraded == nil {
		t.Fatal("the well-known DEV key was accepted by a production box")
	}
	if pf.Healthy() {
		t.Fatal("dev key in prod reported as healthy")
	}
}

// TestPreflight_HealthyWithRealKey — a real (non-dev) key resolves cleanly and
// the boot log names it, so the trusted key is observable rather than assumed.
func TestPreflight_HealthyWithRealKey(t *testing.T) {
	t.Setenv("VULOS_ENV", "prod")
	t.Setenv(envRegistryInsecure, "")
	isolateTrustSources(t)

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(envRegistryPubKey, base64.StdEncoding.EncodeToString(pub))

	pf := PreflightTrust()
	if pf.Fatal != nil || pf.Degraded != nil {
		t.Fatalf("a real trust key should boot cleanly: fatal=%v degraded=%v", pf.Fatal, pf.Degraded)
	}
	if !pf.Healthy() {
		t.Fatal("expected Healthy() with a real trust key")
	}
	if pf.Source == "" {
		t.Error("preflight must report where the trusted key came from")
	}
}

// TestPreflight_UnrecognisedEnvIsFatal — if we cannot tell which environment we
// are in, we do not guess. Guessing is how a prod box ends up running dev rules.
func TestPreflight_UnrecognisedEnvIsFatal(t *testing.T) {
	t.Setenv("VULOS_ENV", "prodd") // typo'd; not a real environment
	isolateTrustSources(t)

	if pf := PreflightTrust(); pf.Fatal == nil {
		t.Fatal("an unrecognised VULOS_ENV must be a refusal to start, not a guess")
	}
}
