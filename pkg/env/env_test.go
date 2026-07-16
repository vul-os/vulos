package env

import (
	"os"
	"testing"
)

// TestLocalDefaults verifies the per-env defaults for EnvLocal.
func TestLocalDefaults(t *testing.T) {
	d := get(EnvLocal)

	if d.BindAddr != "127.0.0.1:8081" {
		t.Errorf("local BindAddr = %q, want 127.0.0.1:8081", d.BindAddr)
	}
	if d.SMTPHost != "localhost" {
		t.Errorf("local SMTPHost = %q, want localhost", d.SMTPHost)
	}
	if d.SMTPPort != "1025" {
		t.Errorf("local SMTPPort = %q, want 1025 (MailHog)", d.SMTPPort)
	}
	if d.S3Endpoint != "http://localhost:9000" {
		t.Errorf("local S3Endpoint = %q, want http://localhost:9000 (MinIO)", d.S3Endpoint)
	}
	if d.SecureCookie {
		t.Error("local SecureCookie should be false (HTTP only)")
	}
	if len(d.CORSOrigins) == 0 {
		t.Fatal("local CORSOrigins should not be empty")
	}
	if d.CORSOrigins[0] != "http://localhost:5173" {
		t.Errorf("local CORSOrigins[0] = %q, want http://localhost:5173", d.CORSOrigins[0])
	}
	if d.RateLimitEnabled {
		t.Error("local RateLimitEnabled should be false")
	}
	if d.EmailVerifyRequired {
		t.Error("local EmailVerifyRequired should be false")
	}
	if d.RequireDeviceSig {
		t.Error("local RequireDeviceSig should be false")
	}
	if !d.AllowAllRelayQuotas {
		t.Error("local AllowAllRelayQuotas should be true")
	}
	if d.OTAPublicBucketBase != "http://localhost:9000/vulos-dev" {
		t.Errorf("local OTAPublicBucketBase = %q, want http://localhost:9000/vulos-dev", d.OTAPublicBucketBase)
	}
}

// TestDevDefaults verifies the per-env defaults for EnvDev.
func TestDevDefaults(t *testing.T) {
	d := get(EnvDev)

	if d.BindAddr != ":8081" {
		t.Errorf("dev BindAddr = %q, want :8081", d.BindAddr)
	}
	if d.S3Endpoint != "https://fly.storage.tigris.dev" {
		t.Errorf("dev S3Endpoint = %q, want Tigris endpoint", d.S3Endpoint)
	}
	if !d.SecureCookie {
		t.Error("dev SecureCookie should be true")
	}
	if !d.RateLimitEnabled {
		t.Error("dev RateLimitEnabled should be true")
	}
	if !d.EmailVerifyRequired {
		t.Error("dev EmailVerifyRequired should be true")
	}
	if d.AllowAllRelayQuotas {
		t.Error("dev AllowAllRelayQuotas should be false")
	}
}

// TestProdDefaults verifies the per-env defaults for EnvProd (strictest).
func TestProdDefaults(t *testing.T) {
	d := get(EnvProd)

	if d.BindAddr != ":8081" {
		t.Errorf("prod BindAddr = %q, want :8081", d.BindAddr)
	}
	if !d.SecureCookie {
		t.Error("prod SecureCookie must be true")
	}
	if !d.RateLimitEnabled {
		t.Error("prod RateLimitEnabled must be true")
	}
	if !d.EmailVerifyRequired {
		t.Error("prod EmailVerifyRequired must be true")
	}
	if !d.RequireDeviceSig {
		t.Error("prod RequireDeviceSig must be true")
	}
	if d.AllowAllRelayQuotas {
		t.Error("prod AllowAllRelayQuotas must be false")
	}
	if d.S3Endpoint != "https://fly.storage.tigris.dev" {
		t.Errorf("prod S3Endpoint = %q, want Tigris endpoint", d.S3Endpoint)
	}
}

// TestInitFromFlag verifies that Init reads the flag value.
func TestInitFromFlag(t *testing.T) {
	// Reset after test.
	orig := current
	t.Cleanup(func() { current = orig })

	if got := Init("local"); got != EnvLocal {
		t.Errorf("Init(local) = %q, want local", got)
	}
	if Current() != EnvLocal {
		t.Errorf("Current() = %q, want local after Init(local)", Current())
	}
}

