package assistant

import (
	"testing"
	"time"
)

// A stored proposal round-trips by id for its owner, exactly once (single-use):
// the second Take is rejected, so a leaked/replayed id cannot re-execute.
func TestLedgerRoundTripAndSingleUse(t *testing.T) {
	l := NewProposalLedger()
	p := Proposal{ID: "prop_abc", Tool: "send_email", Args: map[string]any{"to": "dana@acme.io"}}
	l.Put("user-1", p)

	got, err := l.Take("user-1", "prop_abc")
	if err != nil {
		t.Fatalf("owner Take failed: %v", err)
	}
	if got.Tool != "send_email" || got.Args["to"] != "dana@acme.io" {
		t.Fatalf("stored args not returned: %+v", got)
	}
	// Single-use: it's gone now.
	if _, err := l.Take("user-1", "prop_abc"); err != ErrProposalNotFound {
		t.Fatalf("expected single-use rejection, got %v", err)
	}
}

// An unknown/forged id is rejected — the whole point: a client cannot fabricate
// a valid id→args mapping.
func TestLedgerUnknownIDRejected(t *testing.T) {
	l := NewProposalLedger()
	if _, err := l.Take("user-1", "prop_forged"); err != ErrProposalNotFound {
		t.Fatalf("forged id must be rejected, got %v", err)
	}
}

// A proposal is bound to its owner: another user cannot Take it, AND the probe
// does not consume the real owner's proposal (no cross-user DoS).
func TestLedgerCrossUserRejectedAndPreserved(t *testing.T) {
	l := NewProposalLedger()
	l.Put("owner", Proposal{ID: "prop_x", Tool: "send_email"})

	if _, err := l.Take("attacker", "prop_x"); err != ErrProposalOwner {
		t.Fatalf("cross-user Take must be rejected, got %v", err)
	}
	// The owner's proposal must survive the attacker's probe.
	if _, err := l.Take("owner", "prop_x"); err != nil {
		t.Fatalf("owner lost their proposal to a cross-user probe: %v", err)
	}
}

// An expired proposal is rejected and garbage-collected.
func TestLedgerExpiryRejected(t *testing.T) {
	l := NewProposalLedger()
	base := time.Now()
	l.now = func() time.Time { return base }
	l.Put("user-1", Proposal{ID: "prop_ttl", Tool: "send_email"})

	// Jump past the TTL.
	l.now = func() time.Time { return base.Add(defaultProposalTTL + time.Second) }
	if _, err := l.Take("user-1", "prop_ttl"); err != ErrProposalNotFound {
		t.Fatalf("expired proposal must be rejected, got %v", err)
	}
	if l.Len() != 0 {
		t.Fatalf("expired proposal should be GC'd, len=%d", l.Len())
	}
}

// The per-user cap evicts the oldest proposals so a flood cannot grow memory.
func TestLedgerPerUserCap(t *testing.T) {
	l := NewProposalLedger()
	base := time.Now()
	for i := 0; i < defaultLedgerPerUser+5; i++ {
		i := i
		l.now = func() time.Time { return base.Add(time.Duration(i) * time.Second) }
		l.Put("user-1", Proposal{ID: "p" + time.Duration(i).String(), Tool: "send_email"})
	}
	if l.Len() > defaultLedgerPerUser {
		t.Fatalf("per-user cap breached: len=%d > %d", l.Len(), defaultLedgerPerUser)
	}
}
