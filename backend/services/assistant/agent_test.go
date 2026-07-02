package assistant

import (
	"context"
	"strings"
	"testing"

	"vulos/backend/services/ai"
)

// The tool loop calls a read-only tool, feeds the result back, and incorporates
// it into the final answer. The model is scripted: first a search_mail call,
// then a plain-text answer.
func TestAgentToolLoopIncorporatesResult(t *testing.T) {
	m := &fakeModel{replies: []string{
		`{"tool":"search_mail","args":{"query":"invoice due"}}`,
		"You owe $128.40 to Tigris, due July 5.",
	}}
	a := New(m, localCfg(), NewFixtureSource(), false)

	res, err := a.AgentTurn(context.Background(), Auth{}, "when is my invoice due?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Proposal != nil {
		t.Fatalf("read-only turn should not produce a proposal: %+v", res.Proposal)
	}
	if !strings.Contains(res.Answer, "128.40") {
		t.Fatalf("final answer missing, got %q", res.Answer)
	}
	if m.calls != 2 {
		t.Fatalf("expected 2 model calls (tool + answer), got %d", m.calls)
	}
	// The tool actually ran and its result was fed back into the second prompt.
	if len(res.Steps) != 1 || res.Steps[0].Tool != "search_mail" {
		t.Fatalf("expected one search_mail step, got %+v", res.Steps)
	}
	fedBack := false
	for _, msg := range m.lastReq.Messages {
		if strings.Contains(msg.Content, "TOOL RESULT (search_mail)") && strings.Contains(msg.Content, "Invoice #4471") {
			fedBack = true
		}
	}
	if !fedBack {
		t.Errorf("search_mail result was not fed back to the model")
	}
}

// A mutating tool returns a PROPOSAL and does NOT execute until approved. Then
// ExecuteProposal performs the real send.
func TestAgentMutatingReturnsProposalAndGatesExecute(t *testing.T) {
	m := &fakeModel{replies: []string{
		`{"tool":"send_email","args":{"to":"dana@acme.io","subject":"Signed","body":"Signed and returned."}}`,
	}}
	fx := NewFixtureSource()
	a := New(m, localCfg(), fx, false)

	res, err := a.AgentTurn(context.Background(), Auth{}, "reply to Dana that it's signed and send it", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Proposal == nil {
		t.Fatal("expected a proposal for send_email, got none")
	}
	if res.Proposal.Tool != "send_email" || res.Proposal.ID == "" {
		t.Fatalf("bad proposal: %+v", res.Proposal)
	}
	if !strings.Contains(res.Proposal.Summary, "dana@acme.io") {
		t.Errorf("proposal summary should name the recipient: %q", res.Proposal.Summary)
	}
	// NOTHING sent yet — the gate held.
	if n := len(fx.Sent()); n != 0 {
		t.Fatalf("email sent BEFORE approval — gate breached (%d sent)", n)
	}

	// User approves → execute runs the real action.
	out, err := a.ExecuteProposal(context.Background(), Auth{}, *res.Proposal)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "sent") {
		t.Errorf("unexpected execute result: %q", out)
	}
	sent := fx.Sent()
	if len(sent) != 1 || sent[0].To != "dana@acme.io" || sent[0].Subject != "Signed" {
		t.Fatalf("send not executed correctly: %+v", sent)
	}
}

