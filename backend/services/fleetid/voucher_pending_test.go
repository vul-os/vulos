package fleetid

import (
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// operatorPeer is a VoucherService behind a real server whose approve/pending/
// dismiss gate is switchable, so the tests can exercise both an operator and a
// caller the gate refuses.
type operatorPeer struct {
	box       box
	policy    *ManualApprovalPolicy
	svc       *VoucherService
	srv       *httptest.Server
	gateAllow bool
}

func newOperatorPeer(t *testing.T, b box) *operatorPeer {
	t.Helper()
	policy := NewManualApprovalPolicy()
	svc, err := NewVoucherService(b.priv, policy)
	if err != nil {
		t.Fatalf("NewVoucherService: %v", err)
	}
	op := &operatorPeer{box: b, policy: policy, svc: svc, gateAllow: true}
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux, func(*http.Request) bool { return op.gateAllow })
	op.srv = httptest.NewServer(mux)
	t.Cleanup(op.srv.Close)
	return op
}

func (o *operatorPeer) get(t *testing.T, path string) (int, []byte) {
	t.Helper()
	resp, err := o.srv.Client().Get(o.srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, out
}

func (o *operatorPeer) post(t *testing.T, path string, body any) (int, []byte) {
	t.Helper()
	resp, err := o.srv.Client().Post(o.srv.URL+path, "application/json", jsonBody(t, body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, out
}

// askOnce sends one freshly signed vouch request from subject to the peer.
func askOnce(t *testing.T, o *operatorPeer, subject box, action, requestID string, payload []byte) (int, vouchWireResponse) {
	t.Helper()
	req := VouchRequest{
		Action:      action,
		SubjectID:   subject.vulosID,
		PayloadHash: b64(payload),
		RequestID:   requestID,
	}
	if err := SignVouchRequest(subject.priv, &req, time.Now()); err != nil {
		t.Fatalf("SignVouchRequest: %v", err)
	}
	code, body := o.post(t, DefaultVouchPath, req)
	var wire vouchWireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode vouch response (%d): %v body=%s", code, err, body)
	}
	return code, wire
}

// TestBreakGlassApprovalPath_EndToEnd is the whole point of this feature, wired
// end to end over real HTTP: a peer asks, THIS box records it, an operator READS
// the queue, approves using only what the queue told them, and the peer's next
// ask returns a signed cert.
//
// The load-bearing detail is that the approve call is built EXCLUSIVELY from the
// pending list. Nothing in the test hands the operator the payload hash or the
// request id out of band, because a real operator has no such channel — if the
// queue does not carry enough to approve with, the manual gate is unreachable
// and this test fails.
func TestBreakGlassApprovalPath_EndToEnd(t *testing.T) {
	voucher := newOperatorPeer(t, newBox(t))
	subject := newBox(t)
	payload := hash("the exact break-glass payload")
	const requestID = "req-e2e-001"

	// 1. The peer asks. Deny-by-default: no cert, no signature, just pending.
	code, wire := askOnce(t, voucher, subject, ActionIdentityRecovery, requestID, payload)
	if code != http.StatusAccepted || wire.Status != "pending" {
		t.Fatalf("first ask: got %d/%q, want 202/pending", code, wire.Status)
	}
	if wire.Cert != nil {
		t.Fatal("a cert was issued with no operator approval")
	}

	// 2. The operator opens the queue.
	code, body := voucher.get(t, DefaultPendingPath)
	if code != http.StatusOK {
		t.Fatalf("GET %s: %d body=%s", DefaultPendingPath, code, body)
	}
	var list pendingListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode pending list: %v body=%s", err, body)
	}
	if len(list.Pending) != 1 {
		t.Fatalf("the operator's queue does not show the request that is waiting on them: %s", body)
	}
	item := list.Pending[0]

	// Everything the operator needs to JUDGE it must be there. An approve button
	// on a row that does not say who/what/when has moved the gate, not kept it.
	if item.SubjectID != subject.vulosID {
		t.Errorf("subject_id = %q, want the asking box's identity", item.SubjectID)
	}
	if item.Action != ActionIdentityRecovery {
		t.Errorf("action = %q — the operator cannot see what power they would grant", item.Action)
	}
	if item.PayloadHash != b64(payload) {
		t.Errorf("payload_hash = %q, want %q — this is the string compared out of band", item.PayloadHash, b64(payload))
	}
	if item.RequestID != requestID {
		t.Errorf("request_id = %q", item.RequestID)
	}
	if item.FirstSeenAt == "" || item.LastSeenAt == "" {
		t.Error("no freshness shown — the operator cannot tell a live request from a stale one")
	}
	if item.RequesterEndpoint == "" {
		t.Error("no claimed origin shown")
	}
	if list.SelfVulosID != voucher.svc.SelfVulosID() {
		t.Errorf("self_vulos_id = %q, want this box's own identity", list.SelfVulosID)
	}

	// 3. The operator approves — using ONLY the tuple the list gave them.
	code, body = voucher.post(t, DefaultApprovePath, approveRequest{
		Action:      item.Action,
		SubjectID:   item.SubjectID,
		PayloadHash: item.PayloadHash,
		RequestID:   item.RequestID,
		TTLSeconds:  300,
	})
	if code != http.StatusOK {
		t.Fatalf("approving the request AS LISTED failed: %d body=%s — the queue does not carry enough to approve with", code, body)
	}

	// 4. The peer asks again and now gets a real, verifiable cert.
	code, wire = askOnce(t, voucher, subject, ActionIdentityRecovery, requestID, payload)
	if code != http.StatusOK || wire.Status != "granted" {
		t.Fatalf("after approval: got %d/%q, want 200/granted", code, wire.Status)
	}
	if wire.Cert == nil {
		t.Fatal("granted with no cert")
	}
	cert := *wire.Cert
	if cert.VoucherVulosID != voucher.svc.SelfVulosID() {
		t.Errorf("cert signed by %q, want this box", cert.VoucherVulosID)
	}
	if cert.SubjectID != subject.vulosID || cert.Action != ActionIdentityRecovery || cert.PayloadHash != b64(payload) {
		t.Errorf("the issued cert is not bound to what the operator approved: %+v", cert)
	}
	// The cert must actually verify under the voucher's own rostered key — a
	// well-shaped-but-unverifiable cert would count for nothing at VerifyQuorum.
	if err := verifyCanonical(cert, cert.Sig, voucher.box.priv.Public().(ed25519.PublicKey)); err != nil {
		t.Errorf("issued cert does not verify: %v", err)
	}

	// 5. The queue is now clear: the request is no longer waiting on anyone.
	_, body = voucher.get(t, DefaultPendingPath)
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Pending) != 0 {
		t.Errorf("a granted request is still shown as awaiting approval: %s", body)
	}
}

