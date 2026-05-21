package appnet

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// ValidateConcurrency
// ---------------------------------------------------------------------------

func TestValidateConcurrency(t *testing.T) {
	valid := []string{"singleton", "replicated", "collaborative"}
	for _, v := range valid {
		if err := ValidateConcurrency(v); err != nil {
			t.Errorf("ValidateConcurrency(%q) unexpected error: %v", v, err)
		}
	}

	invalid := []string{"", "active-active", "SINGLETON", "passive", "cluster"}
	for _, v := range invalid {
		if err := ValidateConcurrency(v); err == nil {
			t.Errorf("ValidateConcurrency(%q) expected error, got nil", v)
		}
	}
}

// ---------------------------------------------------------------------------
// AppManifest.Validate — Concurrency field
// ---------------------------------------------------------------------------

// concurrencyManifest returns a Validate-ready manifest with the concurrency
// field set to the given value.  appDir is unused (IconPath is empty).
func concurrencyManifest(id, concurrency string) *AppManifest {
	return &AppManifest{
		ID:          id,
		Name:        "Test App",
		Version:     "1.0.0",
		Command:     "bin/server",
		Port:        8080,
		Description: "a test app",
		Concurrency: concurrency,
	}
}

func TestManifestValidate_ConcurrencyDefaultsSingleton(t *testing.T) {
	m := concurrencyManifest("test-app", "")
	if err := m.Validate("/tmp"); err != nil {
		t.Fatalf("Validate with empty concurrency: %v", err)
	}
	if m.Concurrency != ConcurrencySingleton {
		t.Errorf("Concurrency after Validate = %q, want %q", m.Concurrency, ConcurrencySingleton)
	}
}

func TestManifestValidate_ConcurrencyValid(t *testing.T) {
	for _, v := range ValidConcurrencies {
		m := concurrencyManifest("test-app", v)
		if err := m.Validate("/tmp"); err != nil {
			t.Errorf("Validate(concurrency=%q) unexpected error: %v", v, err)
		}
		if m.Concurrency != v {
			t.Errorf("Validate(concurrency=%q) changed value to %q", v, m.Concurrency)
		}
	}
}

func TestManifestValidate_ConcurrencyInvalid(t *testing.T) {
	for _, bad := range []string{"active-active", "SINGLETON", "passive", "cluster"} {
		m := concurrencyManifest("test-app", bad)
		if err := m.Validate("/tmp"); err == nil {
			t.Errorf("Validate(concurrency=%q) expected error, got nil", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// Round-trip: marshal/unmarshal preserves Concurrency
// ---------------------------------------------------------------------------

func TestManifestConcurrency_RoundTrip(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"singleton", "singleton"},
		{"replicated", "replicated"},
		{"collaborative", "collaborative"},
		// Empty input: Validate normalises to singleton, but raw JSON round-trip
		// (without Validate) preserves the empty string as-is.
		{"", ""},
	}

	for _, c := range cases {
		original := &AppManifest{
			ID:          "round-trip-app",
			Name:        "Round Trip",
			Version:     "0.1.0",
			Command:     "bin/server",
			Port:        9000,
			Description: "round trip test",
			Concurrency: c.input,
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal (concurrency=%q): %v", c.input, err)
		}

		var restored AppManifest
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatalf("unmarshal (concurrency=%q): %v", c.input, err)
		}

		if restored.Concurrency != c.want {
			t.Errorf("round-trip concurrency=%q: got %q, want %q", c.input, restored.Concurrency, c.want)
		}
	}
}

// TestManifestConcurrency_ValidateRoundTrip verifies that Validate both
// normalises an empty value to "singleton" and preserves explicit values
// through a full marshal → unmarshal → Validate cycle.
func TestManifestConcurrency_ValidateRoundTrip(t *testing.T) {
	for _, v := range ValidConcurrencies {
		m := concurrencyManifest("vrt-app", v)
		if err := m.Validate("/tmp"); err != nil {
			t.Fatalf("Validate(concurrency=%q): %v", v, err)
		}

		data, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal after Validate (concurrency=%q): %v", v, err)
		}

		var m2 AppManifest
		if err := json.Unmarshal(data, &m2); err != nil {
			t.Fatalf("unmarshal (concurrency=%q): %v", v, err)
		}
		if err := m2.Validate("/tmp"); err != nil {
			t.Fatalf("second Validate (concurrency=%q): %v", v, err)
		}
		if m2.Concurrency != v {
			t.Errorf("validate round-trip concurrency=%q: got %q", v, m2.Concurrency)
		}
	}
}
