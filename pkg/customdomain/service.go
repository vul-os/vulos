package customdomain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"strings"

	"github.com/vul-os/vulos-management/pkg/env"
)

// DomainAttacher is the narrow seam the service uses to attach / detach a
// custom domain on the underlying compute provider. compute.Provider does not
// currently include an AttachDomain method — production wiring passes a
// concrete adapter (see FlyAttacher) so the contract here can stay independent
// of the broader Provider interface.
type DomainAttacher interface {
	// AttachDomain attaches `domain` to the tenant's bundle-node service.
	// Returns the ACME status string on success ("pending" | "issued" | "...")
	// AND the DNS validation target the customer must point their domain at
	// (validationTarget — the provider's CNAME / ACME challenge hostname; may
	// be empty when the provider does not return one). An error is returned if
	// the provider rejected the call.
	AttachDomain(ctx context.Context, tenantID, domain string) (acmeStatus, validationTarget string, err error)
	// DetachDomain removes the attachment. Idempotent: detaching an
	// already-detached domain returns nil.
	DetachDomain(ctx context.Context, tenantID, domain string) error
}

// BundleNodeResolver maps a tenant to its bundle-node service ref. This is
// the seam the reverse-proxy front uses via Service.RouteFor.
type BundleNodeResolver interface {
	BundleNodeForTenant(ctx context.Context, tenantID string) (BundleNodeRef, error)
}

// DNSResolver is the seam VerifyDNS uses to query TXT records. The default
// implementation wraps net.DefaultResolver; tests inject a fake.
type DNSResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// NetResolver is the production DNSResolver — thin shim over net.DefaultResolver.
type NetResolver struct{}

// LookupTXT implements DNSResolver.
func (NetResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	return net.DefaultResolver.LookupTXT(ctx, name)
}

// Service composes a Store, Attacher, BundleNodeResolver and DNSResolver into
// the public custom-domain plane.
type Service struct {
	Store       Store
	Attacher    DomainAttacher
	NodeLookup  BundleNodeResolver
	DNSResolver DNSResolver
}

// NewService constructs a Service. Nil DNSResolver falls back to NetResolver.
func NewService(s Store, a DomainAttacher, n BundleNodeResolver, dns DNSResolver) *Service {
	if dns == nil {
		dns = NetResolver{}
	}
	return &Service{Store: s, Attacher: a, NodeLookup: n, DNSResolver: dns}
}

// VerificationRecordName returns the TXT record name customers must publish.
func VerificationRecordName(domain string) string {
	return "_vulos-verify." + strings.ToLower(strings.TrimSuffix(domain, "."))
}

// RequestVerification (re)issues a verification token for (tenant, domain) and
// stores the row in StatePending. Returns the customer-facing instruction.
// Idempotent on (domain) — a second call regenerates the token and resets the
// state to pending; it returns ErrTenantMismatch if the existing row is owned
// by a different tenant.
func (s *Service) RequestVerification(ctx context.Context, tenantID, domain string) (VerificationInstruction, error) {
	domain = normaliseDomain(domain)
	if !validDomain(domain) {
		return VerificationInstruction{}, ErrInvalidDomain
	}
	if tenantID == "" {
		return VerificationInstruction{}, ErrTenantMismatch
	}

	existing, err := s.Store.Get(ctx, domain)
	if err == nil && existing.TenantID != tenantID {
		return VerificationInstruction{}, ErrTenantMismatch
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return VerificationInstruction{}, err
	}

	token, err := newToken()
	if err != nil {
		return VerificationInstruction{}, err
	}
	row := CustomDomain{
		Domain:      domain,
		TenantID:    tenantID,
		VerifyToken: token,
		VerifyState: StatePending,
	}
	if existing.Domain != "" {
		// Preserve original CreatedAt across re-issuance.
		row.CreatedAt = existing.CreatedAt
		row.BundleNode = existing.BundleNode
	}
	if err := s.Store.Upsert(ctx, row); err != nil {
		return VerificationInstruction{}, err
	}
	return VerificationInstruction{
		RecordName: VerificationRecordName(domain),
		RecordType: "TXT",
		Token:      token,
	}, nil
}

