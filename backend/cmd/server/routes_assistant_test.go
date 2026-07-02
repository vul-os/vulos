package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/ai"
	"vulos/backend/services/assistant"
)

// scriptedModel is a deterministic Completer for the HTTP round-trip tests: it
// returns canned replies (a tool call, then a plain answer) without a live model.
type scriptedModel struct{ replies []string }

func (s *scriptedModel) Complete(_ context.Context, _ ai.Config, _ ai.CompletionRequest) (string, error) {
	if len(s.replies) == 0 {
		return "done.", nil
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, nil
}
func (s *scriptedModel) Stream(_ context.Context, _ ai.Config, _ ai.CompletionRequest, onChunk func(ai.StreamChunk)) error {
	// The streaming agent loop drives model calls through Stream; consume the same
	// scripted replies so /agent/stream round-trips deterministically.
	if len(s.replies) > 0 {
		r := s.replies[0]
		s.replies = s.replies[1:]
		onChunk(ai.StreamChunk{Content: r})
	}
	onChunk(ai.StreamChunk{Done: true})
	return nil
}

// parseSSE extracts the JSON objects from a recorded text/event-stream body.
func parseSSE(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err == nil {
			out = append(out, ev)
		}
	}
	return out
}

// testAssistantMux wires the assistant routes with an injected scripted model, a
// fresh fixture mailbox, and a shared ledger — the same objects the handlers use,
// so the test can inspect what was actually sent.
func testAssistantMux(t *testing.T, replies []string) (*http.ServeMux, *assistant.FixtureSource, *assistant.ProposalLedger) {
	t.Helper()
	fx := assistant.NewFixtureSource()
	ledger := assistant.NewProposalLedger()
	deps := assistantDeps{
		model:  &scriptedModel{replies: replies},
		cfg:    ai.Config{Provider: ai.ProviderOllama, Model: "llama3", Endpoint: "http://localhost:11434"},
		source: fx,
		ledger: ledger,
	}
	mux := http.NewServeMux()
	registerAssistantRoutesWithDeps(mux, deps)
	return mux, fx, ledger
}

func doAssistantJSON(t *testing.T, mux *http.ServeMux, method, path, user, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		req.Header.Set("X-User-ID", user)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// LEGIT round-trip: /agent returns a proposal with an id (stored server-side);
// /execute with just {id} runs the SERVER-STORED args and the mail is sent.
func TestExecuteRoundTripViaID(t *testing.T) {
	mux, fx, _ := testAssistantMux(t, []string{
		`{"tool":"send_email","args":{"to":"dana@acme.io","subject":"Signed","body":"Done."}}`,
	})
	rec, out := doAssistantJSON(t, mux, "POST", "/api/assistant/agent", "user-1",
		`{"message":"email dana@acme.io that it's signed"}`)
	if rec.Code != 200 {
		t.Fatalf("/agent status = %d", rec.Code)
	}
	prop, _ := out["proposal"].(map[string]any)
	if prop == nil {
		t.Fatalf("no proposal returned: %v", out)
	}
	id, _ := prop["id"].(string)
	if id == "" {
		t.Fatal("proposal has no id")
	}
	if len(fx.Sent()) != 0 {
		t.Fatal("mail sent before approval — gate breached")
	}

	rec2, out2 := doAssistantJSON(t, mux, "POST", "/api/assistant/execute", "user-1", `{"id":"`+id+`"}`)
	if rec2.Code != 200 || out2["executed"] != true {
		t.Fatalf("execute failed: %d %v", rec2.Code, out2)
	}
	sent := fx.Sent()
	if len(sent) != 1 || sent[0].To != "dana@acme.io" || sent[0].Subject != "Signed" {
		t.Fatalf("server-stored args not executed: %+v", sent)
	}
}

// STREAMING text answer: /agent/stream emits token events then a terminal done,
// and never streams the intermediate tool-call JSON as answer tokens.
func TestAgentStreamTextAnswer(t *testing.T) {
	mux, _, _ := testAssistantMux(t, []string{
		`{"tool":"search_mail","args":{"query":"invoice"}}`,
		"You owe $128.40 to Tigris.",
	})
	rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/agent/stream", "user-1",
		`{"message":"when is my invoice due?"}`)
	if rec.Code != 200 {
		t.Fatalf("stream status = %d", rec.Code)
	}
	evs := parseSSE(t, rec.Body.String())
	var tokens string
	var sawDone bool
	for _, e := range evs {
		switch e["type"] {
		case "token":
			tokens += e["content"].(string)
		case "done":
			sawDone = true
		case "proposal":
			t.Fatalf("unexpected proposal on a text turn: %v", e)
		}
	}
	if !strings.Contains(tokens, "128.40") {
		t.Fatalf("streamed answer missing content: %q", tokens)
	}
	if strings.Contains(tokens, `"tool"`) {
		t.Fatalf("tool-call JSON leaked into token stream: %q", tokens)
	}
	if !sawDone {
		t.Fatalf("stream did not end with a done event: %v", evs)
	}
}

