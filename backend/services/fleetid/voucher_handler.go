// voucher_handler.go — the PEER (voucher) side of gathering a break-glass
// quorum: an HTTP handler that lets another box ask THIS box to vouch for its
// break-glass action, and signs a VouchCert ONLY when an explicit approval
// gate (policy.go) has granted that exact request.
//
// # Where this sits relative to the keystone
//
// VoucherService never touches VerifyQuorum — that chokepoint lives entirely
// in vouch.go and is evaluated by whichever box is ASSEMBLING a quorum (the
// initiator side, transport.go's GatherQuorum, or ultimately
// devicekey.BreakGlassRotate). VoucherService's only job is to produce ONE
// correctly-bound, honestly-gated VouchCert when asked, exactly like
// NewVouchCert already lets a test construct one directly — the difference is
// that here the request arrives over the network from an untrusted caller, so
// an approval gate stands between "asked" and "signed" that a direct
// in-process caller of NewVouchCert does not need.
//
// # Self-vouch is refused here too, defense in depth
//
// VerifyQuorum already rejects a self-vouch even with a perfectly valid
// signature (its hard rule #1). VoucherService additionally refuses to even
// SIGN one: if the request's SubjectID equals this box's own fleet identity,
// the handler rejects it before the approval policy is even consulted. Two
// independent layers refusing the same thing costs little and means a bug in
// one layer is not a single point of failure for the hard rule.
package fleetid

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"vulos/backend/services/peering"
)

// DefaultVouchPath is the path VoucherService.RegisterHandlers mounts the
// peer vouch-request endpoint at, and the path HTTPTransport POSTs to by
// default. A deployment gluing these together over a different path (e.g. a
// reverse proxy prefix) can override both independently.
const DefaultVouchPath = "/api/fleetid/vouch/request"

// DefaultApprovePath is the path VoucherService.RegisterHandlers mounts the
// OPERATOR-facing approve endpoint at (see ApproveGate). This endpoint is
// intentionally NOT peer-facing: it is how a human at THIS box turns a
// pending request into a grant.
const DefaultApprovePath = "/api/fleetid/vouch/approve"

// maxVouchBodyBytes bounds request bodies read by this package's handlers,
// mirroring the 64<<10 ceiling used by cmd/server's other small JSON POST
// handlers (e.g. routes_devicekey_lifecycle.go).
const maxVouchBodyBytes = 64 << 10

// VouchRequest is the wire shape POSTed to DefaultVouchPath by the initiator
// side (transport.go's HTTPTransport) and decoded here. PayloadHash is
// base64url (raw, no padding) — the SAME encoding VouchCert.PayloadHash uses.
type VouchRequest struct {
	Action      string `json:"action"`
	SubjectID   string `json:"subject_id"`
	PayloadHash string `json:"payload_hash"`
	RequestID   string `json:"request_id"`
}

// vouchWireResponse is the shape returned from DefaultVouchPath. Status is
// one of "granted" (Cert is populated), "pending", "denied", or "error".
// Only "granted" carries any cryptographic weight; the others are purely
// informative for the initiator's retry/backoff logic (transport.go).
type vouchWireResponse struct {
	Status string     `json:"status"`
	Cert   *VouchCert `json:"cert,omitempty"`
	Reason string     `json:"reason,omitempty"`
}

// Approver is an OPTIONAL capability an ApprovalPolicy may implement to
// support the operator-facing approve endpoint this package registers.
// ManualApprovalPolicy (the default policy) implements it; a policy with
// nothing to approve (e.g. DenyAllPolicy) need not.
type Approver interface {
	Approve(action, subjectID string, payloadHash []byte, requestID string, ttl time.Duration)
}

// ApproveGate authorizes a call to the operator-facing approve endpoint. It
// receives the raw *http.Request so the caller composes whatever this
// deployment requires — mirroring dkRequireOwnerAndStepup in
// cmd/server/routes_devicekey_lifecycle.go (owner role AND a fresh step-up
// token; see the INTEGRATION MANIFEST for the exact wiring). Returning false
// yields a bare 403; the gate decision is entirely the caller's, on purpose —
// this package has no opinion on sessions, roles, or step-up tokens.
type ApproveGate func(r *http.Request) bool