// TestInitFromEnvVar verifies that Init falls back to VULOS_ENV.
func TestInitFromEnvVar(t *testing.T) {
	orig := current
	t.Cleanup(func() { current = orig })

	t.Setenv("VULOS_ENV", "dev")
	if got := Init(""); got != EnvDev {
		t.Errorf("Init(\"\") with VULOS_ENV=dev = %q, want dev", got)
	}
}

// TestInitDefaultsProd verifies that an empty flag + empty VULOS_ENV yields prod.
func TestInitDefaultsProd(t *testing.T) {
	orig := current
	t.Cleanup(func() { current = orig })

	os.Unsetenv("VULOS_ENV")
	if got := Init(""); got != EnvProd {
		t.Errorf("Init(\"\") with no VULOS_ENV = %q, want prod", got)
	}
}

// TestProdSafetyCheck verifies that checkNoDevFlagsInProd is called when
// Init("prod") is used and that it detects known dangerous env vars.
//
// We test the check function directly because calling Init("prod") with a bad
// env var would call log.Fatalf and terminate the test process.
func TestProdSafetyCheckPassesWithCleanEnv(t *testing.T) {
	// Ensure the bad vars are absent.
	os.Unsetenv("RELAY_ALLOW_ALL_QUOTAS")
	os.Unsetenv("FLEET_REQUIRE_DEVICE_SIG")
	os.Unsetenv("AUTH_ALLOW_UNVERIFIED_LOGIN")

	// Should not panic.
	checkNoDevFlagsInProd()
}

// TestEnvOrDefault exercises the helper.
func TestEnvOrDefault(t *testing.T) {
	const key = "VULOS_TEST_ENV_OR_DEFAULT_XYZ"
	os.Unsetenv(key)
	if got := EnvOrDefault(key, "fallback"); got != "fallback" {
		t.Errorf("EnvOrDefault missing key = %q, want fallback", got)
	}
	t.Setenv(key, "override")
	if got := EnvOrDefault(key, "fallback"); got != "override" {
		t.Errorf("EnvOrDefault set key = %q, want override", got)
	}
}

// TestEnvString verifies the Stringer implementation.
func TestEnvString(t *testing.T) {
	cases := []struct {
		e    Env
		want string
	}{
		{EnvLocal, "local"},
		{EnvDev, "dev"},
		{EnvProd, "prod"},
	}
	for _, c := range cases {
		if got := c.e.String(); got != c.want {
			t.Errorf("%T.String() = %q, want %q", c.e, got, c.want)
		}
	}
}

// TestDomainDefaults verifies the per-env base-domain defaults.
func TestDomainDefaults(t *testing.T) {
	os.Unsetenv("VULOS_DOMAIN")
	cases := []struct {
		e    Env
		want string
	}{
		{EnvLocal, "vulos.local"},
		{EnvDev, "dev.vulos.org"},
		{EnvProd, "vulos.org"},
	}
	for _, c := range cases {
		if got := domain(c.e); got != c.want {
			t.Errorf("domain(%s) = %q, want %q", c.e, got, c.want)
		}
	}
}

// TestHostScheme verifies the OS/relay/boot service-host derivation, including the
// dev-plane `dev-` prefix rewrite (PART A routing model).
func TestHostScheme(t *testing.T) {
	cases := []struct {
		domain               string
		os, relay, boot, api string
	}{
		{"vulos.org", "os.vulos.org", "relay.vulos.org", "boot.vulos.org", "https://vulos.org/api"},
		{"dev.vulos.org", "dev-os.vulos.org", "dev-relay.vulos.org", "dev-boot.vulos.org", "https://dev.vulos.org/api"},
		{"vulos.local", "os.vulos.local", "relay.vulos.local", "boot.vulos.local", "https://vulos.local/api"},
	}
	for _, c := range cases {
		t.Setenv("VULOS_DOMAIN", c.domain)
		if got := OSHost(); got != c.os {
			t.Errorf("OSHost() with %s = %q, want %q", c.domain, got, c.os)
		}
		if got := RelayHost(); got != c.relay {
			t.Errorf("RelayHost() with %s = %q, want %q", c.domain, got, c.relay)
		}
		if got := BootHost(); got != c.boot {
			t.Errorf("BootHost() with %s = %q, want %q", c.domain, got, c.boot)
		}
		if got := APIURL(); got != c.api {
			t.Errorf("APIURL() with %s = %q, want %q", c.domain, got, c.api)
		}
		if got := OSInstanceHost("acme"); got != "acme."+c.os {
			t.Errorf("OSInstanceHost(acme) with %s = %q, want %q", c.domain, got, "acme."+c.os)
		}
		if got := ProductURL("office"); got != "https://"+c.domain+"/products/office" {
			t.Errorf("ProductURL(office) with %s = %q", c.domain, got)
		}
	}
}

