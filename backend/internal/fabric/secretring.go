package fabric

// secretring.go — FABRIC-SECRET-ROT-01: rotation for VULOS_FABRIC_SECRET.
//
// # Where the secret lived, and why it could not be rotated
//
// It is an ENVIRONMENT VARIABLE READ ONCE AT STARTUP. cmd/server/main.go does
// `os.Getenv("VULOS_FABRIC_SECRET")` during boot and copies the resulting string
// into three places that then hold it, immutable, for the life of the process:
//
//	fabric.Config.Secret        — compared by Service.authOK on every inbound
//	                              /api/fabric/{changeset,status} request, and
//	                              sent as X-Fabric-Auth on every outbound one.
//	crdtsync.SyncerConfig.Secret — sent as X-Fabric-Auth to LAN peers.
//	crdtsync.SecretAuthorizer(s) — a closure over that same string, gating
//	                              /api/crdt/{pull,push,sync-status}.
//
// That shape has exactly one rotation mechanic available to it — change the
// variable and restart — and exactly one acceptance slot, so the moment the
// first box restarts with the new value it stops being able to talk to every box
// that has not restarted yet. A fleet updates one box at a time. So "rotate the
// secret" meant "partition the fleet, then unpartition it", which is why nobody
// ever did, and why there was no tooling to do it with.
//
// It is NOT a file re-read on change. Nothing watches it, nothing reloads it,
// and no handler can write it. That last property is kept deliberately — see
// "Who may rotate", below.
//
// # What changes
//
// The single acceptance slot becomes a ring with TWO slots and a DEADLINE:
//
//	VULOS_FABRIC_SECRET             the CURRENT secret. Accepted inbound, and
//	                                the only value ever SENT outbound.
//	VULOS_FABRIC_SECRET_ALSO        an ADDITIONAL accepted secret. Accepted
//	                                inbound only. Never sent, ever.
//	VULOS_FABRIC_SECRET_ALSO_UNTIL  RFC3339 absolute instant after which the
//	                                ALSO slot is refused. REQUIRED whenever
//	                                ALSO is set.
//
// The slot is called ALSO and not PREV on purpose. A safe roll needs it to hold
// the NEW secret first and the OLD secret second (see the two-phase procedure
// below); a name that says "previous" would push an operator toward a one-phase
// roll, which partitions the fleet exactly as before. The slot is directional
// only in that it is never transmitted.
//
// # The two-phase roll, which is why one extra slot is enough
//
// Let O be the old secret and N the new one.
//
//	PHASE 1 — PREPARE. On every box, in any order, one at a time:
//	    VULOS_FABRIC_SECRET=O   VULOS_FABRIC_SECRET_ALSO=N   ..._UNTIL=<deadline>
//	  Every box still SENDS O, which every box still accepts. There is no window
//	  in which a prepared box cannot talk to an unprepared one. When the last box
//	  is prepared, the whole fleet accepts both.
//
//	PHASE 2 — COMMIT. On every box, in any order, one at a time:
//	    VULOS_FABRIC_SECRET=N   VULOS_FABRIC_SECRET_ALSO=O   ..._UNTIL=<deadline>
//	  A committed box sends N; an uncommitted box is still in phase 1 and accepts
//	  N in its ALSO slot. An uncommitted box sends O; a committed box accepts O in
//	  its ALSO slot. Every pair works in both directions throughout.
//
//	PHASE 3 — CLOSE. Either do nothing and let the deadline pass, or remove
//	  VULOS_FABRIC_SECRET_ALSO / ..._UNTIL at the next restart. Both close it; the
//	  deadline closes it WITHOUT an operator coming back, which is the point.
//
// # How the window closes, and why it is a deadline
//
// An overlap nobody closes is not a rotation, it is two secrets. Three
// mechanisms close this one, and the first needs nobody:
//
//  1. THE DEADLINE, evaluated PER REQUEST against the wall clock — not at load
//     time. A box that has been running since before the deadline starts
//     refusing the old secret the moment it passes, with no restart and no
//     operator action. This is the mechanism that makes the closing as testable
//     as the opening: the same ring, the same secret, two clock readings, two
//     answers.
//  2. THE HARD CAP. A deadline further out than MaxSecretOverlap is clamped down
//     to it, so a mistyped year cannot turn the overlap into a permanent second
//     secret.
//  3. THE OPERATOR removing the variable.
//
// The deadline is an ABSOLUTE instant supplied by the operator, not
// "now + 24h" measured from process start. A process-relative window is renewed
// by every restart, so a box that restarts daily would hold the window open
// forever while every log line claimed a 24-hour overlap.
//
// A generation counter that closes the window "once every peer has been seen on
// the new secret" was considered and rejected: the shared secret cannot
// attribute a peer — that is its defining weakness and the reason eviction
// needed the signature path at all — so "every peer" is not a set this credential
// can enumerate. Counting DISTINCT SECRETS SEEN would be counting connections,
// and one box reconnecting twice would close the window on the fleet.
//
// # Who may rotate
//
// The operator, on the box, out of band. There is NO rotation endpoint and this
// file exposes no mutator: a SecretRing is immutable after construction.
//
// Two independent reasons, either sufficient:
//
//   - A rotation endpoint any peer could call is a fleet-wide denial-of-service
//     primitive, which is why the comparable routes here are gated the way they
//     are (/api/apps/launch admin-only, process kill admin-only,
//     POST /api/setup/complete owner-only because a route that ends setup can
//     skip it).
//   - Even OWNER-gated it would be wrong, and this is the decisive one. An HTTP
//     rotation would have to DISTRIBUTE the new secret to the other boxes, and
//     the only channel it has is the fabric — which the box being evicted is
//     still authenticated on for the whole overlap. You cannot hand out a new
//     group secret over a channel the evicted member can still read. The
//     distribution has to be out of band, which means the operator, which means
//     an environment variable and a restart.
//
// What this package offers instead is OBSERVABILITY, and it is shaped around the
// only two questions an operator actually has mid-roll:
//
//	"WHICH SECRET IS THIS BOX ON?"  GET /api/fabric/status answers it by telling
//	  the caller WHICH SLOT THE CALLER'S OWN HEADER matched: authenticated_with
//	  is "current" or "overlap". Present the new secret to each box in turn —
//	  "current" means that box has committed (phase 2 done), "overlap" means it
//	  has only prepared (phase 1). This discloses nothing: the box is describing
//	  a value the caller already sent it.
//
//	"HAS EVERY PEER MOVED?"  Answered as honestly as an unattributable credential
//	  permits, which is NOT by naming peers. The ring counts admissions per slot
//	  and stamps the last time the overlap slot was used. While
//	  overlap_last_used_at keeps advancing, SOMEBODY has not moved; when it goes
//	  stale, nobody is still presenting the old value and the window is safe to
//	  close. It cannot say who, and it does not pretend to — the shared secret
//	  names a fleet and not a member, which is the whole reason the signature
//	  path exists.
//
// It deliberately publishes NO digest or fingerprint of either secret. A digest
// of a low-entropy shared secret is the secret, and this endpoint is readable by
// a caller holding only the OLD one.

