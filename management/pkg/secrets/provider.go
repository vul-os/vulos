// SecretsProvider abstraction (RISK-SEC-01).
//
// This file defines a narrow interface for reading externally-managed secrets
// (e.g. CP_SHARED_SECRET, PAYSTACK_SECRET_KEY, SESSION_SECRET, OTA signing
// keys) so the source can be swapped between os.Getenv (today) and a real KMS
// (Vault, cloud-KMS) tomorrow without touching call-sites.
//
// IMPORTANT — this is a DIFFERENT concern from the existing Manager in
// secrets.go: Manager handles *rotation* of secrets the CP generates itself
// (SESSION_SECRET, JWT_SIGNING_KEY, etc.) and persists them encrypted at rest.
// The Provider here is about *sourcing* the values of operator-supplied
// secrets — the values that today live in env vars and tomorrow should live
// in a KMS. Call-sites that read os.Getenv("SOMETHING_SECRET") move to
// provider.Get; rotation/persistence remain Manager's job.
//
// The Provider interface is intentionally tiny:
//
//	Get(ctx, key)   — returns the secret value or ErrSecretNotFound.
//	Has(ctx, key)   — true if the key is currently set (non-empty).
//	Watch(ctx, key, fn) — fires fn(newValue) when the value rotates.
//
// Two implementations ship:
//
//   - EnvProvider — reads os.Getenv(key). PRESERVES CURRENT BEHAVIOUR
//     EXACTLY: missing/empty values return ("", ErrSecretNotFound); callers
//     that want the old "empty string" semantics just ignore the error.
//   - KMSProvider — stub HTTP client targeting a Vault KV-v2 endpoint
//     (CP_KMS_URL, CP_KMS_TOKEN). Caches with bounded TTL. Watch is poll-based
//     for now; a real KMS impl can subscribe via SSE.
//
// Default() chooses the env-backed impl unless CP_KMS_PROVIDER=vault is set.
package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Sentinel error
// ---------------------------------------------------------------------------

// ErrSecretNotFound is returned by Provider.Get when the requested key is not
// present (env unset/empty, or KMS 404). Callers that want the old
// os.Getenv "" semantics can simply ignore the error and use the empty value.
var ErrSecretNotFound = errors.New("secrets: not found")

// ---------------------------------------------------------------------------
// Provider interface
// ---------------------------------------------------------------------------

// SecretsProvider abstracts secret sourcing so call-sites do not need to know
// whether the value lives in an env var, a Vault path, or a cloud KMS.
type SecretsProvider interface {
	// Get returns the current value for key. Returns ErrSecretNotFound when
	// the key is absent or empty. The returned value is the unwrapped string
	// (KMS impls strip JSON envelope).
	Get(ctx context.Context, key string) (string, error)

	// Has reports whether the key currently has a non-empty value. It must
	// not return an error — implementations that cannot reach their backend
	// should report false (callers that need stronger guarantees should call
	// Get directly).
	Has(ctx context.Context, key string) bool

	// Watch invokes fn(newValue) whenever the value for key changes. The
	// callback runs on a background goroutine. Watch returns immediately;
	// when ctx is cancelled the watcher stops. Env impls poll the env var
	// (rotation under env is a process-restart, so this is effectively a
	// no-op once started); KMS impls poll the backend (a future real KMS
	// impl can subscribe to server-sent events instead).
	Watch(ctx context.Context, key string, fn func(string))
}

// ---------------------------------------------------------------------------
// Provider selection
// ---------------------------------------------------------------------------

// Default returns the SecretsProvider selected by CP_KMS_PROVIDER:
//
//	unset / "env"   → EnvProvider (preserves current behaviour exactly)
//	"vault"         → KMSProvider targeting CP_KMS_URL with CP_KMS_TOKEN
//	<anything else> → EnvProvider with a warning log
//
// Default never returns nil.
func Default() SecretsProvider {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CP_KMS_PROVIDER"))) {
	case "", "env":
		return NewEnvProvider()
	case "vault":
		url := strings.TrimRight(os.Getenv("CP_KMS_URL"), "/")
		token := os.Getenv("CP_KMS_TOKEN")
		if url == "" {
			log.Printf("[secrets] CP_KMS_PROVIDER=vault but CP_KMS_URL is unset — falling back to env provider")
			return NewEnvProvider()
		}
		return NewKMSProvider(url, token, 30*time.Second, nil)
	default:
		log.Printf("[secrets] unknown CP_KMS_PROVIDER=%q — falling back to env provider", os.Getenv("CP_KMS_PROVIDER"))
		return NewEnvProvider()
	}
}

