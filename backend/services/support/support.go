// Package support implements the box owner's Help/Support surface: submit a
// support request from Settings and track its tier, channel, and SLA target.
//
// This is a native, single-owner-scope port of the former vulos-management
// SUPPORT-03 three-tier support model (see fold notes). It keeps the tier
// classification and SLA-deadline math, adapted to Vulos's actual plan names
// (free / pro / team — see src/core/settings/PlanBillingPanel.jsx's TIERS)
// and to what a single sovereign box can honestly offer:
//
//   - Free  — no ticket channel. The panel points the owner at docs/community.
//     This is a deliberate hard wall, not a bug: a free/self-hosted box has
//     no live support desk behind it.
//   - Pro   — "email" channel, 1-business-day SLA target.
//   - Team  — "priority" channel, 1-hour SLA target for P1 requests (falls
//     back to the same business-day target for P2/P3).
//
// IMPORTANT HONESTY NOTE: this package only classifies and locally records a
// request with a channel + SLA target. It does NOT itself deliver the request
// anywhere (no outbound email/Slack integration is wired here — see the fold
// manifest's NOTES). Wiring real delivery to a live support desk is a future
// control-plane concern, not something a single Go package on the box can
// promise on its own.
//
// Pure-Go; no CGO. Uses modernc.org/sqlite via database/sql (see store.go).
package support

import (
	"errors"
	"time"
)

// ─── Errors ──────────────────────────────────────────────────────────────────

var (
	// ErrNoTicketChannel is returned when the caller's tier does not include a
	// ticket channel (Free tier). The client should direct the owner to
	// documentation/community channels instead.
	ErrNoTicketChannel = errors.New("support: no ticket channel on the Free tier; upgrade to Pro for email support")

	// ErrNotFound is returned when a request ID does not exist.
	ErrNotFound = errors.New("support: request not found")

	// ErrAlreadyClosed is returned when CloseRequest is called on a closed request.
	ErrAlreadyClosed = errors.New("support: request is already closed")

	// ErrForbidden is returned when a caller does not own the request.
	ErrForbidden = errors.New("support: not the request owner")
)

// ─── Constants ───────────────────────────────────────────────────────────────

// Priority levels accepted by Submit.
const (
	PriorityP1 = "P1" // highest — 1-hour SLA on Team
	PriorityP2 = "P2"
	PriorityP3 = "P3"
)

// Channel values stored on the request row.
const (
	ChannelEmail    = "email"    // Pro
	ChannelPriority = "priority" // Team
)

// businessHoursSLASec is the SLA budget for Pro requests (8 business hours ≈
// 1 business day).
const businessHoursSLASec = 8 * 60 * 60

// p1HourSLASec is the SLA budget for Team P1 requests (1 clock-hour).
const p1HourSLASec = 60 * 60

// ─── Tier classification ──────────────────────────────────────────────────────

// TierClass classifies a plan tier string into Free / Pro / Team, mirroring
// window.__VULOS_TIER / PlanBillingPanel's plan keys.
type TierClass int

const (
	TierFree TierClass = iota
	TierPro
	TierTeam
)

// ClassifyTier maps a plan tier string to a TierClass. Anything unrecognised
// (including the empty string — e.g. a standalone box with no control plane
// wired) classifies as Free.
func ClassifyTier(tier string) TierClass {
	switch tier {
	case "pro":
		return TierPro
	case "team":
		return TierTeam
	default:
		return TierFree
	}
}

// TicketChannelFor returns the support channel for tier, or ErrNoTicketChannel
// if the tier does not include one (the Free wall).
func TicketChannelFor(tier string) (string, error) {
	switch ClassifyTier(tier) {
	case TierTeam:
		return ChannelPriority, nil
	case TierPro:
		return ChannelEmail, nil
	default:
		return "", ErrNoTicketChannel
	}
}

// SLASeconds returns the SLA deadline budget in seconds for a (tier, priority)
// pair. Team P1 → 3600s (1 clock-hour). All others (that have a channel at
// all) → 8 business hours.
func SLASeconds(tier, priority string) int64 {
	if ClassifyTier(tier) == TierTeam && priority == PriorityP1 {
		return p1HourSLASec
	}
	return businessHoursSLASec
}

// BusinessDeadline computes the wall-clock time by which a support request
// should be resolved to meet its SLA target. For Team P1 requests the budget
// is 1 clock-hour; for all others it is 8 business hours (Mon-Fri, 09:00-17:00
// UTC; weekends are skipped). The result is always in UTC.
func BusinessDeadline(openedAt time.Time, tier, priority string) time.Time {
	openedAt = openedAt.UTC()
	if ClassifyTier(tier) == TierTeam && priority == PriorityP1 {
		return openedAt.Add(time.Hour)
	}
	return addBusinessHours(openedAt, 8)
}

// addBusinessHours advances t by n business hours (Mon-Fri, 09:00-17:00 UTC).
func addBusinessHours(t time.Time, hours int) time.Time {
	remaining := hours
	for remaining > 0 {
		t = t.Add(time.Hour)
		if isBusinessHour(t) {
			remaining--
		}
	}
	return t
}

// isBusinessHour reports whether t falls within a business hour (Mon-Fri,
// 09:00-17:00 UTC).
func isBusinessHour(t time.Time) bool {
	t = t.UTC()
	wd := t.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	h := t.Hour()
	return h >= 9 && h < 17
}

// ─── Domain types ─────────────────────────────────────────────────────────────

// Request is a single support request submitted from Settings → Help & Support.
type Request struct {
	ID         int64     `json:"id"`
	AccountID  string    `json:"account_id"`
	Tier       string    `json:"tier"`
	Priority   string    `json:"priority"`
	Channel    string    `json:"channel"`
	Subject    string    `json:"subject"`
	Body       string    `json:"body"`
	State      string    `json:"state"` // "open" | "closed"
	BreachAt   time.Time `json:"breach_at"`
	Breached   bool      `json:"breached"` // computed: state=="open" && now > breach_at
	OpenedAt   time.Time `json:"opened_at"`
	ResolvedAt time.Time `json:"resolved_at,omitempty"`
}
