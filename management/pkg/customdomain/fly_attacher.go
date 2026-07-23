// fly_attacher.go — FlyAttacher: the production DomainAttacher that drives
// Fly.io's custom-domain / certificates API so a tenant can attach
// mail.theirdomain.com to their bundle-node Fly app with Fly-managed auto-TLS
// (Let's Encrypt).
//
// Wire reference (Fly GraphQL API, https://api.fly.io/graphql). Fly manages
// certificates through GraphQL mutations rather than a REST resource:
//
//	mutation { addCertificate(appId, hostname) {
//	    certificate { hostname, configured, clientStatus,
//	                  dnsValidationTarget, dnsValidationHostname,
//	                  dnsValidationInstructions } } }
//	mutation { deleteCertificate(appId, hostname) { ... } }
//
// On addCertificate, Fly returns the certificate plus the DNS validation
// target — the hostname the customer must point their domain at so the cert
// can be issued. For an apex/sub domain that is typically the app's
// "<app>.fly.dev" CNAME target; for ACME-DNS validation it is an
// "_acme-challenge.<domain>" target. We surface dnsValidationTarget (preferring
// it, falling back to dnsValidationHostname) as the validationTarget so the
// service stores it on CustomDomain.IntendedCNAME and the UI can show it.
//
// Status mapping: Fly's certificate.clientStatus is a human string (e.g.
// "Awaiting configuration", "Ready"). We lowercase it and map a "ready"/issued
// status to "issued"; everything else passes through as lowercased text (an
// empty status defaults to "pending"). `configured` true + clientStatus empty
// also maps to "issued".
//
// Test seam: NewFlyAttacher takes a baseURL so httptest.Server can drive the
// real HTTP path. An explicit offline mode is gated on FLY_MOCK=1 (NOT "token
// empty"), matching compute/fly.go — production with an empty token returns a
// clear "FLY_API_TOKEN not configured" error rather than silently fabricating
// an attach.
//
// Concurrency: every method is safe for concurrent use. The underlying
// http.Client is goroutine-safe; the attacher holds no mutable state.
//
// CGO_ENABLED=0 invariant: stdlib net/http + encoding/json only.
package customdomain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultFlyGraphQLURL is the production Fly GraphQL endpoint. Override via
// NewFlyAttacher's baseURL parameter in tests (point at httptest.Server).
const DefaultFlyGraphQLURL = "https://api.fly.io/graphql"

// flyReadLimit caps how much of a response/error body we read so a misbehaving
// upstream can't make us buffer unbounded memory.
const flyReadLimit = 1 << 20 // 1 MiB

// ErrFlyHTTP wraps non-2xx (or GraphQL-errors) failures from the Fly API.
// Callers should inspect with errors.Is rather than parsing the wrapped
// message. The service layer stores the message into CustomDomain.LastError
// and surfaces the error verbatim to the operator.
var ErrFlyHTTP = errors.New("customdomain: fly certificates API call failed")

// FlyAttacher implements DomainAttacher against the Fly GraphQL certificates
// API.
//
// All exported methods MUST be safe for concurrent use.
type FlyAttacher struct {
	token   string
	baseURL string
	// appResolver maps a Vulos tenantID → the tenant's Fly app id/slug (the
	// bundle-node app the certificate is attached to). It must be non-nil in
	// the live path; an empty result yields a clear error rather than a
	// malformed mutation.
	appResolver func(tenantID string) (appID string)
	hc          *http.Client
	mock        bool // FLY_MOCK=1 offline mode (no HTTP performed)
}

// NewFlyAttacher constructs a FlyAttacher.
//
//   - token       — Fly API token (Bearer). An empty token is allowed at
//     construction; in the live path calls return ErrFlyHTTP with a clear
//     "FLY_API_TOKEN not configured" message. (Set FLY_MOCK=1 for an explicit
//     offline mode that performs no HTTP.)
//   - baseURL     — GraphQL endpoint override. nil/empty → DefaultFlyGraphQLURL.
//   - appResolver — tenantID → Fly app id/slug. Required in the live path.
//
// The returned attacher uses a default 30s HTTP timeout.
func NewFlyAttacher(token, baseURL string, appResolver func(string) string) *FlyAttacher {
	if baseURL == "" {
		baseURL = DefaultFlyGraphQLURL
	}
	return &FlyAttacher{
		token:       token,
		baseURL:     strings.TrimRight(baseURL, "/"),
		appResolver: appResolver,
		hc:          &http.Client{Timeout: 30 * time.Second},
		mock:        os.Getenv("FLY_MOCK") == "1",
	}
}

// Compile-time assertion that FlyAttacher satisfies the seam.
var _ DomainAttacher = (*FlyAttacher)(nil)

