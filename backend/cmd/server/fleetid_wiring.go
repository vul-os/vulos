package main

// fleetid_wiring.go — the deployment half of break-glass peer vouching.
//
// The fleetid package deliberately knows nothing about this box: no roster, no
// sessions, no peer addresses. Everything it needs to be USABLE by a human
// therefore has to be supplied here, and this file supplies exactly two things:
//
//  1. fleetVouchAnnotator — the roster lookup that turns a base64 Vulos ID in the
//     operator's approval queue into "this is workshop-box, one of yours" or
//     "this is nobody this box knows". That single fact is the difference
//     between an approval screen a human can act on and one where the only
//     honest answer to every row is "I have no idea".
//
//  2. registerFleetGatherRoute — the INITIATOR side. fleetid.GatherQuorum asks
//     peers for vouches and collects the bundle break-glass rotation needs; it
//     had no production caller at all, so on a real box the only way to collect
//     a quorum was to drive it by hand from another program. The route below is
//     that caller.
//
// Both are wired from main.go; see fleetid_wiring_test.go, which pins the call
// sites by AST rather than by grep — a call that exists but never runs is the
// specific failure this repo has already shipped once (services/sync/hotpath.go).

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"vulos/backend/internal/multiinstance"
	"vulos/backend/services/auth"
	"vulos/backend/services/fleetid"
	"vulos/backend/services/peering"
	"vulos/backend/services/stepup"
)

// fleetVouchAnnotator resolves a Vulos ID against THIS box's own instance
// registry for the operator's approval queue.
//
// It answers a question the fleetid package structurally cannot: fleetid proved
// (VerifyVouchRequest) that a request came from the holder of the key it names,
// which says nothing at all about whether that key is a box the operator owns.
// A compromised box holds its own key and signs perfectly, so "known" is not
// permission either — it is the context a human needs to notice that a recovery
// request is coming from a machine they do not recognise, or from one they
// already revoked.
//
// Returns the zero PeerAnnotation (Known=false) for a stranger. A nil registry
// returns an annotator that knows nothing, so main.go can decide whether to
// wire it at all rather than have this quietly report every peer as unknown.
func fleetVouchAnnotator(reg *multiinstance.Registry) fleetid.PeerAnnotator {
	return func(vulosID string) fleetid.PeerAnnotation {
		if reg == nil || vulosID == "" {
			return fleetid.PeerAnnotation{}
		}
		insts, err := reg.List()
		if err != nil {
			return fleetid.PeerAnnotation{}
		}
		for _, in := range insts {
			pub, ok := decodeInstancePubKey(in.Ed25519PublicKey)
			if !ok {
				continue
			}
			if peering.EncodeVulosID(pub) != vulosID {
				continue
			}
			return fleetid.PeerAnnotation{
				Known:       true,
				DisplayName: in.DisplayName,
				Revoked:     in.Revoked,
			}
		}
		return fleetid.PeerAnnotation{}
	}
}

// ─── The initiator side ──────────────────────────────────────────────────────

// FleetGatherPath is where this box asks its peers for a break-glass quorum.
// Owner + step-up, exactly like the break-glass rotate/revoke endpoints whose
// input this produces (routes_devicekey_lifecycle.go) — gathering is a
// destructive-adjacent operation that names this box as a recovery subject to
// every peer it can reach, so it is not a casual read.
const FleetGatherPath = "/api/fleetid/quorum/gather"

// fleetGatherDeps is what the initiator route needs. A nil Registry or an
// absent signing key degrades to a clear 503 rather than a half-working gather.
type fleetGatherDeps struct {
	AuthStore  *auth.Store
	Registry   *multiinstance.Registry
	SubjectKey ed25519.PrivateKey // this box's OWN fleet (fabric) signing key
}

type fleetGatherRequest struct {
	Action    string `json:"action"`
	Payload   string `json:"payload_hash"` // base64url raw
	RequestID string `json:"request_id"`
	Threshold int    `json:"threshold,omitempty"`
	// PollSeconds asks peers repeatedly while they answer "pending", giving a
	// human at each peer time to approve. 0 = ask each peer exactly once.
	PollSeconds int `json:"poll_seconds,omitempty"`
	// DeadlineSeconds bounds the total time spent polling ONE pending peer.
	DeadlineSeconds int `json:"deadline_seconds,omitempty"`
}

type fleetGatherPeerOutcome struct {
	Endpoint string `json:"endpoint"`
	Counted  bool   `json:"counted"`
	Reason   string `json:"reason,omitempty"`
}

type fleetGatherResponse struct {
	SubjectID string                   `json:"subject_id"`
	Threshold int                      `json:"threshold"`
	Gathered  int                      `json:"gathered"`
	Certs     []fleetid.VouchCert      `json:"certs"`
	Outcomes  []fleetGatherPeerOutcome `json:"outcomes"`
	// Sufficient is GatherQuorum's networking-layer count only. It is NOT an
	// authorization: the certs still go through fleetid.VerifyQuorum inside
	// devicekey.BreakGlassRotate, which is the only thing that authorizes
	// anything. Named to make a false reading of it awkward.
	Sufficient bool   `json:"sufficient_count_only"`
	Reason     string `json:"reason,omitempty"`
}

