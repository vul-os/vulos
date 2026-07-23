package support_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/support"
)

// ─── ClassifyTier ─────────────────────────────────────────────────────────────

func TestClassifyTier(t *testing.T) {
	cases := []struct {
		tier string
		want support.TierClass
	}{
		{"free", support.TierBase},
		{"", support.TierBase},
		{"unknown", support.TierBase},
		{"pro", support.TierPro},
		{"team", support.TierPro},
		{"enterprise", support.TierEnterprise},
	}
	for _, c := range cases {
		got := support.ClassifyTier(c.tier)
		if got != c.want {
			t.Errorf("ClassifyTier(%q) = %v; want %v", c.tier, got, c.want)
		}
	}
}

// ─── TicketChannelFor ─────────────────────────────────────────────────────────

func TestTicketChannelFor_Base(t *testing.T) {
	for _, tier := range []string{"free", "", "unknown"} {
		_, err := support.TicketChannelFor(tier)
		if !errors.Is(err, support.ErrNoTicketChannel) {
			t.Errorf("TicketChannelFor(%q): want ErrNoTicketChannel, got %v", tier, err)
		}
	}
}

func TestTicketChannelFor_Pro(t *testing.T) {
	for _, tier := range []string{"pro", "team"} {
		ch, err := support.TicketChannelFor(tier)
		if err != nil {
			t.Fatalf("TicketChannelFor(%q): unexpected error: %v", tier, err)
		}
		if ch != support.ChannelEmail {
			t.Errorf("TicketChannelFor(%q) = %q; want %q", tier, ch, support.ChannelEmail)
		}
	}
}

func TestTicketChannelFor_Enterprise(t *testing.T) {
	ch, err := support.TicketChannelFor("enterprise")
	if err != nil {
		t.Fatalf("TicketChannelFor(enterprise): %v", err)
	}
	if ch != support.ChannelDedicatedSlack {
		t.Errorf("got %q; want %q", ch, support.ChannelDedicatedSlack)
	}
}

// ─── SLASeconds ───────────────────────────────────────────────────────────────

func TestSLASeconds(t *testing.T) {
	// Enterprise P1 → 1 clock-hour
	if s := support.SLASeconds("enterprise", "P1"); s != 3600 {
		t.Errorf("enterprise P1 SLA = %d; want 3600", s)
	}
	// Enterprise P2 → 8 business hours
	if s := support.SLASeconds("enterprise", "P2"); s != 8*3600 {
		t.Errorf("enterprise P2 SLA = %d; want %d", s, 8*3600)
	}
	// Pro P1 → 8 business hours (not 1 h)
	if s := support.SLASeconds("pro", "P1"); s != 8*3600 {
		t.Errorf("pro P1 SLA = %d; want %d", s, 8*3600)
	}
}

// ─── BusinessDeadline ─────────────────────────────────────────────────────────

func TestBusinessDeadline_EnterpriseP1(t *testing.T) {
	// Enterprise P1 should be exactly 1 hour later.
	openedAt := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	want := openedAt.Add(time.Hour)
	got := support.BusinessDeadline(openedAt, "enterprise", "P1")
	if !got.Equal(want) {
		t.Errorf("BusinessDeadline(enterprise,P1) = %v; want %v", got, want)
	}
}

func TestBusinessDeadline_ProP3SkipsWeekend(t *testing.T) {
	// Opened Friday at 15:00 UTC — 8 business hours crosses the weekend.
	openedAt := time.Date(2026, 5, 22, 15, 0, 0, 0, time.UTC) // Friday
	got := support.BusinessDeadline(openedAt, "pro", "P3")
	// 15:00 → 17:00 = 2 bh Friday; 6 bh remain = Mon 09:00→15:00.
	wantDay := time.Monday
	if got.Weekday() != wantDay {
		t.Errorf("deadline should be Monday; got %v (%v)", got.Weekday(), got)
	}
	if got.Before(openedAt) {
		t.Errorf("deadline %v is before openedAt %v", got, openedAt)
	}
}

// ─── MemStore integration ─────────────────────────────────────────────────────

func newMem() support.Store { return support.NewMemStore() }

func TestMemStore_OpenTicket_BaseWall(t *testing.T) {
	s := newMem()
	_, err := s.OpenTicket(context.Background(), "acct1", "free", "P2", "help", "body")
	if !errors.Is(err, support.ErrNoTicketChannel) {
		t.Fatalf("expected ErrNoTicketChannel; got %v", err)
	}
}

func TestMemStore_OpenTicket_Pro(t *testing.T) {
	s := newMem()
	tk, err := s.OpenTicket(context.Background(), "acct1", "pro", "P2", "subject", "body text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tk.Channel != support.ChannelEmail {
		t.Errorf("channel = %q; want %q", tk.Channel, support.ChannelEmail)
	}
	if tk.State != "open" {
		t.Errorf("state = %q; want open", tk.State)
	}
	if tk.BreachAt.IsZero() {
		t.Error("breach_at is zero")
	}
}