// FrontDoorAppResolver returns the appResolver production wiring MUST use for
// FlyAttacher: every custom domain (mail.<domain>, app.<domain> — see
// RecommendedCustomDomainLabels) is served by the CP's OWN front-door Fly app,
// never a per-tenant serving-pool bundle node. servingpool.Scheduler exists for
// a different job — dedicated/VDI compute placement — and LookupNode legitimately
// returns "" for the vast majority of tenants that never got a pooled node, so
// wiring it here made every non-pooled tenant's Attach fail with "no Fly app id
// for tenant". Fly injects FLY_APP_NAME automatically into every machine
// belonging to an app, so resolving the CP's own app is a plain env read — the
// tenantID argument is accepted only to satisfy the appResolver signature
// (every tenant's custom domain attaches to the same front-door app). An empty
// result (FLY_APP_NAME unset, e.g. local dev off Fly) surfaces through
// appIDFor's existing "no Fly app id for tenant" error rather than silently
// misattaching to the wrong app.
func FrontDoorAppResolver() func(tenantID string) string {
	return func(_ string) string {
		return os.Getenv("FLY_APP_NAME")
	}
}

// ──────────────────────────────────────────────────────────────────────────
// GraphQL wire types (subset of the Fly schema we touch)
//
// TODO(fly): verify field names against the live GraphQL schema — these mirror
// the documented certificate fields (addCertificate / deleteCertificate /
// app.certificate) as of 2026-05-24 but are untestable without a live token.
// ──────────────────────────────────────────────────────────────────────────

// flyGraphQLRequest is the GraphQL POST body.
type flyGraphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// flyCertificate is the Fly certificate object (subset of the GraphQL type).
type flyCertificate struct {
	Hostname              string `json:"hostname"`
	Configured            bool   `json:"configured"`
	ClientStatus          string `json:"clientStatus"`
	DNSValidationTarget   string `json:"dnsValidationTarget"`
	DNSValidationHostname string `json:"dnsValidationHostname"`
}

// flyAddCertificateData is the data envelope for the addCertificate mutation.
type flyAddCertificateData struct {
	AddCertificate struct {
		Certificate flyCertificate `json:"certificate"`
	} `json:"addCertificate"`
}

// flyAppCertificateData is the data envelope for the status query
// (app(name){ certificate(hostname){...} }).
type flyAppCertificateData struct {
	App struct {
		Certificate flyCertificate `json:"certificate"`
	} `json:"app"`
}

// flyGraphQLError is one entry in the GraphQL top-level "errors" array.
type flyGraphQLError struct {
	Message string `json:"message"`
}

// flyGraphQLResponse[T] is the standard GraphQL envelope.
type flyGraphQLResponse[T any] struct {
	Data   T                 `json:"data"`
	Errors []flyGraphQLError `json:"errors"`
}

// ──────────────────────────────────────────────────────────────────────────
// GraphQL documents
// ──────────────────────────────────────────────────────────────────────────

const flyAddCertificateMutation = `mutation($appId: ID!, $hostname: String!) {
  addCertificate(appId: $appId, hostname: $hostname) {
    certificate {
      hostname
      configured
      clientStatus
      dnsValidationTarget
      dnsValidationHostname
    }
  }
}`

const flyDeleteCertificateMutation = `mutation($appId: ID!, $hostname: String!) {
  deleteCertificate(appId: $appId, hostname: $hostname) {
    certificate { hostname }
  }
}`

const flyAppCertificateQuery = `query($appId: String!, $hostname: String!) {
  app(name: $appId) {
    certificate(hostname: $hostname) {
      hostname
      configured
      clientStatus
      dnsValidationTarget
      dnsValidationHostname
    }
  }
}`

// ──────────────────────────────────────────────────────────────────────────
// DomainAttacher implementation
// ──────────────────────────────────────────────────────────────────────────

// AttachDomain creates a Fly certificate for `domain` on the tenant's
// bundle-node Fly app and returns (acmeStatus, validationTarget). Fly begins
// TLS provisioning automatically once the customer's DNS points at the
// returned validation target.
func (f *FlyAttacher) AttachDomain(ctx context.Context, tenantID, domain string) (string, string, error) {
	appID, err := f.appIDFor(tenantID)
	if err != nil {
		return "", "", err
	}

	if f.mock {
		// Deterministic offline target so the dev/CI flow surfaces a CNAME.
		return "pending", flyAppHostname(appID), nil
	}

	var data flyAddCertificateData
	if err := f.do(ctx, flyAddCertificateMutation, map[string]any{
		"appId":    appID,
		"hostname": domain,
	}, &data); err != nil {
		return "", "", err
	}
	cert := data.AddCertificate.Certificate
	return acmeStatusFromFly(cert), validationTargetFromFly(cert, appID), nil
}

// DetachDomain removes the certificate. Idempotent: Fly returning a
// "not found" GraphQL error for an already-removed certificate is treated as
// success (nil).
func (f *FlyAttacher) DetachDomain(ctx context.Context, tenantID, domain string) error {
	appID, err := f.appIDFor(tenantID)
	if err != nil {
		return err
	}
	if f.mock {
		return nil
	}
	var data json.RawMessage
	err = f.do(ctx, flyDeleteCertificateMutation, map[string]any{
		"appId":    appID,
		"hostname": domain,
	}, &data)
	if err != nil && isFlyNotFound(err) {
		return nil // already detached
	}
	return err
}

