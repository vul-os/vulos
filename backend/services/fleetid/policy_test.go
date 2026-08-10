package fleetid

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestManualApprovalPolicy_DefaultsToPending(t *testing.T) {
	p := NewManualApprovalPolicy()
	d := p.Decide(ApprovalRequest{
		Action: ActionIdentityRecovery, SubjectID: "subject", PayloadHash: hash("p"), RequestID: "r1",
	})
	if d != ApprovalPending {
		t.Fatalf("fresh policy must default to Pending (never auto-grant), got %v", d)
	}
}

func TestManualApprovalPolicy_ApproveThenGrant_SingleUse(t *testing.T) {
	p := NewManualApprovalPolicy()
	pl := hash("payload")
	req := ApprovalRequest{Action: ActionDeviceEnroll, SubjectID: "subj", PayloadHash: pl, RequestID: "req-1"}

	// Before Approve: pending.
	if got := p.Decide(req); got != ApprovalPending {
		t.Fatalf("expected Pending before Approve, got %v", got)
	}
	p.Approve(req.Action, req.SubjectID, req.PayloadHash, req.RequestID, time.Minute)
	if got := p.Decide(req); got != ApprovalGranted {
		t.Fatalf("expected Granted after Approve, got %v", got)
	}
	// Single-use: the grant is consumed by the first Decide.
	if got := p.Decide(req); got != ApprovalPending {
		t.Fatalf("expected Pending on second Decide (grant must be single-use), got %v", got)
	}
}

func TestManualApprovalPolicy_ApprovalDoesNotCoverDifferentPayload(t *testing.T) {
	p := NewManualApprovalPolicy()
	p.Approve(ActionDeviceEnroll, "subj", hash("payload-A"), "req-1", time.Minute)
	got := p.Decide(ApprovalRequest{
		Action: ActionDeviceEnroll, SubjectID: "subj", PayloadHash: hash("payload-B"), RequestID: "req-1",
	})
	if got != ApprovalPending {
		t.Fatalf("approval for one payload hash must not cover a different one; got %v", got)
	}
}

func TestManualApprovalPolicy_ExpiredGrant_IsPending(t *testing.T) {
	p := NewManualApprovalPolicy()
	req := ApprovalRequest{Action: ActionSSHRecovery, SubjectID: "subj", PayloadHash: hash("p"), RequestID: "r"}
	p.Approve(req.Action, req.SubjectID, req.PayloadHash, req.RequestID, time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	if got := p.Decide(req); got != ApprovalPending {
		t.Fatalf("expired grant must be Pending, not Granted; got %v", got)
	}
}

func TestManualApprovalPolicy_Revoke(t *testing.T) {
	p := NewManualApprovalPolicy()
	req := ApprovalRequest{Action: ActionSSHRecovery, SubjectID: "subj", PayloadHash: hash("p"), RequestID: "r"}
	p.Approve(req.Action, req.SubjectID, req.PayloadHash, req.RequestID, time.Minute)
	p.Revoke(req.Action, req.SubjectID, req.PayloadHash, req.RequestID)
	if got := p.Decide(req); got != ApprovalPending {
		t.Fatalf("revoked grant must be Pending; got %v", got)
	}
}

// ─── The pending queue: the operator's only view of what may be approved ─────

// TestManualApprovalPolicy_PendingIsRecorded is the load-bearing one. Approve
// takes the exact tuple the REQUESTING box chose; if Decide does not record it,
// no human can ever produce those arguments and the manual gate is unreachable.
func TestManualApprovalPolicy_PendingIsRecorded(t *testing.T) {
	p := NewManualApprovalPolicy()
	pl := hash("payload")
	req := ApprovalRequest{
		Action: ActionIdentityRecovery, SubjectID: "subj-A", PayloadHash: pl, RequestID: "req-1",
		RequesterEndpoint: "203.0.113.9:44321", ReceivedAt: time.Now(),
	}
	if got := p.Decide(req); got != ApprovalPending {
		t.Fatalf("precondition: want Pending, got %v", got)
	}

	pend := p.Pending()
	if len(pend) != 1 {
		t.Fatalf("a request that was asked for and not granted is invisible to the operator: %d records", len(pend))
	}
	got := pend[0]
	if got.Action != req.Action || got.SubjectID != req.SubjectID || got.RequestID != req.RequestID {
		t.Errorf("pending record does not identify the request: %+v", got)
	}
	if !bytes.Equal(got.PayloadHash, pl) {
		t.Errorf("payload hash not recorded: %x want %x", got.PayloadHash, pl)
	}
	if got.RequesterEndpoint != req.RequesterEndpoint {
		t.Errorf("claimed origin not recorded: %q", got.RequesterEndpoint)
	}
	if got.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", got.Attempts)
	}

	// The whole point: the recorded tuple must be sufficient to approve with.
	p.Approve(got.Action, got.SubjectID, got.PayloadHash, got.RequestID, time.Minute)
	if d := p.Decide(req); d != ApprovalGranted {
		t.Fatalf("approving the tuple AS RECORDED did not grant it (got %v) — the record is unusable", d)
	}
}

