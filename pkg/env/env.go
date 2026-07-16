// Package env encodes the three deployment environments for the Vulos Cloud
// control plane: local, dev, and prod.
//
// Usage in main.go:
//
//	envFlag := flag.String("env", "", "deployment environment: local|dev|prod (default: VULOS_ENV or prod)")
//	flag.Parse()
//	e := env.Init(*envFlag)
//	log.Printf("[cp] env=%s", e)
//
// Consumers call env.Current() anywhere after Init has been called once.
package env

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// Version is the control-plane release version. It tracks the git tag that
// triggers the production deploy (`deploy.yml` fires on `v*.*.*` tags). It is a
// `var`, not a `const`, so a release build MAY override it at link time with
//
//	-ldflags="-X github.com/vul-os/vulos-management/pkg/env.Version=v0.1.0"
//
// to stamp the exact tag; absent that, this literal is the source of truth and
// is bumped in lockstep with CHANGELOG.md + the git tag. Surfaced on GET /healthz
// ("version") to honour the /healthz contract the CP enforces on every product.
var Version = "v0.1.1"

// Env is one of the three supported deployment environments.
type Env string

const (
	// EnvLocal is for laptop/desktop development. No real external services
	// required: MailHog handles email, MinIO handles S3, Paystack test keys are
	// used, Secure cookies are disabled, and CORS is opened to localhost:5173.
	EnvLocal Env = "local"

	// EnvDev is the staging environment hosted on Fly. Real Paystack test
	// keys are required, a real managed staging bucket is used, and email
	// verification is enforced. Secure cookies are on (served over HTTPS).
	EnvDev Env = "dev"

	// EnvProd is the live production environment. All safety gates are enforced:
	// strict CORS, Secure cookies, full rate-limits, and no dev shortcuts.
	EnvProd Env = "prod"
)

// current holds the process-wide environment set by Init.
var current Env = EnvProd

// initialized records whether Init has run. Once true, the authoritative prod
// resolver (IsProdResolved) trusts the Init-resolved `current` value; before
// Init it falls back to the raw signals so leaf KEK loaders that run pre-Init
// still resolve fail-safe. resolvedFlag holds the `--env` value Init was given
// so the resolver can consider the explicit flag even when VULOS_ENV is unset.
var (
	initialized  bool
	resolvedFlag string
)

// Init parses the flag value (or falls back to VULOS_ENV, then prod) and
// stores it as the process-wide current environment. It aborts if prod is
// selected but obvious dev-flags are set. Call this once at process startup,
// before reading any defaults.
func Init(flagVal string) Env {
	resolvedFlag = strings.TrimSpace(flagVal)
	raw := resolvedFlag
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("VULOS_ENV"))
	}
	if raw == "" {
		raw = "prod"
	}

	switch Env(raw) {
	case EnvLocal, EnvDev, EnvProd:
		current = Env(raw)
	default:
		log.Fatalf("[env] unknown value %q — must be local|dev|prod", raw)
	}
	initialized = true

	log.Printf("[cp] env=%s", current)

	if IsProdResolved() {
		checkNoDevFlagsInProd()
	}

	return current
}

// Current returns the process-wide deployment environment. Init must have been
// called first; if not, it returns EnvProd (safe default).
func Current() Env { return current }

// ── Base domain ───────────────────────────────────────────────────────────────
//
// VULOS_DOMAIN is the single source of truth for the deployment's base domain.
// EVERY product URL / subdomain is derived from it (see Domain, SiteURL, AppURL,
// SubdomainURL below) so a non-prod deployment can run on a different domain just
// by setting this one env var — no hardcoded vulos.org anywhere in the URL path.
//
// Per-env default when VULOS_DOMAIN is unset:
//
//	local → vulos.local      (use /etc/hosts entries for *.vulos.local in dev)
//	dev   → dev.vulos.org
//	prod  → vulos.org
//
// An explicit VULOS_DOMAIN always wins.

// Domain returns the base domain for the current deployment.
func Domain() string { return domain(current) }

