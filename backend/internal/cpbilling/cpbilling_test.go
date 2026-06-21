package cpbilling

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient points a Client at a stub server with a fast TTL.
func newTestClient(base string) *Client {
	return New(Config{
		BaseURL:      base,
		SharedSecret: "test-secret",
		CacheTTL:     50 * time.Millisecond,
	})
}

func TestDisabledClientIsTransparentNoOp(t *testing.T) {
	c := New(Config{}) // no base url, no CP_URL
	if c.Enabled() {
		t.Fatal("client with no base url must be disabled")
	}
	d := c.Gate(context.Background(), "a@b.c", ProductRelay)
	if !d.Allowed || d.Reason != "disabled" {
		t.Fatalf("disabled gate = %+v, want allowed/disabled", d)
	}
	dl := c.GateLLM(context.Background(), "a@b.c")
	if !dl.Allowed {
		t.Fatalf("disabled GateLLM should allow, got %+v", dl)
	}
	// Meter must not panic / must be a no-op (no server to hit).
	c.Meter(context.Background(), UsageEvent{Product: ProductLLM, AccountID: "a@b.c"})
	c.MeterAsync(UsageEvent{Product: ProductLLM, AccountID: "a@b.c"})
}

func TestEntitlementGateAndSuspension(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Relay-Auth") != "test-secret" {
			t.Errorf("missing/wrong X-Relay-Auth header: %q", r.Header.Get("X-Relay-Auth"))
		}
		acct := r.URL.Query().Get("account_id")
		ent := Entitlement{Tier: "pro"}
		if acct == "suspended@x.com" {
			ent.Suspended = true
		}
		_ = json.NewEncoder(w).Encode(ent)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)

	if d := c.Gate(context.Background(), "ok@x.com", ProductRelay); !d.Allowed || d.Reason != "ok" {
		t.Fatalf("entitled gate = %+v, want allowed/ok", d)
	}
	if d := c.Gate(context.Background(), "suspended@x.com", ProductRelay); d.Allowed {
		t.Fatalf("suspended gate must refuse, got %+v", d)
	} else if d.Reason != "suspended" {
		t.Fatalf("suspended reason = %q, want suspended", d.Reason)
	}
}

func TestGateLLMBudgetAndEnablement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acct := r.URL.Query().Get("account_id")
		ent := Entitlement{Tier: "pro", LLMEnabled: true, LLMBudgetUSD: 5}
		switch acct {
		case "disabled@x.com":
			ent.LLMEnabled = false
		case "broke@x.com":
			ent.LLMBudgetUSD = 0
		}
		_ = json.NewEncoder(w).Encode(ent)
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)

	if d := c.GateLLM(context.Background(), "rich@x.com"); !d.Allowed {
		t.Fatalf("entitled LLM gate must allow, got %+v", d)
	}
	if d := c.GateLLM(context.Background(), "disabled@x.com"); d.Allowed || d.Reason != "llm_disabled" {
		t.Fatalf("llm-disabled gate = %+v, want refuse/llm_disabled", d)
	}
	if d := c.GateLLM(context.Background(), "broke@x.com"); d.Allowed || d.Reason != "llm_budget_exhausted" {
		t.Fatalf("budget-exhausted gate = %+v, want refuse/llm_budget_exhausted", d)
	}
}

func TestUsagePostShape(t *testing.T) {
	var got UsageEvent
	var auth string
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/usage" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		auth = r.Header.Get("X-Relay-Auth")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		close(done)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)

	want := UsageEvent{
		Product: ProductLLM, AccountID: "a@b.c", Kind: KindLLMTokens,
		Count: 1234, CostUSD: 0.045,
	}
	c.Meter(context.Background(), want)
	<-done

	if auth != "test-secret" {
		t.Errorf("usage auth header = %q", auth)
	}
	if got != want {
		t.Errorf("usage body = %+v, want %+v", got, want)
	}
}

func TestBoundedCacheFailOpenStaleAndCold(t *testing.T) {
	var fail atomic.Bool
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(Entitlement{Tier: "pro", Suspended: false})
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)

	// Warm the cache.
	if d := c.Gate(context.Background(), "warm@x.com", ProductRelay); !d.Allowed {
		t.Fatalf("warm gate must allow, got %+v", d)
	}
	// Now cp starts failing; let the TTL lapse so we re-fetch.
	fail.Store(true)
	time.Sleep(60 * time.Millisecond)

	d := c.Gate(context.Background(), "warm@x.com", ProductRelay)
	if !d.Allowed || d.Degraded {
		t.Fatalf("stale-but-present gate should serve last-known (allowed, NOT degraded), got %+v", d)
	}

	// Cold cache + cp error → allow but flagged degraded.
	cold := c.Gate(context.Background(), "never-seen@x.com", ProductRelay)
	if !cold.Allowed || !cold.Degraded || cold.Reason != "degraded" {
		t.Fatalf("cold-cache cp error should allow-degraded, got %+v", cold)
	}
}

func TestCacheServesWithinTTL(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(Entitlement{Tier: "pro"})
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, SharedSecret: "test-secret", CacheTTL: time.Hour})

	for i := 0; i < 5; i++ {
		c.Gate(context.Background(), "x@y.z", ProductRelay)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("expected 1 cp fetch within TTL, got %d", got)
	}
}

func TestConcurrentGateAndMeter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/usage" {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(Entitlement{Tier: "pro"})
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, SharedSecret: "s", CacheTTL: 5 * time.Millisecond})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Gate(context.Background(), "race@x.com", ProductRelay)
			c.Meter(context.Background(), UsageEvent{Product: ProductRelay, AccountID: "race@x.com", Kind: KindRelayBytes, Bytes: 1})
		}()
	}
	wg.Wait()
}
