package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/internal/lan"
)

// Guards for the rename path. The defect being pinned is NOT "rename is
// missing" — the endpoint existed and returned 200. It is that the endpoint
// reported success while changing nothing on the running system: no
// sethostname, no re-advertisement, and a certificate that still named only the
// old name. A test that only asserts a 200 would have passed the whole time.

type fakeRenamer struct {
	current    lan.NameSet
	renamed    []string // every name rename() was actually called with
	takenNames map[string]string
	failRename bool
}

func newFakeRenamer(id, host string) *fakeRenamer {
	return &fakeRenamer{
		current:    lan.NewNameSet(id, host),
		takenNames: map[string]string{},
	}
}

func (f *fakeRenamer) rename(name string) (lan.NameSet, error) {
	if f.failRename {
		return lan.NameSet{}, errFake
	}
	f.renamed = append(f.renamed, name)
	f.current = lan.NewNameSet("01HZZZZZZZZZZZZZZZZZK3N7Q2", name)
	return f.current, nil
}

func (f *fakeRenamer) nameTaken(name string) (bool, string) {
	by, ok := f.takenNames[name]
	return ok, by
}

func (f *fakeRenamer) names() lan.NameSet { return f.current }

type fakeErr struct{}

func (fakeErr) Error() string { return "fake rename failure" }

var errFake = fakeErr{}

