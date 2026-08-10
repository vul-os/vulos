package crdtsync

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sort"
)

// MaxBodyBytes caps an inbound delta/snapshot body. A snapshot of a preferences
// domain is kilobytes; 8 MiB matches internal/fabric's cap and is far above any
// realistic payload while still bounding a hostile peer.
const MaxBodyBytes = 8 << 20

// PullRequest is the body of POST /api/crdt/pull: "here is what I have, send me
// what I am missing".
type PullRequest struct {
	Domain string        `json:"domain"`
	VV     VersionVector `json:"vv"`
	Limit  int           `json:"limit,omitempty"`
}

// PushResponse is the body of a successful POST /api/crdt/push.
type PushResponse struct {
	Applied int `json:"applied"`
}

// DomainStatus is one domain's line in the status response.
type DomainStatus struct {
	Domain    string        `json:"domain"`
	VV        VersionVector `json:"vv"`
	LogOps    int           `json:"log_ops"`
	Registers int           `json:"registers"`
}

// EngineStatus is the body of GET /api/crdt/status.
type EngineStatus struct {
	Actor   string         `json:"actor"`
	Domains []DomainStatus `json:"domains"`
}

// Authorizer decides whether a request may exchange CRDT state. It is injected
// rather than assumed so the engine does not hard-code a trust model: the LAN
// wiring passes the shared-fabric-secret check, and a future WAN transport can
// pass a stronger one at the same seam.
type Authorizer func(*http.Request) bool

// RegisterHandlers mounts the engine's transport endpoints on mux:
//
//	POST /api/crdt/pull    — peer sends its version vector, gets a delta (or a
//	                         snapshot when it is behind the compaction floor)
//	POST /api/crdt/push    — peer sends a delta for us to merge
//	GET  /api/crdt/status  — per-domain version vector / log size (observability)
//
// Every endpoint is gated by authz. A nil Authorizer registers NOTHING and logs
// loudly: an unauthenticated CRDT exchange endpoint would let any host on the
// network read and rewrite replicated state, so the fail-closed behaviour is to
// not exist at all rather than to serve openly.
//
// These handlers must be mounted on the LAN-only mux (the same one
// internal/fabric uses), never the public surface.
func (s *Store) RegisterHandlers(mux *http.ServeMux, authz Authorizer) {
	if authz == nil {
		log.Printf("[crdtsync] REFUSING to register handlers: no Authorizer supplied (an unauthenticated exchange endpoint is never acceptable)")
		return
	}
	mux.HandleFunc("POST /api/crdt/pull", func(w http.ResponseWriter, r *http.Request) {
		if !authz(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var req PullRequest
		if !decodeBody(w, r, &req) {
			return
		}
		if req.Domain == "" {
			http.Error(w, `{"error":"domain required"}`, http.StatusBadRequest)
			return
		}
		d, err := s.Delta(req.Domain, req.VV, req.Limit)
		if err != nil {
			log.Printf("[crdtsync] delta for %s: %v", req.Domain, err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, d)
	})

	mux.HandleFunc("POST /api/crdt/push", func(w http.ResponseWriter, r *http.Request) {
		if !authz(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var d Delta
		if !decodeBody(w, r, &d) {
			return
		}
		if d.Domain == "" {
			http.Error(w, `{"error":"domain required"}`, http.StatusBadRequest)
			return
		}
		n, err := s.Merge(&d)
		if err != nil {
			log.Printf("[crdtsync] merge pushed delta for %s: %v", d.Domain, err)
			http.Error(w, `{"error":"merge failed"}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, PushResponse{Applied: n})
	})

	mux.HandleFunc("GET /api/crdt/status", func(w http.ResponseWriter, r *http.Request) {
		if !authz(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		st, err := s.Status()
		if err != nil {
			log.Printf("[crdtsync] status: %v", err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, st)
	})
}

// Status returns a snapshot of per-domain replication state.
func (s *Store) Status() (EngineStatus, error) {
	out := EngineStatus{Actor: s.actor, Domains: []DomainStatus{}}
	domains, err := s.Domains()
	if err != nil {
		return out, err
	}
	sort.Strings(domains)
	for _, d := range domains {
		vv, err := s.VersionVector(d)
		if err != nil {
			return out, err
		}
		n, err := s.LogSize(d)
		if err != nil {
			return out, err
		}
		var regs int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM crdt_reg WHERE domain=? AND deleted=0`, d).Scan(&regs); err != nil {
			return out, err
		}
		out.Domains = append(out.Domains, DomainStatus{Domain: d, VV: vv, LogOps: n, Registers: regs})
	}
	return out, nil
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if err != nil {
		http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
		return false
	}
	if len(body) > MaxBodyBytes {
		http.Error(w, `{"error":"body too large"}`, http.StatusRequestEntityTooLarge)
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[crdtsync] encode response: %v", err)
	}
}