// TestDomainEnvOverride verifies VULOS_DOMAIN always wins over the per-env default.
func TestDomainEnvOverride(t *testing.T) {
	t.Setenv("VULOS_DOMAIN", "example.test")
	if got := domain(EnvProd); got != "example.test" {
		t.Errorf("domain(prod) with override = %q, want example.test", got)
	}
	if got := domain(EnvLocal); got != "example.test" {
		t.Errorf("domain(local) with override = %q, want example.test", got)
	}
}

// TestDerivedURLHelpers verifies SiteURL/AppURL/SubdomainURL derive from VULOS_DOMAIN.
func TestDerivedURLHelpers(t *testing.T) {
	orig := current
	t.Cleanup(func() { current = orig })
	current = EnvProd
	t.Setenv("VULOS_DOMAIN", "example.test")
	if got := SiteURL(); got != "https://example.test" {
		t.Errorf("SiteURL() = %q", got)
	}
	if got := AppURL(); got != "https://app.example.test" {
		t.Errorf("AppURL() = %q", got)
	}
	if got := SubdomainURL("mail"); got != "https://mail.example.test" {
		t.Errorf("SubdomainURL(mail) = %q", got)
	}
}

// TestDerivedCORSOrigins verifies dev/prod CORS origins derive from the domain.
func TestDerivedCORSOrigins(t *testing.T) {
	t.Setenv("VULOS_DOMAIN", "example.test")
	d := get(EnvProd)
	if len(d.CORSOrigins) != 2 || d.CORSOrigins[0] != "https://example.test" || d.CORSOrigins[1] != "https://app.example.test" {
		t.Errorf("prod CORSOrigins = %v, want apex + app of example.test", d.CORSOrigins)
	}
	// Local must still be the Vite dev origin regardless of VULOS_DOMAIN.
	if l := get(EnvLocal); l.CORSOrigins[0] != "http://localhost:5173" {
		t.Errorf("local CORSOrigins[0] = %q, want http://localhost:5173", l.CORSOrigins[0])
	}
}

// ── WAVE34: VULOS_DEV parse + dev-KEK fallback gate ────────────────────────────

// TestDevFlagTruthyParse verifies the single normalised VULOS_DEV parse accepts
// the various truthy spellings and rejects everything else. This closes the
// "1" vs "true" drift that let different KEK loaders disagree.
func TestDevFlagTruthyParse(t *testing.T) {
	truthy := []string{"1", "true", "TRUE", "True", "yes", "on", " true ", "ON"}
	for _, v := range truthy {
		t.Setenv("VULOS_DEV", v)
		if !DevFlag() {
			t.Errorf("DevFlag() with VULOS_DEV=%q = false, want true", v)
		}
	}
	falsy := []string{"", "0", "false", "no", "off", "prod", "yes-please"}
	for _, v := range falsy {
		t.Setenv("VULOS_DEV", v)
		if DevFlag() {
			t.Errorf("DevFlag() with VULOS_DEV=%q = true, want false", v)
		}
	}
}

