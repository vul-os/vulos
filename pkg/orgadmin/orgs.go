package orgadmin

// orgs.go — multi-org membership + free root mailbox on signup
// (ORG-MULTI-01 / FREE-ORG-MAIL-01).
//
// Every signup creates/owns exactly one org; a user may belong to (and switch
// between) several. On org creation the package provisions a FREE root mailbox
// <slug>@<mail-domain> by handing the request to a MailProvisioner seam
// (the concrete HTTP broker lives in internal/mailprovision). Provisioning is
// ALWAYS asynchronous + retried and NEVER blocks signup: the org row is written
// with mailbox_state='pending' and a background worker flips it to
// 'provisioned' / 'failed'. The root mailbox is a permanent Free-tier
// entitlement — nothing in this package deletes it, so a paid→Free downgrade
// keeps the account and its working address.
//
// This file owns three tables (migration 0003): orgs, org_membership and
// user_active_org. The OrgStore interface is satisfied by the package's
// SQLStore (production) and MemStore (tests / dev).

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// Mailbox provisioning states stored on the org row.
const (
	MailboxNone        = "none"        // no mail domain configured → no root mailbox
	MailboxPending     = "pending"     // provisioning in flight / queued for retry
	MailboxProvisioned = "provisioned" // broker confirmed the mailbox exists
	MailboxFailed      = "failed"      // provisioning exhausted retries (back-fillable)
)

// ErrSlugUnavailable is returned when a unique slug could not be derived.
var ErrSlugUnavailable = errors.New("orgadmin: could not allocate a unique org slug")

// ErrOrgLimitReached is returned when an account has hit its per-account org
// cap or its org-creation rate-limit (CLOUD-BILLING-EDGES edge #1: anti-farming).
// It maps to HTTP 429. The account's FIRST org is never blocked (signup must
// always succeed), so this is only ever returned for additional orgs.
var ErrOrgLimitReached = errors.New("orgadmin: org creation limit reached")

// ErrLastOwner is returned by LeaveOrg when the caller is the sole remaining
// owner — leaving would leave the org ownerless. The caller must transfer
// ownership first (or the org should be deleted). Maps to HTTP 409.
var ErrLastOwner = errors.New("orgadmin: cannot leave — you are the last owner")

// Org-creation anti-farming defaults (CLOUD-BILLING-EDGES edge #1). One account
// spinning up N orgs to mint N free root mailboxes / N free allowances is the
// abuse vector; these bound it. The FIRST org is always free + always allowed.
const (
	// DefaultMaxOrgsPerAccount caps how many orgs a FREE account may own. Only the
	// first carries a free mailbox; the rest exist but are mailbox-less unless the
	// account is on a paid plan.
	DefaultMaxOrgsPerAccount = 3
	// DefaultMaxOrgsPerAccountPaid is the (higher) cap for paid accounts — they pay
	// per additional org's mailbox/seats, so a larger ceiling is fine.
	DefaultMaxOrgsPerAccountPaid = 25
	// DefaultMaxOrgsPerWindow / DefaultOrgCreateWindow rate-limit bursty creation
	// regardless of the total cap: at most N new orgs per rolling window.
	DefaultMaxOrgsPerWindow = 3
	DefaultOrgCreateWindow  = time.Hour
)

// PaidPlanFunc reports whether an account is on a paid subscription tier. Wired
// from the billing store (EffectiveTierFor != "free") so the org plane can grant
// a free root mailbox on additional orgs to paying accounts only — without
// importing the billing package (avoids a dependency cycle). A nil checker (dev /
// unwired) is treated as "not paid" (conservative: free entitlements only).
type PaidPlanFunc func(ctx context.Context, accountID string) (bool, error)

// OrgRecord is the persisted org row.
type OrgRecord struct {
	ID            string
	Slug          string
	Name          string
	OwnerAccount  string
	RootMailbox   string
	MailboxDomain string
	MailboxState  string
	CreatedAt     string // RFC3339
	// HomeRegion is the tenant's home region (MULTI-REGION Phase 0, default "eu").
	//
	// DEPRECATED (REGION-SSOT-01): orgs.home_region is NO LONGER authoritative —
	// georoute.tenants_regions is the single source of truth. This denormalised
	// copy is whitelisted for back-compat reads only; the region reconciler
	// (internal/regionrecon) reconciles it against canonical. Placement decisions
	// must resolve the region via the canonical store / residency policy, not here.
	HomeRegion string
	// ResidencyPolicy constrains where data for this org may be placed.
	//   "any"     — no restriction (default)
	//   "eu_only" — data must stay within the EU region set
	//   "us_only" — data must stay within the US region set
	ResidencyPolicy string
}

