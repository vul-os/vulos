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
	"sort"
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

// PendingVouch is one inbound vouch request THIS box has received and has NOT
// approved. It exists for exactly one reason: an operator cannot approve what
// they cannot see.
//
// Approve is bound to the EXACT (action, subject, payload hash, request id)
// tuple, and every part of that tuple is chosen by the REQUESTING box — the
// payload hash and the request id in particular are opaque values a human at
// this box has no other way to learn. Without a record of what was asked, the
// deny-by-default gate is not "strict", it is unreachable: there is nothing an
// operator could type. This record is the operator's view of the queue, and
// nothing more — holding a PendingVouch grants no authority whatsoever, and
// Decide keeps returning ApprovalPending for it until Approve is called.
//
// Every field here is either cryptographically bound into the resulting
// VouchCert (Action/SubjectID/PayloadHash) or is evidence the operator needs to
// judge the request (freshness, repetition, claimed origin). RequesterEndpoint
// is transport-supplied and therefore UNTRUSTED — it is a hint about where the
// packets came from, never a statement about who sent them; only SubjectID is
// attributable, because VerifyVouchRequest proved the request was signed by the
// key it names.
type PendingVouch struct {
	Action      string
	SubjectID   string
	PayloadHash []byte
	RequestID   string

	// RequesterEndpoint is the transport-reported origin (an IP:port for the
	// HTTP transport). UNTRUSTED — see above.
	RequesterEndpoint string

	// FirstSeenAt / LastSeenAt bound how long this request has been asking, and
	// Attempts counts how many times it has been re-asked (an initiator polls
	// while it is pending). A request that suddenly appears and is hammered, or
	// one that has been sitting for a long time, is exactly the kind of thing an
	// operator should weigh before approving.
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	Attempts    int
}

// maxPendingVouches bounds how many distinct pending requests are remembered.
//
// The vouch request endpoint is reachable by any holder of any Ed25519 key —
// VerifyVouchRequest proves the request is attributable, NOT that its sender is
// anyone this box knows — so an adversary can mint unlimited distinct
// (subject, request id) tuples. An unbounded map would be a memory-growth
// primitive for a stranger. The oldest record is evicted at the cap and the
// eviction is COUNTED (PendingEvicted), because silently dropping the queue an
// operator is reading is itself a way to hide a request: a flood that pushes a
// legitimate entry off the list must be visible as a flood.
const maxPendingVouches = 128

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

	// pending is the operator-visible queue: every tuple that has been asked
	// for and not granted. Written by Decide, read by Pending. It is a VIEW,
	// never an authority — see PendingVouch.
	pending map[approvalKey]*PendingVouch
	// evicted counts pending records dropped at the maxPendingVouches cap.
	evicted uint64

	// now is the clock, overridable for tests via SetClock.
	now func() time.Time
}

// NewManualApprovalPolicy returns a policy with nothing pre-approved.
func NewManualApprovalPolicy() *ManualApprovalPolicy {
	return &ManualApprovalPolicy{
		approved: make(map[approvalKey]approvalEntry),
		pending:  make(map[approvalKey]*PendingVouch),
		now:      time.Now,
	}
}

// SetClock overrides the clock used for grant expiry and pending-record aging
// (test seam), mirroring VoucherService.SetClock.
func (p *ManualApprovalPolicy) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = now
}

// clock reads the current time. Callers must hold p.mu.
func (p *ManualApprovalPolicy) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
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
	if p.approved == nil {
		p.approved = make(map[approvalKey]approvalEntry)
	}
	p.approved[key] = approvalEntry{expiresAt: p.clock().Add(ttl)}
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
		p.recordPendingLocked(key, req)
		return ApprovalPending
	}
	if p.clock().After(entry.expiresAt) {
		delete(p.approved, key)
		p.recordPendingLocked(key, req)
		return ApprovalPending
	}
	// Defense in depth: the map key already encodes payload as a string, but
	// re-compare with bytes.Equal so a future refactor of approvalKey cannot
	// silently weaken this to a prefix/length-insensitive match.
	if !bytes.Equal([]byte(key.payload), req.PayloadHash) {
		p.recordPendingLocked(key, req)
		return ApprovalPending
	}
	delete(p.approved, key) // single-use: consume the grant
	delete(p.pending, key)  // it is no longer waiting on the operator
	return ApprovalGranted
}