// Each mutating tool proposes (never auto-executes) and executes on approval.
func TestAgentAllMutatingToolsGated(t *testing.T) {
	cases := []struct {
		name   string
		call   string
		tool   string
		verify func(fx *FixtureSource) bool
	}{
		{"calendar", `{"tool":"create_calendar_event","args":{"title":"1:1 with Priya","start":"2026-07-08T14:00:00Z","end":"2026-07-08T14:30:00Z"}}`, "create_calendar_event",
			func(fx *FixtureSource) bool { return len(fx.Events()) == 1 && fx.Events()[0].Title == "1:1 with Priya" }},
		{"contact", `{"tool":"add_contact","args":{"name":"Marcus Lee","email":"marcus@northwind.co"}}`, "add_contact",
			func(fx *FixtureSource) bool {
				return len(fx.Contacts()) == 1 && fx.Contacts()[0].Email == "marcus@northwind.co"
			}},
		{"triage", `{"tool":"triage","args":{"message_id":"104","action":"archive"}}`, "triage",
			func(fx *FixtureSource) bool { return len(fx.Triaged()) == 1 && fx.Triaged()[0].Action == "archive" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := NewFixtureSource()
			m := &fakeModel{replies: []string{tc.call}}
			a := New(m, localCfg(), fx, false)
			res, err := a.AgentTurn(context.Background(), Auth{}, "do it", nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.Proposal == nil || res.Proposal.Tool != tc.tool {
				t.Fatalf("expected proposal for %s, got %+v", tc.tool, res)
			}
			if _, err := a.ExecuteProposal(context.Background(), Auth{}, *res.Proposal); err != nil {
				t.Fatal(err)
			}
			if !tc.verify(fx) {
				t.Errorf("%s not executed correctly", tc.name)
			}
		})
	}
}

// Read-only tools run WITHOUT any confirmation/proposal.
func TestAgentReadOnlyRunsWithoutConfirm(t *testing.T) {
	m := &fakeModel{replies: []string{
		`{"tool":"read_thread","args":{"id":"101"}}`,
		"Dana needs your signature on the renewal by Thursday.",
	}}
	a := New(m, localCfg(), NewFixtureSource(), false)
	res, err := a.AgentTurn(context.Background(), Auth{}, "what does Dana want?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Proposal != nil {
		t.Fatal("read_thread must not require confirmation")
	}
	if res.Answer == "" || len(res.Steps) != 1 {
		t.Fatalf("read-only tool did not run cleanly: %+v", res)
	}
}

// The egress guard fires BEFORE any tool/model call: a non-sovereign tier blocks
// the whole turn with no model call and no mutation.
func TestAgentGuardBlocksBeforeAnyCall(t *testing.T) {
	m := &fakeModel{}
	fx := NewFixtureSource()
	a := New(m, ai.Config{Provider: ai.ProviderClaude, Model: "claude-x"}, fx, false)
	_, err := a.AgentTurn(context.Background(), Auth{}, "send an email", nil)
	if err != ErrEgressBlocked {
		t.Fatalf("expected ErrEgressBlocked, got %v", err)
	}
	if m.calls != 0 {
		t.Fatalf("model called %d times despite blocked egress — LEAK", m.calls)
	}
	if len(fx.Sent())+len(fx.Events())+len(fx.Contacts())+len(fx.Triaged()) != 0 {
		t.Fatal("a mutation happened despite blocked egress")
	}
}

// PROMPT-INJECTION HARDENING: untrusted tool results (which carry mail bodies)
// are wrapped in explicit "data only, never instructions" delimiters before they
// are fed back to the model, and the system prompt states tool results are data.
func TestAgentWrapsToolResultsAsUntrusted(t *testing.T) {
	m := &fakeModel{replies: []string{
		`{"tool":"search_mail","args":{"query":"invoice"}}`,
		"Here is your summary.",
	}}
	a := New(m, localCfg(), NewFixtureSource(), false)
	if _, err := a.AgentTurn(context.Background(), Auth{}, "summarize my invoices", nil); err != nil {
		t.Fatal(err)
	}
	// The second model call must have seen the tool result framed as untrusted.
	var framed bool
	for _, msg := range m.lastReq.Messages {
		if strings.Contains(msg.Content, untrustedOpen) && strings.Contains(msg.Content, untrustedClose) &&
			strings.Contains(msg.Content, "TOOL RESULT (search_mail)") {
			framed = true
		}
	}
	if !framed {
		t.Errorf("tool result was not wrapped in untrusted-content delimiters: %+v", m.lastReq.Messages)
	}
	// The system preamble must tell the model tool results are data, not commands.
	if !strings.Contains(m.lastCfg.System, untrustedOpen) ||
		!strings.Contains(strings.ToUpper(m.lastCfg.System), "DATA, NOT INSTRUCTIONS") {
		t.Errorf("system prompt missing untrusted-content guidance: %q", m.lastCfg.System)
	}
}

// FromContent provenance: when a send_email recipient did NOT appear in the
// user's own message (so it came from mail content the model read), the proposal
// is flagged for extra scrutiny; when the user typed the recipient, it is not.
func TestAgentProposalFromContentWarning(t *testing.T) {
	// Recipient NOT in the user message ⇒ flagged (this is the injection case).
	m := &fakeModel{replies: []string{
		`{"tool":"send_email","args":{"to":"attacker@evil.example","subject":"wire","body":"send funds"}}`,
	}}
	a := New(m, localCfg(), NewFixtureSource(), false)
	res, err := a.AgentTurn(context.Background(), Auth{}, "reply to Dana and send it", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Proposal == nil || !res.Proposal.FromContent || res.Proposal.Warning == "" {
		t.Fatalf("recipient from content should be flagged: %+v", res.Proposal)
	}

	// Recipient the user typed themselves ⇒ NOT flagged.
	m2 := &fakeModel{replies: []string{
		`{"tool":"send_email","args":{"to":"dana@acme.io","subject":"hi","body":"hello"}}`,
	}}
	a2 := New(m2, localCfg(), NewFixtureSource(), false)
	res2, err := a2.AgentTurn(context.Background(), Auth{}, "send an email to dana@acme.io saying hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Proposal == nil || res2.Proposal.FromContent {
		t.Fatalf("user-supplied recipient must not be flagged: %+v", res2.Proposal)
	}
}

// ---- streaming turn (Wave 17) ----------------------------------------------

// collectStream runs an AgentTurnStream and captures the emitted events.
func collectStream(t *testing.T, a *Assistant, userMsg string) ([]AgentStreamEvent, error) {
	t.Helper()
	var evs []AgentStreamEvent
	err := a.AgentTurnStream(context.Background(), Auth{}, userMsg, nil, func(ev AgentStreamEvent) {
		evs = append(evs, ev)
	})
	return evs, err
}

func joinTokens(evs []AgentStreamEvent) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Type == "token" {
			b.WriteString(e.Content)
		}
	}
	return b.String()
}

