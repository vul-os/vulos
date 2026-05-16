package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"vulos/backend/services/auth"
	"vulos/backend/services/kitbackup"
)

// registerKitRoutes wires up the recovery-kit download endpoint.
//
// Endpoint: GET /api/recovery/kit
//   - Admin session required (mirrors the SEC-B gate ordering used elsewhere:
//     kill-switch optional but admin gate REQUIRED).
//   - Returns the kit as a JSON attachment with a filename that embeds the
//     instance ULID and the issuance date so files are easy to distinguish.
//   - Every successful issuance is written to the server log for audit purposes.
func registerKitRoutes(mux *http.ServeMux, authStore *auth.Store, home string) {
	mux.HandleFunc("GET /api/recovery/kit", func(w http.ResponseWriter, r *http.Request) {
		// --- Admin gate ---
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, 401, "authentication required")
			return
		}
		profile, ok := authStore.GetProfile(userID)
		if !ok || profile == nil || profile.Role != auth.RoleAdmin {
			writeErr(w, 403, "admin access required")
			return
		}

		// --- Build kit ---
		kit, err := kitbackup.BuildKit(home)
		if err != nil {
			log.Printf("[kitbackup] BUILD ERROR user=%s err=%v", userID, err)
			writeErr(w, 500, "could not assemble recovery kit: "+err.Error())
			return
		}

		// --- Audit log ---
		log.Printf("[kitbackup] ISSUED recovery kit instance=%q user=%s remote=%s",
			kit.InstanceID, userID, r.RemoteAddr)

		// --- Filename: vulos-recovery-kit-{ulid}-{date}.json ---
		ulid := kit.InstanceID
		if ulid == "" {
			ulid = kit.Hostname
		}
		if ulid == "" {
			ulid = "unknown"
		}
		date := time.Now().UTC().Format("20060102")
		filename := fmt.Sprintf("vulos-recovery-kit-%s-%s.json", ulid, date)

		// --- Response headers ---
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
		w.Header().Set("Cache-Control", "no-store")

		json.NewEncoder(w).Encode(kit)
	})
}