// VerifyDNS performs a TXT lookup at the verification record name. On a match
// the row is moved to StateVerified and verified_at is stamped. If the row is
// already in StateAttached / StateActive the call is a no-op.
func (s *Service) VerifyDNS(ctx context.Context, domain string) (CustomDomain, error) {
	domain = normaliseDomain(domain)
	if !validDomain(domain) {
		return CustomDomain{}, ErrInvalidDomain
	}
	row, err := s.Store.Get(ctx, domain)
	if err != nil {
		return CustomDomain{}, err
	}
	if row.VerifyState == StateAttached || row.VerifyState == StateActive {
		return row, nil
	}

	recordName := VerificationRecordName(domain)
	txts, lookupErr := s.DNSResolver.LookupTXT(ctx, recordName)
	if lookupErr != nil {
		row.LastError = "dns lookup: " + lookupErr.Error()
		_ = s.Store.Upsert(ctx, row)
		return row, ErrVerificationFailed
	}
	matched := false
	for _, t := range txts {
		if strings.TrimSpace(t) == row.VerifyToken {
			matched = true
			break
		}
	}
	if !matched {
		row.LastError = "verification token not found in TXT records"
		_ = s.Store.Upsert(ctx, row)
		return row, ErrVerificationFailed
	}

	row.VerifyState = StateVerified
	now := nowUTC()
	row.VerifiedAt = &now
	row.LastError = ""
	if err := s.Store.Upsert(ctx, row); err != nil {
		return CustomDomain{}, err
	}
	return row, nil
}

// Attach moves the row from StateVerified → StateAttached by calling the
// underlying compute provider (Fly). Returns ErrNotVerified when the row is
// still pending; ErrAlreadyAttached when an already-attached row is passed.
// On success ACMEStatus and IntendedCNAME are set from the provider's reply
// and attached_at is stamped.
func (s *Service) Attach(ctx context.Context, tenantID, domain string) (CustomDomain, error) {
	domain = normaliseDomain(domain)
	row, err := s.Store.Get(ctx, domain)
	if err != nil {
		return CustomDomain{}, err
	}
	if row.TenantID != tenantID {
		return CustomDomain{}, ErrTenantMismatch
	}
	switch row.VerifyState {
	case StatePending:
		return row, ErrNotVerified
	case StateAttached, StateActive:
		return row, ErrAlreadyAttached
	}

	if s.Attacher == nil {
		row.LastError = "no attacher configured"
		_ = s.Store.Upsert(ctx, row)
		return row, errors.New("customdomain: attacher not configured")
	}
	acme, validationTarget, err := s.Attacher.AttachDomain(ctx, tenantID, domain)
	if err != nil {
		row.LastError = "attach: " + err.Error()
		_ = s.Store.Upsert(ctx, row)
		return row, err
	}
	// Record which bundle node owns this attachment so RouteFor can stamp
	// the upstream without re-querying the resolver each request.
	if s.NodeLookup != nil {
		if ref, lerr := s.NodeLookup.BundleNodeForTenant(ctx, tenantID); lerr == nil {
			row.BundleNode = ref.App
		}
	}
	now := nowUTC()
	row.AttachedAt = &now
	row.ACMEStatus = acme
	// Surface the DNS validation target so the UI can tell the customer exactly
	// what CNAME to create. Only overwrite when the provider returned one.
	if validationTarget != "" {
		row.IntendedCNAME = validationTarget
	}
	row.VerifyState = StateAttached
	row.LastError = ""
	if err := s.Store.Upsert(ctx, row); err != nil {
		return CustomDomain{}, err
	}
	return row, nil
}

