package support

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestClassifyTier(t *testing.T) {
	cases := map[string]TierClass{
		"":      TierFree,
		"free":  TierFree,
		"bogus": TierFree,
		"pro":   TierPro,
		"team":  TierTeam,
	}
	for tier, want := range cases {
		if got := ClassifyTier(tier); got != want {
			t.Errorf("ClassifyTier(%q) = %v, want %v", tier, got, want)
		}
	}
}

func TestTicketChannelFor(t *testing.T) {
	if _, err := TicketChannelFor("free"); !errors.Is(err, ErrNoTicketChannel) {
		t.Errorf("free tier: want ErrNoTicketChannel, got %v", err)
	}
	if ch, err := TicketChannelFor("pro"); err != nil || ch != ChannelEmail {
		t.Errorf("pro tier: got (%q, %v), want (%q, nil)", ch, err, ChannelEmail)
	}
	if ch, err := TicketChannelFor("team"); err != nil || ch != ChannelPriority {
		t.Errorf("team tier: got (%q, %v), want (%q, nil)", ch, err, ChannelPriority)
	}
}

func TestBusinessDeadline_TeamP1IsOneClockHour(t *testing.T) {
	opened := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC) // Wednesday, within business hours
	got := BusinessDeadline(opened, "team", PriorityP1)
	want := opened.Add(time.Hour)
	if !got.Equal(want) {
		t.Errorf("team P1 deadline = %v, want %v", got, want)
	}
}

func TestBusinessDeadline_ProSkipsWeekend(t *testing.T) {
	// Friday 15:00 UTC + 8 business hours should land Monday morning, not
	// inside the weekend.
	opened := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC) // Friday
	got := BusinessDeadline(opened, "pro", PriorityP3)
	if got.Weekday() == time.Saturday || got.Weekday() == time.Sunday {
		t.Fatalf("deadline fell on a weekend: %v", got)
	}
	if !got.After(opened) {
		t.Fatalf("deadline %v is not after opened %v", got, opened)
	}
}

func TestStore_SubmitListCloseLifecycle(t *testing.T) {
	store, err := OpenStore("")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	req, err := store.Submit(ctx, "owner-1", "pro", "P2", "Sync stuck", "Files stopped syncing.")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if req.Channel != ChannelEmail {
		t.Errorf("channel = %q, want %q", req.Channel, ChannelEmail)
	}
	if req.State != "open" {
		t.Errorf("state = %q, want open", req.State)
	}

	list, err := store.List(ctx, "owner-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != req.ID {
		t.Fatalf("List = %+v, want 1 entry with ID %d", list, req.ID)
	}

	// Wrong owner cannot close it.
	if err := store.CloseRequest(ctx, req.ID, "someone-else"); !errors.Is(err, ErrForbidden) {
		t.Errorf("close by non-owner: got %v, want ErrForbidden", err)
	}

	if err := store.CloseRequest(ctx, req.ID, "owner-1"); err != nil {
		t.Fatalf("CloseRequest: %v", err)
	}
	if err := store.CloseRequest(ctx, req.ID, "owner-1"); !errors.Is(err, ErrAlreadyClosed) {
		t.Errorf("double close: got %v, want ErrAlreadyClosed", err)
	}

	got, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != "closed" || got.ResolvedAt.IsZero() {
		t.Errorf("after close: state=%q resolvedAt=%v", got.State, got.ResolvedAt)
	}
}

func TestStore_FreeTierWall(t *testing.T) {
	store, err := OpenStore("")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.Submit(context.Background(), "owner-1", "free", "", "Help", "Body"); !errors.Is(err, ErrNoTicketChannel) {
		t.Errorf("free tier submit: got %v, want ErrNoTicketChannel", err)
	}
}

func TestStore_GetNotFound(t *testing.T) {
	store, err := OpenStore("")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.Get(context.Background(), 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing: got %v, want ErrNotFound", err)
	}
}