// domain is the testable inner function.
func domain(e Env) string {
	if v := strings.TrimSpace(os.Getenv("VULOS_DOMAIN")); v != "" {
		return v
	}
	switch e {
	case EnvLocal:
		return "vulos.local"
	case EnvDev:
		// Dev runs on the `dev.` prefix of the production apex so its service
		// hosts derive as `dev-os.`, `dev-boot.`, `dev-relay.` (see hostScheme).
		return "dev.vulos.org"
	default:
		return "vulos.org"
	}
}

// ── Host scheme (routing model) ────────────────────────────────────────────────
//
// The final routing model (PART A) serves everything off ONE registrable apex and
// derives service hosts from it:
//
//	prod   vulos.org       → site vulos.org        os os.vulos.org        boot boot.vulos.org
//	dev    dev.vulos.org   → site dev.vulos.org    os dev-os.vulos.org    boot dev-boot.vulos.org
//	local  vulos.local     → site vulos.local      os os.vulos.local      boot boot.vulos.local
//
// A `dev.`-prefixed apex is detected and rewritten to a `dev-` SERVICE-LABEL prefix
// on the shared registrable base, so the dev plane lives on distinct single-label
// hosts (`*.dev-os.vulos.org` is its own wildcard zone) instead of nesting under
// `*.os.dev.vulos.org`. Everything is driven by VULOS_DOMAIN — no hardcoded host.
//
// Product pages, the API, status, and the console are all PATHS on the site apex
// (vulos.org/products/<name>, /api, /status, /console) — never their own
// subdomains. `os` (session-routed OS entry + single-label instances), `relay`,
// and `boot` are the only first-class service subdomains.

// hostScheme returns the service-label prefix and the registrable base derived
// from Domain(). The prefix is "" for prod/local and "dev-" for the dev plane.
func hostScheme() (prefix, base string) {
	d := Domain()
	if strings.HasPrefix(d, "dev.") {
		return "dev-", strings.TrimPrefix(d, "dev.")
	}
	return "", d
}

// OSHost returns the OS entry host: `[dev-]os.<base>` (e.g. os.vulos.org).
// os.<domain> is session-routed to the best box in the user's cluster; single-
// label instances live under it as `<org>.<OSHost>` / `<box-id>.<OSHost>`.
func OSHost() string {
	p, b := hostScheme()
	return p + "os." + b
}

// OSHostSuffix returns "." + OSHost(), the suffix a single-label OS instance host
// (`<org>.os.<domain>` / `<box-id>.os.<domain>`) must end with.
func OSHostSuffix() string { return "." + OSHost() }

// OSInstanceHost returns `<label>.<OSHost>` for an org handle or box id label.
func OSInstanceHost(label string) string { return label + "." + OSHost() }

// OSURL returns https://<OSHost>.
func OSURL() string { return "https://" + OSHost() }

// RelayHost returns the relay ingress host: `[dev-]relay.<base>`.
func RelayHost() string {
	p, b := hostScheme()
	return p + "relay." + b
}

// BootHost returns the netboot/first-boot host: `[dev-]boot.<base>`.
func BootHost() string {
	p, b := hostScheme()
	return p + "boot." + b
}

// ── Path-based surfaces on the site apex (no per-surface subdomain) ──────────────

// ProductURL returns the PATH-based product page URL: https://<domain>/products/<slug>.
func ProductURL(slug string) string { return SiteURL() + "/products/" + slug }

// APIURL returns the PATH-based API base: https://<domain>/api.
func APIURL() string { return SiteURL() + "/api" }

// StatusURL returns the PATH-based status page: https://<domain>/status.
func StatusURL() string { return SiteURL() + "/status" }

// ConsoleURL returns the PATH-based commercial console: https://<domain>/console.
func ConsoleURL() string { return SiteURL() + "/console" }

// SiteURL returns the apex (marketing/landing + auth) origin: https://<domain>.
func SiteURL() string { return "https://" + Domain() }

// AppURL returns the canonical hosted Workspace origin: https://app.<domain>.
// This is the central auth/app surface (the logged-in suite shell).
func AppURL() string { return "https://app." + Domain() }

