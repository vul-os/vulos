// Package resolver resolves the backend target for a logged-in user on
// mail.vulos.org / office.vulos.org / talk.vulos.org / calendar.vulos.org /
// meet.vulos.org.
//
// Decision (per account):
//   - hosted  → our Tigris-backed JMAP endpoint at the cloud service.
//   - selfhost → user has a BYO vulos-mail box registered; the cloud proxies
//     API calls to it over the peering/relay fabric (NAT-friendly outbound
//     tunnel). The fabric route is opaque: the cloud forwards bytes without
//     decrypting the payload (E2E property).
//
// The resolver reads account state from one read-only source:
//   - storage.Store  (BYO=true means the account has a self-managed bucket)
//
// It does NOT write to the store.
//
// ----------------------------------------------------------------------------
// LAN failover (RESOLVE-LAN-01)
// ----------------------------------------------------------------------------
//
// ResolveBackend ALSO returns a LAN endpoint candidate whenever the account
// has a registered box (regardless of hosted vs selfhost-fabric kind). The LAN
// candidate is of the form:
//
//	https://box.<box-id>.lan.vulos.org/jmap/<svc>
//
// where <box-id> is the registered box identifier (today: the account ID).
// LANCERT-01 publishes a publicly-trusted cert for that name and a public
// A-record pointing to the box's LAN IP, so HTTPS on the LAN works without
// extra trust-store fiddling. OFFLINE-01 on the OS side serves that hostname
// from the box itself.
//
// Client-failover contract:
//  1. The web client (mail.vulos.org / office.vulos.org / …) caches BOTH
//     the cloud `Endpoint` (or `ProxyPath`) AND the `LANCandidate.Endpoint`.
//  2. Before each request, the client SHOULD health-check both candidates
//     (low-cost HEAD/GET on a /health path) and PREFER the LAN candidate
//     when reachable (lower latency, survives cloud outages).
//  3. When the preferred candidate fails, the client transparently falls
//     back to the other candidate.
//  4. Both candidates address the same logical backend; sessions/credentials
//     are valid against either, so failover does not require re-auth.
//  5. The LAN candidate is OMITTED when the account has no registered box;
//     clients that see no `lan_candidate` field MUST behave as today (cloud
//     only).
//
// Backward compatibility: the existing `Endpoint`, `FabricRoute`, `ProxyPath`,
// and `Kind` fields are unchanged. Clients that ignore the new optional
// `lan_candidate` field continue to work exactly as before.
//
// Storage: pure-Go modernc.org/sqlite (CGO_ENABLED=0 always).
package resolver

