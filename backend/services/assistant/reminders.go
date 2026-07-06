package assistant

// reminders.go — the sovereign REMINDERS capability for the assistant (Wave 62).
//
// This wires the durable per-user reminders store (reminders_store.go) into the
// tool-using agent as THREE curated tools, following the wave-48 contacts /
// wave-55 files pattern (small, curated surface; a read-only lookup + explicitly
// gated mutations):
//
//   - list_reminders   → READ-ONLY. "what are my reminders" — lists the user's
//                        OWN pending reminders. Runs freely in the loop.
//   - set_reminder     → LEDGER-GATED MUTATION. "remind me to call the dentist
//                        tomorrow at 3pm" — the model PROPOSES it; nothing is
//                        created until the user approves (id-only /execute),
//                        exactly like send_email. A reminder whose TEXT came from
//                        untrusted content (an email the model read) is flagged
//                        from_content so the UI can warn.
//   - cancel_reminder  → LEDGER-GATED MUTATION. Cancels a reminder BY ID (from
//                        list_reminders). Also proposed + approved, never auto-run.
//
// SECURITY INVARIANTS (all preserved from earlier waves):
//   - The mutating tools NEVER auto-fire: AgentTurn returns a Proposal for any
//     [confirm] tool and stops; only ExecuteProposal (reached via id-only
//     /execute after approval) performs the store write. Mirrors the wave-13
//     send contract (TestAgentSetReminderIsLedgerGated).
//   - Per-user isolation lives in the STORE: every list/cancel is scoped by
//     userID, so a user cannot see or cancel another's reminders. The tools pass
//     the request's own auth.UserID and nothing else.
//   - remind_at is BOUNDED (future + sane horizon) in the store; a past/absurd
//     time is rejected at set time. Reminder text is bounded and rendered escaped
//     by the shell (never innerHTML).
//   - The egress Guard still gates every model call up-front in AgentTurn: a
//     blocked tier makes zero model calls and therefore performs zero reminder
//     ops (the tool loop never runs). Creating/cancelling a reminder is a LOCAL
//     on-box write (no model call, no egress), so — like the mail executor — it
//     is not itself Guarded; Guard protects the model, and the model is what
//     decides tool calls.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// maxListReminders caps how many reminders list_reminders returns to the model.
const maxListReminders = 50

// RemindersSource is the seam the assistant's reminder tools use. It is the
// per-user, durable surface over reminders — a read (list) plus the two
// mutations (set/cancel) that the agent only ever reaches through the ledger.
// *RemindersStore satisfies it; nil ⇒ the reminder tools degrade to "reminders
// unavailable" rather than crashing (mirrors the files/mail seams).
type RemindersSource interface {
	// ListReminders returns the user's OWN reminders (pending unless includeDone).
	ListReminders(userID string, includeDone bool, limit int) ([]Reminder, error)
	// SetReminder durably creates a reminder (validated + bounded). Reached only
	// after the user approves the set_reminder proposal.
	SetReminder(userID, text string, remindAt time.Time) (Reminder, error)
	// CancelReminder marks the user's OWN reminder done by id. Reached only after
	// the user approves the cancel_reminder proposal.
	CancelReminder(userID, id string) (Reminder, error)
}

// WithReminders attaches the durable reminders seam so the assistant's
// list_reminders / set_reminder / cancel_reminder tools work. Returns the same
// assistant for chaining. nil leaves the reminder tools unavailable.
func (a *Assistant) WithReminders(r RemindersSource) *Assistant {
	a.reminders = r
	return a
}

// RemindersEnabled reports whether the reminders seam is attached (status/telemetry).
func (a *Assistant) RemindersEnabled() bool { return a.reminders != nil }

// ListReminders is the READ path used by both the list_reminders tool and the
// Home surface: the user's OWN pending reminders, soonest first. Pure on-box
// read — no model call, no egress — so it does NOT go through Guard (Guard is the
// model-egress choke point).
func (a *Assistant) ListReminders(ctx context.Context, auth Auth, includeDone bool, limit int) ([]Reminder, error) {
	if a.reminders == nil {
		return nil, nil // feature not wired ⇒ empty, never an error
	}
	if limit <= 0 || limit > maxListReminders {
		limit = maxListReminders
	}
	return a.reminders.ListReminders(strings.TrimSpace(auth.UserID), includeDone, limit)
}

// formatReminders renders a user's reminders as a compact text list for the
// model. Reminder text is the user's own words; it is still fed back through
// wrapUntrusted like every tool result (defense in depth — a reminder created
// from an email's text is data, never an instruction). The id is included so the
// model can propose a cancel_reminder against a specific one.
func formatReminders(rs []Reminder) string {
	if len(rs) == 0 {
		return "(no reminders set)"
	}
	var b strings.Builder
	for _, r := range rs {
		status := ""
		if r.Done {
			status = " | done"
		}
		fmt.Fprintf(&b, "- id=%s | at %s | %s%s\n",
			r.ID, r.RemindAt.Format("2006-01-02 15:04 MST"), truncate(collapseWS(r.Text), 160), status)
	}
	return b.String()
}

// parseRemindAt interprets the model/user-supplied reminder time. It accepts
// RFC3339 / a couple of common ISO-ish spellings; a bare local datetime is read
// as the instance's local time (the assistant runs ON the user's box, so "3pm
// tomorrow" — which the model resolves to a concrete datetime — is local). It is
// deliberately strict: an unparseable value errors rather than silently defaulting
// to "now", so a garbage time can never create a fires-immediately reminder.
func parseRemindAt(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("a reminder time is required (e.g. an ISO-8601 timestamp)")
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04",
	}
	for i, layout := range layouts {
		// The zone-less layouts (index > 0) are parsed in the instance's local zone.
		if i == 0 {
			if t, err := time.Parse(layout, s); err == nil {
				return t, nil
			}
			continue
		}
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("could not understand the reminder time %q — use an ISO-8601 datetime", s)
}

// newReminderID mints an opaque, unguessable reminder id (shared by the store).
func newReminderID() string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return "rem_" + hex.EncodeToString(b[:])
}
