package cpbilling

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMain opts into the plaintext-http escape hatch for the whole package: the
// httptest servers below serve http:// URLs, which New() otherwise rejects to
// avoid leaking CP_SHARED_SECRET over plaintext (see cpAllowInsecure). This
// mirrors the dev/test-only VULOS_LANCERT_ALLOW_INSECURE usage in the lan tests.
func TestMain(m *testing.M) {
	os.Setenv("VULOS_CP_ALLOW_INSECURE", "1")
	os.Exit(m.Run())
}

// newTestClient points a Client at a stub server with a fast TTL.
func newTestClient(base string) *Client {
	return New(Config{
		BaseURL:      base,
		SharedSecret: "test-secret",
		CacheTTL:     50 * time.Millisecond,
	})
}

// TestPlaintextBaseDisablesClient verifies the SEC fix: without the insecure
// escape hatch, a plaintext-http cp base disables the client (fail closed) so
// the CP_SHARED_SECRET bearer never travels over an unencrypted transport.
func TestPlaintextBaseDisablesClient(t *testing.T) {
	t.Setenv("VULOS_CP_ALLOW_INSECURE", "")
	c := New(Config{BaseURL: "http://cp.example.com", SharedSecret: "s"})
	if c.Enabled() {
		t.Fatal("plaintext http cp base must disable the client (fail closed)")
	}
	// https is accepted.
	c = New(Config{BaseURL: "https://cp.example.com", SharedSecret: "s"})
	if !c.Enabled() {
		t.Fatal("https cp base must enable the client")
	}
	// With the explicit escape hatch, plaintext is permitted (dev/test).
	t.Setenv("VULOS_CP_ALLOW_INSECURE", "1")
	c = New(Config{BaseURL: "http://cp.example.com", SharedSecret: "s"})
	if !c.Enabled() {
		t.Fatal("VULOS_CP_ALLOW_INSECURE=1 must permit plaintext")
	}
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

// ─── Typed gate tests ─────────────────────────────────────────────────────────

func makeEntServer(t *testing.T, ent Entitlement) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/usage" {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(ent)
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL, SharedSecret: "s", CacheTTL: time.Hour})
	return srv, c
}

// ─── GateGPU ─────────────────────────────────────────────────────────────────

func TestGateGPU_AllowedUnderCap(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", GPUEnabled: true, GPUSessionCap: 3})
	d := c.GateGPU(context.Background(), "u@x.com", 2) // 2 active < cap 3
	if !d.Allowed {
		t.Fatalf("under-cap GPU gate must allow, got %+v", d)
	}
}

func TestGateGPU_RefusedAtCap(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", GPUEnabled: true, GPUSessionCap: 2})
	d := c.GateGPU(context.Background(), "u@x.com", 2) // 2 active == cap 2
	if d.Allowed || d.Reason != "gpu_session_cap_reached" {
		t.Fatalf("at-cap GPU gate must refuse gpu_session_cap_reached, got %+v", d)
	}
}

func TestGateGPU_RefusedWhenDisabled(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", GPUEnabled: false, GPUSessionCap: 10})
	d := c.GateGPU(context.Background(), "u@x.com", 0)
	if d.Allowed || d.Reason != "gpu_disabled" {
		t.Fatalf("disabled GPU must refuse gpu_disabled, got %+v", d)
	}
}

func TestGateGPU_RefusedWhenSuspended(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", Suspended: true, GPUEnabled: true, GPUSessionCap: 10})
	d := c.GateGPU(context.Background(), "u@x.com", 0)
	if d.Allowed || d.Reason != "suspended" {
		t.Fatalf("suspended GPU gate must refuse suspended, got %+v", d)
	}
}

func TestGateGPU_StandaloneAllows(t *testing.T) {
	c := New(Config{}) // no CP_URL
	d := c.GateGPU(context.Background(), "u@x.com", 9999)
	if !d.Allowed || d.Reason != "disabled" {
		t.Fatalf("standalone GPU gate must allow (disabled), got %+v", d)
	}
}

// ─── GateCompute ─────────────────────────────────────────────────────────────

func TestGateCompute_AllowedUnderCap(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", ComputeEnabled: true, ComputeBoxCap: 5})
	d := c.GateCompute(context.Background(), "u@x.com", 4) // 4 < cap 5
	if !d.Allowed {
		t.Fatalf("under-cap compute gate must allow, got %+v", d)
	}
}

func TestGateCompute_RefusedAtCap(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", ComputeEnabled: true, ComputeBoxCap: 3})
	d := c.GateCompute(context.Background(), "u@x.com", 3) // 3 == cap 3
	if d.Allowed || d.Reason != "compute_box_cap_reached" {
		t.Fatalf("at-cap compute gate must refuse compute_box_cap_reached, got %+v", d)
	}
}

func TestGateCompute_RefusedWhenDisabled(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", ComputeEnabled: false, ComputeBoxCap: 10})
	d := c.GateCompute(context.Background(), "u@x.com", 0)
	if d.Allowed || d.Reason != "compute_disabled" {
		t.Fatalf("disabled compute must refuse compute_disabled, got %+v", d)
	}
}

