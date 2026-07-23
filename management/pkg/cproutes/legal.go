// legal.go — COMPLY-02: DPA acceptance record + sub-processor list.
//
// Routes:
//
//	POST /api/legal/dpa/accept         — record that the authenticated user accepted
//	                                     the current DPA version (gated: session req)
//	GET  /api/legal/subprocessors      — public list of authorized sub-processors +
//	                                     pending changes + change history
//
// The DPA acceptance is a lightweight audit record stored in the auth DB via a
// dedicated table (legal_dpa_acceptances). The table is created by the auth
// migration 0011_legal_dpa_acceptances (applied when auth.OpenAuthStore runs).
// The sub-processor list is static JSON maintained in this file; a future
// migration can promote it to a DB table when the list needs operator CRUD.
// Pending changes (effective_after > now) are surfaced automatically so the
// frontend can render the 30-day notice banner.
//
// Wire-in:
//
//	RegisterLegal(mux, authStore, authDB)
package cproutes

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/httpx"
)

// ── Sub-processor data ────────────────────────────────────────────────────
// The OSS control plane ships with an EMPTY sub-processor list: a self-hoster
// has their own vendors and must not publish someone else's. An operator
// populates this via SetSubProcessors (the private cloud layer injects its own
// concrete list). Pending changes have EffectiveAfter set to a future date;
// add rows 30+ days before they take effect. The notice email flow is handled
// by the compliance-notification job (not yet built — see COMPLY-02 AC notes).

// SubProcessor is one authorized sub-processor entry.
type SubProcessor struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	URL            string `json:"url"`
	Purpose        string `json:"purpose"`
	DataCategory   string `json:"dataCategory"`
	Region         string `json:"region"`
	Since          string `json:"since"`
	Pending        bool   `json:"pending"`
	EffectiveAfter string `json:"effectiveAfter,omitempty"`
}

// ChangelogEntry is one dated sub-processor change-history line.
type ChangelogEntry struct {
	Date        string `json:"date"`
	Description string `json:"description"`
}

// authorizedSubProcessors is empty by default in the OSS control plane.
// Operators configure their own list via SetSubProcessors.
var authorizedSubProcessors = []SubProcessor{}

// subProcessorChangelog is empty by default; operators supply their own via
// SetSubProcessors.
var subProcessorChangelog = []ChangelogEntry{}

// SetSubProcessors lets the deployment (e.g. the private cloud layer) inject the
// concrete authorized sub-processor list and change history. The OSS default is
// empty so a self-hoster never publishes another operator's vendors. Pass nil to
// leave a list unchanged.
func SetSubProcessors(list []SubProcessor, changelog []ChangelogEntry) {
	if list != nil {
		authorizedSubProcessors = list
	}
	if changelog != nil {
		subProcessorChangelog = changelog
	}
}

// currentDPAVersion is the version string that acceptance records are stamped with.
const currentDPAVersion = "1.0"

// RegisterLegal wires the COMPLY-02 routes into mux.
// authStore gates the DPA acceptance endpoint.
// db is the auth cpdb.DB (already open); the legal_dpa_acceptances table is
// created by auth migration 0011 when auth.OpenAuthStore runs at startup.
func RegisterLegal(mux *http.ServeMux, authStore *auth.Store, db *cpdb.DB) {
	// ── POST /api/legal/dpa/accept ─────────────────────────────────────────
	// Records acceptance of the current DPA version for the authenticated user.
	// Idempotent: records every acceptance (history is useful for audits).
	mux.HandleFunc("POST /api/legal/dpa/accept", func(w http.ResponseWriter, r *http.Request) {
		u := authStore.RequireSession(r.Context(), w, r)
		if u == nil {
			return // RequireSession wrote 401
		}

		var req struct {
			Version    string `json:"version"`
			AcceptedAt string `json:"accepted_at"`
		}
		if !httpx.DecodeJSON(w, r, &req) {
			return
		}

		version := req.Version
		if version == "" {
			version = currentDPAVersion
		}

		acceptedAt := req.AcceptedAt
		if acceptedAt == "" {
			acceptedAt = time.Now().UTC().Format(time.RFC3339)
		}

		// Capture remote IP + UA for the audit record.
		ip := r.RemoteAddr
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			ip = xff
		}
		ua := r.Header.Get("User-Agent")

		// created_at is supplied by the application so both SQLite and Postgres
		// backends produce RFC3339 timestamps (Postgres DEFAULT '' would be empty).
		createdAt := time.Now().UTC().Format(time.RFC3339)

		_, err := db.ExecContext(r.Context(),
			db.Rebind(`INSERT INTO legal_dpa_acceptances
					(account_id, version, accepted_at, ip, user_agent, created_at)
				 VALUES (?, ?, ?, ?, ?, ?)`),
			u.ID, version, acceptedAt, ip, ua, createdAt,
		)
		if err != nil {
			log.Printf("[legal] insert dpa acceptance for %s: %v", u.ID, err)
			httpx.Err(w, http.StatusInternalServerError, "could not record acceptance")
			return
		}

		httpx.JSON(w, map[string]any{
			"ok":          true,
			"account_id":  u.ID,
			"version":     version,
			"accepted_at": acceptedAt,
		})
	})

	// ── GET /api/legal/subprocessors ───────────────────────────────────────
	// Public endpoint — no auth required. Returns the current sub-processor
	// list, pending changes, change history, and last-updated timestamp.
	mux.HandleFunc("GET /api/legal/subprocessors", func(w http.ResponseWriter, r *http.Request) {
		// Derive last_updated from the most recent changelog entry.
		lastUpdated := ""
		if len(subProcessorChangelog) > 0 {
			lastUpdated = subProcessorChangelog[len(subProcessorChangelog)-1].Date
		}

		// Mark pending entries whose effective_after is still in the future.
		now := time.Now().UTC()
		result := make([]SubProcessor, len(authorizedSubProcessors))
		copy(result, authorizedSubProcessors)
		for i := range result {
			if result[i].EffectiveAfter != "" {
				t, err := time.Parse("2006-01-02", result[i].EffectiveAfter)
				if err == nil && t.Before(now) {
					// Past effective date — no longer pending.
					result[i].Pending = false
					result[i].EffectiveAfter = ""
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"subprocessors": result,
			"changelog":     subProcessorChangelog,
			"last_updated":  lastUpdated,
			"dpa_version":   currentDPAVersion,
		})
	})
}
