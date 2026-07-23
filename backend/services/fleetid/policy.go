// policy.go — the APPROVAL GATE a peer box applies before it will ever sign a
// VouchCert for another box's break-glass request.
//
// # The hard rule (non-negotiable, mirrors vouch.go's self-exclusion rule)
//
// A peer must NEVER auto-vouch. Receiving a network request asking "please
// vouch for subject S" is, by itself, ZERO evidence that a human at this box
// actually wants to authorize S's break-glass action — an attacker who can
// merely REACH this box's vouch endpoint (no compromise of its keys required)
// must not be able to manufacture a valid VouchCert. The only thing that may
// turn a request into a signature is an EXPLICIT, out-of-band approval, bound
// to the exact (action, subject, payload hash, request id) being decided.
//
// This is the same idiom already used elsewhere in this codebase for
// "a normal session must not be enough, something extra must happen first":
//
//   - services/stepup: a valid cookie is not enough to call a privileged
//     handler; the caller must first re-prove a factor (password) to mint a
//     short-lived elevated token. Require fails closed on any doubt.
//   - services/passkeys/qrlogin: a kiosk's "please log me in" is not enough by
//     itself; an already-authenticated device must separately call Approve
//     with the matching challenge/nonce before a session is minted.
//
// Here: a peer's "please vouch for me" is not enough by itself; an operator
// must separately call ManualApprovalPolicy.Approve with the matching
// (action, subject, payload hash, request id) before VoucherService will ever
// sign. The default policy wired by NewVoucherService is exactly this manual,
// deny-by-default policy — there is no built-in policy that auto-approves.
package fleetid

import (
	"bytes"
	"sync"
	"time"
)

// ApprovalDecision is the outcome of an ApprovalPolicy's Decide call.
type ApprovalDecision int

const (
	// ApprovalDenied is the ZERO VALUE — an ApprovalPolicy that forgets to set
	// a decision, or any code path that falls through without an explicit
	// decision, fails CLOSED as a denial, never as a grant.
	ApprovalDenied ApprovalDecision = iota
	// ApprovalPending means no decision has been made yet (e.g. an operator
	// has not yet reviewed this specific request). It is NOT a grant; the
	// peer must not sign. A pending request may later become Granted (an
	// operator approves) or stay Denied forever (never approved).
	ApprovalPending
	// ApprovalGranted is the ONLY decision that causes VoucherService to sign.
	// It must be returned only for a request an explicit, out-of-band
	// approval covers exactly (same action/subject/payload hash/request id).
	ApprovalGranted
)

// ApprovalRequest carries everything an ApprovalPolicy needs to decide,
// mirroring exactly the fields that end up bound into the resulting
// VouchCert (see vouch.go's VerifyQuorum binding checks). PayloadHash is the
// raw (decoded) hash bytes, not the wire base64 encoding.
type ApprovalRequest struct {
	Action            string
	SubjectID         string
	PayloadHash       []byte
	RequestID         string
	RequesterEndpoint string // audit-only; the transport-supplied origin, UNTRUSTED
	ReceivedAt        time.Time
}

// ApprovalPolicy decides whether THIS box may vouch for a request it just
// received over the network.
//
// SECURITY-CRITICAL: every implementation MUST fail closed. Decide is called
// on every inbound vouch request BEFORE any signature is produced; returning
// ApprovalGranted is the ONLY way a VouchCert gets minted, so an
// implementation that grants too eagerly directly breaks the hard rule
// documented in vouch.go ("a box must never be able to authorize its own
// recovery" — a peer that rubber-stamps requests is exactly such a hole, just
// one hop removed).
type ApprovalPolicy interface {
	Decide(req ApprovalRequest) ApprovalDecision
}

// DenyAllPolicy never approves anything. It exists as an explicit, auditable
// "vouching disabled" configuration (e.g. a box whose operator has decided
// this box should never act as a voucher) — distinct from ManualApprovalPolicy
// in that no Approve call can ever change its answer.
type DenyAllPolicy struct{}

// Decide always returns ApprovalDenied.
func (DenyAllPolicy) Decide(ApprovalRequest) ApprovalDecision { return ApprovalDenied }

