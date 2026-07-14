package appnet

// registry_preflight.go — REGISTRY-SIGN-03: resolve registry trust at BOOT,
// not at first install.
//
// resolveRegistryTrust() is fail-closed, but it was only ever called from
// InstallFromRegistry — lazily, on the first install attempt. That left one
// hole in an otherwise closed system: a production box with
// VULOS_REGISTRY_INSECURE=1 would boot, serve, and look completely healthy.
// The refusal only surfaced when a user eventually clicked "Install", as a
// failed install rather than as a misconfigured box. An operator who set the
// flag (or inherited it from a copy-pasted unit file) got no signal at all.
//
// The standing posture is fail-closed and loud. So trust is resolved once at
// startup and the verdict is one of three things:
//
//   - Fatal:    verification has been deliberately switched off in production.
//               The process MUST NOT start. This is the "impossible to boot a
//               release build with verification off" guarantee — a refusal to
//               start, never a silent downgrade.
//   - Degraded: trust cannot be resolved (the key ceremony has not been run, or
//               the anchor is unusable). Signature verification stays ON and
//               refuses everything, so App Hub installs fail closed. The rest of
//               the box keeps serving — mail and meetings do not owe their
//               availability to the app registry.
//   - healthy:  a trusted key resolved; log which one, so the trusted key is
//               visible in the boot log rather than an article of faith.

import (
	"fmt"
)

// TrustPreflight is the startup verdict on the registry trust chain.
//
// Exactly one of Fatal / Degraded is non-nil, or neither is (healthy).
type TrustPreflight struct {
	// Fatal, when non-nil, means the process must refuse to start.
	Fatal error

	// Degraded, when non-nil, means app installs will be refused but the box may
	// still serve. Verification is ON — this is a fail-closed state, not a
	// weakened one.
	Degraded error

	// Source describes where the trusted entry-verification key came from
	// (anchor path, release cert + key-id, or VULOS_REGISTRY_PUBKEY).
	Source string

	// Insecure reports that signature verification is being skipped. This can
	// only ever be true outside production — in prod the same request is Fatal.
	Insecure bool
}

// Healthy reports whether a trusted key resolved and verification is active.
func (p TrustPreflight) Healthy() bool {
	return p.Fatal == nil && p.Degraded == nil && !p.Insecure
}

// PreflightTrust resolves the registry trust chain once, at startup.
//
// It is the boot-time counterpart to TrustedKey(): same resolution, same
// fail-closed rules, but run before the box starts serving so that a
// misconfiguration is a boot failure rather than a surprise at install time.
//
// The caller MUST treat a non-nil Fatal as refusal-to-start.
func PreflightTrust() TrustPreflight {
	prod, err := isProdEnv()
	if err != nil {
		// We cannot tell which environment we are in. Guessing is how a prod box
		// ends up running dev rules, so refuse to start.
		return TrustPreflight{Fatal: fmt.Errorf(
			"registry trust preflight: %w — set %s to a recognised value (prod, staging, local) or unset it",
			err, "VULOS_ENV")}
	}

	// The headline gate. Verification cannot be switched off on a production box:
	// asking for it is a startup failure, not a downgrade. Checked before trust
	// resolution so the refusal names the real cause rather than whatever the
	// anchor happens to be.
	if prod && registryInsecureRequested() {
		return TrustPreflight{Fatal: fmt.Errorf(
			"%s=1 is set in a production environment (VULOS_ENV=prod, which is also the default when "+
				"VULOS_ENV is unset). Registry signature verification cannot be disabled in production. "+
				"Unset %s, or set VULOS_ENV to a non-production value if this really is a development box",
			envRegistryInsecure, envRegistryInsecure)}
	}

	trust, err := resolveRegistryTrust()
	if err != nil {
		// Verification stays on and refuses everything. Installs are dead until
		// this is fixed; the box may still serve everything else.
		return TrustPreflight{Degraded: err}
	}

	return TrustPreflight{Source: trust.source, Insecure: trust.insecure}
}
