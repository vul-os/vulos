package assistant

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"vulos/backend/services/ai"
)

// Completer is the minimal model seam the assistant needs. It is satisfied
// as-is by *ai.Service (services/ai), which is the SEAM the parallel llmux
// gateway work sits behind. The assistant depends on this interface (not the
// concrete service) so the model backend can be swapped and unit-tested without
// a live model.
//
// >>> RECONCILIATION NOTE (for the llmux agent) <<<
// The assistant calls exactly two methods, with the exact ai.Config /
// ai.CompletionRequest / ai.StreamChunk types from services/ai. The instance's
// sovereign config comes from ai.DefaultConfig() (defaults to Ollama at
// localhost:11434). If llmux fronts model calls, either (a) keep *ai.Service as
// the implementation and point ai.DefaultConfig() at the llmux endpoint (a
// local/loopback URL, so it stays "on-instance"), or (b) provide an adapter
// implementing this interface. Do NOT bypass Guard(): it must wrap every call
// that carries mail content.
type Completer interface {
	Complete(ctx context.Context, cfg ai.Config, req ai.CompletionRequest) (string, error)
	Stream(ctx context.Context, cfg ai.Config, req ai.CompletionRequest, onChunk func(ai.StreamChunk)) error
}

// Assistant answers questions about the user's mail using retrieved context and
// an on-instance model, subject to the sovereign egress guard.
type Assistant struct {
	model         Completer
	cfg           ai.Config
	mail          MailSource
	allowExternal bool
}

// New builds an assistant. model is the ai seam, cfg the model config
// (typically ai.DefaultConfig()), mail the local mailbox source, and
// allowExternal the operator opt-in for off-box egress (default false).
func New(model Completer, cfg ai.Config, mail MailSource, allowExternal bool) *Assistant {
	return &Assistant{model: model, cfg: cfg, mail: mail, allowExternal: allowExternal}
}

// Sovereignty reports the current no-egress state (for the status endpoint/UI).
func (a *Assistant) Sovereignty() Sovereignty {
	return Evaluate(a.cfg, a.allowExternal)
}

// MailName returns the backing mail source name.
func (a *Assistant) MailName() string { return a.mail.Name() }

const systemPreamble = `You are Vula, the private AI assistant built into Vulos OS. You run ON THE USER'S OWN SERVER; the email you are given never leaves their instance.

Rules:
- Answer ONLY from the provided email context. If the context does not contain the answer, say so plainly — never invent senders, dates, amounts, or facts.
- Be concise and direct. Prefer short paragraphs and tight bullet lists.
- When you reference a message, name the sender and subject so the user can find it.
- You are talking to the mailbox owner. Use "you" for them.`

// complete runs a single guarded, non-streaming completion with a fresh system
// prompt. This is the single choke point for non-stream skills; Guard() blocks
// any completion that would leak mail off-box.
func (a *Assistant) complete(ctx context.Context, system, user string) (string, error) {
	if err := Guard(a.cfg, a.allowExternal); err != nil {
		return "", err
	}
	cfg := a.cfg
	cfg.System = system
	return a.model.Complete(ctx, cfg, ai.CompletionRequest{
		Messages:  []ai.Message{{Role: "user", Content: user}},
		MaxTokens: 1024,
	})
}

// ---- Retrieval (RAG) -------------------------------------------------------
//
// v1 retrieval is deliberately simple and correct: recent inbox messages plus
// server-side keyword-search hits for the query, deduped and capped. It is a
// lexical baseline — good enough to be useful and honest about what it does.
//
// WHERE A REAL EMBEDDING INDEX GOES: replace retrieve() with a call into an
// on-box vector index (backend/internal/vecdb + services/embeddings already
// exist; vulos-mail emits body/attachment text at ingest per internal/search's
// design note). Embed the query, ANN-search the per-account index, and merge
// with the recency signal below. The interface and callers do not change.

