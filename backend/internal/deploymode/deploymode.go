// Package deploymode provides a single typed DEPLOY_MODE enum for the OS
// backend, per the cross-repo "two-class app model" contract:
//
//	DEPLOY_MODE = standalone|os|cloud
//	Each app reads once, validates coherent config, self-reports at boot.
//	Unset => standalone.
//
// The three values describe WHERE and HOW this OS binary is running:
//
//   - Standalone: a fully sovereign, self-hosted box with no cloud control
//     plane involved at all. Today's default behavior — unchanged. All apps
//     are open; no billing/entitlement gating; storage isolation still
//     applies (it protects the box's own users from each other) but never
//     requires a CP.
//   - OS: a box that is gateway-adjacent — the owner has pointed it at a
//     gateway for optional features (integrations, vk_ API keys) but it is not
//     itself a multi-tenant deployment. Entitlement gating is ENFORCED here for
//     vk_-keyed requests (fail closed), matching cloud, since a gateway-adjacent
//     box resolves entitlements upstream.
//   - Cloud: a multi-tenant hosted deployment, where one operator runs boxes for
//     many tenants. Entitlement gating is enforced (fail closed); Tigris-style
//     object storage has no STS, so the presign path is used instead of
//     STS-scoped credentials.
package deploymode

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// Mode is the typed DEPLOY_MODE value.
type Mode string

const (
	// Standalone is the fully sovereign self-host default (DEPLOY_MODE unset).
	Standalone Mode = "standalone"
	// OS is a CP-adjacent self-hosted/provisioned box.
	OS Mode = "os"
	// Cloud is the multi-tenant cloud deployment.
	Cloud Mode = "cloud"
)

// EnvVar is the environment variable name read by FromEnv.
const EnvVar = "DEPLOY_MODE"

// Valid reports whether m is one of the three recognised values.
func (m Mode) Valid() bool {
	switch m {
	case Standalone, OS, Cloud:
		return true
	default:
		return false
	}
}

// IsCloudAdjacent reports whether entitlement gating / CP-brokered features
// should be treated as ACTIVE (fail-closed) for this mode: true for both OS
// and Cloud, false for Standalone.
func (m Mode) IsCloudAdjacent() bool {
	return m == OS || m == Cloud
}

// String implements fmt.Stringer.
func (m Mode) String() string { return string(m) }

// SoftwareKeystoreEnvOptOut is the explicit operator acknowledgement that a
// cloud-managed box may run with the plaintext software device keystore
// (filesystem-only key custody). It exists because the multi-tenant Cloud
// runtime (Fly VMs) has no TPM, so a blanket hard refusal would break that
// legitimate deployment — the opt-out makes running without hardware key
// custody a deliberate, auditable choice rather than a silent fallback.
const SoftwareKeystoreEnvOptOut = "VULOS_ALLOW_SOFTWARE_KEYSTORE"

// RefuseSoftwareKeystore reports whether this box must FAIL CLOSED at boot
// rather than run with a plaintext software (filesystem-only) device keystore.
//
// A cloud-managed box (OS or Cloud — see IsCloudAdjacent) authenticates to the
// control plane as a device and seals its enrolled device key at rest; doing
// that with filesystem-only custody defeats the point, so hardware-backed key
// custody (TPM) is required there. The refusal is bypassed only by the explicit
// operator opt-out (SoftwareKeystoreEnvOptOut), which the Fly-hosted Cloud
// runtime — legitimately TPM-less — uses.
//
// Standalone self-host is UNAFFECTED: the software keystore is its documented,
// legitimate fallback.
//
// It is a pure decision (no env/OS reads) so it is exhaustively table-testable;
// the caller passes the observed keystore kind and opt-out flag.
func (m Mode) RefuseSoftwareKeystore(keystoreIsSoftware, operatorOptOut bool) bool {
	if !m.IsCloudAdjacent() {
		return false // standalone self-host: software keystore is fine
	}
	if !keystoreIsSoftware {
		return false // TPM/hardware-backed custody: fine
	}
	if operatorOptOut {
		return false // deliberate, auditable acknowledgement (e.g. cloud on Fly)
	}
	return true // managed + software + no opt-out => refuse to boot
}

// FromEnv reads DEPLOY_MODE once, validating it. An unset value returns
// (Standalone, nil) — today's default behavior unchanged. An explicit but
// unrecognised value returns (Standalone, err): the caller should log the
// error and continue in the safe (Standalone/fully-open) default rather than
// fail to boot over a typo.
func FromEnv() (Mode, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(EnvVar)))
	if raw == "" {
		return Standalone, nil
	}
	m := Mode(raw)
	if !m.Valid() {
		return Standalone, fmt.Errorf("deploymode: invalid %s=%q (want %q, %q, or %q) — falling back to %q",
			EnvVar, raw, Standalone, OS, Cloud, Standalone)
	}
	return m, nil
}

// Load resolves DEPLOY_MODE via FromEnv, logs any validation problem, runs a
// light coherence check appropriate to the resolved mode, and self-reports at
// boot. It never fails the boot — DEPLOY_MODE is advisory config, not a hard
// gate; callers that need hard gating (e.g. storage isolation) enforce that
// separately and explicitly.
func Load() Mode {
	m, err := FromEnv()
	if err != nil {
		log.Printf("[deploymode] %v", err)
	}
	m.checkCoherence()
	log.Printf("[deploymode] running as %q (%s=%q)", m, EnvVar, os.Getenv(EnvVar))
	return m
}

// checkCoherence logs (non-fatal) warnings when the resolved mode's config
// looks incomplete, so an operator notices a half-configured cloud/os box
// early rather than discovering a silent fail-open/fail-closed surprise later.
func (m Mode) checkCoherence() {
	switch m {
	case Cloud, OS:
		if strings.TrimSpace(os.Getenv("VULOS_CP_BASE_URL")) == "" {
			// BUG FIX (2026-07-12): this message previously claimed entitlement
			// gating "will be inactive (apps ungated)" — that is backwards.
			// gateway.SetEntitlementGating is driven by DEPLOY_MODE alone (see
			// main.go), NOT by VULOS_CP_BASE_URL, so gating stays ENFORCED
			// (fail-closed) here. What actually breaks is vk_ introspection
			// (VKIntrospector stays nil), so no session can ever PROVE a
			// product entitlement — every app that declares a required
			// product becomes permanently inaccessible (402) to every user
			// until VULOS_CP_BASE_URL is configured. That is a worse
			// operational trap than "ungated", so warn accurately.
			log.Printf("[deploymode] WARNING: DEPLOY_MODE=%q but VULOS_CP_BASE_URL is unset — "+
				"vk_ API-key auth is inactive, so no session can prove a product entitlement; "+
				"entitlement gating stays ENFORCED (fail-closed), meaning every app that requires "+
				"a product is now inaccessible to ALL users until VULOS_CP_BASE_URL is configured", m)
		}
	case Standalone:
		if strings.TrimSpace(os.Getenv("VULOS_CP_BASE_URL")) != "" {
			log.Printf("[deploymode] NOTE: DEPLOY_MODE=%q (or unset) with VULOS_CP_BASE_URL configured — "+
				"vk_ API-key auth is active, but entitlement gating stays OPEN because this box is standalone "+
				"(set DEPLOY_MODE=os or DEPLOY_MODE=cloud to enforce it)", m)
		}
	}
}