// OrgMembership pairs an org with the caller's role in it (for list views).
type OrgMembership struct {
	OrgRecord
	Role string
}

// OrgCreateGate carries the anti-farming policy that CreateOrgAtomic enforces in
// the SAME transaction that inserts the org, so the per-account cap and the
// once-per-account free-mailbox entitlement cannot be bypassed by concurrent
// creates racing across replicas (COUNTCAP-TOCTOU-01). All fields are computed
// by the service layer immediately before the call.
type OrgCreateGate struct {
	// Cap is the maximum number of orgs this account may OWN. <= 0 means no cap.
	// The account's FIRST org (live owned-count == 0) is ALWAYS allowed regardless.
	Cap int
	// WindowSince (RFC3339 UTC) + MaxPerWindow bound bursty creation: at most
	// MaxPerWindow orgs owned at/after WindowSince. MaxPerWindow <= 0 disables it.
	WindowSince  string
	MaxPerWindow int
	// PaidPlan reports whether the owner is on a paid tier — paid accounts get a
	// free root mailbox on ADDITIONAL orgs too (not just the first).
	PaidPlan bool
	// MailDomain is the configured root-mailbox domain ("" disables mailboxes).
	// When set and the account is free-mailbox-eligible (first org, or paid), the
	// store stamps rec.RootMailbox = <slug>@<MailDomain> + MailboxPending atomically
	// with the live count, so exactly one org per free account is granted one.
	MailDomain string
}

// OrgStore persists orgs, memberships and the per-user active org. Implemented
// by *SQLStore (production) and *MemStore (tests / dev mode).
type OrgStore interface {
	// CreateOrg inserts the org row AND the creator's owner membership atomically.
	// A duplicate slug surfaces as ErrSlugUnavailable so the service can retry.
	CreateOrg(ctx context.Context, rec OrgRecord) error
	// CreateOrgAtomic inserts the org + owner membership like CreateOrg, but ALSO
	// enforces the anti-farming cap/rate-limit AND decides free-mailbox eligibility
	// inside the same transaction under a per-account lock, so concurrent creates
	// (including across replicas on shared Postgres) cannot exceed the cap or mint
	// more than one free mailbox per free account. It returns the persisted record
	// with its mailbox fields populated when a free mailbox was granted (so the
	// caller knows whether to schedule provisioning). Returns ErrOrgLimitReached
	// when the cap/rate-limit is hit, or ErrSlugUnavailable on a slug collision.
	CreateOrgAtomic(ctx context.Context, rec OrgRecord, gate OrgCreateGate) (OrgRecord, error)
	// GetOrg returns the org by id, or ErrNotFound.
	GetOrg(ctx context.Context, id string) (OrgRecord, error)
	// SlugExists reports whether slug is already taken.
	SlugExists(ctx context.Context, slug string) (bool, error)
	// AddOrgMember upserts a membership row.
	AddOrgMember(ctx context.Context, orgID, accountID, role string) error
	// OrgMemberRole returns the account's role in the org, or ErrNotFound when
	// the account is not a member (no existence disclosure for non-members).
	OrgMemberRole(ctx context.Context, orgID, accountID string) (string, error)
	// ListOrgsForAccount returns the orgs the account belongs to, with the
	// account's role in each, oldest-first.
	ListOrgsForAccount(ctx context.Context, accountID string) ([]OrgMembership, error)
	// CountOrgsOwnedByAccount returns how many orgs the account OWNS (owner_account
	// = accountID). Used by the anti-farming gate to decide whether this is the
	// account's first org (free mailbox) and whether the per-account cap is hit.
	CountOrgsOwnedByAccount(ctx context.Context, accountID string) (int, error)
	// CountOrgsOwnedSince returns how many orgs the account has OWNED that were
	// created at/after sinceRFC3339 (a UTC RFC3339 timestamp). Drives the
	// org-creation rate-limit window.
	CountOrgsOwnedSince(ctx context.Context, accountID, sinceRFC3339 string) (int, error)
	// SetMailboxState updates the org's mailbox provisioning state.
	SetMailboxState(ctx context.Context, orgID, state string) error
	// SetActiveOrg records the account's current org.
	SetActiveOrg(ctx context.Context, accountID, orgID string) error
	// GetActiveOrg returns the account's current org id, or "" (no error) when
	// none is set yet.
	GetActiveOrg(ctx context.Context, accountID string) (string, error)
	// RemoveOrgMember removes the account from the org's membership. It does NOT
	// check last-owner constraints — the caller (service layer) must verify that
	// before calling. Cross-tenant / unknown → no-op (idempotent).
	RemoveOrgMember(ctx context.Context, orgID, accountID string) error
	// CountOrgOwners returns how many members hold the "owner" role in the org.
	// Used by LeaveOrg to block the last owner from leaving.
	CountOrgOwners(ctx context.Context, orgID string) (int, error)
}