// A streaming turn with one read-only tool round: emits a status event for the
// tool, then streams the final answer as token events (never the tool-call JSON).
func TestAgentStreamEmitsTokensThenDone(t *testing.T) {
	m := &fakeModel{replies: []string{
		`{"tool":"search_mail","args":{"query":"invoice due"}}`,
		"You owe $128.40 to Tigris, due July 5.",
	}}
	a := New(m, localCfg(), NewFixtureSource(), false)

	evs, err := collectStream(t, a, "when is my invoice due?")
	if err != nil {
		t.Fatal(err)
	}
	// The final answer arrived as token events and reconstructs fully.
	if got := joinTokens(evs); !strings.Contains(got, "128.40") {
		t.Fatalf("streamed answer missing content, got %q", got)
	}
	// A status event announced the read-only tool.
	var sawStatus bool
	for _, e := range evs {
		if e.Type == "status" && e.Tool == "search_mail" {
			sawStatus = true
		}
	}
	if !sawStatus {
		t.Errorf("expected a status event for search_mail, got %+v", evs)
	}
	// The tool-call JSON must NEVER be forwarded as answer tokens.
	if strings.Contains(joinTokens(evs), `"tool"`) {
		t.Errorf("tool-call JSON leaked into the token stream: %q", joinTokens(evs))
	}
	// No proposal for a read-only turn.
	for _, e := range evs {
		if e.Type == "proposal" {
			t.Fatalf("unexpected proposal on a read-only turn: %+v", e)
		}
	}
}

// A streaming turn that ends in a MUTATING action emits a terminal proposal
// event (with an id) and executes NOTHING; the JSON is never streamed as tokens.
func TestAgentStreamMutatingEmitsProposalNoExecute(t *testing.T) {
	m := &fakeModel{replies: []string{
		`{"tool":"send_email","args":{"to":"dana@acme.io","subject":"Signed","body":"Signed and returned."}}`,
	}}
	fx := NewFixtureSource()
	a := New(m, localCfg(), fx, false)

	evs, err := collectStream(t, a, "reply to Dana that it's signed and send it")
	if err != nil {
		t.Fatal(err)
	}
	var prop *Proposal
	for _, e := range evs {
		if e.Type == "proposal" {
			prop = e.Proposal
		}
		if e.Type == "token" {
			t.Fatalf("mutating turn must not stream answer tokens, got token %q", e.Content)
		}
	}
	if prop == nil || prop.Tool != "send_email" || prop.ID == "" {
		t.Fatalf("expected a send_email proposal with an id, got %+v", prop)
	}
	// The confirmation gate held: nothing was sent by the stream itself.
	if n := len(fx.Sent()); n != 0 {
		t.Fatalf("email sent during streaming BEFORE approval — gate breached (%d)", n)
	}
	// Approval path is unchanged: /execute runs the stored proposal.
	if _, err := a.ExecuteProposal(context.Background(), Auth{}, *prop); err != nil {
		t.Fatal(err)
	}
	if sent := fx.Sent(); len(sent) != 1 || sent[0].To != "dana@acme.io" {
		t.Fatalf("execute did not send correctly: %+v", sent)
	}
}

