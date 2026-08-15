package main

// routes_setup.go — the first-boot completion marker, and the only two routes
// that are allowed to know where it lives.
//
// THE MECHANISM, before this file existed:
//
//   • GET /api/setup/status was an inline os.Stat("/var/lib/vulos/.setup-complete")
//     in main.go. It is the single thing the shell asks to decide whether to run
//     the fifteen-step wizard (frontend/src/App.tsx).
//   • NOTHING in the product wrote that file. The wizard's finish() sent
//     `mkdir -p /var/lib/vulos && touch /var/lib/vulos/.setup-complete` through
//     POST /api/exec — a general-purpose shell endpoint that is admin-gated AND
//     carries a kill switch (VULOS_DISABLE_EXEC → 503).
//
// So the record that a box has been set up was a side effect of an endpoint
// whose entire purpose is something else, and which an operator is encouraged to
// turn off. On a box with exec disabled the touch 503s, the marker is never
// written, and the wizard runs again on the NEXT boot — where it cannot succeed
// either, because the account it asks for already exists and register fails on a
// duplicate username. The box is then stuck in a wizard it can never leave.
//
// This file makes completion a first-class, single-purpose route:
//
//	GET  /api/setup/status    — public; has this box been set up? (unchanged shape)
//	POST /api/setup/complete  — owner-only; record that it has.
//
// Both go through setupMarkerPath, so the writer and the reader can no longer
// disagree about which file means "set up".
//
// AUTHORISATION — a route that marks setup complete is a route that can SKIP
// setup, so who may call it is the whole design:
//
//   - It is NOT in auth.publicPaths. An unauthenticated caller is 401'd by the
//     session middleware before reaching the handler, and the handler re-checks
//     for the empty X-User-ID anyway (the middleware strips client-supplied
//     identity headers, so an empty value here means "no session", never
//     "the caller said so").
//   - It requires RoleAdmin — the box owner. On a fresh box the first account
//     created IS the admin (services/auth Register), and the wizard now creates
//     that account at the `account` step (index 6 of 15), long before `ready`.
//     So the legitimate caller always has an owner session by the time it calls.
//   - The consequence of the admin check is the property that matters: on a box
//     with NO accounts yet, NOBODY can mark setup complete. Setup can only be
//     ended by the person who took ownership of the box, never by a stranger who
//     can reach it on the network and would like the owner to land on a sign-in
//     screen for an account that does not exist.
//   - It is deliberately NOT gated by VULOS_DISABLE_EXEC. That kill switch exists
//     to stop the box running ARBITRARY commands; writing one fixed, non-user-
//     controlled path is not that. Honouring it here would rebuild the exact trap
//     this file removes — an operator hardening their box into an unfinishable
//     wizard.
//   - It is audit-logged like every other privileged filesystem write
//     (ROUTES.md, "Privileged routes have an extra rule").
//
// THE ROUTE THAT IS DELIBERATELY NOT HERE: a setup-time Wi-Fi scan.
//
// Step 6 of the wizard scans for networks and 401s on a real first boot, for
// the same reason step 3 did — no session exists yet. It is NOT fixed the same
// way, and the difference is the point. GET /api/wifi/scan is admin-gated on
// purpose (SEC-WIFI-SCAN-01, routes_wifi.go): a scan is not a read. It shells
// out to `iw dev <iface> scan trigger` as root, takes the Wi-Fi service mutex,
// sleeps two seconds, and repeated calls can disturb the association the box is
// currently using. Exempting it during setup would hand a radio-driving,
// lock-holding, unlimited-repeat operation to any unauthenticated caller who
// can reach a box that has no owner yet — and would publish the box's visible
// SSID list, which is a location oracle for anyone NOT physically near it.
//
// So the scan stays gated and the step keeps degrading honestly ("this box
// would not run a scan for us yet — you can continue on Ethernet and set up
// Wi-Fi from Settings"). Nothing is lost from the flow itself: the wizard
// applies the Wi-Fi choice at finish(), through the admin-gated
// POST /api/wifi/connect, with the owner session the account step created.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"vulos/backend/services/auth"
	bprofiles "vulos/backend/services/profiles"
)

// formFactorReader is the slice of *profiles.DeviceProfileStore this file uses:
// the read, and only the read. Set() is deliberately not in the interface, so
// no future edit to this unauthenticated handler can reach a mutation through
// the dependency it was given.
type formFactorReader interface {
	Get() (profile, suggested bprofiles.FormFactor)
}

// setupMarkerPath is the file whose EXISTENCE means "this box has been set up".
//
// A var, not a const, for one reason: the tests in routes_setup_test.go point it
// at a t.TempDir() so they can drive the real handlers instead of a copy of
// them. It is deliberately not read from the environment — an env-settable
// marker path would be a way to make a box claim it had been set up when it had
// not, which is the failure this file exists to prevent.
var setupMarkerPath = "/var/lib/vulos/.setup-complete"

