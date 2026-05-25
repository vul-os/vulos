package fabric

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"vulos/backend/internal/multiinstance"
)

// maxChangesetBytes caps the size of an inbound POSTed changeset to defend the
// merge path against a hostile peer sending an unbounded body. 8 MiB is far
// larger than any realistic app-registry delta.
const maxChangesetBytes = 8 << 20

// RegisterHandlers wires the two fabric P2P endpoints onto mux:
//
//	GET  /api/fabric/changeset?since=<RFC3339Nano>  — serve our changesets after a cursor
//	POST /api/fabric/changeset                       — accept a peer's changeset
//
// Both require the shared fabric secret in the X-Fabric-Auth header; an empty
// or wrong secret yields 401 before any registry access. This is what gates
// peer-to-peer exchange: discovery alone (mDNS) confers no trust.
//
// Bind note: these handlers must be mounted on the LAN HTTPS listener (which is
// pinned to the LAN IP, not all interfaces). They are deliberately NOT mounted
// on the public cloud surface — fabric is the offline same-LAN path.
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/fabric/changeset", s.handleServeChangeset)
	mux.HandleFunc("POST /api/fabric/changeset", s.handleAcceptChangeset)
	mux.HandleFunc("GET /api/fabric/status", s.handleStatus)
}

// handleStatus serves a JSON snapshot of the fabric sync state (this box's id,
// last-run time, and per-peer cursor + last-sync time/err) for observability.
// It is gated by the same shared fabric secret as the exchange endpoints — the
// roster/cursor data is LAN-only operational state and must not be readable by
// an unauthenticated host, consistent with the LAN-only/auth fabric posture.
func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(r.Header.Get("X-Fabric-Auth")) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.Status()); err != nil {
		log.Printf("[fabric] encode status: %v", err)
	}
}

// handleServeChangeset serves every app_registry row this box has changed since
// the caller's `since` cursor. The response is a multiinstance.AppChangeset so
// the caller can ApplyChangeset it directly through the CRDT merge.
func (s *Service) handleServeChangeset(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(r.Header.Get("X-Fabric-Auth")) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var since time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			http.Error(w, `{"error":"invalid since cursor"}`, http.StatusBadRequest)
			return
		}
		since = t
	}

	entries, err := s.cfg.AppSync.ChangesetSince(since)
	if err != nil {
		log.Printf("[fabric] serve changeset: %v", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	cs, err := s.cfg.AppSync.EmitChangeset(s.cfg.InstanceID, entries)
	if err != nil {
		log.Printf("[fabric] emit changeset: %v", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	if cs.Entries == nil {
		cs.Entries = []multiinstance.AppRegistryEntry{}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(cs); err != nil {
		log.Printf("[fabric] encode changeset: %v", err)
	}
}

// handleAcceptChangeset accepts a peer's changeset and merges it through the
// deterministic CRDT merge. A malformed body is rejected; a changeset that
// fails to merge returns 500 (the peer can retry on its next round).
func (s *Service) handleAcceptChangeset(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(r.Header.Get("X-Fabric-Auth")) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxChangesetBytes+1))
	if err != nil {
		http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
		return
	}
	if len(body) > maxChangesetBytes {
		http.Error(w, `{"error":"changeset too large"}`, http.StatusRequestEntityTooLarge)
		return
	}

	cs, err := multiinstance.UnmarshalChangeset(body)
	if err != nil {
		http.Error(w, `{"error":"invalid changeset"}`, http.StatusBadRequest)
		return
	}
	if err := s.cfg.AppSync.ApplyChangeset(cs); err != nil {
		log.Printf("[fabric] apply inbound changeset from %s: %v", cs.OriginULID, err)
		http.Error(w, `{"error":"merge failed"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"applied":%d}`, len(cs.Entries))
}