// STREAMING mutating turn: /agent/stream emits a terminal proposal event carrying
// a LEDGER id, executes nothing, and /execute then runs the server-stored args.
func TestAgentStreamProposalGoesThroughLedger(t *testing.T) {
	mux, fx, _ := testAssistantMux(t, []string{
		`{"tool":"send_email","args":{"to":"dana@acme.io","subject":"Signed","body":"Done."}}`,
	})
	rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/agent/stream", "user-1",
		`{"message":"email dana@acme.io that it's signed"}`)
	if rec.Code != 200 {
		t.Fatalf("stream status = %d", rec.Code)
	}
	evs := parseSSE(t, rec.Body.String())
	var id string
	for _, e := range evs {
		if e["type"] == "token" {
			t.Fatalf("mutating turn must not stream answer tokens: %v", e)
		}
		if e["type"] == "proposal" {
			prop, _ := e["proposal"].(map[string]any)
			if prop == nil {
				t.Fatalf("proposal event missing proposal: %v", e)
			}
			id, _ = prop["id"].(string)
		}
	}
	if id == "" {
		t.Fatalf("stream did not emit a proposal with a ledger id: %v", evs)
	}
	// Nothing sent by the stream itself — the gate held.
	if len(fx.Sent()) != 0 {
		t.Fatal("mail sent during streaming before approval — GATE BREACHED")
	}
	// The id resolves in the ledger: /execute runs the server-stored args.
	rec2, out2 := doAssistantJSON(t, mux, "POST", "/api/assistant/execute", "user-1", `{"id":"`+id+`"}`)
	if rec2.Code != 200 || out2["executed"] != true {
		t.Fatalf("execute of streamed proposal failed: %d %v", rec2.Code, out2)
	}
	if sent := fx.Sent(); len(sent) != 1 || sent[0].To != "dana@acme.io" {
		t.Fatalf("streamed proposal not executed correctly: %+v", sent)
	}
}

// STREAMING egress block: a non-sovereign tier streams a single terminal error
// event with no tokens and no proposal — and makes zero model calls.
func TestAgentStreamEgressBlocked(t *testing.T) {
	fx := assistant.NewFixtureSource()
	sm := &scriptedModel{replies: []string{"should never be called"}}
	deps := assistantDeps{
		model:  sm,
		cfg:    ai.Config{Provider: ai.ProviderClaude, Model: "claude-x"}, // external, no opt-in
		source: fx,
		ledger: assistant.NewProposalLedger(),
	}
	mux := http.NewServeMux()
	registerAssistantRoutesWithDeps(mux, deps)

	rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/agent/stream", "user-1", `{"message":"send an email"}`)
	evs := parseSSE(t, rec.Body.String())
	if len(evs) != 1 || evs[0]["type"] != "error" {
		t.Fatalf("egress-blocked stream should emit exactly one error event, got %v", evs)
	}
	// Zero model calls: the scripted reply was never consumed.
	if len(sm.replies) != 1 {
		t.Fatalf("model was called despite blocked egress — LEAK")
	}
	if len(fx.Sent()) != 0 {
		t.Fatal("a mutation happened despite blocked egress")
	}
}

