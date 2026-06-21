package airouter

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"vulos/backend/internal/cpbilling"
)

// stubSSEWithUsage serves a canned SSE stream that ends with an OpenAI-style
// usage block, like llmux / OpenAI emit when include_usage is requested.
func stubSSEWithUsage(t *testing.T, totalTokens int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n",
			totalTokens-10, totalTokens)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// cpStub captures entitlement+usage traffic for the LLM surface tests.
type cpStub struct {
	srv      *httptest.Server
	mu       sync.Mutex
	usage    []cpbilling.UsageEvent
	ent      cpbilling.Entitlement
	usageGot chan struct{}
}

func newCPStub(ent cpbilling.Entitlement) *cpStub {
	cs := &cpStub{ent: ent, usageGot: make(chan struct{}, 1)}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/entitlements":
			_ = json.NewEncoder(w).Encode(cs.ent)
		case "/api/usage":
			var ev cpbilling.UsageEvent
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &ev)
			cs.mu.Lock()
			cs.usage = append(cs.usage, ev)
			cs.mu.Unlock()
			select {
			case cs.usageGot <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	return cs
}

func (cs *cpStub) client() *cpbilling.Client {
	return cpbilling.New(cpbilling.Config{BaseURL: cs.srv.URL, SharedSecret: "s", CacheTTL: time.Hour})
}

func (cs *cpStub) usageEvents() []cpbilling.UsageEvent {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := make([]cpbilling.UsageEvent, len(cs.usage))
	copy(out, cs.usage)
	return out
}

func TestLLMSurface_AllowedWhenEntitled_AndMeters(t *testing.T) {
	stub := stubSSEWithUsage(t, 42)
	defer stub.Close()
	cp := newCPStub(cpbilling.Entitlement{Tier: "pro", LLMEnabled: true, LLMBudgetUSD: 10})
	defer cp.srv.Close()

	h, _ := newTestHandler(t, stub.URL)
	h.WithBilling(cp.client())

	body := `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/api/ai/chat", strings.NewReader(body))
	req.Header.Set("X-User-ID", "user@x.com")
	rr := httptest.NewRecorder()
	h.handleChat(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("entitled chat status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	// Meter is async; wait for it.
	select {
	case <-cp.usageGot:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage meter POST")
	}
	evs := cp.usageEvents()
	if len(evs) != 1 {
		t.Fatalf("got %d usage events, want 1", len(evs))
	}
	if evs[0].Product != cpbilling.ProductLLM || evs[0].Kind != cpbilling.KindLLMTokens {
		t.Errorf("usage product/kind = %s/%s", evs[0].Product, evs[0].Kind)
	}
	if evs[0].Count != 42 {
		t.Errorf("metered token count = %d, want 42", evs[0].Count)
	}
	if evs[0].AccountID != "user@x.com" {
		t.Errorf("usage account = %q", evs[0].AccountID)
	}
}

func TestLLMSurface_RefusedWhenSuspended(t *testing.T) {
	stub := stubSSEWithUsage(t, 42)
	defer stub.Close()
	cp := newCPStub(cpbilling.Entitlement{Tier: "pro", Suspended: true, LLMEnabled: true, LLMBudgetUSD: 10})
	defer cp.srv.Close()

	h, _ := newTestHandler(t, stub.URL)
	h.WithBilling(cp.client())

	body := `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/api/ai/chat", strings.NewReader(body))
	req.Header.Set("X-User-ID", "user@x.com")
	rr := httptest.NewRecorder()
	h.handleChat(rr, req)

	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("suspended chat status = %d, want 402; body=%s", rr.Code, rr.Body.String())
	}
	if len(cp.usageEvents()) != 0 {
		t.Errorf("suspended request must not meter usage, got %d", len(cp.usageEvents()))
	}
}

func TestLLMSurface_RefusedWhenBudgetExhausted(t *testing.T) {
	stub := stubSSEWithUsage(t, 42)
	defer stub.Close()
	cp := newCPStub(cpbilling.Entitlement{Tier: "free", LLMEnabled: true, LLMBudgetUSD: 0})
	defer cp.srv.Close()

	h, _ := newTestHandler(t, stub.URL)
	h.WithBilling(cp.client())

	body := `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/api/ai/chat", strings.NewReader(body))
	req.Header.Set("X-User-ID", "user@x.com")
	rr := httptest.NewRecorder()
	h.handleChat(rr, req)

	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("budget-exhausted chat status = %d, want 402", rr.Code)
	}
}

func TestLLMSurface_DisabledBillingIsUngated(t *testing.T) {
	stub := stubSSEWithUsage(t, 42)
	defer stub.Close()

	h, _ := newTestHandler(t, stub.URL)
	// No billing wired → standalone OS path: must serve without any cp call.
	h.WithBilling(cpbilling.New(cpbilling.Config{}))

	body := `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/api/ai/chat", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.handleChat(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("standalone chat status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}
