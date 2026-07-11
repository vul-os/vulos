package deploymode

import (
	"os"
	"testing"
)

func withEnv(t *testing.T, key, val string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if val == "" {
		os.Unsetenv(key)
	} else {
		os.Setenv(key, val)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}

func TestFromEnv_UnsetDefaultsToStandalone(t *testing.T) {
	withEnv(t, EnvVar, "")
	m, err := FromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != Standalone {
		t.Fatalf("mode = %q, want %q", m, Standalone)
	}
}

func TestFromEnv_ValidValues(t *testing.T) {
	cases := []struct {
		in   string
		want Mode
	}{
		{"standalone", Standalone},
		{"os", OS},
		{"cloud", Cloud},
		{"OS", OS},    // case-insensitive
		{"Cloud", Cloud},
	}
	for _, c := range cases {
		withEnv(t, EnvVar, c.in)
		m, err := FromEnv()
		if err != nil {
			t.Fatalf("FromEnv(%q) unexpected error: %v", c.in, err)
		}
		if m != c.want {
			t.Fatalf("FromEnv(%q) = %q, want %q", c.in, m, c.want)
		}
	}
}

func TestFromEnv_InvalidFallsBackToStandaloneWithError(t *testing.T) {
	withEnv(t, EnvVar, "bogus")
	m, err := FromEnv()
	if err == nil {
		t.Fatal("expected an error for an invalid DEPLOY_MODE value")
	}
	if m != Standalone {
		t.Fatalf("mode = %q, want fallback %q", m, Standalone)
	}
}

func TestIsCloudAdjacent(t *testing.T) {
	if Standalone.IsCloudAdjacent() {
		t.Fatal("standalone must not be cloud-adjacent")
	}
	if !OS.IsCloudAdjacent() {
		t.Fatal("os must be cloud-adjacent")
	}
	if !Cloud.IsCloudAdjacent() {
		t.Fatal("cloud must be cloud-adjacent")
	}
}

func TestValid(t *testing.T) {
	if !Standalone.Valid() || !OS.Valid() || !Cloud.Valid() {
		t.Fatal("canonical modes must be valid")
	}
	if Mode("bogus").Valid() {
		t.Fatal("bogus mode must not be valid")
	}
}

// Load must never panic/fail regardless of config coherence — it is advisory.
func TestLoad_NeverFails(t *testing.T) {
	withEnv(t, EnvVar, "cloud")
	withEnv(t, "VULOS_CP_BASE_URL", "")
	if m := Load(); m != Cloud {
		t.Fatalf("Load() = %q, want %q", m, Cloud)
	}
}