// SubdomainURL returns https://<sub>.<domain> for a product/service subdomain.
func SubdomainURL(sub string) string { return "https://" + sub + "." + Domain() }

// ControlPlaneDomain returns the apex domain the control-plane itself is served
// on (distinct from the product Domain()). Defaults to "vulos.cloud" and is
// overridable via VULOS_CLOUD_DOMAIN so a custom deployment is not pinned to the
// hardcoded production value.
func ControlPlaneDomain() string {
	if v := strings.TrimSpace(os.Getenv("VULOS_CLOUD_DOMAIN")); v != "" {
		return v
	}
	return "vulos.cloud"
}

// IsLocal reports whether the current env is local.
func IsLocal() bool { return current == EnvLocal }

// IsDev reports whether the current env is dev.
func IsDev() bool { return current == EnvDev }

// IsProd reports whether the current env is prod. It delegates to the single
// authoritative, fail-safe resolver (IsProdResolved) so there is exactly one
// notion of "prod" across the whole control plane.
func IsProd() bool { return IsProdResolved() }

// ── Authoritative prod resolver + VULOS_DEV parsing / dev-KEK fallback gate ─────
//
// WAVE34 (prod-detection landmine): the KEK loaders (integrations / llmkeys /
// cloudhome / storage) historically gated their all-zeros dev fallback purely on
// os.Getenv("VULOS_DEV"), and did so inconsistently — some compared against "1",
// others against "true". Meanwhile SESSION_SECRET and the rest of the safety
// gates keyed off a different notion of prod (VULOS_ENV, or the Init-set IsProd()
// global). The result was two-plus independent notions of "am I in prod", so a
// stray VULOS_DEV on a real prod box could silently activate all-zeros dev KEKs.
// WAVE34's stopgap read raw VULOS_ENV in the KEK gate, but that treats an UNSET
// environment as "not prod" — an unconfigured box would still open dev KEKs.
//
// WAVE37 (long-term fix): there is now exactly ONE authoritative resolver,
// IsProdResolved(), and EVERY prod / KEK / secret / dev-fallback guard routes
// through it. It considers ALL signals (the --env flag captured at Init, VULOS_ENV,
// and Fly deploy markers), is resolved once at Init, and — critically — DEFAULTS
// TO PROD (fail-safe) whenever the environment is unset or ambiguous. An
// unconfigured box therefore resolves to prod and refuses every dev shortcut.
// Dev/test must OPT IN explicitly by setting VULOS_ENV=local|dev (or --env).

// IsProdResolved is the single authoritative "am I running in production?"
// resolver for the entire control plane. It FAILS SAFE: when no signal says
// otherwise it returns true, so an unconfigured/ambiguous box is treated as prod
// and every dev shortcut (zero KEKs, plaintext secrets, insecure cookies, http
// origins) is refused.
//
// Resolution order (first decisive signal wins):
//  1. After Init has run, the Init-resolved `current` env is authoritative — an
//     operator who explicitly asked for local/dev via --env or VULOS_ENV gets it.
//  2. Before Init (leaf KEK loaders can run first), fall back to the raw signals:
//     the --env flag captured at Init (if any), then VULOS_ENV. An explicit
//     "local"/"dev" from either yields non-prod; anything else — including UNSET —
//     yields prod.
//
// Fly deploy markers (FLY_APP_NAME) are treated as an additional prod signal:
// their presence can only ever push TOWARD prod, never away from it, so they can
// never weaken the fail-safe default.
func IsProdResolved() bool {
	// Once Init has run, trust the resolved env: an explicit non-prod selection
	// (--env=local|dev or VULOS_ENV=local|dev) is honoured; everything else is prod.
	if initialized {
		return current == EnvProd
	}

	// Pre-Init (leaf loaders). Consider the explicit signals; default to prod.
	raw := strings.TrimSpace(resolvedFlag)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("VULOS_ENV"))
	}
	switch strings.ToLower(raw) {
	case string(EnvLocal), string(EnvDev):
		// Explicit non-prod request. A Fly deploy marker does not override an
		// operator's explicit local/dev choice (used by staging Machines).
		return false
	case string(EnvProd):
		return true
	default:
		// Unset or unrecognised → fail safe to prod. (A present Fly deploy
		// marker only reinforces this; it can never flip us to non-prod.)
		return true
	}
}