// A record is a view, not an authority: it must not, by existing, move the gate.
func TestManualApprovalPolicy_PendingRecordGrantsNothing(t *testing.T) {
	p := NewManualApprovalPolicy()
	req := ApprovalRequest{Action: ActionDeviceEnroll, SubjectID: "s", PayloadHash: hash("p"), RequestID: "r"}
	for i := 0; i < 5; i++ {
		if d := p.Decide(req); d != ApprovalGranted {
			continue
		} else {
			t.Fatalf("attempt %d: repeatedly asking granted the request without any operator approval", i)
		}
	}
	if len(p.Pending()) != 1 {
		t.Fatalf("re-asks must collapse onto one record, got %d", len(p.Pending()))
	}
	// Pending() must hand out COPIES. A caller (the HTTP handler, a UI adapter)
	// holding a slice that aliases the policy's own state can corrupt the record
	// an operator is about to read and approve from.
	//
	// Assert the alias directly by re-reading, not by re-Deciding: Decide keys
	// off the REQUEST, so a corrupted pending record would not change its answer
	// and a decision-based check silently passes on an aliased slice.
	orig := append([]byte(nil), p.Pending()[0].PayloadHash...)
	snap := p.Pending()
	snap[0].PayloadHash[0] ^= 0xff
	snap[0].SubjectID = "someone-else"
	after := p.Pending()[0]
	if !bytes.Equal(after.PayloadHash, orig) {
		t.Errorf("Pending() aliases the policy's own record: writing to the returned copy changed "+
			"the payload hash the operator would be shown (%x -> %x)", orig, after.PayloadHash)
	}
	if after.SubjectID != req.SubjectID {
		t.Errorf("Pending() aliases the record's subject: %q", after.SubjectID)
	}
	if d := p.Decide(req); d != ApprovalPending {
		t.Fatalf("mutating a Pending() copy changed the decision: %v", d)
	}
}

func TestManualApprovalPolicy_ReAskUpdatesFreshness(t *testing.T) {
	p := NewManualApprovalPolicy()
	t0 := time.Now()
	req := ApprovalRequest{
		Action: ActionSSHRecovery, SubjectID: "s", PayloadHash: hash("p"), RequestID: "r",
		RequesterEndpoint: "10.0.0.1:1", ReceivedAt: t0,
	}
	p.Decide(req)
	later := req
	later.ReceivedAt = t0.Add(90 * time.Second)
	later.RequesterEndpoint = "10.0.0.2:2"
	p.Decide(later)

	pend := p.Pending()
	if len(pend) != 1 {
		t.Fatalf("want 1 record, got %d", len(pend))
	}
	if pend[0].Attempts != 2 {
		t.Errorf("Attempts = %d, want 2 — an operator cannot see a request being hammered", pend[0].Attempts)
	}
	if !pend[0].FirstSeenAt.Equal(t0) {
		t.Errorf("FirstSeenAt moved: %v want %v — how long this has been asking is evidence", pend[0].FirstSeenAt, t0)
	}
	if !pend[0].LastSeenAt.Equal(t0.Add(90 * time.Second)) {
		t.Errorf("LastSeenAt = %v, want the most recent ask", pend[0].LastSeenAt)
	}
	if pend[0].RequesterEndpoint != "10.0.0.2:2" {
		t.Errorf("claimed origin = %q, want the most recent", pend[0].RequesterEndpoint)
	}
}

// A granted request has stopped waiting on the operator and must leave the
// queue; a queue that only ever grows is one nobody reads.
func TestManualApprovalPolicy_GrantClearsThePendingRecord(t *testing.T) {
	p := NewManualApprovalPolicy()
	req := ApprovalRequest{Action: ActionDeviceEnroll, SubjectID: "s", PayloadHash: hash("p"), RequestID: "r"}
	p.Decide(req)
	p.Approve(req.Action, req.SubjectID, req.PayloadHash, req.RequestID, time.Minute)
	if d := p.Decide(req); d != ApprovalGranted {
		t.Fatalf("want Granted, got %v", d)
	}
	if pend := p.Pending(); len(pend) != 0 {
		t.Fatalf("a granted request is still listed as awaiting approval: %+v", pend)
	}
}

// An expired approval is a request that is waiting again — it must reappear,
// not vanish, or the gate looks broken instead of strict.
func TestManualApprovalPolicy_ExpiredGrantReappearsAsPending(t *testing.T) {
	p := NewManualApprovalPolicy()
	now := time.Now()
	p.SetClock(func() time.Time { return now })
	req := ApprovalRequest{Action: ActionSSHRecovery, SubjectID: "s", PayloadHash: hash("p"), RequestID: "r"}
	p.Approve(req.Action, req.SubjectID, req.PayloadHash, req.RequestID, time.Minute)

	now = now.Add(2 * time.Minute) // the grant has expired
	if d := p.Decide(req); d != ApprovalPending {
		t.Fatalf("expired grant must be Pending, got %v", d)
	}
	if pend := p.Pending(); len(pend) != 1 {
		t.Fatalf("a request refused because the approval EXPIRED is not shown to the operator: %d records", len(pend))
	}
}

