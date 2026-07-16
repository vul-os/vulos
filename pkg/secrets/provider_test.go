package secrets_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/secrets"
)

// ---------------------------------------------------------------------------
// EnvProvider parity with os.Getenv
// ---------------------------------------------------------------------------

// TestEnvProvider_MatchesOsGetenv verifies the env-backed provider preserves
// os.Getenv behaviour exactly for set, unset, and empty values.
func TestEnvProvider_MatchesOsGetenv(t *testing.T) {
	const (
		setKey   = "VULOS_TEST_PROVIDER_SET"
		emptyKey = "VULOS_TEST_PROVIDER_EMPTY"
		unsetKey = "VULOS_TEST_PROVIDER_UNSET" // never set
	)
	t.Setenv(setKey, "hello-world")
	t.Setenv(emptyKey, "")

	p := secrets.NewEnvProvider()
	ctx := context.Background()

	// Set value.
	got, err := p.Get(ctx, setKey)
	if err != nil {
		t.Errorf("Get(set): unexpected error %v", err)
	}
	if got != os.Getenv(setKey) {
		t.Errorf("Get(set)=%q want %q (os.Getenv)", got, os.Getenv(setKey))
	}
	if !p.Has(ctx, setKey) {
		t.Errorf("Has(set)=false, want true")
	}

	// Empty value — must behave like os.Getenv ("" + not found).
	got, err = p.Get(ctx, emptyKey)
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Errorf("Get(empty): want ErrSecretNotFound, got %v", err)
	}
	if got != "" {
		t.Errorf("Get(empty)=%q want %q", got, "")
	}
	if p.Has(ctx, emptyKey) {
		t.Errorf("Has(empty)=true, want false")
	}

	// Unset value.
	got, err = p.Get(ctx, unsetKey)
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Errorf("Get(unset): want ErrSecretNotFound, got %v", err)
	}
	if got != "" {
		t.Errorf("Get(unset)=%q want %q", got, "")
	}
	if p.Has(ctx, unsetKey) {
		t.Errorf("Has(unset)=true, want false")
	}
}

// TestGetOrEmpty_MatchesOsGetenv verifies the migration helper collapses
// errors to "" so it is a true drop-in replacement for os.Getenv.
func TestGetOrEmpty_MatchesOsGetenv(t *testing.T) {
	t.Setenv("VULOS_TEST_GOE_SET", "value")
	t.Setenv("VULOS_TEST_GOE_EMPTY", "")
	ctx := context.Background()
	p := secrets.NewEnvProvider()

	cases := []struct {
		key  string
		want string
	}{
		{"VULOS_TEST_GOE_SET", "value"},
		{"VULOS_TEST_GOE_EMPTY", ""},
		{"VULOS_TEST_GOE_UNSET", ""},
	}
	for _, c := range cases {
		if got := secrets.GetOrEmpty(ctx, p, c.key); got != c.want {
			t.Errorf("GetOrEmpty(%q)=%q want %q", c.key, got, c.want)
		}
		// Parity with os.Getenv.
		if osVal := os.Getenv(c.key); secrets.GetOrEmpty(ctx, p, c.key) != osVal {
			t.Errorf("GetOrEmpty(%q) != os.Getenv(%q)", c.key, c.key)
		}
	}

	// Nil-provider safety: GetOrEmpty falls back to os.Getenv.
	if got := secrets.GetOrEmpty(ctx, nil, "VULOS_TEST_GOE_SET"); got != "value" {
		t.Errorf("GetOrEmpty(nil)=%q want value", got)
	}
}

// ---------------------------------------------------------------------------
// EnvProvider Watch fires on rotation
// ---------------------------------------------------------------------------