// DevFlag reports whether VULOS_DEV is set to a recognised truthy value. This is
// the single source of truth for parsing VULOS_DEV; callers must not compare the
// raw env var themselves (that is exactly how "1" vs "true" drift crept in).
func DevFlag() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VULOS_DEV"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// DevKEKFallbackAllowed reports whether an all-zeros dev KEK fallback (or the
// plaintext-secret dev mode) may be used in place of a missing real KEK. It FAILS
// CLOSED: the fallback is permitted only when VULOS_DEV is truthy AND the process
// is NOT prod per the single authoritative resolver. Because IsProdResolved
// defaults to prod, an unconfigured box (VULOS_ENV unset) with a stray
// VULOS_DEV=true can never open a zero KEK — dev must be requested explicitly.
func DevKEKFallbackAllowed() bool { return DevFlag() && !IsProdResolved() }

// IsProdEnv is retained as a stable name for leaf packages but now delegates to
// the single authoritative resolver (IsProdResolved). Unlike the old
// implementation it no longer treats an unset VULOS_ENV as "not prod": unset is
// fail-safe prod. It does not require Init to have run.
func IsProdEnv() bool { return IsProdResolved() }

// String implements fmt.Stringer.
func (e Env) String() string { return string(e) }

// ── Per-env defaults ─────────────────────────────────────────────────────────

// Defaults holds all tunable defaults for a given environment. Callers in
// main.go and wire_stores.go call Defaults() once and use the returned struct
// to supply fallback values instead of consulting 25+ env vars individually.
type Defaults struct {
	// BindAddr is the default listen address (:8081 everywhere; local binds to
	// 127.0.0.1 to avoid firewall prompts on macOS/Windows).
	BindAddr string

	// SMTPHost is the outbound SMTP server host.
	SMTPHost string
	// SMTPPort is the outbound SMTP server port.
	SMTPPort string

	// S3Endpoint is the S3-compatible endpoint for managed object storage.
	// In local mode this points at MinIO; in dev/prod it is the deployment.s managed S3 endpoint.
	S3Endpoint string

	// S3Region is the S3 region to advertise to the storage SDK.
	S3Region string

	// SecureCookie controls whether session cookies are set with Secure=true.
	// Must be false in local mode (HTTP only); true in dev and prod.
	SecureCookie bool

	// CORSOrigins is the list of allowed cross-origin hosts for state-changing
	// requests. In local mode the Vite dev (5173) + preview (4173) origins are
	// allowed. In dev and prod only the canonical app origins are used.
	CORSOrigins []string

	// RateLimitEnabled reports whether the rate-limit middleware should enforce
	// limits. Always true in prod; can be false in local for easier curl-testing.
	RateLimitEnabled bool

	// EmailVerifyRequired reports whether users must verify their email before
	// they can use the platform. Enforced in dev and prod; optional in local.
	EmailVerifyRequired bool

	// RequireDeviceSig reports whether device signatures are enforced on fleet
	// enroll / boot flows. Always true in prod; false in local.
	RequireDeviceSig bool

	// PaystackBaseURL is the payment-processor API base URL. Identical for all
	// envs — the key prefix (test vs. live) determines live vs. test mode.
	PaystackBaseURL string

	// AllowAllRelayQuotas bypasses payment-processor-backed quota checks in the
	// relay forwarder. Only true in local; false in dev and prod.
	AllowAllRelayQuotas bool

	// OTAPublicBucketBase is the default public base URL for OTA artifacts.
	// In local mode this points at MinIO; in dev/prod it should be overridden
	// by OTA_PUBLIC_BUCKET_BASE.
	OTAPublicBucketBase string
}

// Get returns per-environment defaults for the current environment.
// Individual env vars always win over these defaults when set.
func Get() Defaults { return get(current) }

