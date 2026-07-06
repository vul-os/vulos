package main

// routes_assistant_actions_test.go — HTTP-level tests for the assistant JSON
// action endpoints that the wave-31 suite left uncovered: /summarize, /draft,
// /attention, and POST /search. These drive the shared assistantRespond /
// assistantErr response mappers (both 0% before this file), including the
// security-relevant 428 egress-blocked mapping and the input-validation 400s.
//
// They reuse the wave-31 testAssistantMux + doAssistantJSON helpers (scripted
// model + fixture mailbox, no live model).

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"vulos/backend/services/ai"
	"vulos/backend/services/assistant"
)

// notAuthedSource embeds the fixture but fails every read with a
// "not authenticated" error, so the handler's assistantErr maps it to 401 (the
// "mail not authenticated" branch) rather than a generic 502.
type notAuthedSource struct{ *assistant.FixtureSource }

func (n notAuthedSource) Recent(context.Context, assistant.Auth, string, int) ([]assistant.Message, error) {
	return nil, errors.New("mail not authenticated")
}

// externalTierMux wires the assistant on an EXTERNAL tier with no egress opt-in,
// so every model-backed action is blocked up-front by Guard() and surfaces
// through assistantErr as a 428 — with zero model calls.
func externalTierMux(t *testing.T) (*http.ServeMux, *scriptedModel) {
	t.Helper()
	sm := &scriptedModel{replies: []string{"should never be called"}}
	deps := assistantDeps{
		model:  sm,
		cfg:    ai.Config{Provider: ai.ProviderClaude, Model: "claude-x"}, // external, no opt-in
		source: assistant.NewFixtureSource(),
		ledger: assistant.NewProposalLedger(),
	}
	mux := http.NewServeMux()
	registerAssistantRoutesWithDeps(mux, deps)
	return mux, sm
}

// --- auth gates on every action route ---------------------------------------

func TestAssistantActionsRequireAuth(t *testing.T) {
	mux, _, _ := testAssistantMux(t, nil)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/assistant/summarize", `{}`},
		{"POST", "/api/assistant/draft", `{"uid":"1"}`},
		{"POST", "/api/assistant/attention", `{}`},
		{"POST", "/api/assistant/search", `{"q":"invoice"}`},
	} {
		rec, _ := doAssistantJSON(t, mux, tc.method, tc.path, "", tc.body)
		if rec.Code != 401 {
			t.Errorf("%s %s unauth = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

// --- validation branches (auth passes, bad input) ---------------------------

func TestAssistantDraftRequiresUID(t *testing.T) {
	mux, _, _ := testAssistantMux(t, nil)
	rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/draft", "user-1", `{}`)
	if rec.Code != 400 {
		t.Fatalf("draft without uid = %d, want 400", rec.Code)
	}
}

func TestAssistantSummarizeThreadRequiresUID(t *testing.T) {
	mux, _, _ := testAssistantMux(t, nil)
	// scope=thread with no uid ⇒ 400.
	rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/summarize", "user-1", `{"scope":"thread"}`)
	if rec.Code != 400 {
		t.Fatalf("thread summary without uid = %d, want 400", rec.Code)
	}
}

func TestAssistantSearchRequiresQuery(t *testing.T) {
	mux, _, _ := testAssistantMux(t, nil)
	rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/search", "user-1", `{"q":"   "}`)
	if rec.Code != 400 {
		t.Fatalf("blank q = %d, want 400", rec.Code)
	}
}

// --- 428 egress-blocked mapping (assistantErr) ------------------------------

// TestAssistantActionsEgressBlocked428: on an external tier with no opt-in, the
// model-backed actions map ErrEgressBlocked → 428 Precondition Required and make
// zero model calls.
func TestAssistantActionsEgressBlocked428(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/api/assistant/summarize", `{}`},
		{"/api/assistant/attention", `{}`},
		{"/api/assistant/search", `{"q":"invoice"}`},
	} {
		mux, sm := externalTierMux(t)
		rec, _ := doAssistantJSON(t, mux, "POST", tc.path, "user-1", tc.body)
		if rec.Code != 428 {
			t.Errorf("%s egress-blocked = %d, want 428", tc.path, rec.Code)
		}
		if len(sm.replies) != 1 {
			t.Errorf("%s consumed a model call despite blocked egress", tc.path)
		}
	}
}

// --- success path (assistantRespond writes {answer}) ------------------------

// TestAssistantAttentionSuccess: a local-tier attention call returns 200 with an
// {answer} (the scripted brief), exercising assistantRespond's success branch.
func TestAssistantAttentionSuccess(t *testing.T) {
	mux, _, _ := testAssistantMux(t, []string{"You have one renewal awaiting a reply."})
	rec, out := doAssistantJSON(t, mux, "POST", "/api/assistant/attention", "user-1", `{}`)
	if rec.Code != 200 {
		t.Fatalf("attention = %d, want 200", rec.Code)
	}
	if out["answer"] == nil {
		t.Fatalf("attention response missing answer: %v", out)
	}
}

// TestAssistantSummarizeNotAuthenticated401: when the mail source reports "not
// authenticated", assistantErr maps it to 401 (not a generic 502), so the UI can
// prompt the user to (re)connect mail rather than showing a server error.
func TestAssistantSummarizeNotAuthenticated401(t *testing.T) {
	deps := assistantDeps{
		model:  &scriptedModel{replies: []string{"summary"}},
		cfg:    ai.Config{Provider: ai.ProviderOllama, Model: "llama3", Endpoint: "http://localhost:11434"},
		source: notAuthedSource{assistant.NewFixtureSource()},
		ledger: assistant.NewProposalLedger(),
	}
	mux := http.NewServeMux()
	registerAssistantRoutesWithDeps(mux, deps)
	rec, _ := doAssistantJSON(t, mux, "POST", "/api/assistant/summarize", "user-1", `{}`)
	if rec.Code != 401 {
		t.Fatalf("not-authenticated summarize = %d, want 401", rec.Code)
	}
}

// TestAssistantDraftSuccess: a draft (no save) returns {draft, saved:false}.
func TestAssistantDraftSuccess(t *testing.T) {
	mux, fx, _ := testAssistantMux(t, []string{"Thanks — I'll review and get back to you."})
	// Use a uid that exists in the fixture inbox.
	msgs, err := fx.Recent(context.Background(), assistant.Auth{}, "", 1)
	if err != nil || len(msgs) == 0 {
		t.Skipf("fixture inbox empty: %v", err)
	}
	uid := msgs[0].UID
	rec, out := doAssistantJSON(t, mux, "POST", "/api/assistant/draft", "user-1",
		`{"uid":"`+uid+`","instructions":"decline politely"}`)
	if rec.Code != 200 {
		t.Fatalf("draft = %d, want 200 (%v)", rec.Code, out)
	}
	if out["draft"] == nil || out["saved"] != false {
		t.Fatalf("draft response shape wrong: %v", out)
	}
}
