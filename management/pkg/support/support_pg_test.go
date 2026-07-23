package support_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/support"
)

func openPGTestStore(t *testing.T) *support.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	db, err := cpdb.Open("support_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := support.OpenSQLStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("support.OpenSQLStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS support_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_OpenTicket(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	// Base tier must be rejected.
	_, err := st.OpenTicket(ctx, "pg-a1", "free", "P1", "help", "")
	if !errors.Is(err, support.ErrNoTicketChannel) {
		t.Fatalf("expected ErrNoTicketChannel for free tier; got %v", err)
	}

	// Pro ticket.
	tk, err := st.OpenTicket(ctx, "pg-a1", "pro", "P2", "PG subject", "PG body")
	if err != nil {
		t.Fatalf("open ticket: %v", err)
	}
	if tk.ID == 0 {
		t.Fatal("ticket ID should be non-zero")
	}
	if tk.Channel != support.ChannelEmail {
		t.Errorf("channel = %q; want email", tk.Channel)
	}
}

func TestPG_GetTicket(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	tk, err := st.OpenTicket(ctx, "pg-a2", "enterprise", "P1", "prod down", "everything is broken")
	if err != nil {
		t.Fatalf("open ticket: %v", err)
	}

	got, err := st.GetTicket(ctx, tk.ID)
	if err != nil {
		t.Fatalf("get ticket: %v", err)
	}
	if got.Subject != "prod down" {
		t.Errorf("subject = %q; want %q", got.Subject, "prod down")
	}
	if got.Channel != support.ChannelDedicatedSlack {
		t.Errorf("channel = %q; want dedicated_slack", got.Channel)
	}

	_, err = st.GetTicket(ctx, 9999999)
	if !errors.Is(err, support.ErrTicketNotFound) {
		t.Fatalf("expected ErrTicketNotFound; got %v", err)
	}
}

func TestPG_CloseTicket(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	tk, err := st.OpenTicket(ctx, "pg-a3", "pro", "P3", "billing issue", "")
	if err != nil {
		t.Fatalf("open ticket: %v", err)
	}

	// Wrong owner → ErrForbidden.
	if err := st.CloseTicket(ctx, tk.ID, "pg-other"); !errors.Is(err, support.ErrForbidden) {
		t.Fatalf("expected ErrForbidden; got %v", err)
	}

	// Correct owner.
	if err := st.CloseTicket(ctx, tk.ID, "pg-a3"); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Double close.
	if err := st.CloseTicket(ctx, tk.ID, "pg-a3"); !errors.Is(err, support.ErrAlreadyClosed) {
		t.Fatalf("expected ErrAlreadyClosed; got %v", err)
	}
}

func TestPG_ListTickets(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := st.OpenTicket(ctx, "pg-a4", "pro", "P3", "ticket", ""); err != nil {
			t.Fatalf("open ticket %d: %v", i, err)
		}
	}
	if _, err := st.OpenTicket(ctx, "pg-a4-other", "pro", "P3", "other", ""); err != nil {
		t.Fatalf("open other ticket: %v", err)
	}

	list, err := st.ListTickets(ctx, "pg-a4")
	if err != nil {
		t.Fatalf("list tickets: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("expected 3 tickets for pg-a4; got %d", len(list))
	}
}