// setupIsComplete reports whether first-boot setup has been recorded.
func setupIsComplete() bool {
	_, err := os.Stat(setupMarkerPath)
	return err == nil
}

// markSetupComplete writes the marker, creating its directory if needed.
//
// The content is for a human reading the box later; nothing parses it. Only the
// file's existence is load-bearing, which is why an already-present marker is
// left exactly as it is rather than rewritten.
func markSetupComplete(userID string) error {
	if err := os.MkdirAll(filepath.Dir(setupMarkerPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(setupMarkerPath), err)
	}
	body := fmt.Sprintf("completed_at=%s\ncompleted_by=%s\n",
		time.Now().UTC().Format(time.RFC3339), userID)
	if err := os.WriteFile(setupMarkerPath, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", setupMarkerPath, err)
	}
	return nil
}

// registerSetupRoutes wires the setup status + completion routes into mux.
func registerSetupRoutes(mux *http.ServeMux, authStore *auth.Store, devices formFactorReader) {
	// GET /api/setup/status — public (auth.publicPaths), no auth needed. The
	// shell asks this before it has anyone to authenticate as. It discloses one
	// boolean: whether this box has been through setup.
	mux.HandleFunc("GET /api/setup/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"setup_complete": setupIsComplete()})
	})

	// GET /api/setup/device-profile — public, read-only, and ONLY before this box
	// has been set up.
	//
	// Why a second route instead of exempting GET /api/device-profile: the auth
	// middleware's allow-list is METHOD-BLIND. isPublicPath() is called with
	// r.URL.Path alone (services/auth/handlers.go), so listing "/api/device-profile"
	// would exempt PUT /api/device-profile too — handing any unauthenticated
	// caller on the network the power to switch an unclaimed box into TV, car or
	// watch mode, which changes the entire shell. "Allow-list just the read" is
	// not something that mechanism can express, so the read gets its own path
	// with no mutating sibling, and the interface it is handed (formFactorReader)
	// has no Set().
	//
	// WHAT THIS DISCLOSES, precisely: one value from {pc, tv, car, watch} —
	// twice — to anyone who can reach a box that has not yet been set up. It is
	// the SMBIOS chassis class the box detected about itself, i.e. roughly "this
	// is a laptop" vs "this is a TV". Nothing about its network, its hardware
	// beyond that class, its owner (there isn't one yet) or its contents. The
	// window closes the moment setup completes.
	//
	// Why it is worth that: step 3 of the wizard asks "what kind of device is
	// this?" and pre-selects the detected answer. 401'd, detection is ALWAYS
	// empty and the step falls back to "pc" — so a TV or a car head unit that
	// correctly detected itself came up in the desktop shell unless the user
	// noticed and overrode it.
	mux.HandleFunc("GET /api/setup/device-profile", func(w http.ResponseWriter, r *http.Request) {
		if setupIsComplete() {
			// Not "forbidden because you are nobody" — forbidden because this
			// route only exists during setup. The session-gated
			// /api/device-profile is the one to use afterwards.
			writeErr(w, 403, "setup is already complete; sign in and use /api/device-profile")
			return
		}
		profile, suggested := devices.Get()
		writeJSON(w, map[string]string{"profile": string(profile), "suggested": string(suggested)})
	})

	// POST /api/setup/complete — owner-only; records first-boot completion.
	mux.HandleFunc("POST /api/setup/complete", func(w http.ResponseWriter, r *http.Request) {
		// Idempotent: a box that is already set up is already set up. Answering
		// 200 here discloses nothing the public GET above does not, and it means
		// a retry (double-click, flaky network, a wizard resumed on a second
		// browser) can never turn success into an error the user must act on.
		if setupIsComplete() {
			writeJSON(w, map[string]any{"setup_complete": true, "already_complete": true})
			return
		}

		// No session. Unreachable through main()'s mux — the auth middleware 401s
		// first because this path is not in auth.publicPaths — but a gate that
		// only exists in another file is a gate that moves when that file does.
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, 401, "sign in as the box owner to finish setup")
			return
		}
		// Owner only. On a box with no accounts this is what makes completion
		// impossible for everyone, which is the point: setup ends when the owner
		// says so, not when a stranger on the network does.
		if p, _ := authStore.GetProfile(userID); p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, 403, "only the box owner can finish setup")
			return
		}

		execAuditLog(r, "POST /api/setup/complete", "record first-boot setup complete")

		if err := markSetupComplete(userID); err != nil {
			// Reported, never swallowed: if this fails the wizard runs again on
			// the next boot, so the user has to hear about it now, while they are
			// still in front of the machine.
			writeErr(w, 500, fmt.Sprintf("could not record setup completion: %v", err))
			return
		}
		writeJSON(w, map[string]any{"setup_complete": true})
	})
}