// recordPendingLocked notes that this exact tuple was asked for and not
// granted, so an operator can see it. Callers must hold p.mu.
//
// It is called on EVERY non-granting outcome, including the paths that fail
// closed (expired grant, payload mismatch) — a request that was refused because
// the operator's approval had expired is precisely one they may want to re-approve,
// and hiding it would make the gate look broken rather than strict.
func (p *ManualApprovalPolicy) recordPendingLocked(key approvalKey, req ApprovalRequest) {
	if p.pending == nil {
		p.pending = make(map[approvalKey]*PendingVouch)
	}
	seen := req.ReceivedAt
	if seen.IsZero() {
		seen = p.clock()
	}
	if existing, ok := p.pending[key]; ok {
		existing.LastSeenAt = seen
		existing.Attempts++
		// The endpoint may legitimately move (a peer reconnecting from a new
		// address); show the operator the most recent claimed origin.
		existing.RequesterEndpoint = req.RequesterEndpoint
		return
	}
	p.pruneStalePendingLocked(seen)
	if len(p.pending) >= maxPendingVouches {
		p.evictOldestPendingLocked()
	}
	// Copy the hash: the caller owns req.PayloadHash and this record outlives
	// the request.
	payload := append([]byte(nil), req.PayloadHash...)
	p.pending[key] = &PendingVouch{
		Action:            req.Action,
		SubjectID:         req.SubjectID,
		PayloadHash:       payload,
		RequestID:         req.RequestID,
		RequesterEndpoint: req.RequesterEndpoint,
		FirstSeenAt:       seen,
		LastSeenAt:        seen,
		Attempts:          1,
	}
}

// pruneStalePendingLocked drops records nobody has re-asked within VouchMaxAge.
//
// VouchMaxAge is the same window VerifyQuorum counts a cert in and
// VerifyVouchRequest accepts a request in. An initiator that still wants this
// vouch keeps asking (each attempt refreshes LastSeenAt); one that has been
// silent for longer than the whole freshness window is not waiting on this
// operator any more. Showing it would pad the queue an operator has to read
// with requests that no longer exist, which is how a real one gets missed.
func (p *ManualApprovalPolicy) pruneStalePendingLocked(now time.Time) {
	for k, v := range p.pending {
		if v.LastSeenAt.Before(now.Add(-VouchMaxAge)) {
			delete(p.pending, k)
		}
	}
}

// evictOldestPendingLocked makes room by dropping the least-recently-asked
// record, and counts the drop.
func (p *ManualApprovalPolicy) evictOldestPendingLocked() {
	var oldestKey approvalKey
	var oldest time.Time
	first := true
	for k, v := range p.pending {
		if first || v.LastSeenAt.Before(oldest) {
			oldestKey, oldest, first = k, v.LastSeenAt, false
		}
	}
	if first {
		return
	}
	delete(p.pending, oldestKey)
	p.evicted++
}

// Pending returns a snapshot of the requests waiting on an operator decision,
// most recently asked first. The returned records are copies: mutating them
// cannot affect what this policy will decide.
func (p *ManualApprovalPolicy) Pending() []PendingVouch {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneStalePendingLocked(p.clock())
	out := make([]PendingVouch, 0, len(p.pending))
	for _, v := range p.pending {
		cp := *v
		cp.PayloadHash = append([]byte(nil), v.PayloadHash...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeenAt.Equal(out[j].LastSeenAt) {
			return out[i].RequestID < out[j].RequestID
		}
		return out[i].LastSeenAt.After(out[j].LastSeenAt)
	})
	return out
}

// PendingEvicted reports how many pending records have been dropped at the
// maxPendingVouches cap since this policy was created. Non-zero means the queue
// an operator is reading is INCOMPLETE — surface it, do not swallow it.
func (p *ManualApprovalPolicy) PendingEvicted() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.evicted
}

// Forget drops the pending RECORD for one tuple without approving anything —
// the operator dismissing a request they have judged and refused. It does not
// deny anything (the gate is already deny-by-default) and it does not stop the
// peer re-asking; a re-ask reappears in the queue, which is correct: dismissal
// is not a block-list.
func (p *ManualApprovalPolicy) Forget(action, subjectID string, payloadHash []byte, requestID string) {
	key := approvalKey{action: action, subjectID: subjectID, payload: string(payloadHash), requestID: requestID}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.pending, key)
}