// TestBreakGlassApprovalPath_ReachesVerifyQuorum carries the previous test's
// path all the way to the chokepoint that actually authorizes anything.
//
// Two independent boxes each run the full operator loop — read the queue,
// approve what it showed — and the two certs that fall out are handed to
// VerifyQuorum against a roster. Stopping at "a cert came back" would leave the
// interesting question unanswered: certs produced this way must be the certs
// break-glass recovery accepts, or the queue is a screen that produces nothing
// usable.
func TestBreakGlassApprovalPath_ReachesVerifyQuorum(t *testing.T) {
	subject := newBox(t)
	v1 := newOperatorPeer(t, newBox(t))
	v2 := newOperatorPeer(t, newBox(t))
	payload := hash("break-glass identity recovery payload")
	const requestID = "req-e2e-quorum"

	var certs []VouchCert
	for _, v := range []*operatorPeer{v1, v2} {
		if code, wire := askOnce(t, v, subject, ActionIdentityRecovery, requestID, payload); code != http.StatusAccepted {
			t.Fatalf("first ask: %d/%q", code, wire.Status)
		}
		// The operator at THIS box reads their own queue and approves from it.
		_, body := v.get(t, DefaultPendingPath)
		var list pendingListResponse
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Pending) != 1 {
			t.Fatalf("queue at %s: %s", v.svc.SelfVulosID(), body)
		}
		it := list.Pending[0]
		if code, b := v.post(t, DefaultApprovePath, approveRequest{
			Action: it.Action, SubjectID: it.SubjectID, PayloadHash: it.PayloadHash, RequestID: it.RequestID,
		}); code != http.StatusOK {
			t.Fatalf("approve: %d %s", code, b)
		}
		code, wire := askOnce(t, v, subject, ActionIdentityRecovery, requestID, payload)
		if code != http.StatusOK || wire.Cert == nil {
			t.Fatalf("second ask: %d/%q cert=%v", code, wire.Status, wire.Cert)
		}
		certs = append(certs, *wire.Cert)
	}

	roster := newRoster(v1.box, v2.box)
	res, err := VerifyQuorum(ActionIdentityRecovery, subject.vulosID, payload, certs, roster, 2, time.Now())
	if err != nil {
		t.Fatalf("certs produced by the operator approval path do NOT satisfy VerifyQuorum: %v (result=%+v)", err, res)
	}
	if len(res.Counted) != 2 {
		t.Errorf("counted %d vouchers, want 2: %+v", len(res.Counted), res)
	}

	// Control: the same bundle must still fail for a DIFFERENT payload, or the
	// success above would say nothing about binding.
	if _, err := VerifyQuorum(ActionIdentityRecovery, subject.vulosID, hash("some other payload"), certs, roster, 2, time.Now()); err == nil {
		t.Error("the gathered bundle authorized a payload nobody approved")
	}
}

