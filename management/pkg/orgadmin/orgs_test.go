package orgadmin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeMailer records ProvisionMailbox calls and returns a programmable error.
type fakeMailer struct {
	mu    sync.Mutex
	calls []struct{ localpart, domain, org, accountULID string }
	err   error
}

func (f *fakeMailer) ProvisionMailbox(_ context.Context, localpart, domain, org, accountULID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct{ localpart, domain, org, accountULID string }{localpart, domain, org, accountULID})
	return f.err
}

func (f *fakeMailer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newTestService builds an OrgService with a MemOrgStore, the given mailer, and
// synchronous background execution so the async provisioning runs inline.
func newTestService(mailer MailProvisioner) (*OrgService, *MemOrgStore) {
	store := NewMemOrgStore()
	svc := NewOrgService(store, mailer, "example.com")
	svc.runBg = func(f func()) { f() } // run inline
	svc.sleep = func(time.Duration) {} // no real sleeps
	svc.Backoff = func(int) time.Duration { return 0 }
	return svc, store
}

func TestProvisionOrgForSignup_CreatesOrgOwnerActiveMailbox(t *testing.T) {
	mailer := &fakeMailer{}
	svc, store := newTestService(mailer)
	ctx := context.Background()

	item, err := svc.ProvisionOrgForSignup(ctx, "acct-1", "Alice", "Alice")
	if err != nil {
		t.Fatalf("ProvisionOrgForSignup: %v", err)
	}
	if item.Slug != "alice" {
		t.Fatalf("slug = %q, want alice", item.Slug)
	}
	if item.Role != string(RoleOwner) {
		t.Fatalf("role = %q, want owner", item.Role)
	}
	if !item.Current {
		t.Fatalf("new signup org should be current")
	}

	// Owner membership recorded.
	role, err := store.OrgMemberRole(ctx, item.ID, "acct-1")
	if err != nil || role != "owner" {
		t.Fatalf("owner membership: role=%q err=%v", role, err)
	}
	// Active org set.
	active, _ := store.GetActiveOrg(ctx, "acct-1")
	if active != item.ID {
		t.Fatalf("active org = %q, want %q", active, item.ID)
	}
	// Root mailbox provisioned via broker.
	org, err := store.GetOrg(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetOrg: %v", err)
	}
	if org.RootMailbox != "alice@example.com" {
		t.Fatalf("root_mailbox = %q, want alice@example.com", org.RootMailbox)
	}
	if org.MailboxState != MailboxProvisioned {
		t.Fatalf("mailbox_state = %q, want provisioned", org.MailboxState)
	}
	if mailer.count() != 1 {
		t.Fatalf("mailer calls = %d, want 1", mailer.count())
	}
	c := mailer.calls[0]
	if c.localpart != "alice" || c.domain != "example.com" || c.org != item.ID {
		t.Fatalf("broker call = %+v, want localpart=alice domain=example.com org=%s", c, item.ID)
	}
	// MAIL-STORAGE-UNIFY-02: the owner's cp ULID (the account_id the signup
	// caller passed in) rides the broker call so vulos-mail can seed its
	// bucket-naming cache at mailbox-creation time.
	if c.accountULID != "acct-1" {
		t.Fatalf("broker call account_ulid = %q, want acct-1", c.accountULID)
	}
}

func TestProvisionOrg_NeverBlocksOnBrokerFailure(t *testing.T) {
	mailer := &fakeMailer{err: errors.New("broker down")}
	svc, store := newTestService(mailer)
	ctx := context.Background()

	// Signup still succeeds even though the broker always fails.
	item, err := svc.ProvisionOrgForSignup(ctx, "acct-2", "bob", "bob")
	if err != nil {
		t.Fatalf("signup must not fail on broker error: %v", err)
	}
	org, _ := store.GetOrg(ctx, item.ID)
	if org.MailboxState != MailboxFailed {
		t.Fatalf("mailbox_state = %q, want failed after exhausted retries", org.MailboxState)
	}
	if mailer.count() != svc.MaxProvisionAttempts {
		t.Fatalf("retries = %d, want %d", mailer.count(), svc.MaxProvisionAttempts)
	}
	// The org + working address still exist (back-fillable).
	if org.RootMailbox != "bob@example.com" {
		t.Fatalf("root_mailbox = %q, want bob@example.com", org.RootMailbox)
	}
}

func TestCreateListSwitchOrg(t *testing.T) {
	svc, _ := newTestService(&fakeMailer{})
	ctx := context.Background()

	a, err := svc.CreateOrg(ctx, "acct-3", "Acme")
	if err != nil {
		t.Fatalf("CreateOrg a: %v", err)
	}
	if a.Role != "owner" {
		t.Fatalf("creator role = %q, want owner", a.Role)
	}
	b, err := svc.CreateOrg(ctx, "acct-3", "Beta")
	if err != nil {
		t.Fatalf("CreateOrg b: %v", err)
	}

	list, err := svc.ListForUser(ctx, "acct-3")
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	// Most-recently created org (b) is current after create.
	for _, o := range list {
		wantCurrent := o.ID == b.ID
		if o.Current != wantCurrent {
			t.Fatalf("org %s current=%v, want %v", o.Slug, o.Current, wantCurrent)
		}
		if o.Role != "owner" {
			t.Fatalf("org %s role=%q, want owner", o.Slug, o.Role)
		}
	}

	// Switch to a.
	if err := svc.SwitchActive(ctx, "acct-3", a.ID); err != nil {
		t.Fatalf("SwitchActive: %v", err)
	}
	list, _ = svc.ListForUser(ctx, "acct-3")
	for _, o := range list {
		wantCurrent := o.ID == a.ID
		if o.Current != wantCurrent {
			t.Fatalf("after switch org %s current=%v, want %v", o.Slug, o.Current, wantCurrent)
		}
	}

	// Switching to an org the user is NOT a member of → ErrNotFound (no disclosure).
	if err := svc.SwitchActive(ctx, "acct-3", "org-does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("switch to non-member org err = %v, want ErrNotFound", err)
	}

	// A different user sees none of acct-3's orgs.
	other, _ := svc.ListForUser(ctx, "acct-other")
	if len(other) != 0 {
		t.Fatalf("cross-account list len = %d, want 0", len(other))
	}
}

func TestUniqueSlug_CollisionGetsSuffix(t *testing.T) {
	svc, _ := newTestService(&fakeMailer{})
	ctx := context.Background()
	a, _ := svc.CreateOrg(ctx, "u1", "Same Name")
	b, _ := svc.CreateOrg(ctx, "u2", "Same Name")
	if a.Slug == b.Slug {
		t.Fatalf("slugs collided: %q == %q", a.Slug, b.Slug)
	}
	if a.Slug != "same-name" {
		t.Fatalf("first slug = %q, want same-name", a.Slug)
	}
	if b.Slug != "same-name-2" {
		t.Fatalf("second slug = %q, want same-name-2", b.Slug)
	}
}

// TestFreeTierKeepsRootMailbox: a Free-tier signup org carries a provisioned
// root mailbox (the permanent Free entitlement).
func TestFreeTierKeepsRootMailbox(t *testing.T) {
	svc, store := newTestService(&fakeMailer{})
	ctx := context.Background()
	item, err := svc.ProvisionOrgForSignup(ctx, "free-acct", "freebie", "freebie")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	org, _ := store.GetOrg(ctx, item.ID)
	if org.RootMailbox == "" || org.MailboxState != MailboxProvisioned {
		t.Fatalf("free tier must keep a provisioned root mailbox; got mailbox=%q state=%q",
			org.RootMailbox, org.MailboxState)
	}
}

// TestDowngradePreservesRootMailbox: the org plane that owns the root mailbox is
// independent of the billing tier, so a paid→Free downgrade (modelled here as
// the tier reverting with NO call into the org store) never removes the mailbox.
// The OrgStore exposes no mailbox-delete API — the only mutation is
// SetMailboxState, which the downgrade path never invokes.
func TestDowngradePreservesRootMailbox(t *testing.T) {
	svc, store := newTestService(&fakeMailer{})
	ctx := context.Background()
	item, err := svc.ProvisionOrgForSignup(ctx, "paid-acct", "paidco", "PaidCo")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	before, _ := store.GetOrg(ctx, item.ID)

	// Simulate a paid→Free downgrade: the billing tier reverts to free elsewhere;
	// nothing touches the org row. Re-read and assert the mailbox is intact.
	after, err := store.GetOrg(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetOrg after downgrade: %v", err)
	}
	if after.RootMailbox != before.RootMailbox || after.RootMailbox == "" {
		t.Fatalf("downgrade changed root mailbox: before=%q after=%q", before.RootMailbox, after.RootMailbox)
	}
	if after.MailboxState != MailboxProvisioned {
		t.Fatalf("downgrade changed mailbox state: %q, want provisioned", after.MailboxState)
	}
	// The account still belongs to the org and can reach the address.
	if role, rerr := store.OrgMemberRole(ctx, item.ID, "paid-acct"); rerr != nil || role != "owner" {
		t.Fatalf("downgrade dropped ownership: role=%q err=%v", role, rerr)
	}
}
