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
	"sync/atomic"
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

// active holds the environment resolved at startup (see SetActive).  It is an
// atomic so a late reader in a goroutine cannot race the single startup write.
var active atomic.Value // Env

// SetActive records the environment that main() resolved from --env / VULOS_ENV
// via Parse.  Call it once, immediately after Parse and before any goroutine or
// gate runs.
//
// WHY THIS EXISTS: the production fail-closed gates (Restic dev-passphrase,
// DNS/Caddy/nginx provisioning, fabric key sealing) used to read
// os.Getenv("VULOS_ENV") directly.  But `--env=prod` — which is the documented
// way to start the box, and the way cmd/init actually launches vulos-server
// (`-env <resolved>`, see cmd/init/main.go) — never sets VULOS_ENV.  So every
// one of those gates silently took its DEV branch on a real production box:
// backups encrypted with the well-known dev key, subdomain provisioning
// noop'ing while telling customers their domain was live.  That is exactly the
// fail-open those gates were written to prevent.  Reading the resolved value
// means the flag and the variable cannot disagree.
//
// Passing the empty Env clears the resolution again (used by tests).
//
// Prefer Resolve at a program's entry point: it parses and publishes in one
// call, so the publish step cannot be forgotten.
func SetActive(e Env) { active.Store(e) }

// Resolve parses the --env flag value (falling back to VULOS_ENV, then prod,
// exactly like Parse) AND publishes the result as the process-wide active
// environment.
//
// This is the entry point every main() should use.  Parse alone is a pure
// query; if a main() called it and forgot to publish, every fail-closed gate
// would go back to reading a VULOS_ENV that `--env=prod` never sets — the
// original bug.  Resolve makes forgetting impossible.
func Resolve(flagValue string) (Env, error) {
	e, err := Parse(flagValue)
	if err != nil {
		return "", err
	}
	SetActive(e)
	return e, nil
}

// Active returns the environment resolved at startup by SetActive.
//
// Before SetActive has run — package unit tests, helper binaries, any process
// that never parsed the flag — it falls back to the raw VULOS_ENV value.  It
// deliberately does NOT apply Parse's default-to-prod rule: that default belongs
// to main(), and applying it here would arm every production gate inside every
// package's test binary.  An unset/unrecognised value therefore yields "", which
// is not prod.
func Active() Env {
	if e, ok := active.Load().(Env); ok && e != "" {
		return e
	}
	switch e := Env(os.Getenv("VULOS_ENV")); e {
	case EnvLocal, EnvDev, EnvProd:
		return e
	}
	return ""
}

// IsProdActive reports whether the active runtime environment is production.
// This is the check every fail-closed production gate must use — never
// os.Getenv("VULOS_ENV") == "prod", which misses the --env=prod flag.
func IsProdActive() bool { return Active().IsProd() }

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
