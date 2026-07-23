package orgadmin

// seats.go — org member roster snapshot for GET /api/org/seats.
//
// FLAT-BILLING-01 (2026-07): Vulos billing is per-ACCOUNT flat tiers, not
// per-seat. A subscription's price does NOT depend on member count — every
// paid tier (Personal/Pro/Team) is unlimited-member for a flat monthly price
// (see billingmodel/TIERS.md). This endpoint therefore reports roster COUNTS
// only (total members, active members within the active window) for the org
// admin dashboard — it carries no price and multiplies no cost. The prior
// per-seat PRICE axis (SEAT-BILLING-01: billable seats × per-active-user rate)
// has been removed; billing.PerSeatZARCentsForTier and the SeatPricer seam are
// gone.
//
// MAIL IS NOT SEAT-BILLED (unchanged): users connect their own (external or
// Vulos) mailboxes per-user; a mail connection is priced separately on the
// mail tiers in internal/billing/activeuser and is never an org member, so it
// never enters this roster count.

import (
	"context"
	"time"
)

// seatActiveWindowDays is the look-back window that defines an ACTIVE org
// member. Matches the 30-day active-user window the rest of the billing model
// uses (internal/billing/activeuser).
const seatActiveWindowDays = 30

// SeatsResponse is the GET /api/org/seats body. It reports the org's member
// roster snapshot (active vs total members) — informational only. There is no
// per-seat price: every paid tier is a flat monthly price regardless of member
// count. MailSeatBilled is always false: mail is per-user external and never a
// seat cost (kept so the dashboard can state the exclusion).
type SeatsResponse struct {
	Tier          string `json:"tier"`
	BillableSeats int    `json:"billable_seats"`
	ActiveMembers int    `json:"active_members"`
	TotalMembers  int    `json:"total_members"`
	WindowDays    int    `json:"window_days"`
	// MailSeatBilled is ALWAYS false: connecting a mailbox is not an org seat
	// and adds no seat cost. Surfaced so the UI can show the exclusion.
	MailSeatBilled bool `json:"mail_seat_billed"`
}

// countActiveSeats reports the active-member count and the total roster size
// for the given members at now. A member is ACTIVE when its LastActive
// timestamp parses and is not older than windowDays. Pure function — no clock,
// no I/O — so it is the behavioural source of truth the tests pin.
func countActiveSeats(members []Member, now time.Time, windowDays int) (billable, active, total int) {
	if windowDays <= 0 {
		windowDays = seatActiveWindowDays
	}
	cutoff := now.AddDate(0, 0, -windowDays)
	total = len(members)
	for _, m := range members {
		if memberActiveSince(m.LastActive, cutoff) {
			active++
		}
	}
	billable = active
	return billable, active, total
}

// memberActiveSince reports whether an RFC3339 LastActive stamp is at or after
// cutoff. An empty or unparseable stamp is treated as inactive (fail closed:
// an unknown member does not count as active).
func memberActiveSince(lastActive string, cutoff time.Time) bool {
	if lastActive == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, lastActive)
	if err != nil {
		return false
	}
	return !t.Before(cutoff)
}

// Seats returns the caller-org's member roster snapshot (active vs total
// members) — informational only, no per-seat price. Mail connections are not
// org members and so never count. The Tier field is read (best-effort) from
// the same BillingSummarizer the billing-summary endpoint uses, purely for
// display — it never affects a cost calculation here.
func (s *Service) Seats(ctx context.Context, tenantID string) (SeatsResponse, error) {
	var members []Member
	if s.Members != nil {
		m, err := s.Members.ListMembers(ctx, tenantID)
		if err != nil {
			return SeatsResponse{}, err
		}
		members = m
	}
	billable, active, total := countActiveSeats(members, nowUTC(), seatActiveWindowDays)
	resp := SeatsResponse{
		Tier:           "free",
		BillableSeats:  billable,
		ActiveMembers:  active,
		TotalMembers:   total,
		WindowDays:     seatActiveWindowDays,
		MailSeatBilled: false, // mail is per-user external — never a seat cost.
	}
	if s.Billing != nil {
		if summary, err := s.Billing.Summary(ctx, tenantID); err == nil && summary.Subscription.Tier != "" {
			resp.Tier = summary.Subscription.Tier
		}
	}
	return resp, nil
}