// A grant for one payload must not silently hide a request for another.
func TestManualApprovalPolicy_PayloadMismatchIsStillListed(t *testing.T) {
	p := NewManualApprovalPolicy()
	p.Approve(ActionDeviceEnroll, "s", hash("payload-A"), "r", time.Minute)
	req := ApprovalRequest{Action: ActionDeviceEnroll, SubjectID: "s", PayloadHash: hash("payload-B"), RequestID: "r"}
	if d := p.Decide(req); d != ApprovalPending {
		t.Fatalf("want Pending, got %v", d)
	}
	pend := p.Pending()
	if len(pend) != 1 {
		t.Fatalf("want the mismatching request listed, got %d records", len(pend))
	}
	// Honest note on the strength of this last check: today approvalKey.payload
	// is derived from the request, so recording from the key and recording from
	// the request are indistinguishable and no mutation of the CURRENT code can
	// make this line fail on its own. It is kept as a refactor guard, aimed at
	// exactly the change policy.go's own bytes.Equal comment warns about — an
	// approvalKey whose payload becomes a truncated or hashed form. If that
	// happens, the record must still carry the hash that was ASKED for, because
	// that is the string the operator compares out of band.
	if !bytes.Equal(pend[0].PayloadHash, hash("payload-B")) {
		t.Error("the listed record carries the APPROVED payload, not the one actually asked for — " +
			"an operator would approve a hash they never saw")
	}
}

func TestManualApprovalPolicy_PendingIsBoundedAndEvictionIsVisible(t *testing.T) {
	p := NewManualApprovalPolicy()
	now := time.Now()
	p.SetClock(func() time.Time { return now })

	const flood = maxPendingVouches + 40
	for i := 0; i < flood; i++ {
		now = now.Add(time.Millisecond)
		p.Decide(ApprovalRequest{
			Action: ActionIdentityRecovery, SubjectID: "attacker", PayloadHash: hash("p"),
			RequestID: fmt.Sprintf("req-%04d", i), ReceivedAt: now,
		})
	}
	pend := p.Pending()
	if len(pend) > maxPendingVouches {
		t.Fatalf("pending queue is unbounded: %d records from a stranger", len(pend))
	}
	if got := p.PendingEvicted(); got == 0 {
		t.Fatal("records were dropped at the cap but PendingEvicted() reports 0 — " +
			"a flood that hides a real request would be invisible to the operator")
	} else if got != flood-maxPendingVouches {
		t.Errorf("PendingEvicted() = %d, want %d", got, flood-maxPendingVouches)
	}
	// Eviction is least-recently-asked: the newest ask must survive.
	newest := fmt.Sprintf("req-%04d", flood-1)
	var found bool
	for _, v := range pend {
		if v.RequestID == newest {
			found = true
		}
	}
	if !found {
		t.Errorf("the most recent request was evicted; eviction is not least-recently-asked")
	}
}

func TestManualApprovalPolicy_StalePendingIsPruned(t *testing.T) {
	p := NewManualApprovalPolicy()
	now := time.Now()
	p.SetClock(func() time.Time { return now })
	p.Decide(ApprovalRequest{
		Action: ActionDeviceEnroll, SubjectID: "s", PayloadHash: hash("p"), RequestID: "old", ReceivedAt: now,
	})
	if len(p.Pending()) != 1 {
		t.Fatal("precondition: the record was not made")
	}
	now = now.Add(VouchMaxAge + time.Minute)
	if pend := p.Pending(); len(pend) != 0 {
		t.Fatalf("a request nobody has re-asked for longer than the whole freshness window is still queued: %+v", pend)
	}
}

// Forget is the operator dismissing a request — it must not approve anything,
// and it must not act as a block-list either.
func TestManualApprovalPolicy_ForgetDismissesWithoutApproving(t *testing.T) {
	p := NewManualApprovalPolicy()
	req := ApprovalRequest{Action: ActionDeviceEnroll, SubjectID: "s", PayloadHash: hash("p"), RequestID: "r"}
	p.Decide(req)
	p.Forget(req.Action, req.SubjectID, req.PayloadHash, req.RequestID)
	if pend := p.Pending(); len(pend) != 0 {
		t.Fatalf("Forget did not dismiss the record: %+v", pend)
	}
	if d := p.Decide(req); d != ApprovalPending {
		t.Fatalf("Forget granted something (%v) — dismissal must never authorize", d)
	}
	if len(p.Pending()) != 1 {
		t.Error("a re-ask after a dismissal did not reappear — Forget is behaving as a block-list")
	}
}

func TestDenyAllPolicy_AlwaysDenied(t *testing.T) {
	var p DenyAllPolicy
	got := p.Decide(ApprovalRequest{Action: ActionDeviceEnroll, SubjectID: "s", PayloadHash: hash("p"), RequestID: "r"})
	if got != ApprovalDenied {
		t.Fatalf("DenyAllPolicy must always return ApprovalDenied, got %v", got)
	}
}
