// call_history_record_test.go — the peer call log must contain the calls that
// actually happened.
//
// THE DEFECT. CallHistRecord and POST /api/peering/call/history/record had ZERO
// non-test callers. The store was constructed, both routes were registered, and
// nothing on the box ever wrote a single entry — so
// GET /api/peering/call/history answered [] in production forever. Meanwhile
// the Phone app's Recents tab fetched it on every load
// (phone/telephonyApi.ts:182 → usePhoneData.ts → RecentsTab), merged the empty
// result with the GSM log, and rendered a "Vulos-to-Vulos calls appear here"
// surface that nothing could ever fill. A read of an endpoint nothing writes is
// a state that can never happen, shipped as a feature.
//
// It is reachable now: the shell auto-declines an inbound peer call, that lands
// in handleCallReject, and this is where it gets logged.
//
// WHAT THESE TESTS REFUSE TO LET BACK IN:
//   - a relay that records nothing (the original defect);
//   - a call logged TWICE, which a second decline from a second browser tab
//     would otherwise produce;
//   - "completed" on a call nobody answered, and a DURATION counted from when
//     the phone started ringing — both would put conversations in the log that
//     never took place.

package peering

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// callHistFake captures what the relay records, without touching disk.
type callHistFake struct {
	entries []*CallHistEntry
	err     error
}

func (f *callHistFake) CallHistRecord(e *CallHistEntry) error {
	if f.err != nil {
		return f.err
	}
	f.entries = append(f.entries, e)
	return nil
}

// callHistRelay builds a relay with call history attached and one approved
// contact, and returns everything a test needs to drive a call to its end.
func callHistRelay(t *testing.T, selfID string) (*CallRelay, *callHistFake, *callFakeContacts) {
	t.Helper()
	contacts := newCallFakeContacts()
	relay, _, remoteAddr := newCallRelay(t, selfID, contacts, &callFakeHub{})
	contacts.add(callApprovedContact("vulos:priya", remoteAddr))
	hist := &callHistFake{}
	relay.SetCallHistory(hist)
	return relay, hist, contacts
}

// ringing installs an inbound session: priya called us, we have not answered.
func ringing(relay *CallRelay, callID, selfID string) *callSession {
	sess := &callSession{
		id:        callID,
		callerID:  "vulos:priya",
		calleeID:  selfID,
		state:     callStateRinging,
		createdAt: time.Now().Add(-8 * time.Second),
	}
	relay.mu.Lock()
	relay.sessions[callID] = sess
	relay.mu.Unlock()
	return sess
}

func TestCallHistory_DeclinedInboundCallIsRecorded(t *testing.T) {
	const self = "vulos:me"
	relay, hist, _ := callHistRelay(t, self)
	ringing(relay, "call-1", self)

	rr := callPostJSON(t, relay.handleCallReject, self, callRejectReq{CallID: "call-1"})
	if rr.Code != http.StatusOK {
		t.Fatalf("reject: got %d, want 200", rr.Code)
	}

	if len(hist.entries) != 1 {
		t.Fatalf("recorded %d entries, want 1 — an unrecorded call is a Recents tab backed by nothing", len(hist.entries))
	}
	e := hist.entries[0]
	if e.Status != CallHistStatusRejected {
		t.Errorf("status = %q, want %q", e.Status, CallHistStatusRejected)
	}
	if e.Direction != CallHistDirInbound {
		t.Errorf("direction = %q, want %q", e.Direction, CallHistDirInbound)
	}
	if e.PeerID != "vulos:priya" {
		t.Errorf("peer_id = %q, want the CALLER, not ourselves", e.PeerID)
	}
	if e.PeerDisplay != "Test User" {
		t.Errorf("peer_display = %q, want the contact's display name", e.PeerDisplay)
	}
	if e.DurationSec != 0 {
		t.Errorf("duration = %d, want 0 — nobody spoke on a declined call", e.DurationSec)
	}
	if e.ID != "call-1" {
		t.Errorf("id = %q, want the call id", e.ID)
	}
}

