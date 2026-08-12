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
	"fmt"
	"net/http"
	"time"

	"vulos/backend/services/peering"
	"vulos/backend/services/signing"
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

// DefaultPendingPath is the path VoucherService.RegisterHandlers mounts the
// OPERATOR-facing pending-queue endpoint at. It is the READ half of the manual
// approval gate: DefaultApprovePath takes the exact (action, subject, payload
// hash, request id) tuple, and every one of those values is chosen by the
// REQUESTING box, so without a way to read the queue there is nothing a human
// could put in the approve call. Gated by the same ApproveGate as approve —
// this list names peers and the actions they want, and is nobody's business but
// this box's operator.
const DefaultPendingPath = "/api/fleetid/vouch/pending"

// DefaultDismissPath is the path VoucherService.RegisterHandlers mounts the
// OPERATOR-facing dismiss endpoint at. Dismissing drops a pending RECORD; it
// authorizes nothing, denies nothing (the gate is already deny-by-default) and
// does not block the peer — a re-ask reappears in the queue. It exists so an
// operator can clear requests they have judged and refused, because a queue
// padded with junk is how the one real request gets missed.
const DefaultDismissPath = "/api/fleetid/vouch/dismiss"

// maxVouchBodyBytes bounds request bodies read by this package's handlers,
// mirroring the 64<<10 ceiling used by cmd/server's other small JSON POST
// handlers (e.g. routes_devicekey_lifecycle.go).
const maxVouchBodyBytes = 64 << 10

// VouchRequestType tags every signed vouch request, so a signature made here
// can never be replayed into another context that the subject's key also signs
// for (VouchCert carries VouchCertType for the same reason).
const VouchRequestType = "fleet-vouch-request"

// VouchRequest is the wire shape POSTed to DefaultVouchPath by the initiator
// side (transport.go's HTTPTransport) and decoded here. PayloadHash is
// base64url (raw, no padding) — the SAME encoding VouchCert.PayloadHash uses.
//
// The request is SELF-AUTHENTICATING: Sig is an Ed25519 signature by the
// SUBJECT's own fleet key over the canonical bytes of the request with Sig
// cleared. SubjectID is a Vulos ID, which *is* that public key
// (peering.PublicKeyForVulosID), so a peer can verify the request with no prior
// state, no session, and no shared secret — the same trick
// /.well-known/vulos-id and the prekey endpoints use to be reachable by strangers
// without being open to them. See VerifyVouchRequest for what that does and
// does not buy.
type VouchRequest struct {
	// Type must be VouchRequestType.
	Type string `json:"type"`

	Action      string `json:"action"`
	SubjectID   string `json:"subject_id"`
	PayloadHash string `json:"payload_hash"`
	RequestID   string `json:"request_id"`

	// IssuedAt is the RFC3339 UTC issue time, checked against the same
	// freshness window as a VouchCert so a captured request cannot be replayed
	// at a peer indefinitely.
	IssuedAt string `json:"issued_at"`

	// Sig is base64url (raw) Ed25519 over canonical(request with Sig == "")
	// by the key SubjectID encodes.
	Sig string `json:"sig"`
}

// SignVouchRequest fills in Type/IssuedAt and signs req with subjectPriv, which
// MUST be the private half of the key req.SubjectID encodes — a box asks peers
// to vouch for ITSELF (devicekey's QuorumSubjectID is this box's own fleet
// identity), so it always holds that key. Signing for someone else's SubjectID
// is refused rather than silently produced.
func SignVouchRequest(subjectPriv ed25519.PrivateKey, req *VouchRequest, now time.Time) error {
	if req == nil {
		return errors.New("fleetid: SignVouchRequest: nil request")
	}
	if len(subjectPriv) != ed25519.PrivateKeySize {
		return errors.New("fleetid: SignVouchRequest: subject private key wrong size")
	}
	self := peering.EncodeVulosID(subjectPriv.Public().(ed25519.PublicKey))
	if req.SubjectID == "" {
		req.SubjectID = self
	}
	if req.SubjectID != self {
		return errors.New("fleetid: SignVouchRequest: key does not match subject_id (a box may only request vouches for itself)")
	}

	req.Type = VouchRequestType
	req.IssuedAt = now.UTC().Format(time.RFC3339)
	req.Sig = ""

	payload, err := signing.Canonical(*req)
	if err != nil {
		return err
	}
	req.Sig = base64.RawURLEncoding.EncodeToString(ed25519.Sign(subjectPriv, payload))
	return nil
}