// ---------------------------------------------------------------------------
// EnvProvider
// ---------------------------------------------------------------------------

// EnvProvider reads secrets from process environment variables.
// Its behaviour matches os.Getenv exactly so it can be a drop-in for existing
// call-sites: unset and empty are indistinguishable, and Get returns ("",
// ErrSecretNotFound) in both cases.
type EnvProvider struct {
	mu       sync.Mutex
	watchers map[string][]func(string)
}

// NewEnvProvider constructs an EnvProvider.
func NewEnvProvider() *EnvProvider {
	return &EnvProvider{watchers: make(map[string][]func(string))}
}

// Get returns os.Getenv(key) when non-empty, or ErrSecretNotFound otherwise.
func (e *EnvProvider) Get(_ context.Context, key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("%w: %q", ErrSecretNotFound, key)
	}
	return v, nil
}

// Has reports whether the env var is non-empty.
func (e *EnvProvider) Has(_ context.Context, key string) bool {
	return os.Getenv(key) != ""
}

// Watch records fn and starts a polling goroutine that checks the env var
// every 30s. Under env-backed deployments rotation is a process restart, so
// the watcher's primary value is uniform-API parity with KMS; it does still
// fire if the operator mutates os.Setenv at runtime (e.g. in tests).
func (e *EnvProvider) Watch(ctx context.Context, key string, fn func(string)) {
	if fn == nil {
		return
	}
	e.mu.Lock()
	e.watchers[key] = append(e.watchers[key], fn)
	e.mu.Unlock()

	go func() {
		last := os.Getenv(key)
		t := time.NewTicker(envWatchInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cur := os.Getenv(key)
				if cur != last && cur != "" {
					last = cur
					fn(cur)
				} else if cur != last {
					last = cur
				}
			}
		}
	}()
}

// envWatchInterval is the EnvProvider poll cadence. Exposed as a package
// variable so tests can shrink it via SwapEnvWatchInterval.
var envWatchInterval = 30 * time.Second

// SwapEnvWatchInterval atomically swaps the EnvProvider poll cadence and
// returns the previous value. Intended for tests; not for production use.
func SwapEnvWatchInterval(d time.Duration) time.Duration {
	prev := envWatchInterval
	envWatchInterval = d
	return prev
}

// ---------------------------------------------------------------------------
// KMSProvider (Vault KV-v2 stub)
// ---------------------------------------------------------------------------

// KMSProvider is a thin HTTP client for a Vault-shaped KV-v2 backend. It
// issues GET <url>/v1/secret/data/<key> with X-Vault-Token: <token> and parses
// the standard {data:{data:{value:"..."}}} response.
//
// This is a STUB for the production KMS migration (RISKS-HUMAN-TEAM.md §3,
// docs/KMS.md). The actual production rollout — auth method, namespace,
// secret-engine layout, SSE subscription — is human-team work.
type KMSProvider struct {
	url    string        // e.g. https://vault.internal:8200
	token  string        // X-Vault-Token
	ttl    time.Duration // cache TTL (Watch poll cadence)
	client *http.Client

	mu       sync.RWMutex
	cache    map[string]kmsEntry
	watchers map[string][]func(string)
}

type kmsEntry struct {
	value     string
	expiresAt time.Time
}

// vaultKVv2Response is the standard Vault KV-v2 read shape:
//
//	{
//	  "data": {
//	    "data": { "value": "..." }
//	  }
//	}
type vaultKVv2Response struct {
	Data struct {
		Data map[string]string `json:"data"`
	} `json:"data"`
}

