package orgadmin

import (
	"context"
	"errors"
	"strings"
)

// Sentinel errors mapped to HTTP status codes by routes.go. Cross-tenant and
// unknown-resource cases collapse to ErrNotFound (→ 404) so we never disclose
// the existence of another tenant's resource (matching customdomain/meetalloc).
var (
	// ErrNotFound — the resource does not exist or is owned by another tenant.
	ErrNotFound = errors.New("orgadmin: not found")
	// ErrInvalidRole — the supplied role is not one of owner/admin/billing-admin/member.
	ErrInvalidRole = errors.New("orgadmin: invalid role")
	// ErrInvalidInput — generic bad request (empty email, bad backup mode, …).
	ErrInvalidInput = errors.New("orgadmin: invalid input")
	// ErrForbidden — the caller's role does not permit the requested action
	// (RBAC-ORG-01). Mapped to HTTP 403 by routes.go.
	ErrForbidden = errors.New("orgadmin: forbidden")
)

// ──────────────────────────────────────────────────────────────────────────
// Reader / writer seams. main.go adapts the real subsystems onto these.
// ──────────────────────────────────────────────────────────────────────────

// MemberStore backs the Members tab. Implementations adapt the fleet store
// (internal/fleet) which models orgs/members/invites. The tenant id is the
// session-resolved account; the adapter maps it to the caller's org.
type MemberStore interface {
	// ListMembers returns every seat in the caller's org.
	ListMembers(ctx context.Context, tenantID string) ([]Member, error)
	// Invite creates a pending invite carrying an optional display name (empty =
	// no name). Returns ErrInviteStoreUnavailable when the backing cannot issue
	// invites (the service then falls back to its own InviteStore if one is
	// wired).
	Invite(ctx context.Context, tenantID, email, role, name string) (InviteResponse, error)
	// RemoveMember removes the member identified by memberID from the caller's
	// org. Cross-tenant / unknown → ErrNotFound.
	RemoveMember(ctx context.Context, tenantID, memberID string) error
	// SetRole changes a member's role. Cross-tenant / unknown → ErrNotFound;
	// bad role → ErrInvalidRole.
	SetRole(ctx context.Context, tenantID, memberID, role string) error
	// SetMemberName sets (or clears, when name is empty) a member's fleet-local
	// display name. Cross-tenant / unknown member → ErrNotFound. This is the
	// directly-callable path backing PUT /api/org/members/{id}/name; it exists
	// because the codebase has no invite-accept→member flow at which to apply the
	// invite's stored name automatically.
	SetMemberName(ctx context.Context, tenantID, memberID, name string) error
}

// InstanceLister backs the Instances tab. The adapter fans out to servingpool
// (pooled + dedicated) and the BYO self-host registry (multiloc + lancert).
type InstanceLister interface {
	ListInstances(ctx context.Context, tenantID string) (InstancesResponse, error)
}

// UsageReader backs the Usage tab. The adapter pulls active-users (billing/
// activeuser), gpu-hours (gpusku), meet minutes (meetalloc) and storage.
type UsageReader interface {
	Usage(ctx context.Context, tenantID string) (UsageResponse, error)
}

// QuotaReader backs the Quotas tab. The adapter reads tier (billing) + caps
// (billing.QuotaForTier) and current consumption.
type QuotaReader interface {
	Quotas(ctx context.Context, tenantID string) (QuotasResponse, error)
}

// BillingSummarizer backs GET /api/billing/summary. The adapter reads the
// subscription, wallet balance and exchange rate from the billing store.
type BillingSummarizer interface {
	Summary(ctx context.Context, tenantID string) (BillingSummaryResponse, error)
}

// ──────────────────────────────────────────────────────────────────────────
// Service
// ──────────────────────────────────────────────────────────────────────────