// get is the testable inner function.
func get(e Env) Defaults {
	// Derive cross-origin app origins from the single base domain so dev can run
	// on a different domain by setting VULOS_DOMAIN. Local keeps the Vite dev
	// origin; dev/prod allow the apex + the app subdomain.
	d := domain(e)
	derivedCORS := []string{"https://" + d, "https://app." + d}
	switch e {
	case EnvLocal:
		return Defaults{
			BindAddr:            "127.0.0.1:8081",
			SMTPHost:            "localhost",
			SMTPPort:            "1025",
			S3Endpoint:          "http://localhost:9000",
			S3Region:            "us-east-1", // MinIO default region
			SecureCookie:        false,
			CORSOrigins:         []string{"http://localhost:5173", "http://localhost:4173"},
			RateLimitEnabled:    false,
			EmailVerifyRequired: false,
			RequireDeviceSig:    false,
			PaystackBaseURL:     "https://api.paystack.co",
			AllowAllRelayQuotas: true,
			OTAPublicBucketBase: "http://localhost:9000/vulos-dev",
		}
	case EnvDev:
		return Defaults{
			BindAddr:            ":8081",
			SMTPHost:            "smtp.eu.mailgun.org",
			SMTPPort:            "587",
			S3Endpoint:          "", // deployment supplies the managed S3 endpoint
			S3Region:            "auto",
			SecureCookie:        true,
			CORSOrigins:         derivedCORS,
			RateLimitEnabled:    true,
			EmailVerifyRequired: true,
			RequireDeviceSig:    false, // relaxed for staging test devices
			PaystackBaseURL:     "https://api.paystack.co",
			AllowAllRelayQuotas: false,
			OTAPublicBucketBase: "", // deployment supplies the OTA public base
		}
	default: // prod
		return Defaults{
			BindAddr:            ":8081",
			SMTPHost:            "smtp.eu.mailgun.org",
			SMTPPort:            "587",
			S3Endpoint:          "", // deployment supplies the managed S3 endpoint
			S3Region:            "auto",
			SecureCookie:        true,
			CORSOrigins:         derivedCORS,
			RateLimitEnabled:    true,
			EmailVerifyRequired: true,
			RequireDeviceSig:    true,
			PaystackBaseURL:     "https://api.paystack.co",
			AllowAllRelayQuotas: false,
			OTAPublicBucketBase: "", // deployment supplies the OTA public base
		}
	}
}

// ── Production safety checks ──────────────────────────────────────────────────

// devFlagChecks lists env var + description pairs that must NOT be set to their
// dev-shortcut values when running in prod.
var devFlagChecks = []struct {
	key     string
	badVal  string
	message string
}{
	{
		key:     "RELAY_ALLOW_ALL_QUOTAS",
		badVal:  "1",
		message: "RELAY_ALLOW_ALL_QUOTAS=1 bypasses quota enforcement — must not be set in prod",
	},
	{
		key:     "FLEET_REQUIRE_DEVICE_SIG",
		badVal:  "0",
		message: "FLEET_REQUIRE_DEVICE_SIG=0 disables device-signature checks — must not be set in prod",
	},
	{
		key:     "AUTH_ALLOW_UNVERIFIED_LOGIN",
		badVal:  "1",
		message: "AUTH_ALLOW_UNVERIFIED_LOGIN=1 bypasses email verification — must not be set in prod",
	},
}

// checkNoDevFlagsInProd aborts the process if any known dev-flag is set to its
// dangerous value while running in prod mode.
func checkNoDevFlagsInProd() {
	var violations []string
	for _, c := range devFlagChecks {
		if os.Getenv(c.key) == c.badVal {
			violations = append(violations, fmt.Sprintf("  %s: %s", c.key, c.message))
		}
	}
	if len(violations) > 0 {
		log.Fatalf("[env] prod safety check FAILED — dev flags are set:\n%s\nAborting. Fix the env vars or use --env=dev.", strings.Join(violations, "\n"))
	}
}

// EnvOrDefault returns the value of the named environment variable, or def if
// the variable is empty. This is a convenience helper for main.go so the flag
// defaults and env-var fallbacks can be expressed in one line.
func EnvOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