func TestCallHistory_OneCallIsLoggedOnce(t *testing.T) {
	const self = "vulos:me"
	relay, hist, _ := callHistRelay(t, self)
	ringing(relay, "call-dup", self)

	// Two browser tabs both decline the same call. The second finds no session.
	_ = callPostJSON(t, relay.handleCallReject, self, callRejectReq{CallID: "call-dup"})
	rr := callPostJSON(t, relay.handleCallReject, self, callRejectReq{CallID: "call-dup"})

	if rr.Code != http.StatusNotFound {
		t.Errorf("second decline: got %d, want 404", rr.Code)
	}
	if len(hist.entries) != 1 {
		t.Fatalf("recorded %d entries for one call, want 1", len(hist.entries))
	}
}

func TestCallHistory_AnsweredCallIsCompletedWithSpokenDuration(t *testing.T) {
	const self = "vulos:me"
	relay, hist, _ := callHistRelay(t, self)
	sess := ringing(relay, "call-2", self)
	// Rang for 8s (see createdAt), answered 3s ago.
	relay.mu.Lock()
	sess.state = callStateActive
	sess.answeredAt = time.Now().Add(-3 * time.Second)
	relay.mu.Unlock()

	rr := callPostJSON(t, relay.handleCallHangup, self, callHangupReq{CallID: "call-2"})
	if rr.Code != http.StatusOK {
		t.Fatalf("hangup: got %d, want 200", rr.Code)
	}

	e := hist.entries[0]
	if e.Status != CallHistStatusCompleted {
		t.Errorf("status = %q, want %q", e.Status, CallHistStatusCompleted)
	}
	// The duration is time SPOKEN, not time rung. Counting from createdAt would
	// report ~11s for a 3s conversation.
	if e.DurationSec < 2 || e.DurationSec > 5 {
		t.Errorf("duration = %ds, want ~3s (spoken), not ~11s (rung + spoken)", e.DurationSec)
	}
}

func TestCallHistory_UnansweredCallIsNotCompleted(t *testing.T) {
	const self = "vulos:me"

	t.Run("we never picked up — missed", func(t *testing.T) {
		relay, hist, _ := callHistRelay(t, self)
		ringing(relay, "call-3", self)
		callPostJSON(t, relay.handleCallHangup, self, callHangupReq{CallID: "call-3"})

		if got := hist.entries[0].Status; got != CallHistStatusMissed {
			t.Errorf("status = %q, want %q — nobody answered, so nothing was completed", got, CallHistStatusMissed)
		}
		if got := hist.entries[0].DurationSec; got != 0 {
			t.Errorf("duration = %d, want 0", got)
		}
	})

	t.Run("they never picked up — outgoing", func(t *testing.T) {
		relay, hist, _ := callHistRelay(t, self)
		relay.mu.Lock()
		relay.sessions["call-4"] = &callSession{
			id: "call-4", callerID: self, calleeID: "vulos:priya",
			state: callStateRinging, createdAt: time.Now().Add(-5 * time.Second),
		}
		relay.mu.Unlock()
		callPostJSON(t, relay.handleCallHangup, self, callHangupReq{CallID: "call-4"})

		e := hist.entries[0]
		if e.Status != CallHistStatusOutgoing {
			t.Errorf("status = %q, want %q", e.Status, CallHistStatusOutgoing)
		}
		if e.Direction != CallHistDirOutbound {
			t.Errorf("direction = %q, want %q", e.Direction, CallHistDirOutbound)
		}
		if e.PeerID != "vulos:priya" {
			t.Errorf("peer_id = %q, want the callee", e.PeerID)
		}
	})
}