// MailProvisioner brokers the free root mailbox to vulos-mail. The concrete
// implementation (internal/mailprovision) does a broker-secret-gated
// POST /api/admin/provision-mailbox; the secret never leaves that package and
// is never logged. A nil provisioner (dev / unconfigured) leaves the mailbox
// in its initial state. accountULID (MAIL-STORAGE-UNIFY-02) is the owning
// account's canonical cp ULID, forwarded so vulos-mail can seed its
// address→ULID bucket-naming cache at mailbox-creation time.
type MailProvisioner interface {
	ProvisionMailbox(ctx context.Context, localpart, domain, orgID, accountULID string) error
}

// ── wire types ───────────────────────────────────────────────────────────────

// OrgListItem is one row of GET /api/org. The JSON shape is EXACTLY
// {id,slug,name,role,current} as the Workspace surfaces consume.
type OrgListItem struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	Current bool   `json:"current"`
}

// CreateOrgResult is the POST /api/org/create body: {id,slug,role}.
type CreateOrgResult struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Role string `json:"role"`
}

// ── Service ──────────────────────────────────────────────────────────────────

// OrgService composes the OrgStore with the (optional) mail provisioner and the
// configured mail domain. All time / id / scheduling seams are overridable for
// deterministic tests.
type OrgService struct {
	Store      OrgStore
	Mailer     MailProvisioner // may be nil → no mailbox provisioning
	MailDomain string          // empty disables the root mailbox (no hosted mail)

	// MaxProvisionAttempts bounds the async retry loop (default 5).
	MaxProvisionAttempts int
	// Backoff returns the pause before attempt n (1-based). Default: 2^n seconds
	// capped at 30s. Tests inject a zero-backoff.
	Backoff func(attempt int) time.Duration

	// Anti-farming knobs (CLOUD-BILLING-EDGES edge #1). See the Default* consts.
	MaxOrgsPerAccount     int           // free-account org cap (default 3)
	MaxOrgsPerAccountPaid int           // paid-account org cap (default 25)
	MaxOrgsPerWindow      int           // rate-limit: orgs per window (default 3)
	OrgCreateWindow       time.Duration // rate-limit window (default 1h)

	// PaidPlanChecker reports whether the owner account is on a paid plan, so
	// additional orgs (beyond the first) can be granted a free root mailbox for
	// paying accounts only. nil → treated as not-paid (free entitlements only).
	PaidPlanChecker PaidPlanFunc

	now   func() time.Time
	newID func() string
	sleep func(time.Duration)
	runBg func(func()) // default: go f(); tests run synchronously
}

// NewOrgService builds an OrgService with production defaults.
func NewOrgService(store OrgStore, mailer MailProvisioner, mailDomain string) *OrgService {
	return &OrgService{
		Store:                 store,
		Mailer:                mailer,
		MailDomain:            strings.TrimSpace(mailDomain),
		MaxProvisionAttempts:  5,
		Backoff:               defaultBackoff,
		MaxOrgsPerAccount:     DefaultMaxOrgsPerAccount,
		MaxOrgsPerAccountPaid: DefaultMaxOrgsPerAccountPaid,
		MaxOrgsPerWindow:      DefaultMaxOrgsPerWindow,
		OrgCreateWindow:       DefaultOrgCreateWindow,
		now:                   func() time.Time { return time.Now().UTC() },
		newID:                 newOrgID,
		sleep:                 time.Sleep,
		runBg:                 func(f func()) { go f() },
	}
}

func defaultBackoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// ProvisionOrgForSignup creates the org owned by ownerAccountID at signup time,
// makes it the account's active org, and kicks off async root-mailbox
// provisioning. It NEVER blocks on the mailbox: the org is committed and
// returned immediately. Used by both the native and OAuth-linked signup paths.
func (s *OrgService) ProvisionOrgForSignup(ctx context.Context, ownerAccountID, slugHint, name string) (OrgListItem, error) {
	return s.createOrg(ctx, ownerAccountID, slugHint, name, true)
}

// CreateOrg backs POST /api/org/create: the caller becomes the owner of a new
// org (creator=owner) and it is made their active org.
func (s *OrgService) CreateOrg(ctx context.Context, ownerAccountID, name string) (CreateOrgResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return CreateOrgResult{}, ErrInvalidInput
	}
	item, err := s.createOrg(ctx, ownerAccountID, name, name, true)
	if err != nil {
		return CreateOrgResult{}, err
	}
	return CreateOrgResult{ID: item.ID, Slug: item.Slug, Role: item.Role}, nil
}

// createOrg is the shared org-creation path. slugHint seeds the slug; name is
// the display name (defaults to slugHint). setActive makes the new org current.
func (s *OrgService) createOrg(ctx context.Context, ownerAccountID, slugHint, name string, setActive bool) (OrgListItem, error) {
	ownerAccountID = strings.TrimSpace(ownerAccountID)
	if ownerAccountID == "" {
		return OrgListItem{}, ErrInvalidInput
	}
	if strings.TrimSpace(name) == "" {
		name = slugHint
	}

	// Anti-farming gate (CLOUD-BILLING-EDGES edge #1). COUNTCAP-TOCTOU-01: the cap,
	// the rate-limit AND the once-per-account free-mailbox entitlement are all
	// derived from the account's live owned-count, so they MUST be evaluated in the
	// same transaction as the insert. Reading the count here and inserting in a
	// separate tx (the previous shape) let two concurrent creates from a fresh
	// account each observe owned==0 and each mint a free mailbox / bypass the cap —
	// the process-local store mutex does NOT serialise across replicas on shared
	// Postgres. We now hand the policy to CreateOrgAtomic, which re-counts under a
	// per-account lock and stamps the free mailbox atomically.
	paid := s.isPaidPlan(ctx, ownerAccountID)

	slug, err := s.uniqueSlug(ctx, slugHint)
	if err != nil {
		return OrgListItem{}, err
	}

	rec := OrgRecord{
		ID:           s.newID(),
		Slug:         slug,
		Name:         strings.TrimSpace(name),
		OwnerAccount: ownerAccountID,
		MailboxState: MailboxNone,
		CreatedAt:    s.now().Format(time.RFC3339),
	}

	saved, err := s.Store.CreateOrgAtomic(ctx, rec, s.orgCreateGate(paid))
	if err != nil {
		return OrgListItem{}, err
	}
	if setActive {
		// Best-effort: a failure to set the active org must not fail org creation.
		_ = s.Store.SetActiveOrg(ctx, ownerAccountID, saved.ID)
	}

	// Async, retried, never-blocking root-mailbox provisioning. The store decided
	// eligibility atomically; we schedule iff it stamped the mailbox pending.
	if saved.MailboxState == MailboxPending {
		s.scheduleMailboxProvision(saved.ID, saved.Slug, s.MailDomain, ownerAccountID)
	}

	return OrgListItem{ID: saved.ID, Slug: saved.Slug, Name: saved.Name, Role: string(RoleOwner), Current: setActive}, nil
}

// orgCreateGate builds the anti-farming policy that CreateOrgAtomic enforces
// atomically with the insert. It mirrors the caps that checkOrgCreateLimits used
// to enforce out-of-band, plus the once-per-account free-mailbox rule.
func (s *OrgService) orgCreateGate(paid bool) OrgCreateGate {
	limit := s.MaxOrgsPerAccount
	if limit <= 0 {
		limit = DefaultMaxOrgsPerAccount
	}
	if paid {
		pc := s.MaxOrgsPerAccountPaid
		if pc <= 0 {
			pc = DefaultMaxOrgsPerAccountPaid
		}
		if pc > limit {
			limit = pc
		}
	}
	win := s.OrgCreateWindow
	if win <= 0 {
		win = DefaultOrgCreateWindow
	}
	maxWin := s.MaxOrgsPerWindow
	if maxWin <= 0 {
		maxWin = DefaultMaxOrgsPerWindow
	}
	return OrgCreateGate{
		Cap:          limit,
		WindowSince:  s.now().Add(-win).Format(time.RFC3339),
		MaxPerWindow: maxWin,
		PaidPlan:     paid,
		MailDomain:   s.MailDomain,
	}
}

