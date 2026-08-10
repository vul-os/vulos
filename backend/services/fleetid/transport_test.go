package fleetid

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func b64(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

func jsonBody(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewReader(b)
}

// peerServer wires one VoucherService up behind a real httptest.Server,
// giving each test peer its own independent identity, approval policy, and
// HTTP endpoint — exercising the full wire path (JSON over real HTTP), not
// just in-process function calls.
type peerServer struct {
	box    box
	policy *ManualApprovalPolicy
	svc    *VoucherService
	srv    *httptest.Server
}

func newPeerServer(t *testing.T, b box) *peerServer {
	t.Helper()
	policy := NewManualApprovalPolicy()
	svc, err := NewVoucherService(b.priv, policy)
	if err != nil {
		t.Fatalf("NewVoucherService: %v", err)
	}
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux, func(r *http.Request) bool { return true }) // approve endpoint gate not exercised over HTTP in these tests
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &peerServer{box: b, policy: policy, svc: svc, srv: srv}
}

// ─── 1. A gathered bundle passes fleetid.VerifyQuorum ─────────────────────────

func TestGatherQuorum_ApprovedBundle_PassesVerifyQuorum(t *testing.T) {
	subject := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	p1 := newPeerServer(t, v1)
	p2 := newPeerServer(t, v2)

	action := ActionIdentityRecovery
	pl := hash("break-glass-payload")
	requestID := "req-happy-path"

	// The out-of-band operator approval at EACH peer, matching the exact
	// request that will be sent.
	p1.policy.Approve(action, subject.vulaID, pl, requestID, time.Minute)
	p2.policy.Approve(action, subject.vulaID, pl, requestID, time.Minute)

	greq := GatherRequest{
		Action:      action,
		SubjectID:   subject.vulaID,
		PayloadHash: pl,
		SubjectKey:  subject.priv,
		RequestID:   requestID,
		Peers:       []string{p1.srv.URL, p2.srv.URL},
		Threshold:   2,
	}
	res, err := GatherQuorum(context.Background(), HTTPTransport{}, greq, GatherOptions{})
	if err != nil {
		t.Fatalf("expected GatherQuorum to succeed, got err=%v result=%+v", err, res)
	}
	if len(res.Certs) != 2 {
		t.Fatalf("expected 2 gathered certs, got %d", len(res.Certs))
	}

	roster := newRoster(v1, v2)
	now := time.Now().UTC()
	qres, qerr := VerifyQuorum(action, subject.vulaID, pl, res.Certs, roster, 2, now)
	if qerr != nil {
		t.Fatalf("gathered bundle must pass VerifyQuorum, got err=%v dropped=%v", qerr, qres.Dropped)
	}
	if !qres.OK || qres.DistinctValid != 2 {
		t.Fatalf("expected OK with 2 distinct valid, got OK=%v distinct=%d", qres.OK, qres.DistinctValid)
	}
}

// ─── 2. Self-vouch is refused by the peer, never even reaching the policy ─────

func TestVoucherService_RefusesSelfVouch_EvenIfPreApproved(t *testing.T) {
	self := newBox(t)
	peer := newPeerServer(t, self) // this peer's OWN identity is `self`

	action := ActionSSHRecovery
	pl := hash("self-vouch-attempt")
	requestID := "req-self"

	// Even if an operator mistakenly (or maliciously) pre-approves a request
	// where the subject IS this peer itself, the handler must refuse before
	// ever consulting the policy.
	peer.policy.Approve(action, self.vulaID, pl, requestID, time.Minute)

	// Signed with the subject's OWN key, so the request authenticates and the
	// self-vouch refusal — not the auth check — is what is being exercised.
	cert, err := HTTPTransport{}.RequestVouch(context.Background(), peer.srv.URL, signedRequest(t, self, VouchRequest{
		Action:      action,
		SubjectID:   self.vulaID, // the peer vouching for itself
		PayloadHash: b64(pl),
		RequestID:   requestID,
	}))
	if cert != nil {
		t.Fatalf("self-vouch must never produce a cert, got %+v", cert)
	}
	if err == nil {
		t.Fatalf("expected an error for a self-vouch request")
	}
	if !errors.Is(err, ErrVouchDenied) {
		t.Fatalf("self-vouch must be DENIED (not merely unauthenticated): %v", err)
	}
}

// signedRequest returns req signed by b's fleet key, as a real initiator would
// send it.
func signedRequest(t *testing.T, b box, req VouchRequest) VouchRequest {
	t.Helper()
	if err := SignVouchRequest(b.priv, &req, time.Now()); err != nil {
		t.Fatalf("SignVouchRequest: %v", err)
	}
	return req
}

// ─── 3. An unapproved request yields no cert ──────────────────────────────────