// Service composes the reader seams + the package-owned backup-mode/invite
// store into the org-admin plane. Any seam MAY be nil; a method whose seam is
// nil returns a structurally-correct empty response (so the dashboard degrades
// gracefully rather than erroring) — except writes, which require their store.
type Service struct {
	Members   MemberStore
	Instances InstanceLister
	UsageR    UsageReader
	QuotaR    QuotaReader
	Billing   BillingSummarizer

	// Backup is the package-owned backup-mode store (required for the Backup
	// tab GET/PUT). Invite is an optional standalone invite store used only
	// when Members.Invite reports ErrInviteStoreUnavailable.
	Backup BackupModeStore
	Invite InviteStore

	// Products persists per-org product-status overrides (PRODUCTS-ADMIN-01). When
	// nil the GET /api/products read path serves catalogue defaults only and the
	// PATCH write path reports ErrNotFound (fail closed — no silent no-op accept).
	Products ProductStatusStore

	// Roles resolves the CALLER's role within a tenant for RBAC-ORG-01 checks.
	// When nil the caller is treated as owner (legacy behaviour) so RBAC is
	// additive and only enforced once wired. Set in main.go from the fleet store.
	Roles RoleResolver

	// AuditR reads the caller-org's admin audit trail (ORGADMIN-AUDIT-01). OPTIONAL
	// — when nil GET /api/org/audit returns an empty page (the console shows an
	// empty state). Set in main.go from internal/auditlog. Always own-org scoped.
	AuditR AuditReader
}

// callerRole resolves the caller's role within the tenant for an RBAC decision.
// When no RoleResolver is wired, the caller is treated as owner (legacy: the
// session user IS the tenant owner under the 1:1 account==tenant model). A
// caller who is not a member of the tenant is RoleMember (least privilege) so a
// stray session can never act on another tenant — combined with the
// tenant-scoping this fails closed.
func (s *Service) callerRole(ctx context.Context, tenantID, callerAccountID string) Role {
	if s.Roles == nil || callerAccountID == "" {
		return RoleOwner
	}
	role, err := s.Roles.RoleInOrg(ctx, tenantID, callerAccountID)
	if err != nil {
		return RoleMember
	}
	return ParseRole(role)
}

// Authorize reports nil when the caller (resolved to a role within tenantID) is
// permitted to perform action, or ErrForbidden otherwise. Exposed so routes.go
// gates each mutation before touching the store.
func (s *Service) Authorize(ctx context.Context, tenantID, callerAccountID string, action Action) error {
	if Can(s.callerRole(ctx, tenantID, callerAccountID), action) {
		return nil
	}
	return ErrForbidden
}

// validProductStatuses is the closed set the Workspace shell's normalizeProduct
// contract accepts (productsAdmin.js PRODUCT_STATUSES). Any other value is
// rejected with ErrInvalidInput.
var validProductStatuses = map[string]bool{
	"configured": true,
	"available":  true,
	"off":        true,
}

// ValidProductStatus reports whether status is one of configured|available|off.
func ValidProductStatus(status string) bool { return validProductStatuses[status] }

// ProductStatuses returns the tenant's product-status overrides (productID →
// status). Read-only, tenant-scoped; any member may read (the GET /api/products
// read path is not an admin action). No store wired / no rows ⇒ empty map.
func (s *Service) ProductStatuses(ctx context.Context, tenantID string) (map[string]string, error) {
	if s.Products == nil {
		return map[string]string{}, nil
	}
	return s.Products.ProductStatuses(ctx, tenantID)
}

// SetProductStatus authorizes (ActionProductStatus — owner/admin only), validates
// the status against the closed set, and persists the per-org override. The
// caller (routes.go / the products route) must have already validated productID
// against the live catalogue. Returns:
//   - ErrForbidden    when the caller's role is not owner/admin (→ 403).
//   - ErrInvalidInput when status is not configured|available|off (→ 400).
//   - ErrNotFound     when no product-status store is wired (→ 404, fail closed).
//
// Idempotent: re-setting the same status succeeds without observable change.
func (s *Service) SetProductStatus(ctx context.Context, tenantID, callerAccountID, productID, status string) error {
	if err := s.Authorize(ctx, tenantID, callerAccountID, ActionProductStatus); err != nil {
		return err
	}
	if !ValidProductStatus(status) {
		return ErrInvalidInput
	}
	if s.Products == nil {
		// No backing store — fail closed rather than silently accepting a write the
		// GET path could never reflect.
		return ErrNotFound
	}
	return s.Products.SetProductStatus(ctx, tenantID, productID, status)
}