func TestGateCompute_RefusedWhenSuspended(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", Suspended: true, ComputeEnabled: true, ComputeBoxCap: 10})
	d := c.GateCompute(context.Background(), "u@x.com", 0)
	if d.Allowed || d.Reason != "suspended" {
		t.Fatalf("suspended compute gate must refuse suspended, got %+v", d)
	}
}

func TestGateCompute_StandaloneAllows(t *testing.T) {
	c := New(Config{})
	d := c.GateCompute(context.Background(), "u@x.com", 9999)
	if !d.Allowed || d.Reason != "disabled" {
		t.Fatalf("standalone compute gate must allow (disabled), got %+v", d)
	}
}

// ─── GateRelay ───────────────────────────────────────────────────────────────

func TestGateRelay_AllowedWhenEnabled(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", RelayEnabled: true, RelayBytesBudget: 1 << 30})
	d := c.GateRelay(context.Background(), "u@x.com")
	if !d.Allowed {
		t.Fatalf("enabled relay gate must allow, got %+v", d)
	}
}

func TestGateRelay_RefusedWhenDisabled(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", RelayEnabled: false, RelayBytesBudget: 1 << 30})
	d := c.GateRelay(context.Background(), "u@x.com")
	if d.Allowed || d.Reason != "relay_disabled" {
		t.Fatalf("disabled relay must refuse relay_disabled, got %+v", d)
	}
}

func TestGateRelay_RefusedOnZeroBudget(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", RelayEnabled: true, RelayBytesBudget: 0})
	d := c.GateRelay(context.Background(), "u@x.com")
	if d.Allowed || d.Reason != "relay_budget_exhausted" {
		t.Fatalf("zero-budget relay must refuse relay_budget_exhausted, got %+v", d)
	}
}

func TestGateRelay_RefusedWhenSuspended(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", Suspended: true, RelayEnabled: true, RelayBytesBudget: 1 << 30})
	d := c.GateRelay(context.Background(), "u@x.com")
	if d.Allowed || d.Reason != "suspended" {
		t.Fatalf("suspended relay gate must refuse suspended, got %+v", d)
	}
}

func TestGateRelay_StandaloneAllows(t *testing.T) {
	c := New(Config{})
	d := c.GateRelay(context.Background(), "u@x.com")
	if !d.Allowed || d.Reason != "disabled" {
		t.Fatalf("standalone relay gate must allow (disabled), got %+v", d)
	}
}

// ─── GateMeet ────────────────────────────────────────────────────────────────

func TestGateMeet_AllowedUnderCap(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", MeetEnabled: true, MeetMinutesBudget: 1000, MeetMaxRooms: 5})
	d := c.GateMeet(context.Background(), "u@x.com", 4) // 4 < cap 5
	if !d.Allowed {
		t.Fatalf("under-cap meet gate must allow, got %+v", d)
	}
}

func TestGateMeet_RefusedAtRoomCap(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", MeetEnabled: true, MeetMinutesBudget: 1000, MeetMaxRooms: 2})
	d := c.GateMeet(context.Background(), "u@x.com", 2) // 2 == cap 2
	if d.Allowed || d.Reason != "meet_room_cap_reached" {
		t.Fatalf("at-cap meet gate must refuse meet_room_cap_reached, got %+v", d)
	}
}

func TestGateMeet_RefusedWhenDisabled(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", MeetEnabled: false, MeetMinutesBudget: 1000, MeetMaxRooms: 10})
	d := c.GateMeet(context.Background(), "u@x.com", 0)
	if d.Allowed || d.Reason != "meet_disabled" {
		t.Fatalf("disabled meet must refuse meet_disabled, got %+v", d)
	}
}

func TestGateMeet_RefusedOnZeroBudget(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", MeetEnabled: true, MeetMinutesBudget: 0, MeetMaxRooms: 10})
	d := c.GateMeet(context.Background(), "u@x.com", 0)
	if d.Allowed || d.Reason != "meet_budget_exhausted" {
		t.Fatalf("zero-budget meet must refuse meet_budget_exhausted, got %+v", d)
	}
}

func TestGateMeet_RefusedWhenSuspended(t *testing.T) {
	_, c := makeEntServer(t, Entitlement{Tier: "pro", Suspended: true, MeetEnabled: true, MeetMinutesBudget: 1000, MeetMaxRooms: 10})
	d := c.GateMeet(context.Background(), "u@x.com", 0)
	if d.Allowed || d.Reason != "suspended" {
		t.Fatalf("suspended meet gate must refuse suspended, got %+v", d)
	}
}

func TestGateMeet_StandaloneAllows(t *testing.T) {
	c := New(Config{})
	d := c.GateMeet(context.Background(), "u@x.com", 9999)
	if !d.Allowed || d.Reason != "disabled" {
		t.Fatalf("standalone meet gate must allow (disabled), got %+v", d)
	}
}