func TestVoucherService_UnapprovedRequest_YieldsNoCert(t *testing.T) {
	subject := newBox(t)
	v1 := newBox(t)
	p1 := newPeerServer(t, v1) // no Approve call at all

	action := ActionDeviceEnroll
	pl := hash("unapproved-payload")

	cert, err := HTTPTransport{}.RequestVouch(context.Background(), p1.srv.URL, signedRequest(t, subject, VouchRequest{
		Action:      action,
		SubjectID:   subject.vulaID,
		PayloadHash: b64(pl),
		RequestID:   "req-unapproved",
	}))
	if cert != nil {
		t.Fatalf("unapproved request must not yield a cert, got %+v", cert)
	}
	if err == nil {
		t.Fatalf("expected an error for an unapproved request")
	}
}

// GatherQuorum over unapproved peers must also fail closed with no usable
// bundle (ties task 3's requirement to the fan-out path, not just the
// single-peer transport call).
func TestGatherQuorum_NoPeerApproves_InsufficientAndEmpty(t *testing.T) {
	subject := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	p1 := newPeerServer(t, v1)
	p2 := newPeerServer(t, v2)
	// Deliberately no Approve calls.

	greq := GatherRequest{
		Action:      ActionIdentityRecovery,
		SubjectID:   subject.vulaID,
		PayloadHash: hash("p"),
		SubjectKey:  subject.priv,
		RequestID:   "req-none-approved",
		Peers:       []string{p1.srv.URL, p2.srv.URL},
		Threshold:   2,
	}
	res, err := GatherQuorum(context.Background(), HTTPTransport{}, greq, GatherOptions{})
	if len(res.Certs) != 0 {
		t.Fatalf("expected 0 certs gathered, got %d", len(res.Certs))
	}
	if err == nil {
		t.Fatalf("expected GatherQuorum to report insufficient gathering")
	}
}

// ─── 4. A bundle below threshold is rejected by the caller (VerifyQuorum) ─────

func TestGatherQuorum_BelowThreshold_RejectedByVerifyQuorum(t *testing.T) {
	subject := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	p1 := newPeerServer(t, v1)
	p2 := newPeerServer(t, v2) // will NOT approve — simulates an unresponsive/refusing peer

	action := ActionIdentityRecovery
	pl := hash("below-threshold-payload")
	requestID := "req-below-threshold"

	p1.policy.Approve(action, subject.vulaID, pl, requestID, time.Minute)
	// p2 never approves.

	greq := GatherRequest{
		Action:      action,
		SubjectID:   subject.vulaID,
		PayloadHash: pl,
		SubjectKey:  subject.priv,
		RequestID:   requestID,
		Peers:       []string{p1.srv.URL, p2.srv.URL},
		Threshold:   2,
	}
	res, gerr := GatherQuorum(context.Background(), HTTPTransport{}, greq, GatherOptions{})
	if gerr == nil {
		t.Fatalf("expected GatherQuorum itself to report insufficient gathering")
	}
	if len(res.Certs) != 1 {
		t.Fatalf("expected exactly 1 gathered cert (only p1 approved), got %d", len(res.Certs))
	}

	// Even though GatherQuorum already flags this, the actual authorization
	// chokepoint is VerifyQuorum — confirm IT independently rejects the
	// under-threshold bundle too (the caller must never bypass it).
	roster := newRoster(v1, v2)
	qres, qerr := VerifyQuorum(action, subject.vulaID, pl, res.Certs, roster, 2, time.Now().UTC())
	if qerr == nil || qres.OK {
		t.Fatalf("under-threshold bundle must be rejected by VerifyQuorum; OK=%v err=%v", qres.OK, qerr)
	}
	if qres.DistinctValid != 1 {
		t.Fatalf("expected exactly 1 distinct valid, got %d", qres.DistinctValid)
	}
}

// ─── Approve endpoint: operator gate and self-vouch-approval refusal ──────────

func TestApproveEndpoint_GateRefused_NoGrant(t *testing.T) {
	v1 := newBox(t)
	policy := NewManualApprovalPolicy()
	svc, err := NewVoucherService(v1.priv, policy)
	if err != nil {
		t.Fatalf("NewVoucherService: %v", err)
	}
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux, func(r *http.Request) bool { return false }) // gate always refuses
	srv := httptest.NewServer(mux)
	defer srv.Close()

	subject := newBox(t)
	resp, err := http.Post(srv.URL+DefaultApprovePath, "application/json", jsonBody(t, approveRequest{
		Action: ActionDeviceEnroll, SubjectID: subject.vulaID, PayloadHash: b64(hash("p")), RequestID: "r",
	}))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 when approve gate refuses, got %d", resp.StatusCode)
	}
	// Confirm nothing was actually granted.
	d := policy.Decide(ApprovalRequest{Action: ActionDeviceEnroll, SubjectID: subject.vulaID, PayloadHash: hash("p"), RequestID: "r"})
	if d != ApprovalPending {
		t.Fatalf("gate-refused approve call must not have granted anything, got %v", d)
	}
}