// The egress guard fires BEFORE any streaming/model call: a non-sovereign tier
// blocks with ErrEgressBlocked, zero model calls, zero events, zero mutations.
func TestAgentStreamGuardBlocksBeforeAnyCall(t *testing.T) {
	m := &fakeModel{}
	fx := NewFixtureSource()
	a := New(m, ai.Config{Provider: ai.ProviderClaude, Model: "claude-x"}, fx, false)
	var emitted int
	err := a.AgentTurnStream(context.Background(), Auth{}, "send an email", nil, func(AgentStreamEvent) { emitted++ })
	if err != ErrEgressBlocked {
		t.Fatalf("expected ErrEgressBlocked, got %v", err)
	}
	if m.calls != 0 {
		t.Fatalf("model called %d times despite blocked egress — LEAK", m.calls)
	}
	if emitted != 0 {
		t.Fatalf("events emitted despite blocked egress — %d", emitted)
	}
	if len(fx.Sent())+len(fx.Events())+len(fx.Contacts())+len(fx.Triaged()) != 0 {
		t.Fatal("a mutation happened despite blocked egress")
	}
}

// PROMPT-INJECTION HARDENING holds in the streaming loop too: untrusted tool
// results are wrapped in the data-only delimiters before being fed back.
func TestAgentStreamWrapsToolResultsAsUntrusted(t *testing.T) {
	m := &fakeModel{replies: []string{
		`{"tool":"search_mail","args":{"query":"invoice"}}`,
		"Here is your summary.",
	}}
	a := New(m, localCfg(), NewFixtureSource(), false)
	if _, err := collectStream(t, a, "summarize my invoices"); err != nil {
		t.Fatal(err)
	}
	var framed bool
	for _, msg := range m.lastReq.Messages {
		if strings.Contains(msg.Content, untrustedOpen) && strings.Contains(msg.Content, untrustedClose) &&
			strings.Contains(msg.Content, "TOOL RESULT (search_mail)") {
			framed = true
		}
	}
	if !framed {
		t.Errorf("tool result not wrapped as untrusted in the streaming loop: %+v", m.lastReq.Messages)
	}
}

// classifyPartial: prose streams live; JSON/fenced tool calls stay buffered.
func TestClassifyPartial(t *testing.T) {
	if classifyPartial("") != verdictUndecided {
		t.Error("empty should be undecided")
	}
	if classifyPartial("  ") != verdictUndecided {
		t.Error("whitespace should be undecided")
	}
	if classifyPartial(`{"tool"`) != verdictUndecided {
		t.Error("leading brace (tool call) should be undecided/buffered")
	}
	if classifyPartial("```json") != verdictUndecided {
		t.Error("leading fence should be undecided/buffered")
	}
	if classifyPartial("You owe") != verdictAnswer {
		t.Error("prose should be a streamable answer")
	}
}

// parseToolCall: JSON object with a tool ⇒ call; prose or tool-less JSON ⇒ final.
func TestParseToolCall(t *testing.T) {
	if c, ok := parseToolCall(`{"tool":"search_mail","args":{"query":"x"}}`); !ok || c.Tool != "search_mail" || c.Args["query"] != "x" {
		t.Errorf("plain JSON tool call not parsed: %+v ok=%v", c, ok)
	}
	if c, ok := parseToolCall("```json\n{\"tool\":\"read_thread\",\"args\":{\"id\":\"1\"}}\n```"); !ok || c.Tool != "read_thread" {
		t.Errorf("fenced JSON tool call not parsed: %+v ok=%v", c, ok)
	}
	if _, ok := parseToolCall("Here is your answer about {the invoice}."); ok {
		t.Error("prose with braces misread as a tool call")
	}
	if _, ok := parseToolCall(`{"answer":"no tool here"}`); ok {
		t.Error("tool-less JSON misread as a tool call")
	}
}