const (
	maxContextMessages = 12
	maxBodyChars       = 1600
	maxContextChars    = 14000
)

// retrieve gathers the most relevant messages for a query: recent inbox first,
// then keyword-search hits, deduped by UID and capped. When query is empty it
// returns pure recency (e.g. for "summarize my inbox").
func (a *Assistant) retrieve(ctx context.Context, auth Auth, query string, limit int) ([]Message, error) {
	recent, err := a.mail.Recent(ctx, auth, defaultFolder, limit)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(recent))
	out := make([]Message, 0, limit)
	for _, m := range recent {
		if !seen[m.UID] {
			seen[m.UID] = true
			out = append(out, m)
		}
	}
	if q := strings.TrimSpace(query); q != "" {
		hits, err := a.mail.Search(ctx, auth, defaultFolder, q, limit)
		if err == nil {
			// Prepend search hits (more relevant) that aren't already present.
			var extra []Message
			for _, m := range hits {
				if !seen[m.UID] {
					seen[m.UID] = true
					extra = append(extra, m)
				}
			}
			out = append(extra, out...)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// formatContext renders messages into a compact, token-bounded text block for
// the model. bodies controls whether full (truncated) bodies are included or
// just envelopes+previews.
func formatContext(msgs []Message, bodies bool) string {
	var b strings.Builder
	total := 0
	for i, m := range msgs {
		date := ""
		if !m.Date.IsZero() {
			date = m.Date.Format("Mon 2 Jan 15:04")
		}
		unread := ""
		if m.Unread {
			unread = " [UNREAD]"
		}
		entry := fmt.Sprintf("--- Message %d%s ---\nFrom: %s\nSubject: %s\nDate: %s\n",
			i+1, unread, m.Sender(), strEmpty(m.Subject, "(no subject)"), date)
		text := m.Body
		if !bodies || text == "" {
			text = m.Preview
		}
		text = strings.TrimSpace(collapseWS(text))
		if len(text) > maxBodyChars {
			text = text[:maxBodyChars] + "…"
		}
		if text != "" {
			entry += "\n" + text + "\n"
		}
		if total+len(entry) > maxContextChars {
			break
		}
		b.WriteString(entry)
		b.WriteString("\n")
		total += len(entry)
	}
	if b.Len() == 0 {
		return "(the mailbox has no messages)"
	}
	return b.String()
}

// ---- Skills ----------------------------------------------------------------

// SummarizeInbox produces a short digest of recent inbox activity.
func (a *Assistant) SummarizeInbox(ctx context.Context, auth Auth) (string, error) {
	msgs, err := a.mail.Recent(ctx, auth, defaultFolder, 20)
	if err != nil {
		return "", err
	}
	user := "Summarize my inbox. Group related messages, highlight anything time-sensitive, and keep it to a few tight bullets.\n\nINBOX:\n" +
		formatContext(msgs, false)
	return a.complete(ctx, systemPreamble, user)
}

// SummarizeThread summarizes a single message and its quoted history.
func (a *Assistant) SummarizeThread(ctx context.Context, auth Auth, uid, folder string) (string, error) {
	m, err := a.mail.Get(ctx, auth, uid, folder)
	if err != nil {
		return "", err
	}
	user := "Summarize this email thread: the key points, decisions, and any question or action directed at me.\n\nTHREAD:\n" +
		formatContext([]Message{m}, true)
	return a.complete(ctx, systemPreamble, user)
}

// DraftReply drafts a reply to a message. instructions is optional guidance
// ("keep it short", "decline politely", ...).
func (a *Assistant) DraftReply(ctx context.Context, auth Auth, uid, folder, instructions string) (string, error) {
	m, err := a.mail.Get(ctx, auth, uid, folder)
	if err != nil {
		return "", err
	}
	guidance := strings.TrimSpace(instructions)
	if guidance == "" {
		guidance = "a clear, appropriately-toned reply"
	}
	user := fmt.Sprintf(
		"Draft %s to the email below. Output ONLY the reply body — no subject line, no preamble, no explanation, no signature placeholder.\n\nORIGINAL EMAIL:\n%s",
		guidance, formatContext([]Message{m}, true))
	return a.complete(ctx, systemPreamble, user)
}

// SaveDraftReply drafts a reply and persists it to the Drafts folder, returning
// the drafted text.
func (a *Assistant) SaveDraftReply(ctx context.Context, auth Auth, uid, folder, instructions string) (string, error) {
	m, err := a.mail.Get(ctx, auth, uid, folder)
	if err != nil {
		return "", err
	}
	text, err := a.DraftReply(ctx, auth, uid, folder, instructions)
	if err != nil {
		return "", err
	}
	subject := m.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	err = a.mail.SaveDraft(ctx, auth, Draft{
		To:        m.From,
		Subject:   subject,
		Text:      text,
		InReplyTo: m.MessageID,
	})
	return text, err
}

// Attention answers "what needs my attention today" — a prioritized triage of
// the mailbox.
func (a *Assistant) Attention(ctx context.Context, auth Auth) (string, error) {
	msgs, err := a.mail.Recent(ctx, auth, defaultFolder, 30)
	if err != nil {
		return "", err
	}
	// Surface unread first so the model's recency window favors what's new.
	sort.SliceStable(msgs, func(i, j int) bool {
		if msgs[i].Unread != msgs[j].Unread {
			return msgs[i].Unread
		}
		return msgs[i].Date.After(msgs[j].Date)
	})
	user := fmt.Sprintf(
		"Today is %s. From my recent mail, tell me what needs my attention today. Order by urgency. For each item give one line: who, what they need, and the deadline if any. Ignore newsletters and automated notifications unless they require action. If nothing needs attention, say so.\n\nRECENT MAIL:\n%s",
		time.Now().Format("Monday, 2 January 2006"), formatContext(msgs, false))
	return a.complete(ctx, systemPreamble, user)
}

// SearchResult is the answer to a natural-language mail search.
type SearchResult struct {
	Answer  string    `json:"answer"`
	Results []Message `json:"results"`
}

// Search answers a natural-language question over the mailbox and returns both
// the model's answer and the retrieved messages it was grounded on.
func (a *Assistant) Search(ctx context.Context, auth Auth, query string) (SearchResult, error) {
	msgs, err := a.retrieve(ctx, auth, query, maxContextMessages)
	if err != nil {
		return SearchResult{}, err
	}
	user := fmt.Sprintf(
		"Answer this question about my mail: %q\nGround your answer only in the messages below and cite the sender+subject you used.\n\nMESSAGES:\n%s",
		query, formatContext(msgs, true))
	ans, err := a.complete(ctx, systemPreamble, user)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Answer: ans, Results: msgs}, nil
}

// ChatStream is the freeform assistant chat: it retrieves mail context relevant
// to the user's message, then streams a grounded answer through onChunk. Guard
// runs before any mail content reaches the model.
func (a *Assistant) ChatStream(ctx context.Context, auth Auth, userMsg string, history []ai.Message, onChunk func(ai.StreamChunk)) error {
	if err := Guard(a.cfg, a.allowExternal); err != nil {
		return err
	}
	msgs, err := a.retrieve(ctx, auth, userMsg, maxContextMessages)
	if err != nil {
		return err
	}
	system := systemPreamble + "\n\nThe user's relevant email context for this turn:\n" + formatContext(msgs, true)
	cfg := a.cfg
	cfg.System = system
	convo := append([]ai.Message{}, history...)
	convo = append(convo, ai.Message{Role: "user", Content: userMsg})
	return a.model.Stream(ctx, cfg, ai.CompletionRequest{Messages: convo, MaxTokens: 1024}, onChunk)
}

// ---- small helpers ---------------------------------------------------------

func strEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