// isPaidPlan reports whether the owner account is on a paid plan. A nil checker
// or a lookup error is treated as NOT paid (fail-closed for entitlement-granting:
// a transient billing error must not hand out free mailboxes / extra org slots).
func (s *OrgService) isPaidPlan(ctx context.Context, accountID string) bool {
	if s.PaidPlanChecker == nil {
		return false
	}
	paid, err := s.PaidPlanChecker(ctx, accountID)
	if err != nil {
		return false
	}
	return paid
}

// scheduleMailboxProvision runs the broker call on a background worker with
// bounded retries, flipping the org's mailbox_state to provisioned/failed. The
// broker secret lives inside the provisioner and is never touched/logged here.
// accountULID is the owning account's canonical cp ULID (MAIL-STORAGE-UNIFY-02),
// forwarded to the broker so vulos-mail can seed its bucket-naming cache.
func (s *OrgService) scheduleMailboxProvision(orgID, localpart, domain, accountULID string) {
	if s.Mailer == nil {
		return // unconfigured (dev): leave the row 'pending' for a later back-fill.
	}
	run := s.runBg
	if run == nil {
		run = func(f func()) { go f() }
	}
	run(func() {
		// Detached context: the request that triggered signup may be long gone.
		ctx := context.Background()
		attempts := s.MaxProvisionAttempts
		if attempts <= 0 {
			attempts = 5
		}
		for attempt := 1; attempt <= attempts; attempt++ {
			err := s.Mailer.ProvisionMailbox(ctx, localpart, domain, orgID, accountULID)
			if err == nil {
				_ = s.Store.SetMailboxState(ctx, orgID, MailboxProvisioned)
				return
			}
			if attempt < attempts {
				bo := defaultBackoff
				if s.Backoff != nil {
					bo = s.Backoff
				}
				sleep := s.sleep
				if sleep == nil {
					sleep = time.Sleep
				}
				sleep(bo(attempt))
			}
		}
		_ = s.Store.SetMailboxState(ctx, orgID, MailboxFailed)
	})
}

// ListForUser backs GET /api/org. Returns [{id,slug,name,role,current}] for the
// orgs the account belongs to, with the active org flagged current.
func (s *OrgService) ListForUser(ctx context.Context, accountID string) ([]OrgListItem, error) {
	memberships, err := s.Store.ListOrgsForAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	active, _ := s.Store.GetActiveOrg(ctx, accountID)
	out := make([]OrgListItem, 0, len(memberships))
	for _, m := range memberships {
		out = append(out, OrgListItem{
			ID:      m.ID,
			Slug:    m.Slug,
			Name:    m.Name,
			Role:    m.Role,
			Current: active != "" && m.ID == active,
		})
	}
	return out, nil
}

// SwitchActive backs POST /api/org/switch. The caller must be a member of the
// target org; a non-member / unknown org surfaces as ErrNotFound (no existence
// disclosure).
func (s *OrgService) SwitchActive(ctx context.Context, accountID, orgID string) error {
	orgID = strings.TrimSpace(orgID)
	if orgID == "" {
		return ErrInvalidInput
	}
	if _, err := s.Store.OrgMemberRole(ctx, orgID, accountID); err != nil {
		return ErrNotFound
	}
	return s.Store.SetActiveOrg(ctx, accountID, orgID)
}