// approvalKey identifies one specific decision binding: an operator approving
// one (action, subject, payload hash) does NOT approve a different payload
// hash or a different request id for the same subject, even moments later —
// each break-glass session gets its own approval.
type approvalKey struct {
	action    string
	subjectID string
	payload   string // string(PayloadHash) used as a map key; compared with bytes.Equal on lookup, not as a security boundary
	requestID string
}

type approvalEntry struct {
	expiresAt time.Time
}

// ManualApprovalPolicy is the DEFAULT approval policy: nothing is ever
// approved automatically. An operator (human, admin UI, CLI — the front end
// is intentionally out of scope for this package, see the INTEGRATION
// MANIFEST) must call Approve with the EXACT (action, subjectID, payloadHash,
// requestID) of a specific inbound request before VoucherService will sign
// for it. A request that is never explicitly approved this way stays denied
// forever — there is no timeout-to-grant, no implicit approval, and no way
// to approve "any request from subject S" in bulk (each request id is its own
// decision).
//
// Safe for concurrent use.
type ManualApprovalPolicy struct {
	mu       sync.Mutex
	approved map[approvalKey]approvalEntry
}

// NewManualApprovalPolicy returns a policy with nothing pre-approved.
func NewManualApprovalPolicy() *ManualApprovalPolicy {
	return &ManualApprovalPolicy{approved: make(map[approvalKey]approvalEntry)}
}

// Approve explicitly grants ONE specific pending request, identified by the
// exact (action, subjectID, payloadHash, requestID) tuple the operator
// reviewed. ttl bounds how long the grant remains usable (a grant that is
// never consumed expires rather than sitting around indefinitely); ttl <= 0
// is treated as VouchMaxAge, the same freshness window VerifyQuorum itself
// applies to the resulting cert, so an approval cannot outlive the cert it
// would produce anyway.
//
// Approve does not itself verify anything about the request's authenticity —
// it is the trusted, out-of-band operator action that this whole gate exists
// to require. Calling it is a deliberate, reviewed decision by whatever
// front-end wires it up (see the INTEGRATION MANIFEST for the currently
// UNWIRED HTTP approval endpoint).
func (p *ManualApprovalPolicy) Approve(action, subjectID string, payloadHash []byte, requestID string, ttl time.Duration) {
	if ttl <= 0 {
		ttl = VouchMaxAge
	}
	key := approvalKey{action: action, subjectID: subjectID, payload: string(payloadHash), requestID: requestID}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.approved[key] = approvalEntry{expiresAt: time.Now().Add(ttl)}
}

// Revoke withdraws a grant before it is consumed (an operator changing their
// mind). No-op if the tuple was never approved or already consumed/expired.
func (p *ManualApprovalPolicy) Revoke(action, subjectID string, payloadHash []byte, requestID string) {
	key := approvalKey{action: action, subjectID: subjectID, payload: string(payloadHash), requestID: requestID}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.approved, key)
}

// Decide reports ApprovalGranted only when Approve was previously called for
// EXACTLY this (action, subjectID, payloadHash, requestID) and the grant has
// not expired. A granted decision is consumed (single-use): a matching grant
// is deleted once it has been read, so a second Decide for the same tuple
// (e.g. a retried HTTP request) sees ApprovalPending, never a stale
// already-spent grant being replayed to sign a second, different cert for the
// same bundle. Any other case — no grant, wrong payload hash, expired grant —
// fails closed to ApprovalPending, never to a grant.
func (p *ManualApprovalPolicy) Decide(req ApprovalRequest) ApprovalDecision {
	key := approvalKey{action: req.Action, subjectID: req.SubjectID, payload: string(req.PayloadHash), requestID: req.RequestID}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.approved[key]
	if !ok {
		return ApprovalPending
	}
	if time.Now().After(entry.expiresAt) {
		delete(p.approved, key)
		return ApprovalPending
	}
	// Defense in depth: the map key already encodes payload as a string, but
	// re-compare with bytes.Equal so a future refactor of approvalKey cannot
	// silently weaken this to a prefix/length-insensitive match.
	if !bytes.Equal([]byte(key.payload), req.PayloadHash) {
		return ApprovalPending
	}
	delete(p.approved, key) // single-use: consume the grant
	return ApprovalGranted
}