import (
	"crypto/subtle"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// Slot names reported by Slot and by the status endpoint's authenticated_with.
const (
	// SlotCurrent means the presented secret matched VULOS_FABRIC_SECRET — the
	// value this box also sends.
	SlotCurrent = "current"
	// SlotOverlap means it matched the rotation overlap value, which this box
	// accepts but never sends. A peer seen on this slot has not moved yet.
	SlotOverlap = "overlap"
)

// Environment variables that configure the ring.
const (
	// EnvFabricSecret is the CURRENT shared fabric secret: accepted inbound and
	// the only value ever sent outbound.
	EnvFabricSecret = "VULOS_FABRIC_SECRET"
	// EnvFabricSecretAlso is an ADDITIONAL inbound-only accepted secret, live
	// for the duration of a rotation overlap. Never transmitted.
	EnvFabricSecretAlso = "VULOS_FABRIC_SECRET_ALSO"
	// EnvFabricSecretAlsoUntil is the RFC3339 instant at which the ALSO slot
	// stops being accepted. Required whenever the ALSO slot is set — an overlap
	// with no closing time is not a rotation.
	EnvFabricSecretAlsoUntil = "VULOS_FABRIC_SECRET_ALSO_UNTIL"
)

// MaxSecretOverlap caps how long a rotation overlap may be configured to last.
// A deadline beyond it is clamped down, so a typo in the year cannot silently
// make "two secrets for a week" into "two secrets forever".
//
// Seven days: long enough for an operator to walk a fleet of boxes in different
// houses, on different power schedules, one at a time; short enough that the
// window is not a standing state of the system.
const MaxSecretOverlap = 7 * 24 * time.Hour

// SecretRing is the set of shared-secret values this box will ACCEPT, and the
// single value it will SEND.
//
// # Its POLICY is immutable after construction
//
// current, alt and altUntil are written once, by NewSecretRing, and never
// again. There is no setter, no reload and no handler that reaches them, which
// is what makes "who may rotate" answerable at all — see the file comment. The
// mutable state below it is COUNTERS ONLY: they record what the door already
// decided and can never widen what it decides next. Keeping that distinction
// sharp matters, because "the ring is immutable" is load-bearing for the claim
// that a rotation cannot be triggered over the network.
//
// Safe for concurrent use.
type SecretRing struct {
	current string

	// alt is the additional accepted secret, live only while now() is before
	// altUntil. A zero altUntil means the slot is CLOSED regardless of alt, and
	// every rejected configuration below resolves to exactly that state.
	alt      string
	altUntil time.Time

	now func() time.Time

	// warnings records every configuration input that was refused or clamped,
	// so the caller can log them with its own prefix instead of this package
	// guessing whether it is speaking for fabric or for crdtsync.
	warnings []string

	// ── observability only; see the type doc ─────────────────────────────────
	mu           sync.Mutex
	nCurrent     uint64    // requests admitted on the current secret
	nOverlap     uint64    // requests admitted on the overlap slot
	lastOverlap  time.Time // when the overlap slot was last used
	firstOverlap time.Time // and when it was first used
}

// NewSecretRing builds a ring explicitly.
//
// Every validation failure resolves to the NARROWER ring (the ALSO slot closed),
// never to a wider one, and never to a hard error: a box mid-roll whose deadline
// is malformed must fall back to accepting one secret, which is the state it was
// in before rotation existed. Refusing to boot would turn a typo into an outage
// on a box that is otherwise fine.
//
//	current == ""             → the ring accepts NOTHING (matches the previous
//	                            fail-closed behaviour of an unset secret).
//	alt == ""                 → no overlap. The ordinary steady state.
//	alt == current            → refused: presenting the same value twice is not
//	                            a rotation, and accepting it would report an open
//	                            window that protects nothing.
//	altUntil.IsZero()         → refused: an unbounded overlap is two secrets.
//	altUntil in the past      → closed. This is the NORMAL end state of a roll,
//	                            not an error — an operator may leave the
//	                            variables in place after the window has passed.
//	altUntil > MaxSecretOverlap → clamped to now+MaxSecretOverlap.
func NewSecretRing(current, alt string, altUntil time.Time) *SecretRing {
	r := &SecretRing{current: current, now: time.Now}
	if alt == "" {
		return r
	}
	if current == "" {
		r.warnings = append(r.warnings, fmt.Sprintf(
			"%s is set but %s is not — refusing to run a rotation overlap with no current secret", EnvFabricSecretAlso, EnvFabricSecret))
		return r
	}
	if subtle.ConstantTimeCompare([]byte(alt), []byte(current)) == 1 {
		r.warnings = append(r.warnings, fmt.Sprintf(
			"%s is identical to %s — that is not a rotation, the overlap slot is CLOSED", EnvFabricSecretAlso, EnvFabricSecret))
		return r
	}
	if altUntil.IsZero() {
		r.warnings = append(r.warnings, fmt.Sprintf(
			"%s is set but %s is missing or unparseable — the overlap slot is CLOSED. "+
				"An overlap with no closing time is not a rotation, it is a second permanent secret; set an RFC3339 instant "+
				"(e.g. %s)", EnvFabricSecretAlso, EnvFabricSecretAlsoUntil, time.Now().UTC().Add(24*time.Hour).Format(time.RFC3339)))
		return r
	}
	if latest := time.Now().UTC().Add(MaxSecretOverlap); altUntil.After(latest) {
		r.warnings = append(r.warnings, fmt.Sprintf(
			"%s=%s is further out than the %s maximum overlap — clamped to %s",
			EnvFabricSecretAlsoUntil, altUntil.UTC().Format(time.RFC3339), MaxSecretOverlap, latest.Format(time.RFC3339)))
		altUntil = latest
	}
	r.alt = alt
	r.altUntil = altUntil.UTC()
	return r
}

// LoadSecretRingFromEnv builds the ring for a box whose current secret is
// `current` (already read by the caller, so this function never disagrees with
// the value the caller is about to send).
//
// It does NOT log. The caller logs through LogSummary with its own prefix,
// because both fabric and crdtsync build a ring from the same variables and two
// identical unattributed lines are worse than none.
func LoadSecretRingFromEnv(current string) *SecretRing {
	alt := strings.TrimSpace(os.Getenv(EnvFabricSecretAlso))
	if alt == "" {
		return NewSecretRing(current, "", time.Time{})
	}
	raw := strings.TrimSpace(os.Getenv(EnvFabricSecretAlsoUntil))
	var until time.Time
	if raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			// Leave until zero: NewSecretRing turns that into a closed slot with
			// the explanatory warning, so there is one place that decides what an
			// unusable deadline means.
			return NewSecretRing(current, alt, time.Time{})
		}
		until = t
	}
	return NewSecretRing(current, alt, until)
}