// NewService constructs a Service. All fields may be nil and set afterwards;
// the constructor exists for symmetry with the other planes.
func NewService(members MemberStore, instances InstanceLister, usage UsageReader,
	quotas QuotaReader, billing BillingSummarizer, backup BackupModeStore, invite InviteStore) *Service {
	return &Service{
		Members:   members,
		Instances: instances,
		UsageR:    usage,
		QuotaR:    quotas,
		Billing:   billing,
		Backup:    backup,
		Invite:    invite,
	}
}

// ── Members ──────────────────────────────────────────────────────────────

// ListMembers returns the caller-org's seats.
func (s *Service) ListMembers(ctx context.Context, tenantID string) ([]Member, error) {
	if s.Members == nil {
		return []Member{}, nil
	}
	out, err := s.Members.ListMembers(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Member{}
	}
	return out, nil
}

// CreateInvite issues a pending invite for (email, role, name). name is OPTIONAL
// (trimmed; empty = no name). Prefers the MemberStore (fleet) backing; falls
// back to the package-owned InviteStore when the MemberStore can't issue
// invites. The name is persisted on the invite so it can be applied to the
// member's display name when the seat is created.
func (s *Service) CreateInvite(ctx context.Context, tenantID, email, role, name string) (InviteResponse, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return InviteResponse{}, ErrInvalidInput
	}
	role = normaliseRole(role)
	if !validRole(role) {
		return InviteResponse{}, ErrInvalidRole
	}
	// PRIV-ESC (M1): an invite must NEVER assign the owner role. The invite action
	// is gated by ActionMemberInvite, which an ADMIN holds — but admins are
	// explicitly barred from the destructive member.role / member.remove actions.
	// Without this an admin could invite an owner-role email they control and
	// accept it, escalating themselves to owner (latent under the 1:1
	// account==org model; live once multi-user orgs where org_id != account_id
	// ship). Ownership is established at org creation and transferred through a
	// dedicated owner-only path, never minted by an invite.
	if ParseRole(role) == RoleOwner {
		return InviteResponse{}, ErrInvalidRole
	}
	name = strings.TrimSpace(name)
	if s.Members != nil {
		inv, err := s.Members.Invite(ctx, tenantID, email, role, name)
		if err == nil {
			return inv, nil
		}
		if !errors.Is(err, ErrInviteStoreUnavailable) {
			return InviteResponse{}, err
		}
		// fall through to the standalone invite store.
	}
	if s.Invite != nil {
		return s.Invite.CreateInvite(ctx, tenantID, email, role, name)
	}
	return InviteResponse{}, ErrInviteStoreUnavailable
}

// SetMemberName sets (or clears, when name is empty) a member's fleet-local
// display name. This is the directly-callable path backing PUT
// /api/org/members/{id}/name; it exists because the codebase has no
// invite-accept→member flow at which the invite's stored name is applied
// automatically. The name is trimmed; an empty result clears the name so the
// Members tab falls back to email.
func (s *Service) SetMemberName(ctx context.Context, tenantID, memberID, name string) error {
	if s.Members == nil {
		return ErrNotFound
	}
	if strings.TrimSpace(memberID) == "" {
		return ErrInvalidInput
	}
	return s.Members.SetMemberName(ctx, tenantID, memberID, strings.TrimSpace(name))
}

// RemoveMember removes a member from the caller's org.
func (s *Service) RemoveMember(ctx context.Context, tenantID, memberID string) error {
	if s.Members == nil {
		return ErrNotFound
	}
	if strings.TrimSpace(memberID) == "" {
		return ErrInvalidInput
	}
	return s.Members.RemoveMember(ctx, tenantID, memberID)
}

// ChangeRole changes a member's role within the caller's org.
func (s *Service) ChangeRole(ctx context.Context, tenantID, memberID, role string) error {
	if s.Members == nil {
		return ErrNotFound
	}
	if strings.TrimSpace(memberID) == "" {
		return ErrInvalidInput
	}
	role = normaliseRole(role)
	if !validRole(role) {
		return ErrInvalidRole
	}
	return s.Members.SetRole(ctx, tenantID, memberID, role)
}

// ── Instances / Usage / Quotas / Billing (read-through) ────────────────────

