package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vulos/backend/services/bootmode"
)

// What GET /api/setup/mode can actually say to a browser on a PRISTINE box.
//
// This is the measurement that was never taken. The first-boot wizard branched
// on `mode === 'normal'` under the comment "already set up — shouldn't be here,
// but complete gracefully", and its e2e fixture mocked `mode: 'setup'`. Both
// were guesses about a value nobody had asked the server for. The server's
// answer on a box no human has touched is instance_ready, because
// registerIdentityRoutes calls identity.Load while the routes are being wired —
// before ListenAndServe — and identity.Load CREATES db/instance.json when it is
// missing. So:
//
//   - instance_ready is what every first boot reports, and it says nothing
//     about whether setup is outstanding;
//   - instance_absent is unreachable over HTTP entirely. No client can observe
//     it, so no client-side branch may be written as though it could.
//
// GET /api/setup/status is the only authority on whether setup is outstanding.

func TestModeOnAPristineBoxIsInstanceReady(t *testing.T) {
	home := t.TempDir()

	// Exactly what main.go does, in the order main.go does it: identity routes
	// are registered (which writes the instance identity), then the boot-mode
	// endpoint is registered, and only then is anything served.
	mux := http.NewServeMux()
	registerIdentityRoutes(mux, home, nil)
	bootmode.RegisterHandlers(mux, home)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/setup/mode", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/setup/mode: expected 200, got %d", rec.Code)
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("mode body not JSON: %v (%s)", err, rec.Body.String())
	}

	if body.Mode != bootmode.ModeInstanceReady {
		t.Fatalf("a pristine, never-configured box reports mode %q; the first-boot wizard's "+
			"fixtures and branches are written against this value, so it must be pinned",
			body.Mode)
	}
}

// TestModeInstanceAbsentIsUnreachableOverHTTP states the consequence as an
// assertion: whatever a browser sees, it is never instance_absent, so the
// wizard cannot use "the box says it needs setup" as its trigger. It has to use
// /api/setup/status, which is what it now does.
func TestModeInstanceAbsentIsUnreachableOverHTTP(t *testing.T) {
	home := t.TempDir()

	// Before anything is wired, the on-disk state IS instance_absent.
	if r, err := bootmode.Detect(home); err != nil || r.Mode != bootmode.ModeInstanceAbsent {
		t.Fatalf("empty home should be %q on disk, got %q (err %v)",
			bootmode.ModeInstanceAbsent, r.Mode, err)
	}

	// Wiring the routes is what makes it unobservable.
	mux := http.NewServeMux()
	registerIdentityRoutes(mux, home, nil)
	bootmode.RegisterHandlers(mux, home)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/setup/mode", nil))
	var body struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)

	if body.Mode == bootmode.ModeInstanceAbsent {
		t.Fatalf("instance_absent became observable over HTTP — if identity.Load has moved out " +
			"of route registration, revisit every caller that assumes it never appears")
	}
}

// TestJoinRoutesAreOpenOnAPristineBox is the same measurement for the join
// flow, which had the same bug and worse consequences: its gate was
// joinsync.IsProvisioned(home) == bootmode "normal" == "instance.json exists",
// so on every running box POST /api/setup/join answered 409 and GET
// /api/setup/join/status answered 403. Nothing could ever join a cluster.
func TestJoinRoutesAreOpenOnAPristineBox(t *testing.T) {
	home := t.TempDir()

	mux := http.NewServeMux()
	registerIdentityRoutes(mux, home, nil) // writes instance.json, as startup does
	// The real predicate main.go passes, on a box with no marker and no users.
	registerJoinRoutes(mux, home, func() bool { return false })

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/setup/join/status", nil))
	if rec.Code == http.StatusForbidden {
		t.Fatalf("GET /api/setup/join/status is 403 on a box nobody owns — the join flow is dead again")
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/setup/join", nil))
	if rec.Code == http.StatusConflict {
		t.Fatalf("POST /api/setup/join is 409 on a box nobody owns — the join flow is dead again")
	}
}
