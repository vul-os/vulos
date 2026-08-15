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

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"vulos/backend/services/auth"
)

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
func registerSetupRoutes(mux *http.ServeMux, authStore *auth.Store) {
	// GET /api/setup/status — public (auth.publicPaths), no auth needed. The
	// shell asks this before it has anyone to authenticate as. It discloses one
	// boolean: whether this box has been through setup.
	mux.HandleFunc("GET /api/setup/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"setup_complete": setupIsComplete()})
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