func postHostname(t *testing.T, mux *http.ServeMux, body string) (*httptest.ResponseRecorder, hostnameResponse) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/identity/hostname", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out hostnameResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// TestRenameActuallyReAdvertises is THE guard: a rename must reach the thing
// that advertises the name, not just the identity file.
func TestRenameActuallyReAdvertises(t *testing.T) {
	f := newFakeRenamer("01HZZZZZZZZZZZZZZZZZK3N7Q2", "")
	mux := http.NewServeMux()
	registerIdentityRoutes(mux, t.TempDir(), f)

	rec, out := postHostname(t, mux, `{"hostname":"study"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(f.renamed) != 1 || f.renamed[0] != "study" {
		t.Fatalf("rename() calls = %v, want exactly one call with \"study\" — the endpoint returned success without re-advertising anything", f.renamed)
	}
	if out.Hostname != "study" {
		t.Fatalf("response hostname = %q, want %q", out.Hostname, "study")
	}
	if !hasString(out.Names, "study.local") {
		t.Fatalf("response names = %v, want study.local among them", out.Names)
	}
}

// TestRenameReportsWhenItDidNotTakeEffect: on a host where the system hostname
// cannot be set (non-root, or a developer's macOS), the response must SAY the
// rename is not live. Reporting a plain success here is the exact defect.
func TestRenameReportsWhenItDidNotTakeEffect(t *testing.T) {
	f := newFakeRenamer("01HZZZZZZZZZZZZZZZZZK3N7Q2", "")
	mux := http.NewServeMux()
	registerIdentityRoutes(mux, t.TempDir(), f)

	_, out := postHostname(t, mux, `{"hostname":"study"}`)

	// The test process is not root (and on darwin the call is unsupported), so
	// setSystemHostname must have failed and that must be visible.
	if err := setSystemHostname("study"); err != nil {
		if out.AppliedLive {
			t.Fatalf("applied_live=true but setSystemHostname really fails here (%v) — the response claims a rename took effect that did not", err)
		}
		if out.Notice == "" {
			t.Fatal("applied_live=false with no notice — the user is shown a success with nothing explaining that a restart is needed")
		}
		if !strings.Contains(strings.ToLower(out.Notice), "restart") {
			t.Errorf("notice %q does not tell the user to restart", out.Notice)
		}
	} else if !out.AppliedLive {
		t.Fatal("setSystemHostname succeeded but applied_live=false")
	}
}

// TestRenameWithNoLANServiceIsNotClaimedLive: with VULOS_LAN_ENABLE unset there
// is nothing advertising anything, so a rename cannot be live.
func TestRenameWithNoLANServiceIsNotClaimedLive(t *testing.T) {
	mux := http.NewServeMux()
	registerIdentityRoutes(mux, t.TempDir(), nil)

	rec, out := postHostname(t, mux, `{"hostname":"study"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if out.AppliedLive {
		t.Fatal("applied_live=true with no LAN service running — nothing advertises the new name, so it cannot be live")
	}
	if out.Notice == "" {
		t.Fatal("no notice explaining that the rename is not live")
	}
}

// TestRenameRejectsInvalidNames: the same rule as the mDNS names and the
// certificate SANs, not a fourth private regexp.
func TestRenameRejectsInvalidNames(t *testing.T) {
	f := newFakeRenamer("01HZZZZZZZZZZZZZZZZZK3N7Q2", "")
	mux := http.NewServeMux()
	registerIdentityRoutes(mux, t.TempDir(), f)

	for _, bad := range []string{`""`, `"   "`, `"-nope"`, `"nope-"`, `"has space"`, `"a_b"`, `"` + strings.Repeat("a", 64) + `"`} {
		rec, _ := postHostname(t, mux, `{"hostname":`+bad+`}`)
		if rec.Code != 422 {
			t.Errorf("hostname %s: status %d, want 422", bad, rec.Code)
		}
	}
	if len(f.renamed) != 0 {
		t.Fatalf("an invalid name still reached rename(): %v", f.renamed)
	}

	// Upper case and surrounding whitespace are RECOVERED, not rejected.
	rec, out := postHostname(t, mux, `{"hostname":"  Study  "}`)
	if rec.Code != 200 || out.Hostname != "study" {
		t.Fatalf("status %d hostname %q, want 200 and %q", rec.Code, out.Hostname, "study")
	}
}

// TestHostnameAvailabilityCheck is the collision-prevention guard: the wizard
// must be able to learn a name is taken BEFORE committing to it, which is the
// whole difference between a helpful error and avahi silently renaming the
// losing box to vulos-2 hours later.
func TestHostnameAvailabilityCheck(t *testing.T) {
	f := newFakeRenamer("01HZZZZZZZZZZZZZZZZZK3N7Q2", "kitchen")
	f.takenNames["study"] = "192.168.1.9"
	mux := http.NewServeMux()
	registerIdentityRoutes(mux, t.TempDir(), f)

	check := func(name string) hostnameResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/identity/hostname/available?name="+name, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", name, rec.Code, rec.Body.String())
		}
		var out hostnameResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Available == nil {
			t.Fatalf("%s: response has no availability verdict: %s", name, rec.Body.String())
		}
		return out
	}

	taken := check("study")
	if *taken.Available {
		t.Fatal("a name another box already answers to was reported AVAILABLE — the wizard would let the owner walk into the collision")
	}
	if taken.TakenBy != "192.168.1.9" {
		t.Errorf("taken_by = %q, want the address that answered", taken.TakenBy)
	}

	free := check("library")
	if !*free.Available {
		t.Fatal("an unused name was reported unavailable")
	}

	// The box's own current name must not read as a conflict with itself.
	f.takenNames["kitchen"] = "192.168.1.5"
	own := check("kitchen")
	if !*own.Available {
		t.Fatal("the box's OWN current name was reported as taken — re-submitting the name you already have must not look like a collision")
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/identity/hostname/available?name=not%20valid", nil))
	if rec.Code != 422 {
		t.Errorf("invalid name: status %d, want 422", rec.Code)
	}
}

// TestSetSystemHostnameRefusesGarbage: sethostname(2) validates nothing, so
// this is the last line of defence before the kernel accepts arbitrary bytes.
func TestSetSystemHostnameRefusesGarbage(t *testing.T) {
	if err := setSystemHostname(""); err == nil {
		t.Error("setSystemHostname accepted an empty name")
	}
	if err := setSystemHostname(strings.Repeat("a", 64)); err == nil {
		t.Error("setSystemHostname accepted a 64-character name (RFC-1123 label max is 63)")
	}
}