// WithClock replaces the ring's clock. It exists so the CLOSING of a window is
// as testable as its opening — the same ring, the same presented secret, two
// clock readings, two answers — without a test sleeping past a real deadline.
// It returns the ring for chaining.
func (r *SecretRing) WithClock(now func() time.Time) *SecretRing {
	if r != nil && now != nil {
		r.now = now
	}
	return r
}

// Current returns the secret this box SENDS. The ALSO slot is never returned
// here and is never transmitted: an overlap widens what we accept, never what we
// disclose, so a rotation cannot leak the new secret to a peer that has not been
// given it out of band.
func (r *SecretRing) Current() string {
	if r == nil {
		return ""
	}
	return r.current
}

// Accepts reports whether a presented X-Fabric-Auth value is one this box will
// admit right now.
//
// The comparison is constant time against both slots. The ALSO slot is consulted
// only while the overlap is open, and "open" is decided against the clock ON
// EVERY CALL — that is what lets the window close on a box nobody restarts.
//
// An empty current secret accepts nothing, including an empty presented value.
//
// It RECORDS which slot admitted the request. That record is what answers "has
// every peer moved?" — see Slot and RotationStatus.
func (r *SecretRing) Accepts(presented string) bool {
	slot := r.Slot(presented)
	if slot == "" {
		return false
	}
	r.mu.Lock()
	if slot == SlotOverlap {
		now := r.now().UTC()
		r.nOverlap++
		r.lastOverlap = now
		if r.firstOverlap.IsZero() {
			r.firstOverlap = now
		}
	} else {
		r.nCurrent++
	}
	r.mu.Unlock()
	return true
}