func TestEnvProvider_WatchFiresOnRotation(t *testing.T) {
	// Shrink the env-watch interval for the test (package-scoped variable).
	prev := secrets.SwapEnvWatchInterval(10 * time.Millisecond)
	defer secrets.SwapEnvWatchInterval(prev)

	const key = "VULOS_TEST_WATCH_KEY"
	t.Setenv(key, "v1")

	p := secrets.NewEnvProvider()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fired := make(chan string, 4)
	p.Watch(ctx, key, func(v string) { fired <- v })

	// Give the Watch goroutine a tick to snapshot the initial value, then
	// rotate; without the pause the rotation can race in before the
	// goroutine reads `last`.
	time.Sleep(30 * time.Millisecond)
	t.Setenv(key, "v2")

	select {
	case v := <-fired:
		if v != "v2" {
			t.Errorf("Watch fired with %q, want %q", v, "v2")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not fire within 2s after rotation")
	}
}

// ---------------------------------------------------------------------------
// Default() honors CP_KMS_PROVIDER
// ---------------------------------------------------------------------------

func TestDefault_Selection(t *testing.T) {
	// Unset → env provider.
	t.Setenv("CP_KMS_PROVIDER", "")
	if _, ok := secrets.Default().(*secrets.EnvProvider); !ok {
		t.Errorf("Default() unset: want *EnvProvider, got %T", secrets.Default())
	}

	// "env" → env provider.
	t.Setenv("CP_KMS_PROVIDER", "env")
	if _, ok := secrets.Default().(*secrets.EnvProvider); !ok {
		t.Errorf("Default() env: want *EnvProvider, got %T", secrets.Default())
	}

	// "vault" + URL → KMS provider.
	t.Setenv("CP_KMS_PROVIDER", "vault")
	t.Setenv("CP_KMS_URL", "https://vault.example:8200")
	t.Setenv("CP_KMS_TOKEN", "test-token")
	if _, ok := secrets.Default().(*secrets.KMSProvider); !ok {
		t.Errorf("Default() vault: want *KMSProvider, got %T", secrets.Default())
	}

	// "vault" without URL → falls back to env.
	t.Setenv("CP_KMS_PROVIDER", "vault")
	t.Setenv("CP_KMS_URL", "")
	if _, ok := secrets.Default().(*secrets.EnvProvider); !ok {
		t.Errorf("Default() vault w/o URL: want *EnvProvider fallback, got %T", secrets.Default())
	}

	// Unknown value → env fallback (warned).
	t.Setenv("CP_KMS_PROVIDER", "bogus")
	if _, ok := secrets.Default().(*secrets.EnvProvider); !ok {
		t.Errorf("Default() bogus: want *EnvProvider, got %T", secrets.Default())
	}
}

// ---------------------------------------------------------------------------
// KMSProvider round-trip against httptest.Server
// ---------------------------------------------------------------------------

func TestKMSProvider_RoundTrip(t *testing.T) {
	const key = "CP_SHARED_SECRET"
	const value = "vault-served-secret-12345"

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if !strings.HasSuffix(r.URL.Path, "/v1/secret/data/"+key) {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("X-Vault-Token"); got != "tok" {
			t.Errorf("expected X-Vault-Token=tok, got %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"data": map[string]string{"value": value},
			},
		})
	}))
	defer srv.Close()

	p := secrets.NewKMSProvider(srv.URL, "tok", 50*time.Millisecond, nil)
	ctx := context.Background()

	got, err := p.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != value {
		t.Errorf("Get=%q want %q", got, value)
	}
	if !p.Has(ctx, key) {
		t.Errorf("Has=false want true")
	}

	// Cache hit: second call within TTL should not produce an additional
	// upstream fetch.
	_, _ = p.Get(ctx, key)
	if h := atomic.LoadInt32(&hits); h != 1 {
		t.Errorf("expected 1 upstream hit (cached), got %d", h)
	}
}

// TestKMSProvider_NotFound exercises 404 → ErrSecretNotFound mapping.
func TestKMSProvider_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	p := secrets.NewKMSProvider(srv.URL, "tok", time.Second, nil)
	_, err := p.Get(context.Background(), "MISSING")
	if !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Errorf("want ErrSecretNotFound, got %v", err)
	}
	if p.Has(context.Background(), "MISSING") {
		t.Error("Has(missing)=true, want false")
	}
}

// TestKMSProvider_WatchFiresOnRotation flips the upstream response after a
// few polls and verifies Watch delivers the new value.
func TestKMSProvider_WatchFiresOnRotation(t *testing.T) {
	const key = "ROTATING"
	var mu sync.Mutex
	current := "v1"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		v := current
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"data": map[string]string{"value": v},
			},
		})
	}))
	defer srv.Close()

	p := secrets.NewKMSProvider(srv.URL, "tok", 20*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fired := make(chan string, 4)
	p.Watch(ctx, key, func(v string) { fired <- v })

	// Give the initial Get inside Watch a moment, then rotate upstream.
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	current = "v2"
	mu.Unlock()

	select {
	case v := <-fired:
		if v != "v2" {
			t.Errorf("Watch fired with %q want v2", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not fire within 2s")
	}
}

// TestProvider_MissingNoPanic confirms that calling Get on missing keys
// returns a typed error rather than panicking, for both providers.
func TestProvider_MissingNoPanic(t *testing.T) {
	ctx := context.Background()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on missing secret: %v", r)
		}
	}()

	envP := secrets.NewEnvProvider()
	if _, err := envP.Get(ctx, "DEFINITELY_NOT_SET_XYZ"); !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Errorf("EnvProvider missing: want ErrSecretNotFound, got %v", err)
	}

	// KMS provider pointed at a dead server returns a non-panic error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()
	kmsP := secrets.NewKMSProvider(srv.URL, "", time.Second, nil)
	if _, err := kmsP.Get(ctx, "ANYTHING"); !errors.Is(err, secrets.ErrSecretNotFound) {
		t.Errorf("KMSProvider missing: want ErrSecretNotFound, got %v", err)
	}
}
