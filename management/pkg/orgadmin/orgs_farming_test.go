package orgadmin

// orgs_farming_test.go — CLOUD-BILLING-EDGES edge #1: anti-farming for the free
// root mailbox + free allowances. One account spinning up N orgs must NOT mint N
// free mailboxes, and org creation is capped + rate-limited per account.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestFreeMailbox_OnlyFirstOrgPerAccount: the account's FIRST org gets a free
// root mailbox; additional orgs on a FREE account get NONE.
func TestFreeMailbox_OnlyFirstOrgPerAccount(t *testing.T) {
	mailer := &fakeMailer{}
	svc, store := newTestService(mailer) // no PaidPlanChecker → free account
	ctx := context.Background()

	a, err := svc.CreateOrg(ctx, "acct", "Alpha")
	if err != nil {
		t.Fatalf("create first org: %v", err)
	}
	orgA, _ := store.GetOrg(ctx, a.ID)
	if orgA.RootMailbox == "" || orgA.MailboxState != MailboxProvisioned {
		t.Fatalf("first org should get a provisioned free mailbox: mailbox=%q state=%q",
			orgA.RootMailbox, orgA.MailboxState)
	}

	b, err := svc.CreateOrg(ctx, "acct", "Beta")
	if err != nil {
		t.Fatalf("create second org: %v", err)
	}
	orgB, _ := store.GetOrg(ctx, b.ID)
	if orgB.RootMailbox != "" || orgB.MailboxState != MailboxNone {
		t.Fatalf("additional free-account org must NOT get a free mailbox: mailbox=%q state=%q",
			orgB.RootMailbox, orgB.MailboxState)
	}

	// The broker was called exactly once — for the first org only.
	if mailer.count() != 1 {
		t.Fatalf("broker calls = %d, want 1 (first org only)", mailer.count())
	}
}

// TestFreeMailbox_PaidAccountGetsMailboxOnAdditionalOrgs: a paid account DOES get
// a root mailbox on additional orgs (they pay for it).
func TestFreeMailbox_PaidAccountGetsMailboxOnAdditionalOrgs(t *testing.T) {
	mailer := &fakeMailer{}
	svc, store := newTestService(mailer)
	svc.PaidPlanChecker = func(context.Context, string) (bool, error) { return true, nil }
	ctx := context.Background()

	_, err := svc.CreateOrg(ctx, "paid", "Alpha")
	if err != nil {
		t.Fatalf("create first org: %v", err)
	}
	b, err := svc.CreateOrg(ctx, "paid", "Beta")
	if err != nil {
		t.Fatalf("create second org: %v", err)
	}
	orgB, _ := store.GetOrg(ctx, b.ID)
	if orgB.RootMailbox == "" || orgB.MailboxState != MailboxProvisioned {
		t.Fatalf("paid additional org should get a mailbox: mailbox=%q state=%q",
			orgB.RootMailbox, orgB.MailboxState)
	}
	if mailer.count() != 2 {
		t.Fatalf("broker calls = %d, want 2 (both orgs)", mailer.count())
	}
}

// TestOrgCreate_PerAccountCapEnforced: a free account cannot own more than the
// per-account cap; the over-cap create is rejected with ErrOrgLimitReached.
func TestOrgCreate_PerAccountCapEnforced(t *testing.T) {
	svc, _ := newTestService(&fakeMailer{})
	svc.MaxOrgsPerAccount = 3
	svc.MaxOrgsPerWindow = 100 // isolate the hard cap from the rate-limit
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := svc.CreateOrg(ctx, "acct", fmt.Sprintf("Org%d", i)); err != nil {
			t.Fatalf("create org %d (within cap): %v", i, err)
		}
	}
	_, err := svc.CreateOrg(ctx, "acct", "OverCap")
	if !errors.Is(err, ErrOrgLimitReached) {
		t.Fatalf("4th org over cap: err=%v, want ErrOrgLimitReached", err)
	}
}

// TestOrgCreate_RateLimitEnforced: even under the hard cap, a burst of creations
// in one window is rate-limited.
func TestOrgCreate_RateLimitEnforced(t *testing.T) {
	svc, _ := newTestService(&fakeMailer{})
	svc.MaxOrgsPerAccount = 100 // isolate the rate-limit from the hard cap
	svc.MaxOrgsPerAccountPaid = 100
	svc.MaxOrgsPerWindow = 2
	svc.OrgCreateWindow = time.Hour
	base := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return base } // freeze the clock inside the window
	ctx := context.Background()

	// #1 is the first org (always allowed); #2 is within the window budget.
	if _, err := svc.CreateOrg(ctx, "acct", "One"); err != nil {
		t.Fatalf("create #1: %v", err)
	}
	if _, err := svc.CreateOrg(ctx, "acct", "Two"); err != nil {
		t.Fatalf("create #2: %v", err)
	}
	// #3 exceeds MaxOrgsPerWindow (2 created in-window already).
	_, err := svc.CreateOrg(ctx, "acct", "Three")
	if !errors.Is(err, ErrOrgLimitReached) {
		t.Fatalf("3rd org in window: err=%v, want ErrOrgLimitReached", err)
	}

	// After the window advances, creation is allowed again (cap permitting).
	svc.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, err := svc.CreateOrg(ctx, "acct", "Later"); err != nil {
		t.Fatalf("create after window reset: %v", err)
	}
}

// TestSignupFirstOrgNeverBlocked: the very first org for a brand-new account is
// always allowed (signup must never fail on the anti-farming gate), even with a
// degenerate zero rate-limit budget.
func TestSignupFirstOrgNeverBlocked(t *testing.T) {
	svc, store := newTestService(&fakeMailer{})
	svc.MaxOrgsPerWindow = 0 // pathological; first org must still go through
	svc.MaxOrgsPerAccount = 0
	ctx := context.Background()

	item, err := svc.ProvisionOrgForSignup(ctx, "fresh", "fresh", "Fresh")
	if err != nil {
		t.Fatalf("first signup org must never be blocked: %v", err)
	}
	org, _ := store.GetOrg(ctx, item.ID)
	if org.RootMailbox == "" {
		t.Fatalf("first signup org must carry a free mailbox")
	}
}