// ListInstances returns the caller-org's bundle nodes.
func (s *Service) ListInstances(ctx context.Context, tenantID string) (InstancesResponse, error) {
	empty := InstancesResponse{
		Pooled:    []PooledInstance{},
		Dedicated: []DedicatedInstance{},
		BYO:       []BYOInstance{},
	}
	if s.Instances == nil {
		return empty, nil
	}
	out, err := s.Instances.ListInstances(ctx, tenantID)
	if err != nil {
		return InstancesResponse{}, err
	}
	if out.Pooled == nil {
		out.Pooled = []PooledInstance{}
	}
	if out.Dedicated == nil {
		out.Dedicated = []DedicatedInstance{}
	}
	if out.BYO == nil {
		out.BYO = []BYOInstance{}
	}
	return out, nil
}

// Usage returns the caller-org's current-period usage.
func (s *Service) Usage(ctx context.Context, tenantID string) (UsageResponse, error) {
	if s.UsageR == nil {
		return UsageResponse{GPUHours: GPUHours{}}, nil
	}
	return s.UsageR.Usage(ctx, tenantID)
}

// Quotas returns the caller-org's tier quotas + consumption.
func (s *Service) Quotas(ctx context.Context, tenantID string) (QuotasResponse, error) {
	if s.QuotaR == nil {
		return QuotasResponse{Tier: "free", Items: []QuotaItem{}}, nil
	}
	out, err := s.QuotaR.Quotas(ctx, tenantID)
	if err != nil {
		return QuotasResponse{}, err
	}
	if out.Items == nil {
		out.Items = []QuotaItem{}
	}
	return out, nil
}

// BillingSummary returns the caller-org's billing snapshot.
func (s *Service) BillingSummary(ctx context.Context, tenantID string) (BillingSummaryResponse, error) {
	if s.Billing == nil {
		return BillingSummaryResponse{}, nil
	}
	return s.Billing.Summary(ctx, tenantID)
}

// ── Backup mode ────────────────────────────────────────────────────────────

// GetBackupMode returns the caller-org's backup-mode setting.
func (s *Service) GetBackupMode(ctx context.Context, tenantID string) (BackupModeResponse, error) {
	if s.Backup == nil {
		return BackupModeResponse{Mode: BackupModeCentral, PerLocation: []PerLocationMode{}}, nil
	}
	out, err := s.Backup.GetBackupMode(ctx, tenantID)
	if err != nil {
		return BackupModeResponse{}, err
	}
	if out.PerLocation == nil {
		out.PerLocation = []PerLocationMode{}
	}
	if out.Mode == "" {
		out.Mode = BackupModeCentral
	}
	return out, nil
}

// SetBackupMode persists the caller-org's backup-mode setting after validating
// the top-level mode and each per-location mode.
func (s *Service) SetBackupMode(ctx context.Context, tenantID string, req BackupModeRequest) (BackupModeResponse, error) {
	if !validBackupMode(req.Mode) {
		return BackupModeResponse{}, ErrInvalidInput
	}
	for _, loc := range req.PerLocation {
		// Empty per-location mode means "inherit the org default" — allowed.
		if loc.Mode != "" && !validBackupMode(loc.Mode) {
			return BackupModeResponse{}, ErrInvalidInput
		}
	}
	if s.Backup == nil {
		// No store wired — surface as not-found so the route returns 503-ish
		// 500 via the default branch rather than silently succeeding.
		return BackupModeResponse{}, errors.New("orgadmin: backup-mode store not configured")
	}
	if req.PerLocation == nil {
		req.PerLocation = []PerLocationMode{}
	}
	if err := s.Backup.SetBackupMode(ctx, tenantID, req); err != nil {
		return BackupModeResponse{}, err
	}
	// Return the freshly-read state so the PUT round-trips like the JSX expects.
	return s.GetBackupMode(ctx, tenantID)
}

// ──────────────────────────────────────────────────────────────────────────
// Validation helpers
// ──────────────────────────────────────────────────────────────────────────

// normaliseRole lower-cases + trims a role so the title-case values the JSX
// sends ("Owner"/"Admin"/"Member") match the fleet store's lower-case set.
func normaliseRole(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}

// validRole reports whether role names a real assignable org role. Now includes
// billing-admin (RBAC-ORG-01). Delegates to the RBAC parser so the accepted set
// is defined in exactly one place.
func validRole(role string) bool {
	return validRoleRBAC(role)
}

func validBackupMode(mode string) bool {
	switch mode {
	case BackupModeCentral, BackupModeLocal:
		return true
	default:
		return false
	}
}