// Slot reports WHICH slot a presented secret matches — SlotCurrent,
// SlotOverlap, or "" for a value this box does not accept.
//
// It is the pure-inspection half of Accepts: it counts nothing, so the status
// handler can name the caller's own slot without inflating the very counters an
// operator is reading to decide whether the roll is finished.
func (r *SecretRing) Slot(presented string) string {
	if r == nil || r.current == "" {
		return ""
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(r.current)) == 1 {
		return SlotCurrent
	}
	if !r.overlapOpen() {
		return ""
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(r.alt)) == 1 {
		return SlotOverlap
	}
	return ""
}

// overlapOpen reports whether the ALSO slot is live at this instant.
func (r *SecretRing) overlapOpen() bool {
	if r == nil || r.alt == "" || r.altUntil.IsZero() {
		return false
	}
	return r.now().UTC().Before(r.altUntil)
}

// OverlapOpen reports whether a rotation overlap is currently accepted. It is
// the observable half of the window: an operator rolling a fleet checks that
// this is true on every box before phase 2, and false on every box after the
// deadline.
func (r *SecretRing) OverlapOpen() bool { return r.overlapOpen() }

// OverlapClosesAt returns the instant the overlap stops being accepted, or a
// zero time when no overlap is configured. It is reported even after the
// deadline has passed, so "the window closed at 14:02" stays visible.
func (r *SecretRing) OverlapClosesAt() time.Time {
	if r == nil || r.alt == "" {
		return time.Time{}
	}
	return r.altUntil
}