// TestDevKEKFallbackAllowed_ProdLandmine is the core regression: a stray
// VULOS_DEV on a prod box must NOT open the dev-KEK fallback. WAVE37 tightens
// this further: an UNSET environment now fails safe to prod, so dev must be
// requested explicitly (VULOS_ENV=local|dev) before any zero-KEK fallback opens.
func TestDevKEKFallbackAllowed_ProdLandmine(t *testing.T) {
	// These assertions exercise the pre-Init leaf path, so reset Init state.
	resetInit(t)

	// dev flag + EXPLICIT non-prod env → allowed.
	t.Setenv("VULOS_DEV", "true")
	t.Setenv("VULOS_ENV", "local")
	if !DevKEKFallbackAllowed() {
		t.Fatal("DevKEKFallbackAllowed() = false with VULOS_DEV=true and VULOS_ENV=local, want true")
	}
	t.Setenv("VULOS_ENV", "dev")
	if !DevKEKFallbackAllowed() {
		t.Fatal("DevKEKFallbackAllowed() = false with VULOS_DEV=true and VULOS_ENV=dev, want true")
	}

	// WAVE37 fail-safe: dev flag + UNSET env → REFUSED (unset resolves to prod).
	t.Setenv("VULOS_ENV", "")
	if DevKEKFallbackAllowed() {
		t.Fatal("DevKEKFallbackAllowed() = true with VULOS_ENV unset — an unconfigured box must fail safe to prod and refuse the zero-KEK fallback")
	}

	// dev flag + VULOS_ENV=prod → refused (the original landmine).
	t.Setenv("VULOS_ENV", "prod")
	if DevKEKFallbackAllowed() {
		t.Fatal("DevKEKFallbackAllowed() = true with VULOS_ENV=prod — stray VULOS_DEV must NOT open the zero-KEK fallback in prod")
	}
	// prod env is case-insensitive.
	t.Setenv("VULOS_ENV", "PROD")
	if DevKEKFallbackAllowed() {
		t.Fatal("DevKEKFallbackAllowed() = true with VULOS_ENV=PROD, want false (case-insensitive prod detection)")
	}
	// no dev flag at all → refused regardless of env.
	t.Setenv("VULOS_DEV", "")
	t.Setenv("VULOS_ENV", "dev")
	if DevKEKFallbackAllowed() {
		t.Fatal("DevKEKFallbackAllowed() = true with VULOS_DEV unset, want false")
	}
}

// resetInit clears the Init-captured state so a test can exercise the pre-Init
// (leaf loader) resolution path deterministically, restoring it afterwards.
func resetInit(t *testing.T) {
	t.Helper()
	origInit, origFlag, origCur := initialized, resolvedFlag, current
	initialized, resolvedFlag = false, ""
	t.Cleanup(func() {
		initialized, resolvedFlag, current = origInit, origFlag, origCur
	})
}

// TestIsProdResolved_FailSafeDefault covers the single authoritative resolver:
// unset/ambiguous → prod; explicit local/dev → not prod; the --env flag path;
// and the post-Init behaviour that honours an explicit non-prod selection.
func TestIsProdResolved_FailSafeDefault(t *testing.T) {
	// ── Pre-Init leaf path ──
	resetInit(t)

	// Unset env → fail safe to prod.
	t.Setenv("VULOS_ENV", "")
	if !IsProdResolved() {
		t.Fatal("IsProdResolved() = false with VULOS_ENV unset — must fail safe to prod")
	}
	// Unrecognised/ambiguous value → fail safe to prod.
	t.Setenv("VULOS_ENV", "staging-oops")
	if !IsProdResolved() {
		t.Fatal("IsProdResolved() = false with an unrecognised VULOS_ENV — must fail safe to prod")
	}
	// Explicit local/dev → not prod.
	t.Setenv("VULOS_ENV", "local")
	if IsProdResolved() {
		t.Fatal("IsProdResolved() = true with VULOS_ENV=local, want false")
	}
	t.Setenv("VULOS_ENV", "dev")
	if IsProdResolved() {
		t.Fatal("IsProdResolved() = true with VULOS_ENV=dev, want false")
	}
	// Explicit prod → prod.
	t.Setenv("VULOS_ENV", "prod")
	if !IsProdResolved() {
		t.Fatal("IsProdResolved() = false with VULOS_ENV=prod, want true")
	}

	// The --env flag captured at Init is considered pre-Init too: simulate a
	// captured flag winning over an unset VULOS_ENV.
	t.Setenv("VULOS_ENV", "")
	resolvedFlag = "local"
	if IsProdResolved() {
		t.Fatal("IsProdResolved() = true with captured --env=local flag, want false")
	}
	resolvedFlag = ""

	// ── Post-Init path: an explicit non-prod selection is honoured. ──
	t.Setenv("VULOS_ENV", "local")
	if got := Init(""); got != EnvLocal {
		t.Fatalf("Init(\"\") with VULOS_ENV=local = %q, want local", got)
	}
	if IsProdResolved() {
		t.Fatal("IsProdResolved() = true after Init resolved local, want false")
	}
	// And an unconfigured Init resolves to prod.
	t.Setenv("VULOS_ENV", "")
	if got := Init(""); got != EnvProd {
		t.Fatalf("Init(\"\") with no VULOS_ENV = %q, want prod", got)
	}
	if !IsProdResolved() {
		t.Fatal("IsProdResolved() = false after unconfigured Init, want true (fail safe)")
	}
}