// NewKMSProvider constructs a KMSProvider. ttl bounds cache lifetime + Watch
// poll cadence; pass 0 to use the default of 30s. If client is nil a
// reasonable default is created.
func NewKMSProvider(url, token string, ttl time.Duration, client *http.Client) *KMSProvider {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &KMSProvider{
		url:      url,
		token:    token,
		ttl:      ttl,
		client:   client,
		cache:    make(map[string]kmsEntry),
		watchers: make(map[string][]func(string)),
	}
}

// Get returns the cached value if fresh, otherwise fetches from the backend
// and updates the cache. Returns ErrSecretNotFound on HTTP 404 or empty value.
func (k *KMSProvider) Get(ctx context.Context, key string) (string, error) {
	k.mu.RLock()
	if e, ok := k.cache[key]; ok && time.Now().Before(e.expiresAt) {
		k.mu.RUnlock()
		if e.value == "" {
			return "", fmt.Errorf("%w: %q", ErrSecretNotFound, key)
		}
		return e.value, nil
	}
	k.mu.RUnlock()

	val, err := k.fetch(ctx, key)
	if err != nil {
		return "", err
	}

	k.mu.Lock()
	k.cache[key] = kmsEntry{value: val, expiresAt: time.Now().Add(k.ttl)}
	k.mu.Unlock()

	if val == "" {
		return "", fmt.Errorf("%w: %q", ErrSecretNotFound, key)
	}
	return val, nil
}

// Has issues Get and treats any error (including ErrSecretNotFound) as "no".
func (k *KMSProvider) Has(ctx context.Context, key string) bool {
	v, err := k.Get(ctx, key)
	return err == nil && v != ""
}

// Watch polls the backend at the cache TTL cadence and fires fn(newValue)
// when the value changes. A real KMS impl can swap this for an SSE/long-poll
// subscription — the interface contract is unchanged.
func (k *KMSProvider) Watch(ctx context.Context, key string, fn func(string)) {
	if fn == nil {
		return
	}
	k.mu.Lock()
	k.watchers[key] = append(k.watchers[key], fn)
	k.mu.Unlock()

	go func() {
		last, _ := k.Get(ctx, key)
		t := time.NewTicker(k.ttl)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				// Force a refetch by expiring the cache entry.
				k.mu.Lock()
				delete(k.cache, key)
				k.mu.Unlock()
				cur, err := k.Get(ctx, key)
				if err != nil {
					continue
				}
				if cur != last {
					last = cur
					fn(cur)
				}
			}
		}
	}()
}

// fetch issues GET /v1/secret/data/<key> and parses the KV-v2 envelope.
func (k *KMSProvider) fetch(ctx context.Context, key string) (string, error) {
	u := k.url + "/v1/secret/data/" + key
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("secrets: kms request: %w", err)
	}
	if k.token != "" {
		req.Header.Set("X-Vault-Token", k.token)
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("secrets: kms fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%w: %q", ErrSecretNotFound, key)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("secrets: kms %s status %d: %s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed vaultKVv2Response
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("secrets: kms decode: %w", err)
	}
	// The KV-v2 layout puts the user payload under data.data. We accept the
	// canonical "value" field; future versions can expose richer maps.
	if v, ok := parsed.Data.Data["value"]; ok {
		return v, nil
	}
	return "", fmt.Errorf("%w: %q (no 'value' field in response)", ErrSecretNotFound, key)
}

// ---------------------------------------------------------------------------
// Helper: GetOrEmpty
// ---------------------------------------------------------------------------

// GetOrEmpty is a thin convenience for migrating os.Getenv call-sites where
// the existing behaviour is "treat missing as empty string and let the caller
// decide". It preserves that behaviour exactly: any error (including
// ErrSecretNotFound) collapses to "".
//
// Call-sites that need to distinguish missing-vs-present should use Get
// directly.
func GetOrEmpty(ctx context.Context, p SecretsProvider, key string) string {
	if p == nil {
		return os.Getenv(key)
	}
	v, err := p.Get(ctx, key)
	if err != nil {
		return ""
	}
	return v
}