// SecretRotationStatus is the operator-facing view of a rotation in progress.
// It contains no secret and no digest of one — see the file comment.
type SecretRotationStatus struct {
	// OverlapConfigured is true whenever an overlap value is present and usable,
	// whether or not its deadline has passed. It separates "no rotation is
	// happening" from "a rotation is happening and has closed".
	OverlapConfigured bool `json:"overlap_configured"`
	// OverlapOpen is whether the second secret is accepted AT THIS INSTANT.
	OverlapOpen bool `json:"overlap_open"`
	// OverlapClosesAt is when it stops (or stopped) being accepted.
	OverlapClosesAt *time.Time `json:"overlap_closes_at,omitempty"`

	// AdmittedOnCurrent / AdmittedOnOverlap count inbound requests this process
	// admitted on each slot. Ratio, not absolute value, is the signal.
	AdmittedOnCurrent uint64 `json:"admitted_on_current"`
	AdmittedOnOverlap uint64 `json:"admitted_on_overlap"`

	// OverlapFirstUsedAt / OverlapLastUsedAt bracket the period during which
	// some peer was still presenting the overlap value.
	//
	// LastUsedAt is THE number to watch. While it keeps advancing, a box out
	// there has not been rolled yet and closing the window would partition it
	// off. When it stops advancing, the roll is done. It deliberately does not
	// say WHICH box: a shared bearer secret identifies a fleet and not a member,
	// so any per-peer attribution here would be invented rather than measured.
	OverlapFirstUsedAt *time.Time `json:"overlap_first_used_at,omitempty"`
	OverlapLastUsedAt  *time.Time `json:"overlap_last_used_at,omitempty"`

	// Warnings are configuration inputs that were refused or clamped.
	Warnings []string `json:"warnings,omitempty"`
}

// RotationStatus snapshots the ring for the status endpoint.
func (r *SecretRing) RotationStatus() SecretRotationStatus {
	if r == nil {
		return SecretRotationStatus{}
	}
	out := SecretRotationStatus{
		OverlapConfigured: r.alt != "" && !r.altUntil.IsZero(),
		OverlapOpen:       r.overlapOpen(),
		Warnings:          r.Warnings(),
	}
	if out.OverlapConfigured {
		t := r.altUntil
		out.OverlapClosesAt = &t
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out.AdmittedOnCurrent = r.nCurrent
	out.AdmittedOnOverlap = r.nOverlap
	if !r.firstOverlap.IsZero() {
		t := r.firstOverlap
		out.OverlapFirstUsedAt = &t
	}
	if !r.lastOverlap.IsZero() {
		t := r.lastOverlap
		out.OverlapLastUsedAt = &t
	}
	return out
}

// Warnings returns the configuration inputs that were refused or clamped. Empty
// for a healthy ring.
func (r *SecretRing) Warnings() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.warnings...)
}

// LogSummary writes one line describing the ring's rotation state, plus one line
// per warning, under the caller's log prefix (e.g. "[fabric]").
//
// It prints no secret and no digest of one, for the reason in the file comment.
func (r *SecretRing) LogSummary(prefix string) {
	if r == nil {
		return
	}
	for _, w := range r.warnings {
		log.Printf("%s secret rotation: %s", prefix, w)
	}
	switch {
	case r.alt == "" && len(r.warnings) == 0:
		// Silent: no rotation configured is the ordinary state and does not
		// deserve a line on every boot.
	case r.overlapOpen():
		log.Printf("%s secret rotation OVERLAP OPEN: a second %s value is accepted on inbound requests until %s "+
			"(it is never sent). After that instant it is refused with no restart needed.",
			prefix, EnvFabricSecret, r.altUntil.Format(time.RFC3339))
	case r.alt != "":
		log.Printf("%s secret rotation overlap CLOSED (deadline %s has passed) — only the current %s is accepted",
			prefix, r.altUntil.Format(time.RFC3339), EnvFabricSecret)
	}
}