// LeaveOrg removes the caller from an org they are a member of. Constraints:
//   - The caller must currently be a member (else ErrNotFound).
//   - The caller cannot leave if they are the SOLE owner (ErrLastOwner).
//   - After leaving, the caller's active org is reverted to the first org they
//     still belong to (oldest-first), or cleared when they have no remaining org.
//   - The caller's account and root mailbox are NEVER removed — only the
//     membership row is dropped.
func (s *OrgService) LeaveOrg(ctx context.Context, accountID, orgID string) error {
	orgID = strings.TrimSpace(orgID)
	accountID = strings.TrimSpace(accountID)
	if orgID == "" || accountID == "" {
		return ErrInvalidInput
	}
	// Verify membership.
	role, err := s.Store.OrgMemberRole(ctx, orgID, accountID)
	if err != nil {
		return ErrNotFound // no disclosure
	}
	// Block the last owner.
	if strings.EqualFold(role, string(RoleOwner)) {
		owners, cerr := s.Store.CountOrgOwners(ctx, orgID)
		if cerr != nil {
			return fmt.Errorf("orgadmin: leave org (count owners): %w", cerr)
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}
	// Remove membership.
	if err := s.Store.RemoveOrgMember(ctx, orgID, accountID); err != nil {
		return fmt.Errorf("orgadmin: leave org (remove member): %w", err)
	}
	// Revert active org to the user's first remaining org, or clear it.
	active, _ := s.Store.GetActiveOrg(ctx, accountID)
	if active == orgID {
		remaining, lerr := s.Store.ListOrgsForAccount(ctx, accountID)
		if lerr == nil && len(remaining) > 0 {
			_ = s.Store.SetActiveOrg(ctx, accountID, remaining[0].ID)
		} else {
			// No remaining orgs — clear the active pointer. Best-effort.
			_ = s.Store.SetActiveOrg(ctx, accountID, "")
		}
	}
	return nil
}

// uniqueSlug derives a unique slug from hint, appending -2, -3, … on collision
// and falling back to a random suffix.
func (s *OrgService) uniqueSlug(ctx context.Context, hint string) (string, error) {
	base := Slugify(hint)
	if base == "" {
		base = "org"
	}
	candidate := base
	for i := 2; i < 1000; i++ {
		exists, err := s.Store.SlugExists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		candidate = base + "-" + strconv.Itoa(i)
	}
	// Pathological collision: fall back to a random suffix.
	candidate = base + "-" + randSuffix()
	exists, err := s.Store.SlugExists(ctx, candidate)
	if err != nil {
		return "", err
	}
	if exists {
		return "", ErrSlugUnavailable
	}
	return candidate, nil
}

// Slugify reduces s to a lowercase URL-safe slug: a-z0-9 kept, every other run
// of characters collapses to a single '-', leading/trailing '-' trimmed, capped
// at 40 chars.
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}

func randSuffix() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	}
	return hex.EncodeToString(b)
}

func newOrgID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "org-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "org-" + hex.EncodeToString(b)
}

// ──────────────────────────────────────────────────────────────────────────
// SQLStore implementation of OrgStore
// ──────────────────────────────────────────────────────────────────────────

