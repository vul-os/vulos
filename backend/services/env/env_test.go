package env_test

import (
	"os"
	"testing"

	"vulos/backend/services/env"
)

// TestParse verifies the three valid strings, the VULOS_ENV fallback, the
// empty-string default, and the error path for invalid inputs.
func TestParse(t *testing.T) {
	t.Run("explicit local", func(t *testing.T) {
		e, err := env.Parse("local")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e != env.EnvLocal {
			t.Fatalf("want EnvLocal, got %q", e)
		}
	})

	t.Run("explicit dev", func(t *testing.T) {
		e, err := env.Parse("dev")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e != env.EnvDev {
			t.Fatalf("want EnvDev, got %q", e)
		}
	})

	t.Run("explicit prod", func(t *testing.T) {
		e, err := env.Parse("prod")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e != env.EnvProd {
			t.Fatalf("want EnvProd, got %q", e)
		}
	})

	t.Run("empty string defaults to prod", func(t *testing.T) {
		os.Unsetenv("VULOS_ENV")
		e, err := env.Parse("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e != env.EnvProd {
			t.Fatalf("want EnvProd as default, got %q", e)
		}
	})

	t.Run("VULOS_ENV fallback", func(t *testing.T) {
		os.Setenv("VULOS_ENV", "local")
		defer os.Unsetenv("VULOS_ENV")
		e, err := env.Parse("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e != env.EnvLocal {
			t.Fatalf("want EnvLocal from VULOS_ENV, got %q", e)
		}
	})

	t.Run("VULOS_ENV dev fallback", func(t *testing.T) {
		os.Setenv("VULOS_ENV", "dev")
		defer os.Unsetenv("VULOS_ENV")
		e, err := env.Parse("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e != env.EnvDev {
			t.Fatalf("want EnvDev from VULOS_ENV, got %q", e)
		}
	})

	t.Run("explicit flag overrides VULOS_ENV", func(t *testing.T) {
		os.Setenv("VULOS_ENV", "local")
		defer os.Unsetenv("VULOS_ENV")
		e, err := env.Parse("prod")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e != env.EnvProd {
			t.Fatalf("want EnvProd (explicit flag wins over env var), got %q", e)
		}
	})

	t.Run("invalid value returns error", func(t *testing.T) {
		_, err := env.Parse("staging")
		if err == nil {
			t.Fatal("expected error for invalid env value, got nil")
		}
	})

	t.Run("invalid VULOS_ENV returns error", func(t *testing.T) {
		os.Setenv("VULOS_ENV", "nope")
		defer os.Unsetenv("VULOS_ENV")
		_, err := env.Parse("")
		if err == nil {
			t.Fatal("expected error for invalid VULOS_ENV value, got nil")
		}
	})
}

// TestDefaultsFor validates that each environment returns the correct
// behavioural settings.
func TestDefaultsFor(t *testing.T) {
	t.Run("local defaults", func(t *testing.T) {
		d := env.DefaultsFor(env.EnvLocal)
		if d.BindHost != "127.0.0.1" {
			t.Errorf("BindHost: want 127.0.0.1, got %q", d.BindHost)
		}
		if !d.SkipHardwareChecks {
			t.Error("SkipHardwareChecks should be true for local")
		}
		if !d.AllowSelfSignedCerts {
			t.Error("AllowSelfSignedCerts should be true for local")
		}
		if d.StrictCookies {
			t.Error("StrictCookies should be false for local")
		}
		if !d.DebugEndpoints {
			t.Error("DebugEndpoints should be true for local")
		}
		if d.AllowStagingBrokerKey {
			t.Error("AllowStagingBrokerKey should be false for local")
		}
	})

	t.Run("dev defaults", func(t *testing.T) {
		d := env.DefaultsFor(env.EnvDev)
		if d.BindHost != "127.0.0.1" {
			t.Errorf("BindHost: want 127.0.0.1, got %q", d.BindHost)
		}
		if !d.SkipHardwareChecks {
			t.Error("SkipHardwareChecks should be true for dev")
		}
		if !d.AllowSelfSignedCerts {
			t.Error("AllowSelfSignedCerts should be true for dev")
		}
		if d.StrictCookies {
			t.Error("StrictCookies should be false for dev")
		}
		if d.DebugEndpoints {
			t.Error("DebugEndpoints should be false for dev")
		}
		if !d.AllowStagingBrokerKey {
			t.Error("AllowStagingBrokerKey should be true for dev")
		}
	})

	t.Run("prod defaults", func(t *testing.T) {
		d := env.DefaultsFor(env.EnvProd)
		if d.BindHost != "" {
			t.Errorf("BindHost: want empty (all interfaces), got %q", d.BindHost)
		}
		if d.SkipHardwareChecks {
			t.Error("SkipHardwareChecks should be false for prod")
		}
		if d.AllowSelfSignedCerts {
			t.Error("AllowSelfSignedCerts should be false for prod")
		}
		if !d.StrictCookies {
			t.Error("StrictCookies should be true for prod")
		}
		if d.DebugEndpoints {
			t.Error("DebugEndpoints should be false for prod")
		}
		if d.AllowStagingBrokerKey {
			t.Error("AllowStagingBrokerKey should be false for prod")
		}
	})
}