// The approve gate protects the READ too: what this box has been asked to vouch
// for names peers and the powers they want, and is operator-only.
func TestPendingQueue_GateRefused(t *testing.T) {
	voucher := newOperatorPeer(t, newBox(t))
	subject := newBox(t)
	payload := hash("p")
	askOnce(t, voucher, subject, ActionDeviceEnroll, "req-gate", payload)

	voucher.gateAllow = false
	if code, body := voucher.get(t, DefaultPendingPath); code != http.StatusForbidden {
		t.Errorf("GET %s with the gate refusing: %d, want 403. body=%s", DefaultPendingPath, code, body)
	}
	if code, body := voucher.post(t, DefaultDismissPath, approveRequest{
		Action: ActionDeviceEnroll, SubjectID: subject.vulosID, PayloadHash: b64(payload), RequestID: "req-gate",
	}); code != http.StatusForbidden {
		t.Errorf("POST %s with the gate refusing: %d, want 403. body=%s", DefaultDismissPath, code, body)
	}
	// And the refusal must be real, not a 403 over an already-emptied queue.
	voucher.gateAllow = true
	if code, body := voucher.get(t, DefaultPendingPath); code != http.StatusOK {
		t.Fatalf("the gate-allowed read failed too (%d) — the 403s above proved nothing. body=%s", code, body)
	}
}

// Dismissal clears a record without authorizing anything, and is not a block.
func TestPendingQueue_DismissGrantsNothing(t *testing.T) {
	voucher := newOperatorPeer(t, newBox(t))
	subject := newBox(t)
	payload := hash("p")
	const requestID = "req-dismiss"
	askOnce(t, voucher, subject, ActionSSHRecovery, requestID, payload)

	code, body := voucher.post(t, DefaultDismissPath, approveRequest{
		Action: ActionSSHRecovery, SubjectID: subject.vulosID, PayloadHash: b64(payload), RequestID: requestID,
	})
	if code != http.StatusOK {
		t.Fatalf("dismiss: %d body=%s", code, body)
	}
	var list pendingListResponse
	_, body = voucher.get(t, DefaultPendingPath)
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Pending) != 0 {
		t.Errorf("dismiss did not clear the record: %s", body)
	}

	// The peer re-asks: still refused (dismiss authorized nothing) and visible
	// again (dismiss is not a block-list).
	code, wire := askOnce(t, voucher, subject, ActionSSHRecovery, requestID, payload)
	if code != http.StatusAccepted || wire.Status != "pending" {
		t.Fatalf("after dismiss the peer got %d/%q — dismissal must never authorize", code, wire.Status)
	}
	_, body = voucher.get(t, DefaultPendingPath)
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Pending) != 1 {
		t.Errorf("a re-ask after dismissal did not reappear — dismiss is behaving as a block-list: %s", body)
	}
}

