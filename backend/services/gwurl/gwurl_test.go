package gwurl

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// resetState restores package globals to a clean slate between tests.
func resetState(t *testing.T) {
	t.Helper()
	mu.Lock()
	configured = ""
	persistPath = ""
	mu.Unlock()
	envLookup = os.Getenv
	checkReachable = defaultCheckReachable
	t.Cleanup(func() {
		mu.Lock()
		configured = ""
		persistPath = ""
		mu.Unlock()
		envLookup = os.Getenv
		checkReachable = defaultCheckReachable
	})
}

// withEnv installs a fake env lookup returning m, restoring the real one after.
func withEnv(t *testing.T, m map[string]string) {
	t.Helper()
	envLookup = func(k string) string { return m[k] }
	t.Cleanup(func() { envLookup = os.Getenv })
}

// stubProbe replaces the reachability probe with one that always accepts a valid
// URL — so persistence/authz paths can be exercised without a live public host
// (the real probe correctly refuses loopback httptest servers).
func stubProbe(t *testing.T) {
	t.Helper()
	checkReachable = func(_ context.Context, raw string) (string, error) {
		return Validate(raw)
	}
}

func TestResolved_DefaultWhenNothingSet(t *testing.T) {
	resetState(t)
	withEnv(t, nil)
	u, src := Resolved()
	if u != Default || src != SourceDefault {
		t.Fatalf("Resolved() = (%q, %q), want (%q, %q)", u, src, Default, SourceDefault)
	}
	if URL() != Default {
		t.Fatalf("URL() = %q, want %q", URL(), Default)
	}
}

func TestResolved_EnvPrecedence(t *testing.T) {
	resetState(t)
	// VULOS_CP_URL beats VULOS_CLOUD_URL beats VULOS_CLOUD_API_URL.
	withEnv(t, map[string]string{
		"VULOS_CP_URL":        "https://cp.example.org/",
		"VULOS_CLOUD_URL":     "https://cloud.example.org",
		"VULOS_CLOUD_API_URL": "https://api.example.org",
	})
	u, src := Resolved()
	if src != SourceEnv {
		t.Fatalf("source = %q, want env", src)
	}
	if u != "https://cp.example.org" { // trailing slash normalised
		t.Fatalf("url = %q, want https://cp.example.org", u)
	}

	// Fall through to the next var when the higher one is empty.
	withEnv(t, map[string]string{"VULOS_CLOUD_API_URL": "https://api.example.org"})
	if u, _ := Resolved(); u != "https://api.example.org" {
		t.Fatalf("url = %q, want https://api.example.org", u)
	}
}

func TestResolved_ConfiguredBeatsEnv(t *testing.T) {
	resetState(t)
	withEnv(t, map[string]string{"VULOS_CLOUD_URL": "https://env.example.org"})
	mu.Lock()
	configured = "https://configured.example.org"
	mu.Unlock()
	u, src := Resolved()
	if src != SourceConfigured || u != "https://configured.example.org" {
		t.Fatalf("Resolved() = (%q, %q), want configured override", u, src)
	}
}

func TestValidate(t *testing.T) {
	resetState(t)
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"https://cp.vulos.org", "https://cp.vulos.org", false},
		{"https://cp.vulos.org/", "https://cp.vulos.org", false},
		{"  https://cp.vulos.org  ", "https://cp.vulos.org", false},
		{"https://cp.vulos.org:8443", "https://cp.vulos.org:8443", false},
		{"http://cp.vulos.org", "", true},      // plaintext rejected
		{"", "", true},                         // empty rejected
		{"https://", "", true},                 // no host
		{"https://cp.vulos.org/api", "", true}, // path rejected
		{"ftp://cp.vulos.org", "", true},       // wrong scheme
		{"not a url", "", true},
	}
	for _, c := range cases {
		got, err := Validate(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("Validate(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Validate(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Validate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCheckReachable_Unreachable(t *testing.T) {
	resetState(t)
	// A syntactically valid https host that does not resolve/answer.
	if _, err := CheckReachable(context.Background(), "https://gateway.invalid.example.test"); err == nil {
		t.Fatal("CheckReachable accepted an unreachable host, want error")
	}
}

func TestCheckReachable_InvalidRejectedBeforeDial(t *testing.T) {
	resetState(t)
	if _, err := CheckReachable(context.Background(), "http://insecure.example.org"); err == nil {
		t.Fatal("CheckReachable accepted a plaintext URL, want error")
	}
}

func TestCheckReachable_PrivateBlockedByDefault(t *testing.T) {
	resetState(t)
	withEnv(t, nil) // VULOS_GATEWAY_ALLOW_PRIVATE unset → private hosts denied
	if _, err := CheckReachable(context.Background(), "https://127.0.0.1:9"); err == nil {
		t.Fatal("CheckReachable accepted a loopback host without the LAN opt-in, want error")
	}
	if _, err := CheckReachable(context.Background(), "https://10.1.2.3:443"); err == nil {
		t.Fatal("CheckReachable accepted a private host without the LAN opt-in, want error")
	}
}

func TestSetAndReload(t *testing.T) {
	resetState(t)
	stubProbe(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	const want = "https://cp.myorg.example"
	if err := Set(context.Background(), want+"/"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, src := Resolved(); got != want || src != SourceConfigured {
		t.Fatalf("after Set: Resolved() = (%q, %q), want (%q, configured)", got, src, want)
	}
	// The override must survive a fresh Init (persistence).
	mu.Lock()
	configured = ""
	mu.Unlock()
	if err := Init(dir); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	if got := Configured(); got != want {
		t.Fatalf("after reload Configured() = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "gateway.json")); err != nil {
		t.Fatalf("gateway.json missing: %v", err)
	}

	// Clear reverts to default and removes the file.
	if err := Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, src := Resolved(); src != SourceDefault {
		t.Fatalf("after Clear source = %q, want default", src)
	}
	if _, err := os.Stat(filepath.Join(dir, "gateway.json")); !os.IsNotExist(err) {
		t.Fatalf("gateway.json should be removed after Clear, stat err = %v", err)
	}
}

func TestSet_InvalidRejectedAndNotPersisted(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := Set(context.Background(), "http://insecure.example.org"); err == nil {
		t.Fatal("Set accepted a plaintext URL, want error")
	}
	if Configured() != "" {
		t.Fatal("invalid Set must not persist an override")
	}
	if _, err := os.Stat(filepath.Join(dir, "gateway.json")); !os.IsNotExist(err) {
		t.Fatalf("no file should be written for an invalid Set, stat err = %v", err)
	}
}

func TestSet_RequiresInit(t *testing.T) {
	resetState(t)
	stubProbe(t)
	// persistPath is unset (no Init) → Set must fail closed after the probe.
	if err := Set(context.Background(), "https://cp.myorg.example"); err == nil {
		t.Fatal("Set without Init should fail, got nil")
	}
}