// TestEnvHelpers verifies the convenience predicate methods.
func TestEnvHelpers(t *testing.T) {
	if !env.EnvLocal.IsLocal() {
		t.Error("EnvLocal.IsLocal() should be true")
	}
	if env.EnvLocal.IsDev() {
		t.Error("EnvLocal.IsDev() should be false")
	}
	if env.EnvLocal.IsProd() {
		t.Error("EnvLocal.IsProd() should be false")
	}

	if !env.EnvDev.IsDev() {
		t.Error("EnvDev.IsDev() should be true")
	}
	if env.EnvDev.IsLocal() {
		t.Error("EnvDev.IsLocal() should be false")
	}
	if env.EnvDev.IsProd() {
		t.Error("EnvDev.IsProd() should be false")
	}

	if !env.EnvProd.IsProd() {
		t.Error("EnvProd.IsProd() should be true")
	}
	if env.EnvProd.IsLocal() {
		t.Error("EnvProd.IsLocal() should be false")
	}
	if env.EnvProd.IsDev() {
		t.Error("EnvProd.IsDev() should be false")
	}
}

// TestString verifies that String() round-trips correctly.
func TestString(t *testing.T) {
	cases := []struct {
		e    env.Env
		want string
	}{
		{env.EnvLocal, "local"},
		{env.EnvDev, "dev"},
		{env.EnvProd, "prod"},
	}
	for _, c := range cases {
		if got := c.e.String(); got != c.want {
			t.Errorf("String() for %q: want %q, got %q", c.e, c.want, got)
		}
	}
}

// TestFirstNonEmptyEnv covers the alias-fallback helper used to unify the
// LLM/llmux seam variable (LLMUX_URL canonical, VULOS_LLMUX_URL alias).
func TestFirstNonEmptyEnv(t *testing.T) {
	const canon, alias = "TEST_LLMUX_URL", "TEST_VULOS_LLMUX_URL"

	t.Run("canonical only", func(t *testing.T) {
		t.Setenv(canon, "http://a:4000")
		t.Setenv(alias, "")
		if got := env.FirstNonEmptyEnv(canon, alias); got != "http://a:4000" {
			t.Fatalf("want canonical value, got %q", got)
		}
	})

	t.Run("alias only", func(t *testing.T) {
		t.Setenv(canon, "")
		t.Setenv(alias, "http://b:4000")
		if got := env.FirstNonEmptyEnv(canon, alias); got != "http://b:4000" {
			t.Fatalf("want alias value, got %q", got)
		}
	})

	t.Run("canonical wins when both set", func(t *testing.T) {
		t.Setenv(canon, "http://a:4000")
		t.Setenv(alias, "http://b:4000")
		if got := env.FirstNonEmptyEnv(canon, alias); got != "http://a:4000" {
			t.Fatalf("first key must win, got %q", got)
		}
	})

	t.Run("neither set", func(t *testing.T) {
		t.Setenv(canon, "")
		t.Setenv(alias, "")
		if got := env.FirstNonEmptyEnv(canon, alias); got != "" {
			t.Fatalf("want empty, got %q", got)
		}
	})

	t.Run("whitespace is trimmed and skipped", func(t *testing.T) {
		t.Setenv(canon, "   ")
		t.Setenv(alias, "  http://b:4000  ")
		if got := env.FirstNonEmptyEnv(canon, alias); got != "http://b:4000" {
			t.Fatalf("want trimmed alias value, got %q", got)
		}
	})
}
