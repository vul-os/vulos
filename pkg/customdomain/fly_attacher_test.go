// fly_attacher_test.go — FlyAttacher tests against a mocked Fly GraphQL API.
//
// AttachDomain / DetachDomain / Status are exercised against an httptest.Server
// that asserts the method, headers (Authorization), and GraphQL request body
// shape, then returns a realistic Fly GraphQL response. Error paths assert
// ErrFlyHTTP; Detach "not found" asserts the idempotent nil. FLY_MOCK offline
// mode is tested separately.
package customdomain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestFlyAttacher stands up an httptest.Server with the provided handler and
// returns a FlyAttacher pointed at it (real HTTP path, token set). The
// appResolver maps every tenant to a fixed app id unless overridden.
func newTestFlyAttacher(t *testing.T, appID string, handler http.HandlerFunc) *FlyAttacher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewFlyAttacher("test-token", srv.URL, func(string) string { return appID })
}

// decodeGraphQL reads the GraphQL request body for assertions.
func decodeGraphQL(t *testing.T, r *http.Request) flyGraphQLRequest {
	t.Helper()
	var v flyGraphQLRequest
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil && err != io.EOF {
		t.Errorf("decode body: %v", err)
	}
	return v
}

func TestFlyAttachDomainSuccess(t *testing.T) {
	t.Parallel()
	var gotReq flyGraphQLRequest
	a := newTestFlyAttacher(t, "vulos-acme", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		gotReq = decodeGraphQL(t, r)
		_ = json.NewEncoder(w).Encode(flyGraphQLResponse[flyAddCertificateData]{
			Data: flyAddCertificateData{
				AddCertificate: struct {
					Certificate flyCertificate `json:"certificate"`
				}{Certificate: flyCertificate{
					Hostname:            "mail.acme.example",
					Configured:          false,
					ClientStatus:        "Awaiting configuration",
					DNSValidationTarget: "vulos-acme.fly.dev",
				}},
			},
		})
	})

	acme, target, err := a.AttachDomain(context.Background(), "tenant-1", "mail.acme.example")
	if err != nil {
		t.Fatalf("AttachDomain: %v", err)
	}
	if acme != "awaiting configuration" {
		t.Errorf("acmeStatus = %q, want awaiting configuration", acme)
	}
	if target != "vulos-acme.fly.dev" {
		t.Errorf("validationTarget = %q, want vulos-acme.fly.dev", target)
	}
	// The mutation must carry the appId + hostname variables.
	if gotReq.Variables["appId"] != "vulos-acme" {
		t.Errorf("appId var = %v, want vulos-acme", gotReq.Variables["appId"])
	}
	if gotReq.Variables["hostname"] != "mail.acme.example" {
		t.Errorf("hostname var = %v, want mail.acme.example", gotReq.Variables["hostname"])
	}
	if !strings.Contains(gotReq.Query, "addCertificate") {
		t.Errorf("query missing addCertificate: %q", gotReq.Query)
	}
}

func TestFlyAttachDomainReadyMapsToIssued(t *testing.T) {
	t.Parallel()
	a := newTestFlyAttacher(t, "vulos-acme", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(flyGraphQLResponse[flyAddCertificateData]{
			Data: flyAddCertificateData{
				AddCertificate: struct {
					Certificate flyCertificate `json:"certificate"`
				}{Certificate: flyCertificate{
					Hostname:     "mail.acme.example",
					Configured:   true,
					ClientStatus: "Ready",
				}},
			},
		})
	})
	acme, target, err := a.AttachDomain(context.Background(), "tenant-1", "mail.acme.example")
	if err != nil {
		t.Fatalf("AttachDomain: %v", err)
	}
	if acme != "issued" {
		t.Errorf("acmeStatus = %q, want issued", acme)
	}
	// No explicit validation target → falls back to <app>.fly.dev.
	if target != "vulos-acme.fly.dev" {
		t.Errorf("validationTarget fallback = %q, want vulos-acme.fly.dev", target)
	}
}

