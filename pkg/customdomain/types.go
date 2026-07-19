// Package customdomain implements the paid-tier custom-domain plane for the
// vulos.cloud control plane (OPS-AUTOSCALER-CUSTOMDOMAIN-01).
//
// What it does:
//   - Customers add a domain (e.g. mail.acme.example) → CP issues a verification
//     TXT token they publish in their DNS zone.
//   - Customer calls verify → CP queries DNS, marks state verified when the
//     token is present.
//   - On verify, CP attaches the domain to the tenant's bundle-node service
//     via a DomainAttacher (FlyAttacher) so the front edge (Fly) issues TLS
//     and routes traffic.
//   - The reverse-proxy front (wire_subdomain_serve.go) calls RouteFor(host) to
//     resolve mail.acme.example → tenant's bundle-node binding.
//
// Persistence: pure-Go modernc/sqlite (CGO_ENABLED=0). One table:
// custom_domains keyed by (domain) with an index on (tenant_id).
//
// CUSTOM-DOMAIN-LEAN-2-RECORD-01: RouteFor/RequestVerification/Attach all key
// on a full domain string, so a customer's "custom domain" is really N
// independently-verified rows under one tenant — there is no built-in notion
// of "the domain" as a single unit. RecommendedCustomDomainLabels below is
// the PRODUCT decision (PINNED CROSS-REPO CONTRACT) that a customer only ever
// needs to add TWO records to get the whole suite on their own domain:
// mail.<domain> (the Mail exception's cloud-central webmail) and
// app.<domain> (the Workspace hub, which embeds every other Class-P app via
// iframe+SSO — see WORKSPACE-INTEGRATION.md) — never one record per
// office./files. subdomain. This constant is consumed by
// the frontend custom-domain settings UI; the backend routing already
// supports any two (or more) domain rows per tenant with zero code changes.
package customdomain

import (
	"errors"
	"time"
)

// RecommendedCustomDomainLabels are the ONLY two subdomain labels a customer
// needs to point at Vulos to run the whole suite on their own domain — the
// lean 2-record model. mail.<domain> serves the Mail-exception webmail client
// (cloud-central always); app.<domain> serves the Workspace hub, which
// embeds every other Class-P app (office/files) via iframe+SSO, so
// those apps need no DNS record of their own on a custom domain.
var RecommendedCustomDomainLabels = []string{"mail", "app"}

// Verification states.
const (
	StatePending  = "pending"  // token issued, DNS not yet verified
	StateVerified = "verified" // TXT record observed; Attach can proceed
	StateAttached = "attached" // Fly certificate created; ACME validation pending
	StateActive   = "active"   // ACME cert issued; routing live
	StateFailed   = "failed"   // permanent failure (e.g. invalid domain)
	StateDetached = "detached" // explicitly removed
)

// Sentinel errors.
var (
	// ErrInvalidDomain — domain was empty, contained whitespace, or used a
	// reserved suffix (vulos.org / vulos.cloud — those land on the existing
	// wildcard, not the customdomain plane).
	ErrInvalidDomain = errors.New("customdomain: invalid domain")
	// ErrNotFound — no row matched the lookup.
	ErrNotFound = errors.New("customdomain: not found")
	// ErrNotVerified — Attach was called before DNS verification succeeded.
	ErrNotVerified = errors.New("customdomain: not verified yet")
	// ErrAlreadyAttached — Attach was called on a row already in attached /
	// active state (callers should detach first).
	ErrAlreadyAttached = errors.New("customdomain: already attached")
	// ErrTenantMismatch — Detach / Attach was called with a tenant that
	// doesn't own the row.
	ErrTenantMismatch = errors.New("customdomain: tenant does not own domain")
	// ErrVerificationFailed — the TXT lookup did not find the expected token.
	ErrVerificationFailed = errors.New("customdomain: verification token missing")
)

// CustomDomain is a single row in the custom-domain plane. One row per
// (domain) — domains are globally unique.
type CustomDomain struct {
	Domain      string `json:"domain"`
	TenantID    string `json:"tenant_id"`
	BundleNode  string `json:"bundle_node,omitempty"`
	VerifyToken string `json:"verify_token"`
	VerifyState string `json:"verify_state"`
	ACMEStatus  string `json:"acme_status,omitempty"`
	// IntendedCNAME is the DNS validation target the customer must point their
	// domain at so the certificate can be issued — populated from the Fly
	// addCertificate response on Attach. For Fly this is typically the app's
	// "<app>.fly.dev" hostname (CNAME target) or an ACME "_acme-challenge"
	// hostname. Surfaced in the domain JSON so the UI can show the user exactly
	// what DNS record to create.
	IntendedCNAME string     `json:"intended_cname,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	AttachedAt    *time.Time `json:"attached_at,omitempty"`
	VerifiedAt    *time.Time `json:"verified_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
}

// VerificationInstruction is what we hand the customer to publish in DNS.
// The TXT record at "_vulos-verify.<domain>" must contain Token.
type VerificationInstruction struct {
	RecordName string `json:"record_name"`
	RecordType string `json:"record_type"`
	Token      string `json:"token"`
}

// BundleNodeRef identifies the tenant's bundle-node service on the compute
// provider. RouteFor returns this so the reverse-proxy front can stamp the
// correct upstream.
type BundleNodeRef struct {
	Provider string `json:"provider"`
	App      string `json:"app"`
	ID       string `json:"id,omitempty"`
}
