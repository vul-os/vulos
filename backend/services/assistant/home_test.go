package assistant

import (
	"context"
	"testing"
	"time"

	"vulos/backend/services/ai"
)

// TestHomeAggregatesSections verifies Home returns a brief (from the guarded
// model), a deterministic focus list of unread mail, an agenda, and a recent
// activity feed — all from the fixture source.
func TestHomeAggregatesSections(t *testing.T) {
	m := &fakeModel{reply: "Dana needs your signature by Thursday."}
	a := New(m, localCfg(), NewFixtureSource(), false)

	hd := a.Home(context.Background(), Auth{UserID: "u1"})

	if hd.Brief == "" || hd.BriefError != "" {
		t.Fatalf("expected a brief, got brief=%q err=%q", hd.Brief, hd.BriefError)
	}
	if len(hd.Focus) == 0 {
		t.Fatal("expected focus items from unread fixture mail")
	}
	for _, f := range hd.Focus {
		if !f.Unread {
			t.Errorf("focus item %q should be unread", f.Subject)
		}
		if f.UID == "" {
			t.Error("focus item missing UID (not actionable)")
		}
	}
	if len(hd.Activity) == 0 {
		t.Fatal("expected recent activity items")
	}
	if hd.Activity[0].Kind != "mail" {
		t.Errorf("activity kind = %q, want mail", hd.Activity[0].Kind)
	}
	if len(hd.Agenda) == 0 {
		t.Fatal("expected a demo agenda from the fixture source")
	}
	if hd.Greeting == "" {
		t.Error("expected a greeting")
	}
	if hd.MailSource != "fixture" {
		t.Errorf("mail_source = %q, want fixture", hd.MailSource)
	}
}

// TestHomeDegradesWhenModelBlocked ensures a blocked egress tier fails the brief
// section only — the agenda and activity still render.
func TestHomeDegradesWhenModelBlocked(t *testing.T) {
	m := &fakeModel{reply: "should never be called"}
	// A non-local (Claude) endpoint without the external opt-in trips Guard().
	a := New(m, ai.Config{Provider: ai.ProviderClaude, Model: "claude-x"}, NewFixtureSource(), false)

	hd := a.Home(context.Background(), Auth{UserID: "u1"})

	if hd.BriefError == "" {
		t.Fatal("expected brief_error when egress is blocked")
	}
	if hd.Brief != "" {
		t.Errorf("brief should be empty when blocked, got %q", hd.Brief)
	}
	// The non-model sections must still populate.
	if len(hd.Activity) == 0 {
		t.Error("activity should render even when the brief is blocked")
	}
	if len(hd.Agenda) == 0 {
		t.Error("agenda should render even when the brief is blocked")
	}
}

// Home surfaces the new calendar-awareness sections: an agenda freshness flag
// and the invites-awaiting-RSVP list, each degrading independently.
func TestHomeSurfacesInvitesAndAgendaFreshness(t *testing.T) {
	m := &fakeModel{reply: "You have an invite to reply to."}
	a := New(m, localCfg(), NewFixtureSource(), false)

	hd := a.Home(context.Background(), Auth{UserID: "u1"})

	if !hd.AgendaFresh {
		t.Error("agenda read succeeded — AgendaFresh should be true")
	}
	if hd.InvitesError != "" {
		t.Fatalf("unexpected invites error: %q", hd.InvitesError)
	}
	if len(hd.Invites) != 1 {
		t.Fatalf("expected exactly one pending invite (the kickoff), got %d: %+v", len(hd.Invites), hd.Invites)
	}
	inv := hd.Invites[0]
	if inv.MessageUID != "107" || inv.Invite.Summary != "Pilot expansion kickoff" {
		t.Errorf("wrong pending invite surfaced: %+v", inv)
	}
	if !inv.Invite.AwaitsRSVP() {
		t.Error("surfaced invite should still await RSVP")
	}
}

// AwaitsRSVP: only live REQUEST invites with needs-action/blank partstat are
// pending; CANCEL/REPLY methods and answered invites are not.
func TestInviteAwaitsRSVP(t *testing.T) {
	cases := []struct {
		iv   *MessageInvite
		want bool
	}{
		{nil, false},
		{&MessageInvite{Method: "REQUEST", RSVP: "needs-action"}, true},
		{&MessageInvite{Method: "REQUEST", RSVP: ""}, true},
		{&MessageInvite{Method: "", RSVP: ""}, true}, // unset method treated as request
		{&MessageInvite{Method: "REQUEST", RSVP: "accepted"}, false},
		{&MessageInvite{Method: "REQUEST", RSVP: "declined"}, false},
		{&MessageInvite{Method: "CANCEL", RSVP: "needs-action"}, false},
		{&MessageInvite{Method: "REPLY", RSVP: ""}, false},
	}
	for i, c := range cases {
		if got := c.iv.AwaitsRSVP(); got != c.want {
			t.Errorf("case %d: AwaitsRSVP()=%v want %v (%+v)", i, got, c.want, c.iv)
		}
	}
}

// PendingInvites sorts by soonest event start and excludes answered invites.
func TestPendingInvitesSortedAndFiltered(t *testing.T) {
	a := New(&fakeModel{}, localCfg(), NewFixtureSource(), false)
	invs, err := a.PendingInvites(context.Background(), Auth{}, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 1 {
		t.Fatalf("expected 1 pending invite, got %d", len(invs))
	}
	if invs[0].MessageUID != "107" {
		t.Errorf("expected kickoff invite (107), got %s", invs[0].MessageUID)
	}
}

// TestFixtureListEventsWindow checks the demo agenda is anchored to today and
// respects the requested window.
func TestFixtureListEventsWindow(t *testing.T) {
	f := NewFixtureSource()
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	to := from.AddDate(0, 0, 7)
	evs, err := f.ListEvents(context.Background(), Auth{}, from.Format(time.RFC3339), to.Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		t.Fatal("expected demo events in the 7-day window")
	}
	// A tiny window in the far past should return nothing.
	past := from.AddDate(-1, 0, 0)
	none, _ := f.ListEvents(context.Background(), Auth{}, past.Format(time.RFC3339), past.Add(time.Hour).Format(time.RFC3339))
	if len(none) != 0 {
		t.Errorf("expected no events in a past window, got %d", len(none))
	}
}