// approveRequest is the wire shape POSTed to DefaultApprovePath by an
// operator-facing UI/CLI to grant one specific pending vouch request.
type approveRequest struct {
	Action      string `json:"action"`
	SubjectID   string `json:"subject_id"`
	PayloadHash string `json:"payload_hash"`
	RequestID   string `json:"request_id"`
	TTLSeconds  int    `json:"ttl_seconds,omitempty"`
}

// VoucherService is THIS box acting as a VOUCHER (peer) for another box's
// break-glass request. Construct with NewVoucherService; the zero value is
// not usable (a signing key is required).
type VoucherService struct {
	priv       ed25519.PrivateKey
	selfVulaID string
	policy     ApprovalPolicy
	now        func() time.Time
}

// NewVoucherService builds a VoucherService that signs with signerPriv — this
// box's OWN fleet identity key (the same key/Vula-ID space vouch.go's
// VouchCert and NewVouchCert use). The service's own fleet identity
// (SelfVulaID) is DERIVED from signerPriv's public key via
// peering.EncodeVulaID — it is never taken as a separately supplied
// parameter, so there is no way to construct a service whose self-identity
// disagrees with the key it actually signs with.
//
// policy is REQUIRED and is consulted for every inbound request; passing nil
// is refused rather than silently defaulting, so a caller cannot accidentally
// end up with an implicit policy by omission. Use NewManualApprovalPolicy()
// for the standard "operator must explicitly approve" behavior, or
// DenyAllPolicy{} to run a box that never vouches at all.
func NewVoucherService(signerPriv ed25519.PrivateKey, policy ApprovalPolicy) (*VoucherService, error) {
	if len(signerPriv) != ed25519.PrivateKeySize {
		return nil, errors.New("fleetid: NewVoucherService: signing key wrong size")
	}
	if policy == nil {
		return nil, errors.New("fleetid: NewVoucherService: nil approval policy (refusing to guess a default — pass NewManualApprovalPolicy() or DenyAllPolicy{})")
	}
	pub := signerPriv.Public().(ed25519.PublicKey)
	return &VoucherService{
		priv:       signerPriv,
		selfVulaID: peering.EncodeVulaID(pub),
		policy:     policy,
		now:        time.Now,
	}, nil
}

// SelfVulaID returns this voucher's own fleet identity — the value a request
// whose SubjectID matches will always be refused as a self-vouch.
func (s *VoucherService) SelfVulaID() string { return s.selfVulaID }

// SetClock overrides the clock used to timestamp minted certs (test seam).
func (s *VoucherService) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// RegisterHandlers wires this service's two HTTP endpoints into mux:
//
//   - POST DefaultVouchPath    — peer-facing. Any caller may ask; the
//     approval gate (not caller authentication) is what protects this box.
//   - POST DefaultApprovePath  — operator-facing, gated by approveGate. Pass
//     a gate that always returns false to disable the approve endpoint
//     entirely (e.g. a box that only ever gets approvals through some other
//     out-of-band mechanism calling policy.Approve directly in-process).
func (s *VoucherService) RegisterHandlers(mux *http.ServeMux, approveGate ApproveGate) {
	mux.HandleFunc("POST "+DefaultVouchPath, s.handleVouchRequest)
	mux.HandleFunc("POST "+DefaultApprovePath, func(w http.ResponseWriter, r *http.Request) {
		s.handleApprove(w, r, approveGate)
	})
}

