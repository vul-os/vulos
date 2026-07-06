// Package env defines runtime environment constants and per-env defaults for
// the Vulos backend.  The active environment is chosen by:
//
//  1. The --env CLI flag (local | dev | prod, default: prod)
//  2. The VULOS_ENV environment variable when the flag is absent or empty.
//
// Canonical values and their semantics:
//
//	local  - developer laptop; loose security, no hardware checks required,
//	         self-signed certs acceptable, debug endpoints enabled.
//	dev    - CI / staging; slightly stricter than local; staging broker pubkey
//	         trusted alongside prod; hardware checks still skipped.
//	prod   - production bare-metal / cloud; full security posture, hardware
//	         checks enforced where hardware is present, debug endpoints off.
package env

import (
	"fmt"
	"os"
	"strings"
)

// Env is the runtime environment tag.
type Env string

const (
	// EnvLocal is the developer-laptop mode.  Security is relaxed so that a
	// fresh checkout works without mkcert, TPM hardware, or cloud accounts.
	EnvLocal Env = "local"

	// EnvDev is the CI / staging mode.  Similar to local but slightly stricter:
	// the staging cloud-broker public key is accepted alongside the prod key.
	EnvDev Env = "dev"

	// EnvProd is the production mode.  All security features are active.
	EnvProd Env = "prod"
)

// Defaults carries the per-environment behavioural defaults that the rest of
// the backend reads at startup.
type Defaults struct {
	// BindHost is the interface the HTTP server binds to.
	//   local/dev: "127.0.0.1" (loopback only; reverse-proxy or Vite in front)
	//   prod:      "" (all interfaces, same as "0.0.0.0")
	BindHost string

	// SkipHardwareChecks disables TPM / fingerprint presence requirements.
	// True for local and dev so developers can run on laptops without a TPM.
	SkipHardwareChecks bool

	// AllowSelfSignedCerts allows the HTTP client to accept self-signed TLS
	// certificates when calling upstream services (e.g. staging broker).
	AllowSelfSignedCerts bool

	// StrictCookies enables Secure + SameSite=None on session cookies.  In
	// prod this is always true; in local/dev the auth layer already sets flags
	// based on the actual request TLS state, so this is informational.
	StrictCookies bool

	// DebugEndpoints enables extra diagnostic HTTP routes (e.g. /debug/pprof,
	// /api/debug/*).  Never enabled in prod.
	DebugEndpoints bool

	// AllowStagingBrokerKey permits the staging cloud-broker pubkey alongside
	// the production key.  Only used in dev.
	AllowStagingBrokerKey bool
}

// Parse resolves the Env value from the supplied string, falling back to
// VULOS_ENV if s is empty, and finally defaulting to EnvProd.
//
// Returns an error for any non-empty, unrecognised value so callers can abort
// with a clear message rather than silently running in an unexpected mode.
func Parse(s string) (Env, error) {
	if s == "" {
		s = os.Getenv("VULOS_ENV")
	}
	if s == "" {
		return EnvProd, nil
	}
	switch Env(s) {
	case EnvLocal, EnvDev, EnvProd:
		return Env(s), nil
	default:
		return "", fmt.Errorf("unrecognised --env value %q: must be local, dev, or prod", s)
	}
}

// DefaultsFor returns the Defaults struct for env.
func DefaultsFor(e Env) Defaults {
	switch e {
	case EnvLocal:
		return Defaults{
			BindHost:              "127.0.0.1",
			SkipHardwareChecks:    true,
			AllowSelfSignedCerts:  true,
			StrictCookies:         false,
			DebugEndpoints:        true,
			AllowStagingBrokerKey: false,
		}
	case EnvDev:
		return Defaults{
			BindHost:              "127.0.0.1",
			SkipHardwareChecks:    true,
			AllowSelfSignedCerts:  true,
			StrictCookies:         false,
			DebugEndpoints:        false,
			AllowStagingBrokerKey: true,
		}
	default: // EnvProd
		return Defaults{
			BindHost:              "",
			SkipHardwareChecks:    false,
			AllowSelfSignedCerts:  false,
			StrictCookies:         true,
			DebugEndpoints:        false,
			AllowStagingBrokerKey: false,
		}
	}
}

// FirstNonEmptyEnv returns the trimmed value of the first environment variable
// in keys that is set to a non-empty (after trimming) value, or "" if none are.
//
// It exists to unify aliased environment variables so that setting any accepted
// name works. For the LLM/llmux seam the canonical variable is LLMUX_URL, with
// VULOS_LLMUX_URL kept as an accepted alias; call
// FirstNonEmptyEnv("LLMUX_URL", "VULOS_LLMUX_URL") to read either. Keys are
// checked in order, so list the canonical name first.
func FirstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// String returns the lower-case string representation of the environment.
func (e Env) String() string { return string(e) }

// IsProd reports whether e is the production environment.
func (e Env) IsProd() bool { return e == EnvProd }

// IsLocal reports whether e is the local developer environment.
func (e Env) IsLocal() bool { return e == EnvLocal }

// IsDev reports whether e is the development / staging environment.
func (e Env) IsDev() bool { return e == EnvDev }