// "Is this one of my boxes?" is the first question an operator asks and the one
// they cannot answer by eye from a base64 Vula ID. The annotation carries it —
// and its ABSENCE must be distinguishable from a negative answer.
func TestPendingQueue_RosterAnnotation(t *testing.T) {
	subject := newBox(t)
	payload := hash("p")

	t.Run("unannotated says so", func(t *testing.T) {
		voucher := newOperatorPeer(t, newBox(t))
		askOnce(t, voucher, subject, ActionIdentityRecovery, "r", payload)
		_, body := voucher.get(t, DefaultPendingPath)
		var list pendingListResponse
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		if list.Annotated {
			t.Error("annotated=true with no annotator wired — a UI would read 'not recognised' " +
				"where the truth is 'not checked'")
		}
		if list.Pending[0].Subject != nil {
			t.Errorf("an annotation was invented with no annotator: %+v", list.Pending[0].Subject)
		}
	})

	t.Run("annotated carries the roster verdict", func(t *testing.T) {
		voucher := newOperatorPeer(t, newBox(t))
		voucher.svc.SetPeerAnnotator(func(id string) PeerAnnotation {
			if id == subject.vulosID {
				return PeerAnnotation{Known: true, DisplayName: "workshop-box", Revoked: true}
			}
			return PeerAnnotation{}
		})
		askOnce(t, voucher, subject, ActionIdentityRecovery, "r", payload)
		_, body := voucher.get(t, DefaultPendingPath)
		var list pendingListResponse
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		if !list.Annotated {
			t.Fatal("annotated=false with an annotator wired")
		}
		ann := list.Pending[0].Subject
		if ann == nil {
			t.Fatal("no annotation on the listed request")
		}
		if !ann.Known || ann.DisplayName != "workshop-box" {
			t.Errorf("roster membership not surfaced: %+v", ann)
		}
		if !ann.Revoked {
			t.Error("a REVOKED peer's request is shown as routine — VerifyQuorum would refuse " +
				"its cert anyway, and an operator approving it should be told")
		}
	})

	t.Run("a stranger is reported as not known", func(t *testing.T) {
		voucher := newOperatorPeer(t, newBox(t))
		voucher.svc.SetPeerAnnotator(func(string) PeerAnnotation { return PeerAnnotation{} })
		askOnce(t, voucher, subject, ActionIdentityRecovery, "r", payload)
		_, body := voucher.get(t, DefaultPendingPath)
		var list pendingListResponse
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		if !list.Annotated {
			t.Fatal("annotated=false with an annotator wired")
		}
		if list.Pending[0].Subject == nil || list.Pending[0].Subject.Known {
			t.Errorf("an unrostered stranger is not reported as unknown: %+v", list.Pending[0].Subject)
		}
	})
}

// A self-vouch is refused BEFORE the policy is consulted, so it must never even
// reach the queue — an operator must not be shown a row they could click.
func TestPendingQueue_SelfVouchNeverQueued(t *testing.T) {
	b := newBox(t)
	voucher := newOperatorPeer(t, b)
	payload := hash("p")
	code, wire := askOnce(t, voucher, b, ActionIdentityRecovery, "req-self", payload)
	if code != http.StatusForbidden || wire.Status != "denied" {
		t.Fatalf("self-vouch: got %d/%q, want 403/denied", code, wire.Status)
	}
	_, body := voucher.get(t, DefaultPendingPath)
	var list pendingListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Pending) != 0 {
		t.Errorf("a self-vouch reached the operator's approval queue: %s", body)
	}
}

// An unauthenticated request is refused by VerifyVouchRequest before the policy
// runs, so it must not be able to fill the operator's queue either — otherwise
// anyone who can reach the port can flood the one screen the security control
// lives on.
func TestPendingQueue_UnauthenticatedNeverQueued(t *testing.T) {
	voucher := newOperatorPeer(t, newBox(t))
	subject := newBox(t)
	code, _ := voucher.post(t, DefaultVouchPath, VouchRequest{
		Type:        VouchRequestType,
		Action:      ActionIdentityRecovery,
		SubjectID:   subject.vulosID,
		PayloadHash: b64(hash("p")),
		RequestID:   "req-unsigned",
		IssuedAt:    time.Now().UTC().Format(time.RFC3339),
		// no Sig
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("unsigned request: %d, want 401", code)
	}
	_, body := voucher.get(t, DefaultPendingPath)
	var list pendingListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Pending) != 0 {
		t.Errorf("an unauthenticated request reached the operator's queue: %s", body)
	}
}

// DenyAllPolicy has no queue to show and no approvals to take; both operator
// endpoints must say so rather than 200-with-an-empty-list, which would look
// like "nothing is waiting".
func TestPendingQueue_DenyAllPolicyReportsNotImplemented(t *testing.T) {
	b := newBox(t)
	svc, err := NewVoucherService(b.priv, DenyAllPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux, func(*http.Request) bool { return true })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + DefaultPendingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("GET %s on a DenyAll box: %d, want 501 (not an empty queue)", DefaultPendingPath, resp.StatusCode)
	}
}
