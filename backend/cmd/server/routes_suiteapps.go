package main

// routes_suiteapps.go — BUNDLE-01: default-everything (batteries-included, opt-out)
// suite selection made at install/onboarding time.
//
// The founder-confirmed model: the OS ships batteries-included. At first boot the
// user gets EVERYTHING pre-selected — a Vulos account handle (which also enables
// the Mail app; Mail itself stays a bring-your-own connector, no mailbox is
// provisioned) plus the full productivity bundle (Calendar, Files, Ofisi/Docs,
// unified Home + Search — whiteboards are an Ofisi document type, not a separate
// Board app). A lean user (e.g. a gamer) can OPT OUT: uncheck the productivity
// bundle to drop Ofisi/Calendar/Contacts, and/or decline the account handle to
// drop Mail. Mail is coupled to the handle — declining it is the only way to
// drop Mail.
//
// This file persists that selection to ~/.vulos/db/suite-selection.json and serves
// it back so the shell's launcher can hide the suite tiles the user opted out of.
//
// Additive + reversible + fail-open by design:
//   - No file present  ⇒ EVERYTHING enabled (email + workspace). This makes the
//     change invisible to existing installs: nothing is hidden until the user
//     explicitly opts out during onboarding.
//   - The selection is a preference, not a security boundary. The backend suite
//     apps (lilmail/office/board/…) are always compiled in and reachable via the
//     gateway; this only governs whether the launcher SHOWS their tiles. (Board
//     has no launcher tile of its own — Ofisi embeds it as the whiteboard
//     document type — but its backend route is still live.)
//
// Wiring: registerSuiteAppsRoutes(mux, home) is called from main.go alongside the
// other setup routes (registerStorageRoutes etc.).

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"vulos/backend/services/auth"
)

// suiteSelection is the JSON persisted to ~/.vulos/db/suite-selection.json.
//
// Email      — the user claimed a Vulos account handle. Coupled to Mail: keeping
//
//	the handle keeps Mail enabled; declining it is how Mail is dropped. Mail
//	itself stays a bring-your-own connector — no mailbox is provisioned here.
//
// Workspace  — the user kept the full productivity bundle (Ofisi/Docs, Calendar,
//
//	Contacts). Unchecking it drops those tiles. Ofisi lives in this bundle,
//	NOT stapled to the email/handle.
type suiteSelection struct {
	Email     bool `json:"email"`
	Workspace bool `json:"workspace"`
	// Chosen records that the user actually made a choice during onboarding.
	// Absent/false + no file ⇒ defaults (everything on). Present ⇒ honour the
	// stored booleans verbatim.
	Chosen bool `json:"chosen"`
}

// defaultSuiteSelection is the batteries-included default: everything on.
func defaultSuiteSelection() suiteSelection {
	return suiteSelection{Email: true, Workspace: true, Chosen: false}
}

// registerSuiteAppsRoutes wires the suite-selection endpoints into mux.
//
//	GET  /api/setup/apps — returns the current suite selection (defaults=all-on)
//	POST /api/setup/apps — persists the user's opt-out choices at onboarding time
//
// Both are setup-time endpoints (see publicPaths in services/auth/handlers.go),
// so neither is behind the auth middleware's 401. The GET is public-safe (two
// booleans). The POST is a PERMANENT, REPEATABLE write that can strip Mail and
// the whole productivity bundle from the launcher, so it carries its own gate — the
// same one handleRegister uses for the other unauthenticated setup-time write:
// unauthenticated ONLY while the box has no users (first-boot onboarding, where
// the wizard's apps step may run before any account exists); once an account
// exists the caller must be an authenticated ADMIN. Anonymous reachability of
// the box's HTTP port is then no longer enough to rewrite the launcher.
func registerSuiteAppsRoutes(mux *http.ServeMux, authStore *auth.Store, home string) {
	selPath := filepath.Join(home, ".vulos", "db", "suite-selection.json")

	// writeGate mirrors handleRegister: open pre-account, admin-only afterwards.
	writeGate := func(w http.ResponseWriter, r *http.Request) bool {
		if !authStore.HasAnyUsers() {
			return true // first-boot onboarding: no account exists to authenticate as
		}
		userID := r.Header.Get("X-User-ID") // stamped by the auth middleware; never client-supplied
		if userID == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return false
		}
		p, _ := authStore.GetProfile(userID)
		if p == nil || p.Role != auth.RoleAdmin {
			writeErr(w, http.StatusForbidden, "admin only")
			return false
		}
		return true
	}

	readSelection := func() suiteSelection {
		sel := defaultSuiteSelection()
		data, err := os.ReadFile(selPath)
		if err != nil {
			return sel // absent ⇒ batteries-included default
		}
		var stored suiteSelection
		if err := json.Unmarshal(data, &stored); err != nil {
			return sel // corrupt ⇒ fail-open to default (never hide the suite)
		}
		return stored
	}

	// GET /api/setup/apps — public-safe: no secrets, just booleans.
	mux.HandleFunc("GET /api/setup/apps", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, readSelection())
	})

	// POST /api/setup/apps — persist the onboarding selection. Body: {email,workspace}.
	// Coupling is enforced by the caller (the wizard), but we normalise here too:
	// the Workspace flag implies its own tiles; Mail is coupled to Email. We store
	// exactly what we're told (with Chosen=true) so the launcher can honour opt-outs.
	mux.HandleFunc("POST /api/setup/apps", func(w http.ResponseWriter, r *http.Request) {
		if !writeGate(w, r) {
			return
		}
		var req struct {
			Email     *bool `json:"email"`
			Workspace *bool `json:"workspace"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid JSON")
			return
		}
		// Default any omitted field to ON (batteries-included) rather than OFF, so a
		// partial body can never silently strip an app the user didn't opt out of.
		sel := suiteSelection{Email: true, Workspace: true, Chosen: true}
		if req.Email != nil {
			sel.Email = *req.Email
		}
		if req.Workspace != nil {
			sel.Workspace = *req.Workspace
		}

		if err := os.MkdirAll(filepath.Dir(selPath), 0o755); err != nil {
			writeErr(w, 500, "failed to create config dir: "+err.Error())
			return
		}
		out, err := json.MarshalIndent(sel, "", "  ")
		if err != nil {
			writeErr(w, 500, "failed to encode selection")
			return
		}
		if err := os.WriteFile(selPath, out, 0o644); err != nil {
			writeErr(w, 500, "failed to persist selection: "+err.Error())
			return
		}
		writeJSON(w, sel)
	})
}