// Status reads the current certificate state for (tenant, domain). It is not
// part of the DomainAttacher seam but is exposed so an operator / reconciler
// can poll Fly for the live ACME status. Returns ErrNotFound when Fly reports
// no certificate for the hostname.
func (f *FlyAttacher) Status(ctx context.Context, tenantID, domain string) (acmeStatus, validationTarget string, err error) {
	appID, aerr := f.appIDFor(tenantID)
	if aerr != nil {
		return "", "", aerr
	}
	if f.mock {
		return "pending", flyAppHostname(appID), nil
	}
	var data flyAppCertificateData
	if derr := f.do(ctx, flyAppCertificateQuery, map[string]any{
		"appId":    appID,
		"hostname": domain,
	}, &data); derr != nil {
		return "", "", derr
	}
	cert := data.App.Certificate
	if cert.Hostname == "" {
		return "", "", ErrNotFound
	}
	return acmeStatusFromFly(cert), validationTargetFromFly(cert, appID), nil
}

// ──────────────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────────────

// appIDFor resolves the tenant's Fly app id and errors clearly when no
// resolver is wired or the resolver returns empty.
func (f *FlyAttacher) appIDFor(tenantID string) (string, error) {
	appID := f.resolveAppID(tenantID)
	if appID == "" {
		return "", fmt.Errorf("%w: no Fly app id for tenant %q", ErrFlyHTTP, tenantID)
	}
	return appID, nil
}

// resolveAppID is a nil-safe wrapper over appResolver.
func (f *FlyAttacher) resolveAppID(tenantID string) string {
	if f.appResolver == nil {
		return ""
	}
	return f.appResolver(tenantID)
}

// validationTargetFromFly prefers the cert's dnsValidationTarget, falls back to
// dnsValidationHostname, and finally to the app's "<app>.fly.dev" hostname so a
// CNAME target is always surfaced to the customer.
func validationTargetFromFly(cert flyCertificate, appID string) string {
	if t := strings.TrimSpace(cert.DNSValidationTarget); t != "" {
		return t
	}
	if h := strings.TrimSpace(cert.DNSValidationHostname); h != "" {
		return h
	}
	return flyAppHostname(appID)
}

// flyAppHostname derives the default "<app>.fly.dev" CNAME target from a Fly
// app id/slug. Used as the fallback validation target.
func flyAppHostname(appID string) string {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return ""
	}
	return appID + ".fly.dev"
}

// acmeStatusFromFly maps a Fly certificate to the service's ACME-status string.
// A "ready"/issued clientStatus (or configured==true with no client status)
// → "issued"; everything else is lowercased verbatim. An empty status defaults
// to "pending".
func acmeStatusFromFly(cert flyCertificate) string {
	s := strings.ToLower(strings.TrimSpace(cert.ClientStatus))
	switch {
	case s == "ready", s == "issued", s == "active":
		return "issued"
	case s == "" && cert.Configured:
		return "issued"
	case s == "":
		return "pending"
	default:
		return s
	}
}

// isFlyNotFound reports whether err looks like a Fly "certificate not found"
// GraphQL error so DetachDomain can treat it as already-detached.
func isFlyNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "could not find")
}

// do executes a GraphQL request against the Fly API and decodes the data
// envelope into out (when out != nil). It centralises auth, content-type, and
// error translation:
//
//   - transport / non-2xx → ErrFlyHTTP wrapping the status code + body;
//   - GraphQL top-level errors → ErrFlyHTTP wrapping the first message;
//   - empty token in the live path → ErrFlyHTTP("FLY_API_TOKEN not configured").
func (f *FlyAttacher) do(ctx context.Context, query string, vars map[string]any, out any) error {
	if f.token == "" {
		return fmt.Errorf("%w: FLY_API_TOKEN not configured", ErrFlyHTTP)
	}

	reqBody, err := json.Marshal(flyGraphQLRequest{Query: query, Variables: vars})
	if err != nil {
		return fmt.Errorf("%w: marshal request: %v", ErrFlyHTTP, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.baseURL, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrFlyHTTP, err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrFlyHTTP, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, flyReadLimit))
		_ = resp.Body.Close()
	}()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, flyReadLimit))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("%w: status %d: %s", ErrFlyHTTP, resp.StatusCode, msg)
	}

	// Decode the GraphQL envelope generically so we can surface top-level
	// errors before unmarshalling the data into the caller's typed shape.
	var env struct {
		Data   json.RawMessage   `json:"data"`
		Errors []flyGraphQLError `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w: decode envelope: %v", ErrFlyHTTP, err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("%w: %s", ErrFlyHTTP, env.Errors[0].Message)
	}
	if out == nil {
		return nil
	}
	if len(env.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("%w: decode data: %v", ErrFlyHTTP, err)
	}
	return nil
}
