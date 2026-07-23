package main

// routes_compliance.go — POPIA / GDPR data-subject request intake.
//
// Backs Settings → Privacy: a formal, timestamped RECORD of a data-subject
// request (access/export or erasure) that the box owner can act on within the
// statutory window. This is deliberately NOT an automated export or deletion
// engine — see services/compliance's package doc. It is COMPLEMENTARY to
// GET /api/export/data (Settings → Export My Data, routes_export.go), which
// already self-serves an immediate download; this endpoint instead creates an
// auditable trail, including for erasure, which has no self-serve mechanics.
//
// Session-authed: identity is ALWAYS the X-User-ID header injected by the auth
// middleware, never the request body — same rule as routes_export.go. A caller
// can only record and list their OWN requests.
//
//   POST /api/compliance/requests — record a new request {kind, note}
//   GET  /api/compliance/requests — list the caller's own requests, newest first

import (
	"encoding/json"
	"log"
	"net/http"

	"vulos/backend/services/compliance"
)

// registerComplianceRoutes wires the data-subject request intake endpoints.
func registerComplianceRoutes(mux *http.ServeMux, store *compliance.SQLStore) {
	mux.HandleFunc("POST /api/compliance/requests", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}

		var body struct {
			Kind string `json:"kind"`
			Note string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if !compliance.ValidKind(body.Kind) {
			writeErr(w, http.StatusBadRequest, `kind must be "export" or "erasure"`)
			return
		}
		// Keep the free-text note bounded — this is a reference note, not a
		// place to paste unbounded content.
		const maxNoteLen = 2000
		if len(body.Note) > maxNoteLen {
			body.Note = body.Note[:maxNoteLen]
		}

		req, err := store.Record(r.Context(), userID, body.Kind, body.Note)
		if err != nil {
			log.Printf("[compliance] record error user=%s kind=%s err=%v", userID, body.Kind, err)
			writeErr(w, http.StatusInternalServerError, "could not record request")
			return
		}
		log.Printf("[compliance] DSR recorded id=%s user=%s kind=%s", req.ID, userID, req.Kind)
		writeJSON(w, req)
	})

	mux.HandleFunc("GET /api/compliance/requests", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		reqs, err := store.ListByAccount(r.Context(), userID)
		if err != nil {
			log.Printf("[compliance] list error user=%s err=%v", userID, err)
			writeErr(w, http.StatusInternalServerError, "could not list requests")
			return
		}
		writeJSON(w, reqs)
	})
}
