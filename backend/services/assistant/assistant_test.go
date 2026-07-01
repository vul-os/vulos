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

// --- Sovereign guard (tiered) -----------------------------------------------

func TestSovereignGuard(t *testing.T) {
	cases := []struct {
		name          string
		cfg           ai.Config
		allowExternal bool
		wantTier      Tier
		wantBlocked   bool
	}{
		// local — always allowed, no opt-in.
		{"ollama-localhost", localCfg(), false, TierLocal, false},
		{"ollama-empty-endpoint", ai.Config{Provider: ai.ProviderOllama}, false, TierLocal, false},
		// A loopback endpoint stays local even if the operator declared a lesser
		// tier — most private wins, F4/F5 hardening intact.
		{"loopback-ignores-declaration", ai.Config{Provider: ai.ProviderCustom, Endpoint: "http://localhost:4000/v1", Tier: "external"}, false, TierLocal, false},

		// sovereign — an EXPLICIT off-box operator declaration; allowed by default.
		{"declared-sovereign-allowed", ai.Config{Provider: ai.ProviderCustom, Endpoint: "https://eu.sovereign.vulos.net/v1", Tier: "sovereign"}, false, TierSovereign, false},

		// brokered — blocked until opted in, then allowed.
		{"declared-brokered-blocked", ai.Config{Provider: ai.ProviderCustom, Endpoint: "https://broker.example/v1", Tier: "brokered"}, false, TierBrokered, true},
		{"declared-brokered-optin", ai.Config{Provider: ai.ProviderCustom, Endpoint: "https://broker.example/v1", Tier: "brokered"}, true, TierBrokered, false},

		// A LAN box / .local / .internal name is a DIFFERENT machine and is NEVER
		// inferred as sovereign from a private IP — off-box, unmarked → external.
		{"custom-lan-external", ai.Config{Provider: ai.ProviderCustom, Endpoint: "http://192.168.1.50:8000"}, false, TierExternal, true},
		{"custom-mdns-external", ai.Config{Provider: ai.ProviderCustom, Endpoint: "http://gpu.local:8000"}, false, TierExternal, true},
		{"custom-internal-external", ai.Config{Provider: ai.ProviderCustom, Endpoint: "https://exfil.internal/v1"}, false, TierExternal, true},
		{"custom-lan-optin", ai.Config{Provider: ai.ProviderCustom, Endpoint: "http://192.168.1.50:8000"}, true, TierExternal, false},

		// Cloud providers are external; blocked without opt-in.
		{"claude-external", ai.Config{Provider: ai.ProviderClaude, Model: "claude-x"}, false, TierExternal, true},
		{"openai-external", ai.Config{Provider: ai.ProviderOpenAI}, false, TierExternal, true},
		{"custom-public-external", ai.Config{Provider: ai.ProviderCustom, Endpoint: "https://api.example.com"}, false, TierExternal, true},
		{"claude-optin", ai.Config{Provider: ai.ProviderClaude}, true, TierExternal, false},
		{"custom-public-optin", ai.Config{Provider: ai.ProviderCustom, Endpoint: "https://api.example.com"}, true, TierExternal, false},

		// Fail closed: an unverifiable "local" declaration on an off-box endpoint,
		// and a garbage tier, both collapse to external/blocked.
		{"offbox-local-lie-blocked", ai.Config{Provider: ai.ProviderCustom, Endpoint: "https://api.example.com", Tier: "local"}, false, TierExternal, true},
		{"unknown-tier-blocked", ai.Config{Provider: ai.ProviderCustom, Endpoint: "https://api.example.com", Tier: "wat"}, false, TierExternal, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := Evaluate(tc.cfg, tc.allowExternal)
			if s.Tier != tc.wantTier {
				t.Errorf("tier = %q, want %q", s.Tier, tc.wantTier)
			}
			if s.Label != TierLabel(tc.wantTier) {
				t.Errorf("label = %q, want %q", s.Label, TierLabel(tc.wantTier))
			}
			if s.Allowed == tc.wantBlocked {
				t.Errorf("allowed = %v, wantBlocked %v", s.Allowed, tc.wantBlocked)
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

// TestTierLabelsMatchContract pins the exact UI strings shared with the llmux
// side; a drift here breaks the cross-repo vocabulary contract.
func TestTierLabelsMatchContract(t *testing.T) {
	want := map[Tier]string{
		TierLocal:     "On your device",
		TierSovereign: "Vulos sovereign · in-region, no-train",
		TierBrokered:  "Brokered · no-train",
		TierExternal:  "External · not private",
	}
	for tier, label := range want {
		if got := TierLabel(tier); got != label {
			t.Errorf("TierLabel(%q) = %q, want %q", tier, got, label)
		}
	}
}

// TestLLMuxConfigStaysSovereign confirms that routing the assistant through the
// on-box llmux gateway (VULOS_LLMUX_URL) yields a config the guard still
// classifies as on-instance — the choke point remains sovereign, with no
// external opt-in required.
func TestLLMuxConfigStaysSovereign(t *testing.T) {
	for _, k := range []string{"AI_PROVIDER", "AI_MODEL", "AI_ENDPOINT", "AI_API_KEY", "AI_SYSTEM_PROMPT"} {
		t.Setenv(k, "")
	}
	t.Setenv("VULOS_LLMUX_URL", "http://localhost:4000/v1")

	cfg := ai.DefaultConfig()
	if cfg.Provider != ai.ProviderCustom {
		t.Fatalf("provider = %q, want custom", cfg.Provider)
	}
	if s := Evaluate(cfg, false); s.Tier != TierLocal {
		t.Errorf("llmux tier = %q, want local (on-instance loopback)", s.Tier)
	}
	if err := Guard(cfg, false); err != nil {
		t.Errorf("Guard blocked the on-box llmux endpoint: %v", err)
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
