package telephony

// active_test.go — ActiveCall() and GET /api/telephony/call/active.
//
// WHY THIS ENDPOINT NEEDED A TEST OF ITS OWN. Every other call route is a
// command: dial, hang up, accept. This one is the only OBSERVATION, and an
// in-call bar is drawn from it — so its two failure modes are the ones that put
// a lie on the screen:
//
//	saying ACTIVE when nothing is up   → a "Hang up" button that hangs up nothing
//	saying INACTIVE when a call is up  → the user's only Hang up button vanishes
//	                                     mid-call
//
// The second is why a transient mmcli failure must NOT read as "no call": at a
// 3s poll one hiccup would tear the bar down while the modem is still talking.
//
// EVERY assertion below runs against the fake mmcli in calls_test.go, which
// implements the documented `-K` key-value contract. That proves this code
// parses ModemManager's contract correctly. It does NOT prove a real modem
// emits exactly that — no modem exists on the machine this was written on.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const callPath = "/org/freedesktop/ModemManager1/Call/7"

// A call the modem reports as `active` is a call in progress, with the far end
// and the direction carried through.
func TestActiveCallReportsCallInProgress(t *testing.T) {
	f := newCallFake(t)
	s := ownerSvc(nil)

	f.setCall(t, callPath, "+27831112222", "outgoing", "active")

	got := s.ActiveCall()
	if !got.Active {
		t.Fatalf("a call in state `active` must report Active, got %+v", got)
	}
	if got.Number != "+27831112222" {
		t.Errorf("Number = %q, want +27831112222", got.Number)
	}
	if got.Direction != "outgoing" {
		t.Errorf("Direction = %q, want outgoing", got.Direction)
	}
	if got.State != "active" {
		t.Errorf("State = %q, want active — the raw ModemManager state, not a re-coined one", got.State)
	}
}

// A call that is still ringing IS in progress. This is the state an in-call bar
// must be able to tell apart from `active` (one says "Ringing…", the other says
// "On a call"), which is why the raw state is passed through rather than
// flattened to a boolean.
func TestActiveCallDistinguishesRingingFromConnected(t *testing.T) {
	f := newCallFake(t)
	s := ownerSvc(nil)

	for _, state := range []string{"dialing", "ringing-out", "ringing-in", "held", "waiting"} {
		f.setCall(t, callPath, "+27831112222", "incoming", state)
		got := s.ActiveCall()
		if !got.Active {
			t.Errorf("state %q must count as a call in progress, got %+v", state, got)
		}
		if got.State != state {
			t.Errorf("state %q was reported as %q — the UI cannot distinguish ringing from connected", state, got.State)
		}
	}
}

// A `terminated` object is NOT a call. ModemManager can leave one listed briefly
// after the call ends; treating it as live leaves a stale in-call bar on screen
// offering to hang up a call that is already over.
func TestActiveCallIgnoresTerminatedAndUnknownStates(t *testing.T) {
	f := newCallFake(t)
	s := ownerSvc(nil)

	for _, state := range []string{"terminated", "unknown", ""} {
		f.setCall(t, callPath, "+27831112222", "outgoing", state)
		if got := s.ActiveCall(); got.Active {
			t.Errorf("state %q must NOT report a call in progress, got %+v", state, got)
		}
	}
}

// No call objects at all ⇒ inactive.
func TestActiveCallIsInactiveWithNoCalls(t *testing.T) {
	f := newCallFake(t)
	s := ownerSvc(nil)
	f.clearCalls(t)

	if got := s.ActiveCall(); got.Active {
		t.Fatalf("no call objects must report inactive, got %+v", got)
	}
}

// A transient mmcli failure must not be read as "the call ended". This is the
// defect that would be invisible in every happy-path test: a modem that stalls
// for one poll would drop the in-call bar, and with it the only way to hang up.
func TestActiveCallDoesNotTreatAnMmcliFailureAsCallEnded(t *testing.T) {
	f := newCallFake(t)
	s := ownerSvc(nil)

	f.setCall(t, callPath, "+27831112222", "outgoing", "active")
	if !s.ActiveCall().Active {
		t.Fatalf("precondition: the call must be reported active first")
	}

	f.breakList(t)
	got := s.ActiveCall()
	if got.Active {
		t.Fatalf("an mmcli failure must not fabricate a call, got %+v", got)
	}
	if got.Number != "" || got.State != "" {
		t.Errorf("a failed read must carry no call detail, got %+v", got)
	}
}

// With no modem the endpoint answers a clean inactive state rather than an
// error — a box with no GSM hardware is not a broken box. This is the case that
// holds on EVERY box without a modem, which is most of them.
func TestActiveCallHandlerWithNoModemIsCleanlyInactive(t *testing.T) {
	// No fake mmcli on PATH at all: mmcliPresent() is false, modemIndex() is "".
	t.Setenv("PATH", t.TempDir())
	s := ownerSvc(nil)

	mux := http.NewServeMux()
	RegisterHandlers(mux, s)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/call/active", nil)
	req.Header.Set("X-User-ID", "owner")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no modem is a state, not an error)", rec.Code)
	}
	var got ActiveCall
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got.Active {
		t.Errorf("a box with no modem must report no active call, got %+v", got)
	}
}

// The observation route is owner-gated like every other telephony route: who is
// on the box's line is exactly as private as the SMS on it.
func TestActiveCallHandlerDeniesNonOwner(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	s := ownerSvc(nil)

	mux := http.NewServeMux()
	RegisterHandlers(mux, s)

	for _, user := range []string{"", "someone-else"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/telephony/call/active", nil)
		if user != "" {
			req.Header.Set("X-User-ID", user)
		}
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("user %q: status = %d, want 403", user, rec.Code)
		}
	}
}

// The route must be reachable as ITSELF. `GET /api/telephony/` is registered as
// a subtree catch-all serving Status, so a mux that did not prefer the more
// specific pattern would answer this with a modem Status — which decodes
// cleanly into ActiveCall (both are JSON objects) and would silently report
// `active:false` forever, no matter what the modem was doing.
func TestActiveCallRouteIsNotSwallowedByTheStatusSubtree(t *testing.T) {
	f := newCallFake(t)
	s := ownerSvc(nil)
	f.setCall(t, callPath, "+27831112222", "outgoing", "active")

	mux := http.NewServeMux()
	RegisterHandlers(mux, s)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/call/active", nil)
	req.Header.Set("X-User-ID", "owner")
	mux.ServeHTTP(rec, req)

	var got ActiveCall
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if !got.Active || got.Number != "+27831112222" {
		t.Fatalf("the active-call route answered %q — the status subtree swallowed it", rec.Body.String())
	}
}