func TestCallHistory_RemoteEndedCallIsRecorded(t *testing.T) {
	const self = "vulos:me"

	t.Run("caller gave up while it rang — missed", func(t *testing.T) {
		relay, hist, contacts := callHistRelay(t, self)
		contacts.add(callApprovedContact("vulos:priya", "127.0.0.1:1"))
		ringing(relay, "call-5", self)

		env := callMakeSignalingEnvelope("vulos:priya", self, sigKindHangup, "call-5", nil)
		rr := httptest.NewRecorder()
		relay.HandleInboundCallSignal(rr, callInboundRequest(env))

		if len(hist.entries) != 1 {
			t.Fatalf("recorded %d entries, want 1", len(hist.entries))
		}
		if got := hist.entries[0].Status; got != CallHistStatusMissed {
			t.Errorf("status = %q, want %q", got, CallHistStatusMissed)
		}
	})

	t.Run("they declined us — rejected", func(t *testing.T) {
		relay, hist, contacts := callHistRelay(t, self)
		contacts.add(callApprovedContact("vulos:priya", "127.0.0.1:1"))
		relay.mu.Lock()
		relay.sessions["call-6"] = &callSession{
			id: "call-6", callerID: self, calleeID: "vulos:priya",
			state: callStateRinging, createdAt: time.Now(),
		}
		relay.mu.Unlock()

		env := callMakeSignalingEnvelope("vulos:priya", self, sigKindReject, "call-6", nil)
		rr := httptest.NewRecorder()
		relay.HandleInboundCallSignal(rr, callInboundRequest(env))

		if len(hist.entries) != 1 {
			t.Fatalf("recorded %d entries, want 1", len(hist.entries))
		}
		if got := hist.entries[0].Status; got != CallHistStatusRejected {
			t.Errorf("status = %q, want %q", got, CallHistStatusRejected)
		}
	})
}

func TestCallHistory_RelayWithoutAStoreStillWorks(t *testing.T) {
	const self = "vulos:me"
	contacts := newCallFakeContacts()
	relay, _, remoteAddr := newCallRelay(t, self, contacts, &callFakeHub{})
	contacts.add(callApprovedContact("vulos:priya", remoteAddr))
	ringing(relay, "call-7", self)

	// No SetCallHistory: recording is optional and must never break a call.
	rr := callPostJSON(t, relay.handleCallReject, self, callRejectReq{CallID: "call-7"})
	if rr.Code != http.StatusOK {
		t.Fatalf("reject without a history store: got %d, want 200", rr.Code)
	}
}

func TestCallHistory_StorageFailureDoesNotBreakTheCall(t *testing.T) {
	const self = "vulos:me"
	relay, hist, _ := callHistRelay(t, self)
	hist.err = errStr("disk full")
	ringing(relay, "call-8", self)

	rr := callPostJSON(t, relay.handleCallReject, self, callRejectReq{CallID: "call-8"})
	if rr.Code != http.StatusOK {
		t.Fatalf("reject with a failing log: got %d, want 200 — the signalling already happened", rr.Code)
	}
}

// TestCallHistory_DeclinedCallReachesTheReadEndpoint is the end-to-end shape of
// the defect: drive a real decline through a real store and read it back off
// the very endpoint the Phone app calls. This is the test that goes red if the
// relay is ever disconnected from the log again.
func TestCallHistory_DeclinedCallReachesTheReadEndpoint(t *testing.T) {
	const self = "vulos:me"
	mux := http.NewServeMux()
	store := RegisterCallHistoryHandlers(mux, t.TempDir())

	contacts := newCallFakeContacts()
	relay, _, remoteAddr := newCallRelay(t, self, contacts, &callFakeHub{})
	contacts.add(callApprovedContact("vulos:priya", remoteAddr))
	relay.SetCallHistory(store)
	RegisterCallHandlers(mux, relay)

	// Empty before anything happens — which is what it answered forever.
	if got := readHistory(t, mux); len(got) != 0 {
		t.Fatalf("history started with %d entries, want 0", len(got))
	}

	ringing(relay, "call-e2e", self)
	if rr := callPostJSON(t, relay.handleCallReject, self, callRejectReq{CallID: "call-e2e"}); rr.Code != http.StatusOK {
		t.Fatalf("reject: got %d, want 200", rr.Code)
	}

	entries := readHistory(t, mux)
	if len(entries) != 1 {
		t.Fatalf("GET /api/peering/call/history returned %d entries, want 1 — the log is only real if it can be read back", len(entries))
	}
	if entries[0].PeerID != "vulos:priya" || entries[0].Status != CallHistStatusRejected {
		t.Errorf("read back %+v, want a rejected call from vulos:priya", entries[0])
	}
}

func readHistory(t *testing.T, mux *http.ServeMux) []*CallHistEntry {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/peering/call/history", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("history list: got %d, want 200", rr.Code)
	}
	var out []*CallHistEntry
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	return out
}