// handleVouchRequest is the peer-facing endpoint. It NEVER signs without a
// prior ApprovalGranted decision from s.policy, and it NEVER vouches for
// itself regardless of what the policy says.
func (s *VoucherService) handleVouchRequest(w http.ResponseWriter, r *http.Request) {
	var req VouchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxVouchBodyBytes)).Decode(&req); err != nil {
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "invalid JSON body"})
		return
	}
	if !validAction(req.Action) {
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "unknown action"})
		return
	}
	if req.SubjectID == "" || req.RequestID == "" {
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "missing subject_id or request_id"})
		return
	}
	if _, err := peering.PublicKeyForVulaID(req.SubjectID); err != nil {
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "malformed subject_id"})
		return
	}
	payloadHash, err := base64.RawURLEncoding.DecodeString(req.PayloadHash)
	if err != nil || len(payloadHash) == 0 {
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "malformed payload_hash"})
		return
	}

	// ── HARD RULE, defense in depth: never vouch for ourselves ─────────────
	// VerifyQuorum already refuses a self-vouch unconditionally (vouch.go's
	// DropSelfVouch), but a compromised or merely misconfigured peer should
	// not even be ABLE to produce one — refusing before the approval gate is
	// even consulted means no policy misconfiguration can accidentally grant
	// a self-vouch either.
	if req.SubjectID == s.selfVulaID {
		writeVoucherJSON(w, http.StatusForbidden, vouchWireResponse{Status: "denied", Reason: "self-vouch refused"})
		return
	}

	decision := s.policy.Decide(ApprovalRequest{
		Action:            req.Action,
		SubjectID:         req.SubjectID,
		PayloadHash:       payloadHash,
		RequestID:         req.RequestID,
		RequesterEndpoint: r.RemoteAddr,
		ReceivedAt:        s.clock(),
	})

	switch decision {
	case ApprovalGranted:
		cert, err := NewVouchCert(s.priv, req.Action, req.SubjectID, payloadHash, s.clock())
		if err != nil {
			writeVoucherJSON(w, http.StatusInternalServerError, vouchWireResponse{Status: "error", Reason: "signing failed"})
			return
		}
		writeVoucherJSON(w, http.StatusOK, vouchWireResponse{Status: "granted", Cert: cert})
	case ApprovalPending:
		writeVoucherJSON(w, http.StatusAccepted, vouchWireResponse{Status: "pending", Reason: "awaiting explicit operator approval"})
	default: // ApprovalDenied and any unrecognized value — FAIL CLOSED
		writeVoucherJSON(w, http.StatusForbidden, vouchWireResponse{Status: "denied", Reason: "not approved"})
	}
}

// handleApprove is the operator-facing endpoint. It requires BOTH
// approveGate(r) to pass AND s.policy to implement Approver; a policy that
// cannot be approved into (e.g. DenyAllPolicy) makes this endpoint always
// report 501, which is the correct behavior for a box configured to never
// vouch.
func (s *VoucherService) handleApprove(w http.ResponseWriter, r *http.Request, approveGate ApproveGate) {
	if approveGate == nil || !approveGate(r) {
		writeVoucherJSON(w, http.StatusForbidden, vouchWireResponse{Status: "error", Reason: "operator approval gate refused this caller"})
		return
	}
	approver, ok := s.policy.(Approver)
	if !ok {
		writeVoucherJSON(w, http.StatusNotImplemented, vouchWireResponse{Status: "error", Reason: "this box's approval policy does not support explicit approvals"})
		return
	}
	var req approveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxVouchBodyBytes)).Decode(&req); err != nil {
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "invalid JSON body"})
		return
	}
	if !validAction(req.Action) || req.SubjectID == "" || req.RequestID == "" {
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "missing/invalid action, subject_id, or request_id"})
		return
	}
	payloadHash, err := base64.RawURLEncoding.DecodeString(req.PayloadHash)
	if err != nil || len(payloadHash) == 0 {
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "malformed payload_hash"})
		return
	}
	if req.SubjectID == s.selfVulaID {
		// An operator cannot approve a self-vouch into existence either — the
		// handler above would refuse to sign it regardless, but reject the
		// approval itself too so the pending-request UI never even offers it.
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "cannot approve a self-vouch"})
		return
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	approver.Approve(req.Action, req.SubjectID, payloadHash, req.RequestID, ttl)
	writeVoucherJSON(w, http.StatusOK, vouchWireResponse{Status: "granted"})
}

func (s *VoucherService) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func writeVoucherJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