func TestMemStore_OpenTicket_Enterprise(t *testing.T) {
	s := newMem()
	tk, err := s.OpenTicket(context.Background(), "acct1", "enterprise", "P1", "prod down", "all services unreachable")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tk.Channel != support.ChannelDedicatedSlack {
		t.Errorf("channel = %q; want %q", tk.Channel, support.ChannelDedicatedSlack)
	}
	// P1 Enterprise breach_at should be ~1 hour from now.
	delta := tk.BreachAt.Sub(tk.OpenedAt)
	if delta < 59*time.Minute || delta > 61*time.Minute {
		t.Errorf("P1 breach delta = %v; expected ~1h", delta)
	}
}

func TestMemStore_CloseTicket(t *testing.T) {
	s := newMem()
	tk, _ := s.OpenTicket(context.Background(), "acct1", "pro", "P3", "sub", "body")

	// Wrong owner → forbidden.
	if err := s.CloseTicket(context.Background(), tk.ID, "other"); !errors.Is(err, support.ErrForbidden) {
		t.Fatalf("expected ErrForbidden; got %v", err)
	}

	// Correct owner → closed.
	if err := s.CloseTicket(context.Background(), tk.ID, "acct1"); err != nil {
		t.Fatalf("unexpected error closing: %v", err)
	}

	// Double-close → ErrAlreadyClosed.
	if err := s.CloseTicket(context.Background(), tk.ID, "acct1"); !errors.Is(err, support.ErrAlreadyClosed) {
		t.Fatalf("expected ErrAlreadyClosed; got %v", err)
	}
}

func TestMemStore_ListTickets(t *testing.T) {
	s := newMem()
	s.OpenTicket(context.Background(), "acct1", "pro", "P2", "a", "")
	s.OpenTicket(context.Background(), "acct1", "pro", "P3", "b", "")
	s.OpenTicket(context.Background(), "acct2", "enterprise", "P1", "c", "")

	list, err := s.ListTickets(context.Background(), "acct1")
	if err != nil {
		t.Fatalf("list tickets: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("acct1 tickets = %d; want 2", len(list))
	}
	for _, tk := range list {
		if tk.AccountID != "acct1" {
			t.Errorf("leaked ticket from other account: %+v", tk)
		}
	}
}

func TestMemStore_MarkBreaches(t *testing.T) {
	s := support.NewMemStore()
	// Manually insert a ticket with an already-past breach_at via the store
	// by opening a ticket and then advancing the clock via MemStore's public API.
	// Since breach_at is set from BusinessDeadline, open with enterprise P1 and
	// verify MarkBreaches doesn't flip it yet.
	tk, _ := s.OpenTicket(context.Background(), "acct1", "pro", "P3", "sub", "body")
	// Should not be breached yet.
	n, err := s.MarkBreaches(context.Background())
	if err != nil {
		t.Fatalf("mark breaches: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 breaches; got %d", n)
	}

	// GetTicket reflects current state.
	fresh, _ := s.GetTicket(context.Background(), tk.ID)
	if fresh.Breached {
		t.Error("ticket should not be breached yet")
	}
}

func TestMemStore_GetTicket_NotFound(t *testing.T) {
	s := newMem()
	_, err := s.GetTicket(context.Background(), 9999)
	if !errors.Is(err, support.ErrTicketNotFound) {
		t.Errorf("expected ErrTicketNotFound; got %v", err)
	}
}

// ─── SQLStore integration ─────────────────────────────────────────────────────

func openTestSQLStore(t *testing.T) *support.SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := support.OpenSQLStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("support.OpenSQLStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSQLStore_OpenCloseList(t *testing.T) {
	st := openTestSQLStore(t)

	ctx := context.Background()

	// Base tier wall.
	_, err := st.OpenTicket(ctx, "a1", "free", "P1", "help", "")
	if !errors.Is(err, support.ErrNoTicketChannel) {
		t.Fatalf("expected ErrNoTicketChannel; got %v", err)
	}

	// Pro ticket.
	tk, err := st.OpenTicket(ctx, "a1", "pro", "P2", "SQL subject", "SQL body")
	if err != nil {
		t.Fatalf("open ticket: %v", err)
	}
	if tk.Channel != support.ChannelEmail {
		t.Errorf("channel = %q; want email", tk.Channel)
	}

	// Retrieve.
	got, err := st.GetTicket(ctx, tk.ID)
	if err != nil {
		t.Fatalf("get ticket: %v", err)
	}
	if got.Subject != "SQL subject" {
		t.Errorf("subject = %q; want %q", got.Subject, "SQL subject")
	}

	// List.
	list, err := st.ListTickets(ctx, "a1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("len = %d; want 1", len(list))
	}

	// Close.
	if err := st.CloseTicket(ctx, tk.ID, "a1"); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Double close.
	if err := st.CloseTicket(ctx, tk.ID, "a1"); !errors.Is(err, support.ErrAlreadyClosed) {
		t.Fatalf("expected ErrAlreadyClosed; got %v", err)
	}
}

func TestSQLStore_MarkBreaches_Noop(t *testing.T) {
	st := openTestSQLStore(t)
	ctx := context.Background()
	st.OpenTicket(ctx, "a1", "pro", "P3", "sub", "")
	n, err := st.MarkBreaches(ctx)
	if err != nil {
		t.Fatalf("mark breaches: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 breaches; got %d", n)
	}
}

func TestErrNoTicketChannelMessage(t *testing.T) {
	if !strings.Contains(support.ErrNoTicketChannel.Error(), "Base tier") {
		t.Errorf("error message missing 'Base tier': %q", support.ErrNoTicketChannel.Error())
	}
}
