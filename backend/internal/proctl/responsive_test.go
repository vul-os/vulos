package proctl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeHTTP_RespondingWhenTheAppReplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	got := ProbeHTTP(context.Background(), srv.Client(), srv.URL)
	if got.Status != StatusResponding {
		t.Fatalf("Status = %q, want %q (detail: %s)", got.Status, StatusResponding, got.Detail)
	}
	if got.Method != MethodHTTPProbe {
		t.Errorf("Method = %q, want %q", got.Method, MethodHTTPProbe)
	}
}

// A 500 is EVIDENCE OF LIFE: the process accepted a connection, routed the
// request and wrote a reply. Calling that "not responding" would have the user
// force-quitting an app that is merely erroring.
func TestProbeHTTP_A500IsStillResponding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()

	got := ProbeHTTP(context.Background(), srv.Client(), srv.URL)
	if got.Status != StatusResponding {
		t.Fatalf("Status = %q, want %q — a 500 proves the event loop is running", got.Status, StatusResponding)
	}
}

func TestProbeHTTP_NotRespondingWhenTheAppNeverReplies(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // never answers: the app's loop is wedged
	}))
	defer func() { close(block); srv.Close() }()

	client := &http.Client{Timeout: 300 * time.Millisecond}
	got := ProbeHTTP(context.Background(), client, srv.URL)
	if got.Status != StatusNotResponding {
		t.Fatalf("Status = %q, want %q", got.Status, StatusNotResponding)
	}
	if got.Method != MethodHTTPProbe {
		t.Errorf("Method = %q, want %q — an unanswered probe is still a MEASUREMENT and must say so", got.Method, MethodHTTPProbe)
	}
}

// The four categories must not collapse into each other. This asserts the
// property the founder's brief turns on: only a category with a real mechanism
// is ever allowed to report a responding/not-responding verdict.
func TestOnlyMeasuredCategoriesMayClaimAVerdict(t *testing.T) {
	verdict := map[Status]bool{StatusResponding: true, StatusNotResponding: true}

	cases := []struct {
		name string
		got  Responsiveness
	}{
		{"streamed X11 app", StreamUnknown()},
		{"built-in React surface", BuiltinNotApplicable()},
		{"system process in D state", StateNote("D")},
		{"system process in R state", StateNote("R")},
		{"system process in S state", StateNote("S")},
		{"system process, zombie", StateNote("Z")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if verdict[tc.got.Status] {
				t.Fatalf("%s reports %q — no mechanism exists to measure it, so it must not claim a verdict",
					tc.name, tc.got.Status)
			}
			if tc.got.Method == MethodHTTPProbe {
				t.Fatalf("%s claims method %q but no probe was made", tc.name, tc.got.Method)
			}
			if tc.got.Detail == "" {
				t.Errorf("%s gives no reason; an unexplained 'unknown' is indistinguishable from a bug", tc.name)
			}
		})
	}
}

// "unknown" and "not_applicable" are different answers and must stay
// different. Unknown is a gap Vulos could close (X11 _NET_WM_PING); not
// applicable is a category error (a React view is not a process). Folding them
// together would hide the gap.
func TestUnknownAndNotApplicableAreDistinct(t *testing.T) {
	if StreamUnknown().Status != StatusUnknown {
		t.Errorf("streamed apps must report unknown, not %q", StreamUnknown().Status)
	}
	if BuiltinNotApplicable().Status != StatusNotApplicable {
		t.Errorf("built-ins must report not_applicable, not %q", BuiltinNotApplicable().Status)
	}
	if StatusUnknown == StatusNotApplicable {
		t.Error("the two statuses collapsed to the same string")
	}
}

// D state is the one most likely to be mislabelled "frozen". It must say
// storage, and it must say signals will not land, because that is what
// determines whether the Force Quit button can possibly work.
func TestStateNote_DStateExplainsItselfHonestly(t *testing.T) {
	got := StateNote("D")
	if got.Status != StatusUnknown {
		t.Errorf("Status = %q, want unknown — a state letter is not an event-loop measurement", got.Status)
	}
	if got.Method != MethodProcState {
		t.Errorf("Method = %q, want %q", got.Method, MethodProcState)
	}
	for _, want := range []string{"storage", "signals are not delivered"} {
		if !contains(got.Detail, want) {
			t.Errorf("D-state detail %q does not mention %q", got.Detail, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