// FORGED / unknown id is rejected and nothing is sent — a client cannot conjure
// a valid id→args mapping.
func TestExecuteForgedIDRejected(t *testing.T) {
	mux, fx, _ := testAssistantMux(t, nil)
	rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/execute", "user-1", `{"id":"prop_forged"}`)
	if rec.Code != 404 {
		t.Fatalf("forged id should be 404, got %d", rec.Code)
	}
	if len(fx.Sent()) != 0 {
		t.Fatal("forged proposal executed — GATE BREACHED")
	}
}

// A forged FULL proposal body (the old attack: client supplies tool+args) is
// rejected: with no id it is a 400 and nothing runs.
func TestExecuteRejectsClientSuppliedArgs(t *testing.T) {
	mux, fx, _ := testAssistantMux(t, nil)
	rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/execute", "user-1",
		`{"tool":"send_email","args":{"to":"attacker@evil.example","subject":"x","body":"y"}}`)
	if rec.Code < 400 {
		t.Fatalf("client-supplied proposal must be rejected, got %d", rec.Code)
	}
	if len(fx.Sent()) != 0 {
		t.Fatal("client-supplied args executed — GATE BREACHED")
	}
}

// CROSS-USER: user-2 cannot execute a proposal stored for user-1 (403), and the
// probe does not consume it — user-1 can still execute it afterward.
func TestExecuteCrossUserRejected(t *testing.T) {
	mux, fx, _ := testAssistantMux(t, []string{
		`{"tool":"send_email","args":{"to":"dana@acme.io","subject":"S","body":"b"}}`,
	})
	_, out := doAssistantJSON(t, mux, "POST", "/api/assistant/agent", "user-1",
		`{"message":"email dana@acme.io"}`)
	id := out["proposal"].(map[string]any)["id"].(string)

	rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/execute", "user-2", `{"id":"`+id+`"}`)
	if rec.Code != 403 {
		t.Fatalf("cross-user execute should be 403, got %d", rec.Code)
	}
	if len(fx.Sent()) != 0 {
		t.Fatal("cross-user execute sent mail — GATE BREACHED")
	}
	// Owner can still execute (probe did not consume it).
	rec2, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/execute", "user-1", `{"id":"`+id+`"}`)
	if rec2.Code != 200 || len(fx.Sent()) != 1 {
		t.Fatalf("owner lost their proposal to a cross-user probe: %d sent=%d", rec2.Code, len(fx.Sent()))
	}
}

// SINGLE-USE: replaying a consumed id is rejected and does not send twice.
func TestExecuteSingleUse(t *testing.T) {
	mux, fx, _ := testAssistantMux(t, []string{
		`{"tool":"send_email","args":{"to":"dana@acme.io","subject":"S","body":"b"}}`,
	})
	_, out := doAssistantJSON(t, mux, "POST", "/api/assistant/agent", "user-1", `{"message":"email dana"}`)
	id := out["proposal"].(map[string]any)["id"].(string)

	if rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/execute", "user-1", `{"id":"`+id+`"}`); rec.Code != 200 {
		t.Fatalf("first execute failed: %d", rec.Code)
	}
	rec2, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/execute", "user-1", `{"id":"`+id+`"}`)
	if rec2.Code != 404 {
		t.Fatalf("replayed id should be 404, got %d", rec2.Code)
	}
	if len(fx.Sent()) != 1 {
		t.Fatalf("single-use breached: %d sent", len(fx.Sent()))
	}
}

// UNAUTHENTICATED: no X-User-ID ⇒ 401, nothing runs. (TTL-expiry rejection is
// covered deterministically at the ledger level in TestLedgerExpiryRejected.)
func TestExecuteRequiresAuth(t *testing.T) {
	mux, fx, _ := testAssistantMux(t, nil)
	rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/execute", "", `{"id":"prop_x"}`)
	if rec.Code != 401 {
		t.Fatalf("unauthenticated execute should be 401, got %d", rec.Code)
	}
	if len(fx.Sent()) != 0 {
		t.Fatal("unauthenticated execute sent mail")
	}
}