// VerifyVouchRequest is the endpoint's OWN authentication, and the reason it
// can sit in auth.publicPaths without "public" meaning "unauthenticated and
// trusted". It fails closed on anything it cannot prove.
//
// What it proves: this request was produced by the holder of the fleet key
// SubjectID names, recently, for exactly this (action, subject, payload,
// request id) — not by an anonymous caller who merely reached the port, and not
// replayed from a capture older than the freshness window.
//
// What it does NOT prove, and must not be mistaken for: that the subject is
// entitled to a vouch. Nothing about a signature makes a box trustworthy — a
// fully compromised box holds its own key and can sign perfectly. Authorization
// remains, exactly as before, the deny-by-default approval gate (policy.go):
// no VouchCert is ever signed without an operator explicitly approving that one
// tuple. This check exists so that gate is only ever reached by requests whose
// origin is attributable, and so an operator's approval prompt names a peer
// that provably sent it.
func VerifyVouchRequest(req VouchRequest, now time.Time) error {
	if req.Type != VouchRequestType {
		return fmt.Errorf("fleetid: vouch request: wrong type %q (want %q)", req.Type, VouchRequestType)
	}
	pub, err := peering.PublicKeyForVulosID(req.SubjectID)
	if err != nil {
		return fmt.Errorf("fleetid: vouch request: subject id: %w", err)
	}
	issuedAt, err := time.Parse(time.RFC3339, req.IssuedAt)
	if err != nil {
		return fmt.Errorf("fleetid: vouch request: bad issued_at %q", req.IssuedAt)
	}
	// Same window a VouchCert itself is counted in: a request that could sit
	// around longer than the cert it would produce buys an attacker nothing.
	if issuedAt.Before(now.Add(-VouchMaxAge)) {
		return errors.New("fleetid: vouch request: stale (outside the freshness window)")
	}
	if issuedAt.After(now.Add(ClockSkew)) {
		return errors.New("fleetid: vouch request: issued in the future")
	}

	sig, err := base64.RawURLEncoding.DecodeString(req.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("fleetid: vouch request: malformed signature")
	}
	unsigned := req
	unsigned.Sig = ""
	payload, err := signing.Canonical(unsigned)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return errors.New("fleetid: vouch request: signature does not verify against subject_id's key")
	}
	return nil
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

// PendingLister is the OPTIONAL capability an ApprovalPolicy implements to
// support the operator-facing pending-queue endpoint. ManualApprovalPolicy
// implements it; DenyAllPolicy has no queue (nothing it lists could ever be
// approved) and does not.
type PendingLister interface {
	Pending() []PendingVouch
	PendingEvicted() uint64
}

// Forgetter is the OPTIONAL capability backing DefaultDismissPath.
type Forgetter interface {
	Forget(action, subjectID string, payloadHash []byte, requestID string)
}

// PeerAnnotation is what THIS box knows about a peer identity, supplied by the
// deployment (see VoucherService.SetPeerAnnotator) so the operator's approval
// prompt can answer the first question a human will ask: "is this one of my
// boxes?"
//
// This is the single most decision-relevant fact in the whole prompt and this
// package cannot compute it — fleetid deliberately holds no roster; the roster
// belongs to the box (cmd/server's registryRoster over multiinstance.Registry).
// VerifyVouchRequest already proved the request came from the holder of the key
// SubjectID names; whether that key is a box the operator actually owns is a
// completely separate question, and one an operator has no way to answer from a
// base64 Vulos ID by eye.
type PeerAnnotation struct {
	// Known reports that SubjectID is a member of this box's own fleet roster.
	Known bool `json:"known"`
	// DisplayName is the roster's name for it, when known.
	DisplayName string `json:"display_name,omitempty"`
	// Revoked reports that the roster has this identity marked revoked — a
	// vouch request from a revoked peer is a red flag, not a routine ask, and
	// VerifyQuorum would refuse to count a cert signed by one anyway.
	Revoked bool `json:"revoked,omitempty"`
}

