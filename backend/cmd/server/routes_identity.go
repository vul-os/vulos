package main

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"runtime"

	"vulos/backend/services/identity"
)

// hostnameRE validates a simple RFC-952 hostname label (lowercase + digits + hyphens).
var hostnameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9\-]{0,61}[a-z0-9])?$`)

// registerIdentityRoutes wires the identity API into mux.
//
//	GET  /api/identity          — returns the current Instance as JSON
//	POST /api/identity/hostname — validates and persists a new hostname
func registerIdentityRoutes(mux *http.ServeMux, home string) {
	// Load (or generate) the instance at startup; cache it in a closure-scoped pointer
	// that is refreshed on each hostname write so reads are always consistent.
	inst, err := identity.Load(home)
	if err != nil {
		// Non-fatal: log and serve a zero-value instance so the server still starts.
		inst = &identity.Instance{}
	}

	mux.HandleFunc("GET /api/identity", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(inst)
	})

	mux.HandleFunc("POST /api/identity/hostname", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Hostname string `json:"hostname"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid JSON")
			return
		}
		if !hostnameRE.MatchString(req.Hostname) {
			writeErr(w, 422, "hostname must be lowercase alphanumeric with hyphens (max 63 chars)")
			return
		}

		inst.Hostname = req.Hostname
		if err := identity.Save(inst, home); err != nil {
			writeErr(w, 500, "failed to persist identity: "+err.Error())
			return
		}

		// Best-effort: write /etc/hostname on Linux (skip if not root or read-only).
		if runtime.GOOS == "linux" {
			if info, err := os.Stat("/etc/hostname"); err == nil && !info.IsDir() {
				_ = os.WriteFile("/etc/hostname", []byte(req.Hostname+"\n"), info.Mode())
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(inst)
	})
}
