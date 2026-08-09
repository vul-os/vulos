package llmuxclient

// byok_test.go — the account token still selects BYOK vs the central key.
//
// On the remote backend the OS sends LLMUX_KEY as a bearer token and llmux
// resolves it to an account, whose own provider key (if it has registered one)
// is used instead of the operator's central key — and that request is then
// unmetered. Embedding llmux replaces that hop with Gateway.Authorize, so the
// token has to travel the same distance in-process or a BYOK account would
// silently start spending the operator's key.
//
// The proof is what arrives at the upstream: these tests read the Authorization
// header the provider actually received.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/vul-os/llmux/core/pricing"
)

// testKEK is 64 hex chars = 32 bytes, the BYOK key-encryption key.
const testKEK = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// keyRecordingProvider is an upstream that records the Authorization header of
// the last request it served.
type keyRecordingProvider struct {
	*httptest.Server
	mu   sync.Mutex
	seen string
}

func newKeyRecordingProvider(t *testing.T) *keyRecordingProvider {
	t.Helper()
	p := &keyRecordingProvider{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.seen = r.Header.Get("Authorization")
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamUnary))
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *keyRecordingProvider) lastAuth() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen
}

// writeKeyedConfig writes a config with a credential wall (one static virtual
// key) and BYOK enabled, so Authorize has something to resolve and dispatch has
// somewhere to look up an account's own key.
func writeKeyedConfig(t *testing.T, upstream string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "llmux.json")
	cfg := `{
	  "log_level": "error",
	  "providers": [{"name": "fake", "type": "passthrough", "base_url": "` + upstream + `/v1", "api_key": "central-key"}],
	  "routes": [{"model": "*", "provider": "fake"}],
	  "keys": [{"key": "vk-account-token", "name": "acct-1"}],
	  "byok": {"kek": "` + testKEK + `"},
	  "pricing": {"sources": []}
	}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func newKeyedEmbedded(t *testing.T, upstream, token string) *Embedded {
	t.Helper()
	isolateEnv(t)
	e, err := NewEmbedded(context.Background(), EmbeddedOptions{
		ConfigPath: writeKeyedConfig(t, upstream),
		Token:      token,
	})
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

const unaryChatBody = `{"model":"fake-1","messages":[{"role":"user","content":"hi"}]}`

// TestEmbedded_AccountTokenSelectsBYOKKey is the BYOK half: an account that has
// registered its own provider key gets that key on the wire, not the central one.
func TestEmbedded_AccountTokenSelectsBYOKKey(t *testing.T) {
	upstream := newKeyRecordingProvider(t)
	e := newKeyedEmbedded(t, upstream.URL, "vk-account-token")

	store := e.Gateway().BYOK()
	if store == nil {
		t.Fatal("BYOK store not wired despite a configured KEK — every request would use the central key")
	}
	if err := store.Set("acct-1", "fake", "the-accounts-own-key"); err != nil {
		t.Fatalf("register BYOK key: %v", err)
	}

	rec := postJSON(t, muxFor(e), "/api/ai/chat", unaryChatBody)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got, want := upstream.lastAuth(), "Bearer the-accounts-own-key"; got != want {
		t.Fatalf("upstream saw %q, want %q — the account token did not reach BYOK resolution", got, want)
	}
}

// TestEmbedded_NoBYOKKeyFallsBackToCentral is the control: the SAME token, with
// nothing registered, must use the operator's central key. Without it the test
// above could pass on a gateway that ignored central keys entirely.
func TestEmbedded_NoBYOKKeyFallsBackToCentral(t *testing.T) {
	upstream := newKeyRecordingProvider(t)
	e := newKeyedEmbedded(t, upstream.URL, "vk-account-token")

	rec := postJSON(t, muxFor(e), "/api/ai/chat", unaryChatBody)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got, want := upstream.lastAuth(), "Bearer central-key"; got != want {
		t.Fatalf("upstream saw %q, want %q", got, want)
	}
}

// TestEmbedded_UnknownAccountTokenIsRefused proves Authorize is actually
// consulted in-process: with a credential wall configured, a wrong token must
// not reach the provider at all. An embedder that skipped Authorize would get a
// laxer check than a network client.
func TestEmbedded_UnknownAccountTokenIsRefused(t *testing.T) {
	upstream := newKeyRecordingProvider(t)
	e := newKeyedEmbedded(t, upstream.URL, "vk-wrong-token")

	rec := postJSON(t, muxFor(e), "/api/ai/chat", unaryChatBody)
	if rec.Code == 200 {
		t.Fatalf("an unknown account token was served: %s", rec.Body.String())
	}
	if upstream.lastAuth() != "" {
		t.Fatalf("the provider was called despite an unknown account token (saw %q)", upstream.lastAuth())
	}
}

// TestEmbedded_RefusesUnmeterableOnABudgetedKey shows llmux's fail-closed
// metering guard is live in-process: a budgeted key may not be served a model
// the catalog cannot price, because the spend would go uncounted and the budget
// would never trip. Embedding the gateway must not have bypassed it.
func TestEmbedded_RefusesUnmeterableOnABudgetedKey(t *testing.T) {
	upstream := newKeyRecordingProvider(t)
	e := newBudgetedEmbedded(t, upstream.URL, false)

	rec := postJSON(t, muxFor(e), "/api/ai/chat", unaryChatBody)
	if rec.Code == 200 {
		t.Fatalf("an unpriced model was served against a budgeted key: %s", rec.Body.String())
	}
	if upstream.lastAuth() != "" {
		t.Fatalf("the provider was called despite the pre-flight refusal")
	}
}

// TestEmbedded_AuthorizeReleasesItsBudgetHold pins the one contract in
// Gateway.Authorize that leaks silently when broken: the returned release frees
// the budget gate's in-flight reservation, and must be called for every request
// whatever the outcome.
//
// The key's budget (0.06 USD) covers exactly one 0.05 StaticReservationHold at
// a time, and the priced model costs a rounding error per call, so the only way
// five sequential requests all succeed is if each one's hold was released.
func TestEmbedded_AuthorizeReleasesItsBudgetHold(t *testing.T) {
	upstream := newKeyRecordingProvider(t)
	e := newBudgetedEmbedded(t, upstream.URL, true)

	mux := muxFor(e)
	for i := 0; i < 5; i++ {
		rec := postJSON(t, mux, "/api/ai/chat", unaryChatBody)
		if rec.Code != 200 {
			t.Fatalf("request %d got %d (%s) — a budget hold was not released", i+1, rec.Code, rec.Body.String())
		}
	}
}

// newBudgetedEmbedded builds an embedded gateway whose single key carries a
// budget just large enough for one in-flight reservation.
func newBudgetedEmbedded(t *testing.T, upstream string, priced bool) *Embedded {
	t.Helper()
	isolateEnv(t)
	path := filepath.Join(t.TempDir(), "llmux.json")
	cfg := `{
	  "log_level": "error",
	  "providers": [{"name": "fake", "type": "passthrough", "base_url": "` + upstream + `/v1", "api_key": "central-key"}],
	  "routes": [{"model": "*", "provider": "fake"}],
	  "keys": [{"key": "vk-account-token", "name": "acct-1", "budget_usd": 0.06}],
	  "pricing": {"sources": []}
	}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	e, err := NewEmbedded(context.Background(), EmbeddedOptions{ConfigPath: path, Token: "vk-account-token"})
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if priced {
		// NOTE: a config FILE cannot supply this. llmux's config.merge copies
		// pricing.catalog_path, sync_interval_minutes and sources from a file but
		// NOT overrides / override_path / azure_pricing, so file-supplied price
		// overrides are silently dropped. Inject through the catalog's own API.
		e.Gateway().Catalog().SetSource(pricing.SourceNameOverride, pricing.PriorityOverride,
			map[string]pricing.Price{
				"fake-1": {Model: "fake-1", Provider: "fake", InputPerMTok: 0.001, OutputPerMTok: 0.001},
			})
	}
	return e
}