// PeerAnnotator resolves a Vulos ID against this box's own roster.
type PeerAnnotator func(vulosID string) PeerAnnotation

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
	priv        ed25519.PrivateKey
	selfVulosID string
	policy      ApprovalPolicy
	now         func() time.Time
	annotate    PeerAnnotator
}

// NewVoucherService builds a VoucherService that signs with signerPriv — this
// box's OWN fleet identity key (the same key/Vulos-ID space vouch.go's
// VouchCert and NewVouchCert use). The service's own fleet identity
// (SelfVulosID) is DERIVED from signerPriv's public key via
// peering.EncodeVulosID — it is never taken as a separately supplied
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
		priv:        signerPriv,
		selfVulosID: peering.EncodeVulosID(pub),
		policy:      policy,
		now:         time.Now,
	}, nil
}

// SelfVulosID returns this voucher's own fleet identity — the value a request
// whose SubjectID matches will always be refused as a self-vouch.
func (s *VoucherService) SelfVulosID() string { return s.selfVulosID }

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
	mux.HandleFunc("GET "+DefaultPendingPath, func(w http.ResponseWriter, r *http.Request) {
		s.handlePending(w, r, approveGate)
	})
	mux.HandleFunc("POST "+DefaultDismissPath, func(w http.ResponseWriter, r *http.Request) {
		s.handleDismiss(w, r, approveGate)
	})
}