// Detach calls the provider to detach the domain and updates the row to
// StateDetached. Idempotent: a no-op when the row is already detached.
func (s *Service) Detach(ctx context.Context, tenantID, domain string) error {
	domain = normaliseDomain(domain)
	row, err := s.Store.Get(ctx, domain)
	if err != nil {
		return err
	}
	if row.TenantID != tenantID {
		return ErrTenantMismatch
	}
	if row.VerifyState == StateDetached {
		return nil
	}
	if s.Attacher != nil {
		if err := s.Attacher.DetachDomain(ctx, tenantID, domain); err != nil {
			row.LastError = "detach: " + err.Error()
			_ = s.Store.Upsert(ctx, row)
			return err
		}
	}
	row.VerifyState = StateDetached
	row.LastError = ""
	if err := s.Store.Upsert(ctx, row); err != nil {
		return err
	}
	return nil
}

// List returns every custom domain owned by tenantID.
func (s *Service) List(ctx context.Context, tenantID string) ([]CustomDomain, error) {
	return s.Store.ListByTenant(ctx, tenantID)
}

// RouteFor resolves an inbound Host header to (tenantID, BundleNodeRef). Used
// by the reverse-proxy front to stamp the correct upstream for non-vulos.org
// hosts. Returns ErrNotFound for unknown / un-attached domains.
func (s *Service) RouteFor(ctx context.Context, domain string) (string, BundleNodeRef, error) {
	domain = normaliseDomain(domain)
	row, err := s.Store.Get(ctx, domain)
	if err != nil {
		return "", BundleNodeRef{}, err
	}
	if row.VerifyState != StateAttached && row.VerifyState != StateActive {
		return "", BundleNodeRef{}, ErrNotFound
	}
	var ref BundleNodeRef
	if s.NodeLookup != nil {
		if r, err := s.NodeLookup.BundleNodeForTenant(ctx, row.TenantID); err == nil {
			ref = r
		}
	}
	if ref.App == "" {
		ref.App = row.BundleNode
	}
	return row.TenantID, ref, nil
}

// ──────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "vulos-verify-" + hex.EncodeToString(b), nil
}

func normaliseDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimSuffix(d, ".")
	return d
}

// reservedSuffixes — these are served by the existing wildcard plane; we do
// NOT want operators accidentally re-routing tenant traffic through the
// custom-domain plane.
var reservedSuffixes = []string{
	".vulos.org",
	".vulos.cloud",
}

func validDomain(d string) bool {
	if d == "" {
		return false
	}
	if strings.ContainsAny(d, " \t\r\n") {
		return false
	}
	if !strings.Contains(d, ".") {
		return false
	}
	if len(d) > 253 {
		return false
	}
	for _, suf := range reservedSuffixes {
		if strings.HasSuffix(d, suf) {
			return false
		}
	}
	// Reject obviously bad characters; full RFC-1035 validation is overkill
	// here — the verification TXT check is the load-bearing guard.
	for _, r := range d {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

// staticAttacher is a built-in DomainAttacher useful for dev mode and the
// MemStore fallback path. It records calls in-memory without contacting any
// provider — the real production wiring uses FlyAttacher (see fly_attacher.go).
type staticAttacher struct{}

// NewStaticAttacher returns a DomainAttacher suitable for dev / MemStore mode.
// AttachDomain always returns ACME status "pending", an empty validation
// target, and DetachDomain is a no-op success.
func NewStaticAttacher() DomainAttacher { return staticAttacher{} }

func (staticAttacher) AttachDomain(_ context.Context, tenantID, domain string) (string, string, error) {
	// WAVE34-4: defence in depth. main.go refuses to wire the static attacher in
	// prod, but if it is ever reached there (misconfiguration), warn LOUDLY on every
	// call rather than silently reporting "pending" while no cert is provisioned.
	if env.IsProd() {
		log.Printf("[customdomain] WARNING (no-op-in-prod): static DomainAttacher.AttachDomain(tenant=%s domain=%s) returned pending but NO certificate was provisioned — FLY_API_TOKEN is missing", tenantID, domain)
	}
	return "pending", "", nil
}
func (staticAttacher) DetachDomain(_ context.Context, _, _ string) error { return nil }