func TestFlyAttachDomainGraphQLError(t *testing.T) {
	t.Parallel()
	a := newTestFlyAttacher(t, "vulos-acme", func(w http.ResponseWriter, r *http.Request) {
		// GraphQL returns 200 with a top-level errors array.
		_, _ = io.WriteString(w, `{"errors":[{"message":"boom"}]}`)
	})
	_, _, err := a.AttachDomain(context.Background(), "tenant-1", "mail.acme.example")
	if !errors.Is(err, ErrFlyHTTP) {
		t.Errorf("err = %v, want ErrFlyHTTP", err)
	}
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want wrapped GraphQL message", err)
	}
}

func TestFlyAttachDomainHTTPError(t *testing.T) {
	t.Parallel()
	for _, code := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		a := newTestFlyAttacher(t, "vulos-acme", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `nope`)
		})
		_, _, err := a.AttachDomain(context.Background(), "tenant-1", "mail.acme.example")
		if !errors.Is(err, ErrFlyHTTP) {
			t.Errorf("code %d: err = %v, want ErrFlyHTTP", code, err)
		}
	}
}

func TestFlyAttachDomainNoApp(t *testing.T) {
	t.Parallel()
	// appResolver returns empty → clear error, no HTTP performed.
	a := NewFlyAttacher("test-token", "http://127.0.0.1:0", func(string) string { return "" })
	_, _, err := a.AttachDomain(context.Background(), "tenant-x", "mail.acme.example")
	if !errors.Is(err, ErrFlyHTTP) {
		t.Fatalf("err = %v, want ErrFlyHTTP for missing app id", err)
	}
}

func TestFlyDetachDomainSuccess(t *testing.T) {
	t.Parallel()
	var gotReq flyGraphQLRequest
	a := newTestFlyAttacher(t, "vulos-acme", func(w http.ResponseWriter, r *http.Request) {
		gotReq = decodeGraphQL(t, r)
		_, _ = io.WriteString(w, `{"data":{"deleteCertificate":{"certificate":{"hostname":"mail.acme.example"}}}}`)
	})
	if err := a.DetachDomain(context.Background(), "tenant-1", "mail.acme.example"); err != nil {
		t.Fatalf("DetachDomain: %v", err)
	}
	if !strings.Contains(gotReq.Query, "deleteCertificate") {
		t.Errorf("query missing deleteCertificate: %q", gotReq.Query)
	}
	if gotReq.Variables["hostname"] != "mail.acme.example" {
		t.Errorf("hostname var = %v, want mail.acme.example", gotReq.Variables["hostname"])
	}
}

func TestFlyDetachDomainNotFoundIsNil(t *testing.T) {
	t.Parallel()
	a := newTestFlyAttacher(t, "vulos-acme", func(w http.ResponseWriter, r *http.Request) {
		// Fly returns a "not found" GraphQL error for an already-removed cert.
		_, _ = io.WriteString(w, `{"errors":[{"message":"Certificate not found"}]}`)
	})
	if err := a.DetachDomain(context.Background(), "tenant-1", "mail.acme.example"); err != nil {
		t.Fatalf("DetachDomain (not found) = %v, want nil", err)
	}
}