// fleetPeerEndpoints lists the OTHER boxes to ask, from this box's own
// registry. Self is excluded (a self-vouch could never count — fleetid refuses
// it at three separate layers — so asking would only waste the window), as are
// revoked peers (VerifyQuorum will not count their certs) and members with no
// endpoint URL to reach.
func fleetPeerEndpoints(reg *multiinstance.Registry, selfVulosID string) []string {
	if reg == nil {
		return nil
	}
	insts, err := reg.List()
	if err != nil {
		return nil
	}
	var out []string
	for _, in := range insts {
		if in.Revoked || in.EndpointURL == "" {
			continue
		}
		pub, ok := decodeInstancePubKey(in.Ed25519PublicKey)
		if !ok {
			continue
		}
		if peering.EncodeVulosID(pub) == selfVulosID {
			continue
		}
		out = append(out, strings.TrimSuffix(in.EndpointURL, "/"))
	}
	return out
}

var errNoGatherIdentity = errors.New("this box has no fleet identity yet — cannot ask peers to vouch for it")

// registerFleetGatherRoute mounts the initiator endpoint. It is the production
// caller fleetid.GatherQuorum did not have.
func registerFleetGatherRoute(mux *http.ServeMux, deps fleetGatherDeps) {
	mux.HandleFunc("POST "+FleetGatherPath, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := dkRequireOwnerAndStepup(w, r, deps.AuthStore); !ok {
			return
		}
		if deps.Registry == nil || len(deps.SubjectKey) != ed25519.PrivateKeySize {
			writeErr(w, http.StatusServiceUnavailable, errNoGatherIdentity.Error())
			return
		}
		var req fleetGatherRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		payload, err := base64.RawURLEncoding.DecodeString(req.Payload)
		if err != nil || len(payload) == 0 {
			writeErr(w, http.StatusBadRequest, "malformed payload_hash (want base64url, unpadded)")
			return
		}
		if req.RequestID == "" {
			writeErr(w, http.StatusBadRequest, "request_id is required")
			return
		}

		// The subject is ALWAYS this box. GatherQuorum refuses a SubjectKey that
		// does not match SubjectID, so there is no way to spend this endpoint
		// gathering vouches for somebody else even if a caller asked.
		selfID := peering.EncodeVulosID(deps.SubjectKey.Public().(ed25519.PublicKey))
		peers := fleetPeerEndpoints(deps.Registry, selfID)
		if len(peers) == 0 {
			w.WriteHeader(http.StatusOK)
			writeJSON(w, fleetGatherResponse{
				SubjectID: selfID,
				Threshold: fleetid.MinThreshold,
				Certs:     []fleetid.VouchCert{},
				Outcomes:  []fleetGatherPeerOutcome{},
				Reason: "no reachable peers to ask — peer vouching needs at least two OTHER boxes; " +
					"a 1-2 box fleet recovers through the off-box mnemonic anchor instead",
			})
			return
		}

		opts := fleetid.GatherOptions{}
		if req.PollSeconds > 0 {
			opts.PollInterval = time.Duration(req.PollSeconds) * time.Second
			opts.PollDeadline = time.Duration(req.DeadlineSeconds) * time.Second
		}
		res, gerr := fleetid.GatherQuorum(r.Context(), fleetid.HTTPTransport{}, fleetid.GatherRequest{
			Action:      req.Action,
			SubjectID:   selfID,
			PayloadHash: payload,
			RequestID:   req.RequestID,
			SubjectKey:  deps.SubjectKey,
			Peers:       peers,
			Threshold:   req.Threshold,
		}, opts)

		// A programmer/caller error (unknown action, bad key) is a 400; running
		// out of peers is a normal, reportable outcome with a partial bundle.
		if gerr != nil && !errors.Is(gerr, fleetid.ErrGatherInsufficient) {
			writeErr(w, http.StatusBadRequest, gerr.Error())
			return
		}
		resp := fleetGatherResponse{
			SubjectID:  selfID,
			Threshold:  res.Threshold,
			Gathered:   len(res.Certs),
			Certs:      res.Certs,
			Outcomes:   make([]fleetGatherPeerOutcome, 0, len(res.Outcomes)),
			Sufficient: gerr == nil,
		}
		if resp.Certs == nil {
			resp.Certs = []fleetid.VouchCert{}
		}
		for _, o := range res.Outcomes {
			resp.Outcomes = append(resp.Outcomes, fleetGatherPeerOutcome{Endpoint: o.Endpoint, Counted: o.Counted, Reason: o.Reason})
		}
		if gerr != nil {
			resp.Reason = fmt.Sprintf("%v — each peer's operator must approve this exact request before it will sign", gerr)
		}
		w.WriteHeader(http.StatusOK)
		writeJSON(w, resp)
	})
}

// compile-time assertion that the step-up gate this route relies on is the same
// one the break-glass endpoints use; if stepup.Require ever changes shape this
// file must be revisited deliberately.
var _ = stepup.Require
