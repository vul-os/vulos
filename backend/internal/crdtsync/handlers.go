package crdtsync

import (
	"encoding/json"
	"errors"
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
// wiring passes the shared-fabric-secret check, and the WAN path passes the
// per-peer Ed25519 signature check (PeerKeyAuthorizer) at the same seam.
type Authorizer func(*http.Request) bool

// HandlerOption configures RegisterHandlers.
type HandlerOption func(*handlerOpts)

type handlerOpts struct {
	identity *PeerIdentity
}

// WithResponseSigning makes the endpoints sign their 200 responses with this
// box's peer identity whenever the caller presented a peer-auth envelope.
//
// This is not decoration. A pull RESPONSE is merged into the caller's database,
// and the caller reached this box at an address an UNSIGNED rendezvous
// /resolve answer gave it (fabric/rendezvous.go documents that gap). Without a
// signature over the response body, a relay that lies about an address is a
// relay that writes to your database. TLS does not close this: it proves the
// responder holds a certificate for the name the relay chose, which an attacker
// who controls the relay has for their own domain.
//
// Requests that carry no peer-auth envelope — i.e. the LAN shared-secret path —
// get an unsigned response exactly as before, so this changes nothing on the
// LAN.
func WithResponseSigning(id *PeerIdentity) HandlerOption {
	return func(o *handlerOpts) { o.identity = id }
}

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
func (s *Store) RegisterHandlers(mux *http.ServeMux, authz Authorizer, options ...HandlerOption) {
	if authz == nil {
		log.Printf("[crdtsync] REFUSING to register handlers: no Authorizer supplied (an unauthenticated exchange endpoint is never acceptable)")
		return
	}
	opts := &handlerOpts{}
	for _, o := range options {
		if o != nil {
			o(opts)
		}
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
			// A domain this box does not replicate is a policy refusal, not a
			// fault: answer 403 rather than 500, and do not log it as an
			// internal error (a peer asking for one is routine, not a bug).
			var notAllowed *ErrDomainNotAllowed
			if errors.As(err, &notAllowed) {
				http.Error(w, `{"error":"domain not replicated"}`, http.StatusForbidden)
				return
			}
			log.Printf("[crdtsync] delta for %s: %v", req.Domain, err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		opts.respond(w, r, d)
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
			var notAllowed *ErrDomainNotAllowed
			if errors.As(err, &notAllowed) {
				http.Error(w, `{"error":"domain not replicated"}`, http.StatusForbidden)
				return
			}
			log.Printf("[crdtsync] merge pushed delta for %s: %v", d.Domain, err)
			http.Error(w, `{"error":"merge failed"}`, http.StatusBadRequest)
			return
		}
		opts.respond(w, r, PushResponse{Applied: n})
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
		opts.respond(w, r, st)
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

// respond writes a 200 JSON body, signing it when the caller authenticated with
// a peer-auth envelope and this box has a signing identity.
//
// The body is marshalled to bytes FIRST and the header set BEFORE anything is
// written, because a signature computed after the body has begun streaming
// cannot be sent — headers are already gone. That is also why the signature
// rides in a header rather than being folded into the JSON: the Delta/Snapshot
// shapes are the merge engine's wire types and must not grow a transport field.
func (o *handlerOpts) respond(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		log.Printf("[crdtsync] encode response: %v", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	if o != nil && o.identity != nil {
		// The requester's key and nonce come from the envelope it presented.
		// Parsing it here does not re-authenticate anything — the Authorizer
		// already did, and refusing to sign would only deny the caller a proof
		// it asked for. A caller that presented no envelope (the LAN
		// shared-secret path) gets an unsigned response, unchanged.
		if aud, nonce, ok := requesterEnvelopeRef(r); ok {
			if sig, serr := o.identity.SignResponse(aud, nonce, http.StatusOK, body); serr != nil {
				log.Printf("[crdtsync] sign response: %v", serr)
			} else {
				w.Header().Set(PeerAuthResponseHeader, sig)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(body); err != nil {
		log.Printf("[crdtsync] write response: %v", err)
	}
}

// requesterEnvelopeRef pulls the caller's peer key and nonce out of the
// peer-auth header so a response can be bound to them.
func requesterEnvelopeRef(r *http.Request) (peerKey, nonce string, ok bool) {
	header := r.Header.Get(PeerAuthHeader)
	if header == "" {
		return "", "", false
	}
	var env peerAuthEnvelope
	if err := decodeEnvelope(header, &env); err != nil {
		return "", "", false
	}
	if env.PeerKey == "" || env.Nonce == "" {
		return "", "", false
	}
	return env.PeerKey, env.Nonce, true
}
