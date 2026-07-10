package main

// routes_suiteapps.go — BUNDLE-01: default-everything (batteries-included, opt-out)
// suite selection made at install/onboarding time.
//
// The founder-confirmed model: the OS ships batteries-included. At first boot the
// user gets EVERYTHING pre-selected — an @vulos email (which auto-provisions Mail)
// plus the full Workspace productivity suite (Calendar, Files, Office/Docs, Board,
// unified Home + Search). A lean user (e.g. a gamer) can OPT OUT: uncheck Workspace
// to drop Office/Board/Calendar/Contacts, and/or decline the email address to drop
// Mail. Mail is coupled to the address — an address with no mailbox is broken, so
// declining the address is the only way to drop Mail.
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
//     gateway; this only governs whether the launcher SHOWS their tiles.
//
// Wiring: registerSuiteAppsRoutes(mux, home) is called from main.go alongside the
// other setup routes (registerStorageRoutes etc.).

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

// suiteSelection is the JSON persisted to ~/.vulos/db/suite-selection.json.
//
// Email      — the user claimed an @vulos address. Coupled to Mail: keeping the
//              address keeps Mail; declining it is how Mail is dropped.
// Workspace  — the user kept the full Workspace bundle (Office/Docs, Board,
//              Calendar, Contacts, unified Workspace shell). Unchecking it drops
//              those tiles. Office lives in this bundle, NOT stapled to the email.
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
// Both are setup-time endpoints (see publicPaths in services/auth/handlers.go).
func registerSuiteAppsRoutes(mux *http.ServeMux, home string) {
	selPath := filepath.Join(home, ".vulos", "db", "suite-selection.json")

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
	// Workspace implies its own tiles; Mail is coupled to Email. We store exactly
	// what we're told (with Chosen=true) so the launcher can honour opt-outs.
	mux.HandleFunc("POST /api/setup/apps", func(w http.ResponseWriter, r *http.Request) {
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
