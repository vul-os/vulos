package fleetid

import (
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

func TestDenyAllPolicy_AlwaysDenied(t *testing.T) {
	var p DenyAllPolicy
	got := p.Decide(ApprovalRequest{Action: ActionDeviceEnroll, SubjectID: "s", PayloadHash: hash("p"), RequestID: "r"})
	if got != ApprovalDenied {
		t.Fatalf("DenyAllPolicy must always return ApprovalDenied, got %v", got)
	}
}
