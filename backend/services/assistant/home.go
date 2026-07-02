package assistant

// home.go — the Home surface aggregator (Wave 11).
//
// Home is the OS's default landing surface: a proactive, human brief of what
// needs the user today, their agenda, and a light recent-activity feed. This
// aggregates the assistant's existing skills + the local /v1 mail & calendar
// reads into ONE payload the shell renders, so Home is fast (one round-trip) and
// degrades cleanly — every section carries its own error and the others still
// render when one fails.
//
// SOVEREIGNTY: the only model call here is Attention() (the brief), which goes
// through Guard() like every other skill. The agenda/activity are pure on-box
// /v1 reads. No new egress is introduced.

import (
	"context"
	"sort"
	"strings"
	"time"
)

// HomeFocusItem is one message the user likely needs to act on (unread inbox
// mail), surfaced deterministically from the mailbox so each item is ACTIONABLE
// (open / reply-via-assistant / snooze) — distinct from the model's prose brief.
type HomeFocusItem struct {
	UID      string    `json:"uid"`
	From     string    `json:"from"`
	FromName string    `json:"from_name,omitempty"`
	Subject  string    `json:"subject"`
	Preview  string    `json:"preview,omitempty"`
	Date     time.Time `json:"date"`
	Unread   bool      `json:"unread"`
	Folder   string    `json:"folder,omitempty"`
}

// HomeActivityItem is one entry in the light cross-surface recent-activity feed.
// Today it is mail-only (the real surface we have); the shape is intentionally
// product-tagged so other products can extend it later without a schema change.
type HomeActivityItem struct {
	Kind    string    `json:"kind"` // "mail" for now
	UID     string    `json:"uid,omitempty"`
	Title   string    `json:"title"`   // subject
	Subtitle string   `json:"subtitle"` // sender
	Preview string    `json:"preview,omitempty"`
	Date    time.Time `json:"date"`
	Unread  bool      `json:"unread,omitempty"`
}

// HomeData is the whole Home payload. Per-section *Error fields let the shell
// render a partial Home when the model or a /v1 read is unavailable.
type HomeData struct {
	Now         time.Time          `json:"now"`
	Greeting    string             `json:"greeting"`
	Brief       string             `json:"brief,omitempty"`
	BriefError  string             `json:"brief_error,omitempty"`
	Focus       []HomeFocusItem    `json:"focus"`
	Agenda      []CalendarEvent    `json:"agenda"`
	AgendaError string             `json:"agenda_error,omitempty"`
	Activity    []HomeActivityItem `json:"activity"`
	MailError   string             `json:"mail_error,omitempty"`
	MailSource  string             `json:"mail_source"`
	Sovereignty Sovereignty        `json:"sovereignty"`
}

const (
	homeFocusMax    = 5
	homeActivityMax = 8
)

// Home builds the Home payload. It never returns an error: each section fails
// independently into its own *Error field so the surface always renders.
func (a *Assistant) Home(ctx context.Context, auth Auth) HomeData {
	now := time.Now()
	hd := HomeData{
		Now:         now,
		Greeting:    greeting(now),
		MailSource:  a.mail.Name(),
		Sovereignty: a.Sovereignty(),
		Focus:       []HomeFocusItem{},
		Agenda:      []CalendarEvent{},
		Activity:    []HomeActivityItem{},
	}

	// --- Recent mail → focus items (unread) + activity feed --------------------
	recent, err := a.mail.Recent(ctx, auth, defaultFolder, 30)
	if err != nil {
		hd.MailError = err.Error()
	} else {
		sort.SliceStable(recent, func(i, j int) bool { return recent[i].Date.After(recent[j].Date) })
		for _, m := range recent {
			if len(hd.Activity) < homeActivityMax {
				hd.Activity = append(hd.Activity, HomeActivityItem{
					Kind:     "mail",
					UID:      m.UID,
					Title:    strEmpty(m.Subject, "(no subject)"),
					Subtitle: m.Sender(),
					Preview:  truncate(collapseWS(strEmpty(m.Preview, m.Body)), 140),
					Date:     m.Date,
					Unread:   m.Unread,
				})
			}
			if m.Unread && len(hd.Focus) < homeFocusMax {
				hd.Focus = append(hd.Focus, HomeFocusItem{
					UID:      m.UID,
					From:     m.From,
					FromName: m.FromName,
					Subject:  strEmpty(m.Subject, "(no subject)"),
					Preview:  truncate(collapseWS(strEmpty(m.Preview, m.Body)), 160),
					Date:     m.Date,
					Unread:   m.Unread,
					Folder:   strEmpty(m.Folder, defaultFolder),
				})
			}
		}
	}

	// --- Agenda: today → +7 days ----------------------------------------------
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to := from.AddDate(0, 0, 7)
	if events, eerr := a.mail.ListEvents(ctx, auth, from.Format(time.RFC3339), to.Format(time.RFC3339)); eerr != nil {
		hd.AgendaError = eerr.Error()
	} else {
		hd.Agenda = events
	}

	// --- Brief: the model's human "what needs you today" (guarded) -------------
	if brief, berr := a.Attention(ctx, auth); berr != nil {
		hd.BriefError = berr.Error()
	} else {
		hd.Brief = strings.TrimSpace(brief)
	}

	return hd
}

// greeting returns a time-of-day greeting ("Good morning" ...).
func greeting(t time.Time) string {
	switch h := t.Hour(); {
	case h < 5:
		return "Good evening"
	case h < 12:
		return "Good morning"
	case h < 18:
		return "Good afternoon"
	default:
		return "Good evening"
	}
}