// CreateOrg implements OrgStore.
func (s *SQLStore) CreateOrg(ctx context.Context, rec OrgRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("orgadmin: create org begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	_, err = tx.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO orgs (id, slug, name, owner_account, root_mailbox, mailbox_domain, mailbox_state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		rec.ID, rec.Slug, rec.Name, rec.OwnerAccount, rec.RootMailbox, rec.MailboxDomain, rec.MailboxState, rec.CreatedAt,
	)
	if err != nil {
		if cpdb.IsUniqueViolation(err) && strings.Contains(err.Error(), "slug") {
			return ErrSlugUnavailable
		}
		return fmt.Errorf("orgadmin: create org insert: %w", err)
	}
	_, err = tx.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO org_membership (org_id, account_id, role, joined_at)
		VALUES (?, ?, 'owner', ?)
		ON CONFLICT(org_id, account_id) DO UPDATE SET role = excluded.role`),
		rec.ID, rec.OwnerAccount, rec.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("orgadmin: create org owner: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("orgadmin: create org commit: %w", err)
	}
	return nil
}

// CreateOrgAtomic implements OrgStore. COUNTCAP-TOCTOU-01: the anti-farming cap,
// the rate-limit window and the once-per-account free-mailbox grant are all
// re-derived from the account's live owned-count INSIDE the insert transaction,
// serialised per-account, so concurrent creates (even across replicas on shared
// Postgres) cannot exceed the cap or mint a second free mailbox.
//
// Cross-replica correctness: on Postgres a transaction-scoped advisory lock keyed
// on the owner account serialises all concurrent creates for that account across
// every process — under READ COMMITTED each waiter re-reads the count only after
// the prior committer's insert is visible, so the count → decide → insert sequence
// is effectively atomic per account. On SQLite the single-writer pool
// (MaxOpenConns=1) + the process mutex already serialise write transactions, so no
// advisory lock is needed (and the syntax is unsupported); self-host stays correct.
func (s *SQLStore) CreateOrgAtomic(ctx context.Context, rec OrgRecord, gate OrgCreateGate) (OrgRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OrgRecord{}, fmt.Errorf("orgadmin: create org atomic begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Per-account serialisation across replicas (Postgres only; SQLite serialises
	// writers already). hashtext maps the account id to the advisory lock's bigint
	// key; a hash collision only causes rare, harmless extra contention.
	if s.db.Backend() == cpdb.BackendPostgres {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, rec.OwnerAccount); err != nil {
			return OrgRecord{}, fmt.Errorf("orgadmin: create org atomic lock: %w", err)
		}
	}

	// Re-count owned orgs under the lock — this is the authoritative count.
	var owned int
	if err := tx.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM orgs WHERE owner_account = ?`), rec.OwnerAccount,
	).Scan(&owned); err != nil {
		return OrgRecord{}, fmt.Errorf("orgadmin: create org atomic count: %w", err)
	}

	// The FIRST org is always allowed (signup must never fail); caps/rate-limits
	// apply only to additional orgs.
	if owned >= 1 {
		if gate.Cap > 0 && owned >= gate.Cap {
			return OrgRecord{}, ErrOrgLimitReached
		}
		if gate.MaxPerWindow > 0 && gate.WindowSince != "" {
			var recent int
			if err := tx.QueryRowContext(ctx,
				s.db.Rebind(`SELECT COUNT(*) FROM orgs WHERE owner_account = ? AND created_at >= ?`),
				rec.OwnerAccount, gate.WindowSince,
			).Scan(&recent); err != nil {
				return OrgRecord{}, fmt.Errorf("orgadmin: create org atomic window count: %w", err)
			}
			if recent >= gate.MaxPerWindow {
				return OrgRecord{}, ErrOrgLimitReached
			}
		}
	}

	// Free-mailbox eligibility decided atomically from the live count: the account's
	// FIRST org (owned==0), or any org on a paid plan.
	if gate.MailDomain != "" && (owned == 0 || gate.PaidPlan) {
		rec.RootMailbox = rec.Slug + "@" + gate.MailDomain
		rec.MailboxDomain = gate.MailDomain
		rec.MailboxState = MailboxPending
	}

	if _, err := tx.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO orgs (id, slug, name, owner_account, root_mailbox, mailbox_domain, mailbox_state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		rec.ID, rec.Slug, rec.Name, rec.OwnerAccount, rec.RootMailbox, rec.MailboxDomain, rec.MailboxState, rec.CreatedAt,
	); err != nil {
		if cpdb.IsUniqueViolation(err) && strings.Contains(err.Error(), "slug") {
			return OrgRecord{}, ErrSlugUnavailable
		}
		return OrgRecord{}, fmt.Errorf("orgadmin: create org atomic insert: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO org_membership (org_id, account_id, role, joined_at)
		VALUES (?, ?, 'owner', ?)
		ON CONFLICT(org_id, account_id) DO UPDATE SET role = excluded.role`),
		rec.ID, rec.OwnerAccount, rec.CreatedAt,
	); err != nil {
		return OrgRecord{}, fmt.Errorf("orgadmin: create org atomic owner: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return OrgRecord{}, fmt.Errorf("orgadmin: create org atomic commit: %w", err)
	}
	return rec, nil
}

// GetOrg implements OrgStore.
func (s *SQLStore) GetOrg(ctx context.Context, id string) (OrgRecord, error) {
	var rec OrgRecord
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT id, slug, name, owner_account, root_mailbox, mailbox_domain, mailbox_state, created_at
		  FROM orgs WHERE id = ?`), id,
	).Scan(&rec.ID, &rec.Slug, &rec.Name, &rec.OwnerAccount, &rec.RootMailbox, &rec.MailboxDomain, &rec.MailboxState, &rec.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OrgRecord{}, ErrNotFound
	}
	if err != nil {
		return OrgRecord{}, fmt.Errorf("orgadmin: get org: %w", err)
	}
	return rec, nil
}

