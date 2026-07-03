package assistant

import (
	"context"
	"testing"
	"time"
)

// ExecuteProposal is the ONLY path that performs a real mutation, and it is
// reached with a server-stored proposal. These tests pin its fail-closed
// validation: a proposal missing its required field must NOT reach the mail
// service (no half-executed send), and an unknown/non-mutating tool is rejected.
// Together with the ledger tests this closes the loop: a forged id never yields a
// proposal, and a malformed-but-stored proposal never executes a partial action.
func TestExecuteProposalValidationFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		p    Proposal
	}{
		{"send_email without recipient", Proposal{ID: "p1", Tool: "send_email", Args: map[string]any{"subject": "hi", "body": "x"}}},
		{"send_email blank recipient", Proposal{ID: "p1", Tool: "send_email", Args: map[string]any{"to": "   ", "body": "x"}}},
		{"calendar without title", Proposal{ID: "p2", Tool: "create_calendar_event", Args: map[string]any{"start": "2026-07-08T14:00:00Z"}}},
		{"calendar without start", Proposal{ID: "p2", Tool: "create_calendar_event", Args: map[string]any{"title": "sync"}}},
		{"contact without name or email", Proposal{ID: "p3", Tool: "add_contact", Args: map[string]any{"phone": "555"}}},
		{"triage without message_id", Proposal{ID: "p4", Tool: "triage", Args: map[string]any{"action": "archive"}}},
		{"triage without action", Proposal{ID: "p4", Tool: "triage", Args: map[string]any{"message_id": "104"}}},
		{"unknown tool", Proposal{ID: "p5", Tool: "rm_rf_slash", Args: map[string]any{}}},
		{"read-only tool is not mutating", Proposal{ID: "p6", Tool: "search_mail", Args: map[string]any{"query": "x"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := NewFixtureSource()
			a := New(&fakeModel{}, localCfg(), fx, false)
			out, err := a.ExecuteProposal(context.Background(), Auth{}, tc.p)
			if err == nil {
				t.Fatalf("expected validation error, got out=%q", out)
			}
			// Nothing must have been written to the mailbox on a rejected proposal.
			if n := len(fx.Sent()) + len(fx.Events()) + len(fx.Contacts()) + len(fx.Triaged()); n != 0 {
				t.Fatalf("rejected proposal caused a side effect (%d mutations)", n)
			}
		})
	}
}

// A stored proposal executes exactly the server-side args — never anything the
// caller could have appended after approval. (The ledger returns the stored
// Proposal; ExecuteProposal must act only on p.Args.)
func TestExecuteProposalUsesStoredArgsOnly(t *testing.T) {
	fx := NewFixtureSource()
	a := New(&fakeModel{}, localCfg(), fx, false)
	p := Proposal{ID: "p", Tool: "send_email", Args: map[string]any{"to": "real@dest.io", "subject": "S", "body": "B"}}
	if _, err := a.ExecuteProposal(context.Background(), Auth{}, p); err != nil {
		t.Fatal(err)
	}
	sent := fx.Sent()
	if len(sent) != 1 || sent[0].To != "real@dest.io" {
		t.Fatalf("send used unexpected args: %+v", sent)
	}
}

// The global ledger cap evicts the oldest entries across ALL users, so a flood
// of proposals from many sessions cannot grow memory without bound. This drives
// the global-eviction path (evictOldestLocked) that the per-user cap alone does
// not reach.
func TestLedgerGlobalCapEvictsOldest(t *testing.T) {
	l := NewProposalLedger()
	l.max = 4       // shrink caps so the global path trips without 1024 users
	l.perUser = 100 // keep per-user cap out of the way
	base := time.Now()
	// Put 8 proposals across 8 distinct users, oldest first.
	for i := 0; i < 8; i++ {
		i := i
		l.now = func() time.Time { return base.Add(time.Duration(i) * time.Second) }
		l.Put("user-"+time.Duration(i).String(), Proposal{ID: "p" + time.Duration(i).String(), Tool: "send_email"})
	}
	if l.Len() > 4 {
		t.Fatalf("global cap breached: len=%d > 4", l.Len())
	}
	// The oldest (user-0's) proposal must have been evicted first.
	l.now = func() time.Time { return base.Add(8 * time.Second) }
	if _, err := l.Take("user-"+time.Duration(0).String(), "p"+time.Duration(0).String()); err != ErrProposalNotFound {
		t.Fatalf("oldest entry should have been evicted, got %v", err)
	}
}
