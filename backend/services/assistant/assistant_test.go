package assistant

import (
	"context"
	"strings"
	"testing"

	"vulos/backend/services/ai"
)

// fakeModel is a deterministic Completer that records the last request and echoes
// a canned answer. It lets us test skills + the guard without a live model.
type fakeModel struct {
	lastCfg    ai.Config
	lastReq    ai.CompletionRequest
	reply      string
	calls      int
	streamText string
}

func (f *fakeModel) Complete(_ context.Context, cfg ai.Config, req ai.CompletionRequest) (string, error) {
	f.calls++
	f.lastCfg = cfg
	f.lastReq = req
	if f.reply == "" {
		return "ok", nil
	}
	return f.reply, nil
}

func (f *fakeModel) Stream(_ context.Context, cfg ai.Config, req ai.CompletionRequest, onChunk func(ai.StreamChunk)) error {
	f.calls++
	f.lastCfg = cfg
	f.lastReq = req
	onChunk(ai.StreamChunk{Content: f.streamText})
	onChunk(ai.StreamChunk{Done: true})
	return nil
}

func localCfg() ai.Config {
	return ai.Config{Provider: ai.ProviderOllama, Model: "llama3", Endpoint: "http://localhost:11434"}
}

// --- Sovereign guard --------------------------------------------------------

func TestSovereignGuard(t *testing.T) {
	cases := []struct {
		name          string
		cfg           ai.Config
		allowExternal bool
		wantEgress    Egress
		wantBlocked   bool
	}{
		{"ollama-localhost", localCfg(), false, EgressOnInstance, false},
		{"ollama-empty-endpoint", ai.Config{Provider: ai.ProviderOllama}, false, EgressOnInstance, false},
		{"custom-lan", ai.Config{Provider: ai.ProviderCustom, Endpoint: "http://192.168.1.50:8000"}, false, EgressOnInstance, false},
		{"custom-mdns", ai.Config{Provider: ai.ProviderCustom, Endpoint: "http://gpu.local:8000"}, false, EgressOnInstance, false},
		{"claude-blocked", ai.Config{Provider: ai.ProviderClaude, Model: "claude-x"}, false, EgressBlocked, true},
		{"openai-blocked", ai.Config{Provider: ai.ProviderOpenAI}, false, EgressBlocked, true},
		{"custom-public-blocked", ai.Config{Provider: ai.ProviderCustom, Endpoint: "https://api.example.com"}, false, EgressBlocked, true},
		{"claude-allowed-optin", ai.Config{Provider: ai.ProviderClaude}, true, EgressExternalConfigured, false},
		{"custom-public-allowed-optin", ai.Config{Provider: ai.ProviderCustom, Endpoint: "https://api.example.com"}, true, EgressExternalConfigured, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := Evaluate(tc.cfg, tc.allowExternal)
			if s.Egress != tc.wantEgress {
				t.Errorf("egress = %q, want %q", s.Egress, tc.wantEgress)
			}
			err := Guard(tc.cfg, tc.allowExternal)
			if tc.wantBlocked && err == nil {
				t.Errorf("expected Guard to block, got nil")
			}
			if !tc.wantBlocked && err != nil {
				t.Errorf("expected Guard to allow, got %v", err)
			}
		})
	}
}

// The guard must fire BEFORE any mail content reaches the model.
func TestGuardBlocksBeforeModelCall(t *testing.T) {
	m := &fakeModel{}
	a := New(m, ai.Config{Provider: ai.ProviderClaude, Model: "claude-x"}, NewFixtureSource(), false)
	_, err := a.SummarizeInbox(context.Background(), Auth{})
	if err == nil {
		t.Fatal("expected egress block, got nil")
	}
	if m.calls != 0 {
		t.Fatalf("model was called %d times despite blocked egress — LEAK", m.calls)
	}
}

// --- Skills over the fixture mailbox ---------------------------------------

func TestSummarizeInboxFeedsContext(t *testing.T) {
	m := &fakeModel{reply: "summary"}
	a := New(m, localCfg(), NewFixtureSource(), false)
	got, err := a.SummarizeInbox(context.Background(), Auth{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "summary" {
		t.Fatalf("got %q", got)
	}
	user := m.lastReq.Messages[0].Content
	if !strings.Contains(user, "Dana Whitfield") || !strings.Contains(user, "Contract renewal") {
		t.Errorf("inbox context not passed to model; user prompt = %s", user)
	}
	if m.lastCfg.System == "" || !strings.Contains(m.lastCfg.System, "never leaves") {
		t.Errorf("sovereign system preamble missing")
	}
}

func TestSearchGroundsAndReturnsResults(t *testing.T) {
	m := &fakeModel{reply: "You owe $128.40 to Tigris."}
	a := New(m, localCfg(), NewFixtureSource(), false)
	res, err := a.Search(context.Background(), Auth{}, "invoice due")
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer == "" || len(res.Results) == 0 {
		t.Fatalf("empty result: %+v", res)
	}
	// The invoice message should have been retrieved via keyword search.
	found := false
	for _, r := range res.Results {
		if strings.Contains(r.Subject, "Invoice #4471") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected invoice in retrieved results, got %d msgs", len(res.Results))
	}
}

func TestDraftReplyUsesOriginal(t *testing.T) {
	m := &fakeModel{reply: "Sure, Wednesday at 2pm works."}
	fx := NewFixtureSource()
	a := New(m, localCfg(), fx, false)
	text, err := a.SaveDraftReply(context.Background(), Auth{}, "102", "INBOX", "accept the Wednesday slot")
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Fatal("empty draft")
	}
	if !strings.Contains(m.lastReq.Messages[0].Content, "Priya") {
		t.Errorf("original message not included in draft prompt")
	}
	drafts := fx.Drafts()
	if len(drafts) != 1 {
		t.Fatalf("expected 1 saved draft, got %d", len(drafts))
	}
	if drafts[0].To != "scheduling@calendly.app" || !strings.HasPrefix(drafts[0].Subject, "Re:") {
		t.Errorf("draft addressed/subjected wrong: %+v", drafts[0])
	}
}

func TestAttentionRuns(t *testing.T) {
	m := &fakeModel{reply: "1. Dana — contract sign-off due Thursday."}
	a := New(m, localCfg(), NewFixtureSource(), false)
	got, err := a.Attention(context.Background(), Auth{})
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("empty attention output")
	}
	if !strings.Contains(m.lastReq.Messages[0].Content, "needs my attention") {
		t.Errorf("attention prompt malformed")
	}
}

func TestChatStreamGuardedAndGrounded(t *testing.T) {
	m := &fakeModel{streamText: "Dana needs your signature by Thursday."}
	a := New(m, localCfg(), NewFixtureSource(), false)
	var buf strings.Builder
	err := a.ChatStream(context.Background(), Auth{}, "what's the deal with the contract?", nil, func(c ai.StreamChunk) {
		buf.WriteString(c.Content)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.lastCfg.System, "Contract renewal") {
		t.Errorf("chat did not ground on retrieved mail context")
	}
	if buf.String() == "" {
		t.Errorf("no streamed output")
	}
}

func TestChatStreamBlockedNoLeak(t *testing.T) {
	m := &fakeModel{}
	a := New(m, ai.Config{Provider: ai.ProviderOpenAI}, NewFixtureSource(), false)
	err := a.ChatStream(context.Background(), Auth{}, "hi", nil, func(ai.StreamChunk) {})
	if err == nil {
		t.Fatal("expected block")
	}
	if m.calls != 0 {
		t.Fatalf("model streamed despite block — LEAK (%d calls)", m.calls)
	}
}
