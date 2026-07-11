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
//   - OS: a self-hosted (or operator-provisioned) box that is CP-adjacent —
//     it may talk to a Vulos Cloud control plane for optional features
//     (integrations, vk_ API keys, cloud login) but is not itself the
//     multi-tenant cloud deployment. Entitlement gating is ENFORCED here
//     for vk_-keyed requests (fail closed), matching cloud, since a
//     CP-adjacent box already has billing wired.
//   - Cloud: the multi-tenant cloud-hosted deployment (vulos-cloud's
//     provisioned boxes / the shared cloud runtime). Entitlement gating is
//     enforced (fail closed); Tigris-style object storage has no STS, so the
//     presign path is used instead of STS-scoped credentials.
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
			log.Printf("[deploymode] WARNING: DEPLOY_MODE=%q but VULOS_CP_BASE_URL is unset — "+
				"vk_ API-key auth and entitlement gating will be inactive (session-only auth, apps ungated) "+
				"until it is configured", m)
		}
	case Standalone:
		if strings.TrimSpace(os.Getenv("VULOS_CP_BASE_URL")) != "" {
			log.Printf("[deploymode] NOTE: DEPLOY_MODE=%q (or unset) with VULOS_CP_BASE_URL configured — "+
				"vk_ API-key auth is active, but entitlement gating stays OPEN because this box is standalone "+
				"(set DEPLOY_MODE=os or DEPLOY_MODE=cloud to enforce it)", m)
		}
	}
}