import (
	"context"
	"errors"
	"fmt"

	"github.com/vul-os/vulos-management/pkg/env"
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Service identifies a vulos web-app service.
type Service string

const (
	ServiceMail     Service = "mail"
	ServiceOffice   Service = "office"
	ServiceTalk     Service = "talk"
	ServiceCalendar Service = "calendar"
	ServiceMeet     Service = "meet"
)

// validServices is the set of accepted Service values.
var validServices = map[Service]bool{
	ServiceMail:     true,
	ServiceOffice:   true,
	ServiceTalk:     true,
	ServiceCalendar: true,
	ServiceMeet:     true,
}

// BackendKind identifies how the cloud should route API traffic.
type BackendKind string

const (
	// KindHosted means the account uses the Vulos-hosted JMAP/API endpoint.
	KindHosted BackendKind = "hosted"
	// KindSelfHostFabric means the account's box is self-hosted; the cloud
	// proxies requests over the peering/relay fabric without decryption.
	KindSelfHostFabric BackendKind = "selfhost-fabric"
)

// LANCandidate describes a same-LAN endpoint that the client may use as a
// failover/preference target when reachable. See the package doc comment for
// the client-failover contract.
type LANCandidate struct {
	// BoxID is the registered box identifier (today: the account ID).
	BoxID string `json:"box_id"`

	// Endpoint is the direct HTTPS URL the client should use when on the same
	// LAN as the box, e.g.
	//   https://box.<box-id>.lan.vulos.org/jmap/<svc>
	Endpoint string `json:"endpoint"`
}

// BackendTarget is returned by ResolveBackend.
type BackendTarget struct {
	// Kind distinguishes hosted vs. self-hosted routing.
	Kind BackendKind `json:"kind"`

	// Endpoint is the direct URL used for hosted accounts.
	// Empty for selfhost-fabric (the proxy uses FabricRoute instead).
	Endpoint string `json:"endpoint,omitempty"`

	// FabricRoute is the canonical fabric address used to route a self-hosted
	// box over the relay/peering tunnel. The cloud proxy opens an outbound
	// channel to this address and forwards bytes opaquely (E2E).
	// Populated only when Kind == KindSelfHostFabric.
	FabricRoute string `json:"fabric_route,omitempty"`

	// ProxyPath is the same-origin HTTP path the web client should use.
	// The cloud's reverse-proxy handler is mounted at this path and forwards
	// to the resolved backend.
	// e.g. "/backend/mail" for both hosted and selfhost-fabric callers.
	ProxyPath string `json:"proxy_path"`

	// LANCandidate is an optional same-LAN endpoint for the registered box.
	// Set whenever the account has a registered box (hosted OR self-hosted);
	// omitted otherwise. See the package doc comment for the failover contract.
	LANCandidate *LANCandidate `json:"lan_candidate,omitempty"`
}

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrUnknownAccount is returned when no account record can be found.
	ErrUnknownAccount = errors.New("resolver: unknown account")

	// ErrInvalidService is returned when the service name is not recognised.
	ErrInvalidService = errors.New("resolver: invalid service name")
)

// ---------------------------------------------------------------------------
// AccountSource — narrow read interface
// ---------------------------------------------------------------------------

// StorageSource is the narrow interface the resolver uses to determine whether
// an account has BYO (self-managed) storage — a reliable proxy for self-host
// enrollment before a dedicated enrollment store is available.
type StorageSource interface {
	// IsSelfHost returns true when the account has byo=true storage, which
	// signals that the user runs their own vulos-mail box.
	// An error (e.g. store unavailable) is propagated to the caller.
	IsSelfHost(ctx context.Context, accountID string) (bool, error)
}

// EnrollmentSource is the narrow interface the resolver uses to fetch the
// self-hosted box's endpoint and fabric route.
type EnrollmentSource interface {
	// FabricRoute returns the fabric address of the self-hosted box for the
	// account.  Returns ("", ErrNoEnrollment) when no box is enrolled.
	FabricRoute(ctx context.Context, accountID string) (string, error)

	// BoxID returns a stable identifier for the registered box associated with
	// the account.  Returns ("", ErrNoEnrollment) when the account has no
	// registered box.  Today this is the account ID itself; the indirection
	// lets future enrollment sources hand out per-device IDs without changing
	// callers.
	//
	// Used to construct the LAN candidate hostname
	// `box.<box-id>.lan.vulos.org` (see package doc).
	BoxID(ctx context.Context, accountID string) (string, error)
}

// ErrNoEnrollment is returned by EnrollmentSource.FabricRoute when the
// account has no self-hosted box enrolled.
var ErrNoEnrollment = errors.New("resolver: no self-host enrollment")

// ---------------------------------------------------------------------------
// Resolver
// ---------------------------------------------------------------------------