// SetPeerAnnotator supplies the roster lookup used to annotate the pending
// queue (see PeerAnnotation). Optional: with no annotator the queue is served
// UNANNOTATED and says so (annotated:false), rather than reporting every
// subject as unknown — a UI must be able to tell "this box checked its roster
// and does not recognise this peer" from "nothing checked", because those two
// call for opposite operator reactions.
func (s *VoucherService) SetPeerAnnotator(fn PeerAnnotator) { s.annotate = fn }

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
	if _, err := peering.PublicKeyForVulosID(req.SubjectID); err != nil {
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "malformed subject_id"})
		return
	}
	payloadHash, err := base64.RawURLEncoding.DecodeString(req.PayloadHash)
	if err != nil || len(payloadHash) == 0 {
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "malformed payload_hash"})
		return
	}

	// ── The endpoint's OWN authentication ──────────────────────────────────
	// This route is peer-facing and therefore exempt from the OS session
	// middleware (auth.publicPaths). Exempt must not mean unauthenticated: the
	// request has to carry a fresh Ed25519 signature by the very key its
	// subject_id encodes, or it never reaches the approval policy at all.
	// 401, not 403 — the caller did not authenticate, rather than being
	// refused something it asked for.
	if verr := VerifyVouchRequest(req, s.clock()); verr != nil {
		writeVoucherJSON(w, http.StatusUnauthorized, vouchWireResponse{
			Status: "error",
			Reason: "unauthenticated vouch request: " + verr.Error(),
		})
		return
	}

	// ── HARD RULE, defense in depth: never vouch for ourselves ─────────────
	// VerifyQuorum already refuses a self-vouch unconditionally (vouch.go's
	// DropSelfVouch), but a compromised or merely misconfigured peer should
	// not even be ABLE to produce one — refusing before the approval gate is
	// even consulted means no policy misconfiguration can accidentally grant
	// a self-vouch either.
	if req.SubjectID == s.selfVulosID {
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
	if req.SubjectID == s.selfVulosID {
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

// pendingVouchWire is one queued request as the operator's UI sees it. Every
// field is either part of the tuple that must be echoed back to approve, or
// evidence the operator needs to judge it.
type pendingVouchWire struct {
	Action      string `json:"action"`
	SubjectID   string `json:"subject_id"`
	PayloadHash string `json:"payload_hash"` // base64url raw — the string compared out of band
	RequestID   string `json:"request_id"`

	// RequesterEndpoint is transport-reported and UNTRUSTED; the wire name says
	// so, so no UI can present it as an identity.
	RequesterEndpoint string `json:"requester_endpoint_untrusted,omitempty"`

	FirstSeenAt string `json:"first_seen_at"`
	LastSeenAt  string `json:"last_seen_at"`
	Attempts    int    `json:"attempts"`

	// Subject is this box's own roster's view of SubjectID, present only when
	// an annotator is wired (see the response's Annotated field).
	Subject *PeerAnnotation `json:"subject,omitempty"`
}

// pendingListResponse is the operator-facing queue.
type pendingListResponse struct {
	Status      string             `json:"status"`
	SelfVulosID string             `json:"self_vulos_id"`
	Now         string             `json:"now"`
	Pending     []pendingVouchWire `json:"pending"`

	// Evicted is how many records were dropped at the cap. Non-zero means this
	// list is INCOMPLETE and the box is being flooded — a UI must show it.
	Evicted uint64 `json:"evicted"`

	// Annotated reports whether the Subject annotations were computed at all.
	// False means no roster lookup was available — absent annotations then mean
	// "not checked", NOT "not recognised".
	Annotated bool `json:"annotated"`
}

// handlePending serves the operator's view of the queue. Same gate as approve:
// what this box has been asked to vouch for is operator-only.
func (s *VoucherService) handlePending(w http.ResponseWriter, r *http.Request, approveGate ApproveGate) {
	if approveGate == nil || !approveGate(r) {
		writeVoucherJSON(w, http.StatusForbidden, vouchWireResponse{Status: "error", Reason: "operator approval gate refused this caller"})
		return
	}
	lister, ok := s.policy.(PendingLister)
	if !ok {
		writeVoucherJSON(w, http.StatusNotImplemented, vouchWireResponse{Status: "error", Reason: "this box's approval policy keeps no pending queue"})
		return
	}
	now := s.clock()
	resp := pendingListResponse{
		Status:      "ok",
		SelfVulosID: s.selfVulosID,
		Now:         now.UTC().Format(time.RFC3339),
		Pending:     []pendingVouchWire{},
		Evicted:     lister.PendingEvicted(),
		Annotated:   s.annotate != nil,
	}
	for _, p := range lister.Pending() {
		item := pendingVouchWire{
			Action:            p.Action,
			SubjectID:         p.SubjectID,
			PayloadHash:       base64.RawURLEncoding.EncodeToString(p.PayloadHash),
			RequestID:         p.RequestID,
			RequesterEndpoint: p.RequesterEndpoint,
			FirstSeenAt:       p.FirstSeenAt.UTC().Format(time.RFC3339),
			LastSeenAt:        p.LastSeenAt.UTC().Format(time.RFC3339),
			Attempts:          p.Attempts,
		}
		if s.annotate != nil {
			ann := s.annotate(p.SubjectID)
			item.Subject = &ann
		}
		resp.Pending = append(resp.Pending, item)
	}
	writeVoucherJSON(w, http.StatusOK, resp)
}

// handleDismiss drops one pending RECORD. It grants nothing and blocks nothing.
func (s *VoucherService) handleDismiss(w http.ResponseWriter, r *http.Request, approveGate ApproveGate) {
	if approveGate == nil || !approveGate(r) {
		writeVoucherJSON(w, http.StatusForbidden, vouchWireResponse{Status: "error", Reason: "operator approval gate refused this caller"})
		return
	}
	forgetter, ok := s.policy.(Forgetter)
	if !ok {
		writeVoucherJSON(w, http.StatusNotImplemented, vouchWireResponse{Status: "error", Reason: "this box's approval policy keeps no pending queue"})
		return
	}
	var req approveRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxVouchBodyBytes)).Decode(&req); err != nil {
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "invalid JSON body"})
		return
	}
	payloadHash, err := base64.RawURLEncoding.DecodeString(req.PayloadHash)
	if err != nil || len(payloadHash) == 0 {
		writeVoucherJSON(w, http.StatusBadRequest, vouchWireResponse{Status: "error", Reason: "malformed payload_hash"})
		return
	}
	forgetter.Forget(req.Action, req.SubjectID, payloadHash, req.RequestID)
	writeVoucherJSON(w, http.StatusOK, vouchWireResponse{Status: "dismissed"})
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
