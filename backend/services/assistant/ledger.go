package assistant

// ledger.go — the server-side PROPOSAL LEDGER (Wave 13 security).
//
// The confirmation gate for mutating actions (send_email, triage, …) must be
// REAL, not UI-only. Before this ledger, /api/assistant/execute decoded a
// Proposal straight from the request body and acted on the client-supplied args
// verbatim — so a forged proposal that no human ever saw could send mail.
//
// The ledger closes that hole: when AgentTurn produces a mutating Proposal the
// route STORES it here, keyed by its opaque random Id and BOUND to the calling
// user's session. /execute then takes just an {id}, looks up the SERVER-STORED
// proposal (never trusting client args), verifies ownership, executes once, and
// deletes it. A client can no longer fabricate a valid id→args mapping, so a
// forged proposal cannot execute.
//
// Properties enforced here:
//   - single-use   — a proposal is consumed (deleted) on a successful Take.
//   - TTL          — proposals expire (default 10m) and are then rejected + GC'd.
//   - ownership    — a Take by a different user is rejected and does NOT consume
//                    the real owner's proposal (no cross-user probe/DoS).
//   - bounded      — per-user and global caps evict the oldest entries so a
//                    flood of proposals cannot grow memory without limit.

import (
	"errors"
	"sync"
	"time"
)

// Ledger rejection reasons. All map to a 4xx at the HTTP layer — the caller
// cannot tell "unknown" from "expired" from "already used" (all ErrProposalNotFound),
// which avoids leaking which ids exist.
var (
	// ErrProposalNotFound covers unknown, expired, and already-consumed ids.
	ErrProposalNotFound = errors.New("proposal not found, expired, or already used")
	// ErrProposalOwner is returned when the id exists but belongs to another
	// session. It is kept distinct so the server can log a cross-user attempt.
	ErrProposalOwner = errors.New("proposal does not belong to this session")
)

const (
	// defaultProposalTTL is how long an approved-in-UI proposal remains
	// executable. Long enough for a human to read + click, short enough that a
	// leaked id is not indefinitely replayable (it is also single-use).
	defaultProposalTTL = 10 * time.Minute
	// defaultLedgerMax caps total live proposals across all users.
	defaultLedgerMax = 1024
	// defaultLedgerPerUser caps live proposals for a single user, so one session
	// cannot evict everyone else's pending proposals.
	defaultLedgerPerUser = 16
)

type ledgerEntry struct {
	proposal Proposal
	userID   string
	created  time.Time
}

// ProposalLedger is the in-memory, server-side store of mutating proposals
// awaiting the user's approval. It is safe for concurrent use.
type ProposalLedger struct {
	mu      sync.Mutex
	entries map[string]*ledgerEntry
	ttl     time.Duration
	max     int
	perUser int
	now     func() time.Time // injectable clock for tests
}

// NewProposalLedger builds a ledger with the default TTL and size caps.
func NewProposalLedger() *ProposalLedger {
	return &ProposalLedger{
		entries: make(map[string]*ledgerEntry),
		ttl:     defaultProposalTTL,
		max:     defaultLedgerMax,
		perUser: defaultLedgerPerUser,
		now:     time.Now,
	}
}

// Put stores p bound to userID (its key is p.ID, an opaque random id). It first
// garbage-collects expired entries, then enforces the per-user and global caps
// by evicting the oldest entries. Storing a duplicate id overwrites.
func (l *ProposalLedger) Put(userID string, p Proposal) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked()
	l.enforcePerUserLocked(userID)
	for len(l.entries) >= l.max {
		if !l.evictOldestLocked() {
			break
		}
	}
	l.entries[p.ID] = &ledgerEntry{proposal: p, userID: userID, created: l.now()}
}

// Take atomically fetches and REMOVES (single-use) the proposal for id, after
// verifying it has not expired and belongs to userID. Unknown/expired/used ids
// return ErrProposalNotFound; an id owned by a different user returns
// ErrProposalOwner and is left intact (a cross-user probe must not consume the
// real owner's proposal).
func (l *ProposalLedger) Take(userID, id string) (Proposal, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[id]
	if !ok {
		return Proposal{}, ErrProposalNotFound
	}
	if l.now().Sub(e.created) > l.ttl {
		delete(l.entries, id) // expired: consume so it can't be retried
		return Proposal{}, ErrProposalNotFound
	}
	if e.userID != userID {
		return Proposal{}, ErrProposalOwner // do NOT delete: not this user's to consume
	}
	delete(l.entries, id) // single-use
	return e.proposal, nil
}

// Len reports the number of live entries (for tests/telemetry).
func (l *ProposalLedger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// gcLocked drops expired entries. Caller holds l.mu.
func (l *ProposalLedger) gcLocked() {
	cutoff := l.now().Add(-l.ttl)
	for id, e := range l.entries {
		if e.created.Before(cutoff) {
			delete(l.entries, id)
		}
	}
}

// enforcePerUserLocked evicts this user's oldest proposals until they are under
// the per-user cap (leaving room for one more). Caller holds l.mu.
func (l *ProposalLedger) enforcePerUserLocked(userID string) {
	for {
		count := 0
		oldestID := ""
		var oldest time.Time
		for id, e := range l.entries {
			if e.userID != userID {
				continue
			}
			count++
			if oldestID == "" || e.created.Before(oldest) {
				oldestID, oldest = id, e.created
			}
		}
		if count < l.perUser || oldestID == "" {
			return
		}
		delete(l.entries, oldestID)
	}
}

// evictOldestLocked removes the single oldest entry across all users and reports
// whether one was removed. Caller holds l.mu.
func (l *ProposalLedger) evictOldestLocked() bool {
	oldestID := ""
	var oldest time.Time
	for id, e := range l.entries {
		if oldestID == "" || e.created.Before(oldest) {
			oldestID, oldest = id, e.created
		}
	}
	if oldestID == "" {
		return false
	}
	delete(l.entries, oldestID)
	return true
}