// Resolver resolves backend targets for web-app service requests.
type Resolver struct {
	// HostedEndpointBase is the base URL for hosted JMAP endpoints.
	// Per-service endpoints are derived as HostedEndpointBase + "/" + service.
	// Default: "https://mail.vulos.org/jmap".
	HostedEndpointBase string

	// LANEndpointDomain is the public DNS suffix used for LAN-failover
	// hostnames.  The full hostname is "box.<box-id>." + LANEndpointDomain.
	// Default: "lan.vulos.org".  (LANCERT-01 issues certs for this domain.)
	LANEndpointDomain string

	// Storage is used to determine whether an account has BYO storage (selfhost
	// proxy for enrollment detection). Required.
	Storage StorageSource

	// Enrollment is used to fetch the fabric route for self-hosted boxes.
	// Required when self-host accounts exist.
	Enrollment EnrollmentSource
}

// ResolveBackend resolves the BackendTarget for the given account + service.
//
// Flow:
//  1. Validate service name.
//  2. Ask StorageSource whether the account is a self-hoster (byo=true).
//  3. If self-hosted: fetch fabric route from EnrollmentSource.
//  4. If hosted: build direct JMAP endpoint URL.
//  5. Regardless of kind, attach a LAN candidate when the account has a
//     registered box (see RESOLVE-LAN-01).
func (r *Resolver) ResolveBackend(ctx context.Context, accountID string, svc Service) (BackendTarget, error) {
	if accountID == "" {
		return BackendTarget{}, ErrUnknownAccount
	}
	if !validServices[svc] {
		return BackendTarget{}, fmt.Errorf("%w: %q", ErrInvalidService, svc)
	}

	selfHost, err := r.Storage.IsSelfHost(ctx, accountID)
	if err != nil {
		if errors.Is(err, errStorageUnknown) {
			return BackendTarget{}, ErrUnknownAccount
		}
		return BackendTarget{}, fmt.Errorf("resolver: storage lookup: %w", err)
	}

	proxyPath := "/backend/" + string(svc)

	var target BackendTarget
	if selfHost {
		if r.Enrollment == nil {
			return BackendTarget{}, fmt.Errorf("resolver: self-host enrollment source not configured")
		}
		fabricRoute, err := r.Enrollment.FabricRoute(ctx, accountID)
		if err != nil {
			if errors.Is(err, ErrNoEnrollment) {
				// Self-host flag set but no enrolled box yet — fall back to hosted.
				target = r.hostedTarget(svc, proxyPath)
			} else {
				return BackendTarget{}, fmt.Errorf("resolver: fabric lookup: %w", err)
			}
		} else {
			target = BackendTarget{
				Kind:        KindSelfHostFabric,
				FabricRoute: fabricRoute,
				ProxyPath:   proxyPath,
			}
		}
	} else {
		target = r.hostedTarget(svc, proxyPath)
	}

	// Attach LAN candidate when a box is registered for this account.
	// Failure to look up a box ID is non-fatal — the LAN candidate is purely
	// an optimisation and the cloud path always works.
	if r.Enrollment != nil {
		if boxID, lookupErr := r.Enrollment.BoxID(ctx, accountID); lookupErr == nil && boxID != "" {
			target.LANCandidate = &LANCandidate{
				BoxID:    boxID,
				Endpoint: r.lanEndpointURL(boxID, svc),
			}
		}
	}

	return target, nil
}

func (r *Resolver) hostedTarget(svc Service, proxyPath string) BackendTarget {
	base := r.HostedEndpointBase
	if base == "" {
		base = "https://mail." + env.Domain() + "/jmap"
	}
	return BackendTarget{
		Kind:      KindHosted,
		Endpoint:  base + "/" + string(svc),
		ProxyPath: proxyPath,
	}
}

// lanEndpointURL builds "https://box.<box-id>.<lan-domain>/jmap/<svc>".
func (r *Resolver) lanEndpointURL(boxID string, svc Service) string {
	domain := r.LANEndpointDomain
	if domain == "" {
		domain = "lan." + env.Domain()
	}
	return "https://box." + boxID + "." + domain + "/jmap/" + string(svc)
}

// errStorageUnknown is a sentinel returned by memStorageSource to distinguish
// "account does not exist" from other errors.
var errStorageUnknown = errors.New("resolver: storage: account not found")
