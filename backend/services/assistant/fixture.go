package assistant

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// FixtureSource is an in-memory mailbox used when no live mail service is
// configured (VULOS_MAIL_URL unset) and by tests. It makes the whole assistant
// demoable and testable fully offline — no IMAP, no network. It is deterministic
// relative to a fixed "now" so summaries read naturally.
type FixtureSource struct {
	mu       sync.Mutex
	msgs     []Message
	drafts   []Draft
	sent     []Draft
	events   []CalendarEvent
	contacts []Contact
	triaged  []TriageAction
}

// NewFixtureSource returns a fixture mailbox seeded with a small, realistic
// inbox covering the v1 skills (an urgent thread, a scheduling request, an
// invoice, a newsletter, a personal note).
func NewFixtureSource() *FixtureSource {
	base := time.Date(2026, time.July, 1, 9, 0, 0, 0, time.UTC)
	msgs := []Message{
		{
			UID: "101", From: "dana@acme.io", FromName: "Dana Whitfield", To: "you@vulos.host",
			Subject: "Contract renewal — need your sign-off by Thursday",
			Preview: "Legal cleared the redlines. We just need your signature on the renewal before Thursday EOD or the current rate lapses.",
			Body:    "Hi,\n\nLegal cleared the last redlines on the renewal this morning. The only thing outstanding is your signature. If we don't have it before Thursday end of day, the current rate lapses and we'd have to re-quote at the new list price (about 18% higher).\n\nDocuSign link is in the previous email. Can you get to it today or tomorrow?\n\nThanks,\nDana",
			Date:    base.Add(-2 * time.Hour), Unread: true, Folder: "INBOX",
			MessageID: "<renew-101@acme.io>",
		},
		{
			UID: "102", From: "scheduling@calendly.app", FromName: "Priya Nair", To: "you@vulos.host",
			Subject: "Can we move our 1:1 to Wednesday?",
			Preview: "Something came up Tuesday afternoon. Could we shift our weekly 1:1 to Wednesday at 2pm your time?",
			Body:    "Hey — something came up on Tuesday afternoon so I can't make our usual slot. Could we shift the weekly 1:1 to Wednesday at 2pm your time? If that doesn't work, Thursday morning is also open for me. Let me know what's easiest.\n\n— Priya",
			Date:    base.Add(-5 * time.Hour), Unread: true, Folder: "INBOX",
			MessageID: "<sched-102@calendly.app>",
		},
		{
			UID: "103", From: "billing@tigris.dev", FromName: "Tigris Billing", To: "you@vulos.host",
			Subject: "Invoice #4471 — $128.40 due Jul 5",
			Preview: "Your July invoice for object storage is ready. Amount due: $128.40. Auto-pay is off for this account.",
			Body:    "Invoice #4471\nPeriod: June 2026\nObject storage + egress: $128.40\nDue: July 5, 2026\n\nAuto-pay is currently OFF for this account, so payment must be made manually. Log in to settle.",
			Date:    base.Add(-20 * time.Hour), Unread: true, Folder: "INBOX",
			MessageID: "<inv-4471@tigris.dev>",
		},
		{
			UID: "104", From: "newsletter@frontendweekly.com", FromName: "Frontend Weekly", To: "you@vulos.host",
			Subject: "Issue #388: Signals everywhere, and a CSS anchor deep-dive",
			Preview: "This week: framework signals go mainstream, anchor positioning ships, and 7 links worth your time.",
			Body:    "Welcome to issue #388. This week we cover signals landing in three more frameworks, the new CSS anchor positioning API, and a roundup of performance wins. Unsubscribe anytime.",
			Date:    base.Add(-26 * time.Hour), Unread: false, Folder: "INBOX",
			MessageID: "<fw-388@frontendweekly.com>",
		},
		{
			UID: "105", From: "marcus@northwind.co", FromName: "Marcus Lee", To: "you@vulos.host",
			Subject: "Re: pilot feedback",
			Preview: "The team loved the sovereign angle — nobody else lets us keep mail data on our own box. Two questions before we expand.",
			Body:    "Following up on the pilot. The team genuinely loved the sovereign angle — the fact that nothing leaves our own server was the deciding factor over the incumbents. Two questions before we expand to the wider org:\n\n1. Can the assistant run against our on-prem Ollama box instead of anything cloud?\n2. What's the story for shared/team mailboxes?\n\nHappy to jump on a call next week.\n\nBest,\nMarcus",
			Date:    base.Add(-30 * time.Hour), Unread: false, Folder: "INBOX",
			MessageID: "<pilot-105@northwind.co>",
		},
		{
			UID: "106", From: "mom@family.example", FromName: "Mom", To: "you@vulos.host",
			Subject: "Sunday dinner?",
			Preview: "Are you coming for dinner on Sunday? Bring nothing, just yourself. Let me know so I know how much to cook.",
			Body:    "Are you coming for dinner on Sunday? Bring nothing, just yourself. Let me know by Friday so I know how much to cook. Love, Mom",
			Date:    base.Add(-40 * time.Hour), Unread: false, Folder: "INBOX",
			MessageID: "<dinner-106@family.example>",
		},
	}
	return &FixtureSource{msgs: msgs}
}

func (f *FixtureSource) Name() string { return "fixture" }

func (f *FixtureSource) snapshot() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Message, len(f.msgs))
	copy(out, f.msgs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out
}

func (f *FixtureSource) Recent(_ context.Context, _ Auth, _ string, limit int) ([]Message, error) {
	out := f.snapshot()
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *FixtureSource) Get(_ context.Context, _ Auth, uid, _ string) (Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.msgs {
		if m.UID == uid {
			return m, nil
		}
	}
	return Message{}, errNotFound
}

func (f *FixtureSource) Search(_ context.Context, _ Auth, _ string, query string, limit int) ([]Message, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []Message
	for _, m := range f.snapshot() {
		if q == "" || strings.Contains(strings.ToLower(m.Subject+" "+m.From+" "+m.FromName+" "+m.Body), q) {
			out = append(out, m)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *FixtureSource) SaveDraft(_ context.Context, _ Auth, d Draft) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drafts = append(f.drafts, d)
	return nil
}

// Drafts returns saved drafts (test/demo introspection).
func (f *FixtureSource) Drafts() []Draft {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Draft, len(f.drafts))
	copy(out, f.drafts)
	return out
}

// --- Mutating actions (offline demo/test doubles for the confirm/execute path).

func (f *FixtureSource) SendEmail(_ context.Context, _ Auth, d Draft) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, d)
	return nil
}

func (f *FixtureSource) CreateEvent(_ context.Context, _ Auth, ev CalendarEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (f *FixtureSource) AddContact(_ context.Context, _ Auth, c Contact) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contacts = append(f.contacts, c)
	return nil
}

func (f *FixtureSource) Triage(_ context.Context, _ Auth, a TriageAction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.triaged = append(f.triaged, a)
	return nil
}

// Sent/Events/Contacts/Triaged expose executed mutations for test/demo assertions.
func (f *FixtureSource) Sent() []Draft {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Draft(nil), f.sent...)
}
func (f *FixtureSource) Events() []CalendarEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CalendarEvent(nil), f.events...)
}
func (f *FixtureSource) Contacts() []Contact {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Contact(nil), f.contacts...)
}
func (f *FixtureSource) Triaged() []TriageAction {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]TriageAction(nil), f.triaged...)
}

type sentinelError string

func (e sentinelError) Error() string { return string(e) }

const errNotFound = sentinelError("assistant: message not found")