func TestFlyDetachDomainOtherErrorPropagates(t *testing.T) {
	t.Parallel()
	a := newTestFlyAttacher(t, "vulos-acme", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"internal explosion"}]}`)
	})
	err := a.DetachDomain(context.Background(), "tenant-1", "mail.acme.example")
	if !errors.Is(err, ErrFlyHTTP) {
		t.Fatalf("err = %v, want ErrFlyHTTP on non-notfound error", err)
	}
}

func TestFlyStatusSuccess(t *testing.T) {
	t.Parallel()
	a := newTestFlyAttacher(t, "vulos-acme", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(flyGraphQLResponse[flyAppCertificateData]{
			Data: flyAppCertificateData{
				App: struct {
					Certificate flyCertificate `json:"certificate"`
				}{Certificate: flyCertificate{
					Hostname:            "mail.acme.example",
					ClientStatus:        "Ready",
					DNSValidationTarget: "vulos-acme.fly.dev",
				}},
			},
		})
	})
	acme, target, err := a.Status(context.Background(), "tenant-1", "mail.acme.example")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if acme != "issued" {
		t.Errorf("acmeStatus = %q, want issued", acme)
	}
	if target != "vulos-acme.fly.dev" {
		t.Errorf("validationTarget = %q, want vulos-acme.fly.dev", target)
	}
}

func TestFlyStatusNoCertReturnsNotFound(t *testing.T) {
	t.Parallel()
	a := newTestFlyAttacher(t, "vulos-acme", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"app":{"certificate":{"hostname":""}}}}`)
	})
	if _, _, err := a.Status(context.Background(), "tenant-1", "mail.acme.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestFlyAttacherEmptyTokenLivePathErrors(t *testing.T) {
	t.Parallel()
	// Empty token in the live (non-mock) path must error clearly, not mock.
	a := NewFlyAttacher("", DefaultFlyGraphQLURL, func(string) string { return "vulos-acme" })
	if a.mock {
		t.Skip("FLY_MOCK set in environment; empty-token live path not exercised")
	}
	_, _, err := a.AttachDomain(context.Background(), "tenant-1", "mail.acme.example")
	if !errors.Is(err, ErrFlyHTTP) {
		t.Fatalf("err = %v, want ErrFlyHTTP for empty token", err)
	}
}

func TestFlyAttacherMockMode(t *testing.T) {
	// Not parallel: mutates process env. FLY_MOCK=1 → no HTTP performed.
	t.Setenv("FLY_MOCK", "1")
	a := NewFlyAttacher("", "", func(string) string { return "vulos-acme" })
	acme, target, err := a.AttachDomain(context.Background(), "tenant-1", "mail.acme.example")
	if err != nil {
		t.Fatalf("mock AttachDomain: %v", err)
	}
	if acme != "pending" {
		t.Errorf("mock acmeStatus = %q, want pending", acme)
	}
	if target != "vulos-acme.fly.dev" {
		t.Errorf("mock validationTarget = %q, want vulos-acme.fly.dev", target)
	}
	if err := a.DetachDomain(context.Background(), "tenant-1", "mail.acme.example"); err != nil {
		t.Fatalf("mock DetachDomain: %v", err)
	}
}

// TestFrontDoorAppResolverUsesFlyAppName verifies the production appResolver
// wiring targets the CP's OWN Fly app (FLY_APP_NAME, injected by Fly into
// every machine) rather than any per-tenant serving-pool bundle node — a
// mail/hub custom domain must attach to the CP's front door, not a VDI/Meet
// dedicated placement. The tenantID argument is deliberately ignored: every
// tenant's custom domain resolves to the same front-door app.
func TestFrontDoorAppResolverUsesFlyAppName(t *testing.T) {
	t.Setenv("FLY_APP_NAME", "vulos-cp-prod")
	resolve := FrontDoorAppResolver()
	for _, tenant := range []string{"tenant-a", "tenant-b", ""} {
		if got := resolve(tenant); got != "vulos-cp-prod" {
			t.Errorf("FrontDoorAppResolver()(%q) = %q, want vulos-cp-prod", tenant, got)
		}
	}
}

// TestFrontDoorAppResolverEmptyWhenUnset verifies an unset FLY_APP_NAME (e.g.
// local dev off Fly) surfaces as an empty app id — appIDFor then returns its
// existing "no Fly app id for tenant" error rather than silently attaching to
// a wrong/empty app.
func TestFrontDoorAppResolverEmptyWhenUnset(t *testing.T) {
	t.Setenv("FLY_APP_NAME", "")
	resolve := FrontDoorAppResolver()
	if got := resolve("tenant-a"); got != "" {
		t.Errorf("FrontDoorAppResolver()(tenant-a) = %q, want empty", got)
	}
}

func TestFlyAttacherDefaultBaseURL(t *testing.T) {
	t.Parallel()
	a := NewFlyAttacher("test-token", "", nil)
	if a.baseURL != DefaultFlyGraphQLURL {
		t.Errorf("baseURL = %q, want %q", a.baseURL, DefaultFlyGraphQLURL)
	}
}