// SlugExists implements OrgStore.
func (s *SQLStore) SlugExists(ctx context.Context, slug string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT 1 FROM orgs WHERE slug = ?`), slug).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("orgadmin: slug exists: %w", err)
	}
	return true, nil
}

// AddOrgMember implements OrgStore.
func (s *SQLStore) AddOrgMember(ctx context.Context, orgID, accountID, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO org_membership (org_id, account_id, role, joined_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(org_id, account_id) DO UPDATE SET role = excluded.role`),
		orgID, accountID, role, nowUTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("orgadmin: add org member: %w", err)
	}
	return nil
}

// OrgMemberRole implements OrgStore.
func (s *SQLStore) OrgMemberRole(ctx context.Context, orgID, accountID string) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT role FROM org_membership WHERE org_id = ? AND account_id = ?`), orgID, accountID,
	).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("orgadmin: org member role: %w", err)
	}
	return role, nil
}

// ListOrgsForAccount implements OrgStore.
func (s *SQLStore) ListOrgsForAccount(ctx context.Context, accountID string) ([]OrgMembership, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT o.id, o.slug, o.name, o.owner_account, o.root_mailbox, o.mailbox_domain, o.mailbox_state, o.created_at, m.role
		  FROM org_membership m
		  JOIN orgs o ON o.id = m.org_id
		 WHERE m.account_id = ?
		 ORDER BY o.created_at, o.id`), accountID,
	)
	if err != nil {
		return nil, fmt.Errorf("orgadmin: list orgs for account: %w", err)
	}
	defer rows.Close()
	out := []OrgMembership{}
	for rows.Next() {
		var m OrgMembership
		if err := rows.Scan(&m.ID, &m.Slug, &m.Name, &m.OwnerAccount, &m.RootMailbox,
			&m.MailboxDomain, &m.MailboxState, &m.CreatedAt, &m.Role); err != nil {
			return nil, fmt.Errorf("orgadmin: scan org membership: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountOrgsOwnedByAccount implements OrgStore.
func (s *SQLStore) CountOrgsOwnedByAccount(ctx context.Context, accountID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM orgs WHERE owner_account = ?`), accountID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("orgadmin: count orgs owned: %w", err)
	}
	return n, nil
}

// CountOrgsOwnedSince implements OrgStore. created_at is stored as RFC3339 UTC,
// so a lexicographic >= comparison is a correct chronological comparison.
func (s *SQLStore) CountOrgsOwnedSince(ctx context.Context, accountID, sinceRFC3339 string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM orgs WHERE owner_account = ? AND created_at >= ?`), accountID, sinceRFC3339,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("orgadmin: count orgs owned since: %w", err)
	}
	return n, nil
}

// SetMailboxState implements OrgStore.
func (s *SQLStore) SetMailboxState(ctx context.Context, orgID, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`UPDATE orgs SET mailbox_state = ? WHERE id = ?`), state, orgID)
	if err != nil {
		return fmt.Errorf("orgadmin: set mailbox state: %w", err)
	}
	return nil
}

// SetActiveOrg implements OrgStore.
func (s *SQLStore) SetActiveOrg(ctx context.Context, accountID, orgID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO user_active_org (account_id, org_id, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET org_id = excluded.org_id, updated_at = excluded.updated_at`),
		accountID, orgID, nowUTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("orgadmin: set active org: %w", err)
	}
	return nil
}

// GetActiveOrg implements OrgStore.
func (s *SQLStore) GetActiveOrg(ctx context.Context, accountID string) (string, error) {
	var orgID string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT org_id FROM user_active_org WHERE account_id = ?`), accountID,
	).Scan(&orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("orgadmin: get active org: %w", err)
	}
	return orgID, nil
}

// RemoveOrgMember implements OrgStore. Idempotent: no error when the row does
// not exist.
func (s *SQLStore) RemoveOrgMember(ctx context.Context, orgID, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM org_membership WHERE org_id = ? AND account_id = ?`),
		orgID, accountID,
	)
	if err != nil {
		return fmt.Errorf("orgadmin: remove org member: %w", err)
	}
	return nil
}

// CountOrgOwners implements OrgStore.
func (s *SQLStore) CountOrgOwners(ctx context.Context, orgID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM org_membership WHERE org_id = ? AND role = 'owner'`), orgID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("orgadmin: count org owners: %w", err)
	}
	return n, nil
}

// compile-time assertions.
var (
	_ OrgStore = (*SQLStore)(nil)
	_ OrgStore = (*MemOrgStore)(nil)
)
