// resolver_test.go — unit tests for the resolver package.
//
// Tests:
//   - hosted → hosted endpoint
//   - selfhost → fabric route
//   - unknown account → ErrUnknownAccount
//   - invalid service → ErrInvalidService
//   - all valid service enum values
//   - selfhost with no enrollment falls back to hosted
//   - proxy path is always /backend/<service>
package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func newTestResolver() *Resolver {
	return &Resolver{
		HostedEndpointBase: "https://mail.vulos.org/jmap",
		Storage:            NewMemStorageSource(),
		Enrollment:         NewMemEnrollmentSource(),
	}
}

// --- Test 1: hosted user → KindHosted with correct endpoint ---

func TestResolveBackend_Hosted(t *testing.T) {
	r := newTestResolver()
	st := r.Storage.(*MemStorageSource)
	st.Set("acct-hosted-1", false) // not self-host

	target, err := r.ResolveBackend(context.Background(), "acct-hosted-1", ServiceMail)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.Kind != KindHosted {
		t.Errorf("expected Kind=%q, got %q", KindHosted, target.Kind)
	}
	if target.Endpoint != "https://mail.vulos.org/jmap/mail" {
		t.Errorf("unexpected endpoint: %q", target.Endpoint)
	}
	if target.FabricRoute != "" {
		t.Errorf("expected empty FabricRoute for hosted, got %q", target.FabricRoute)
	}
	if target.ProxyPath != "/backend/mail" {
		t.Errorf("unexpected ProxyPath: %q", target.ProxyPath)
	}
}

// --- Test 2: self-host user → KindSelfHostFabric with fabric route ---

func TestResolveBackend_SelfHostFabric(t *testing.T) {
	r := newTestResolver()
	st := r.Storage.(*MemStorageSource)
	en := r.Enrollment.(*MemEnrollmentSource)

	st.Set("acct-selfhost-1", true)
	en.Set("acct-selfhost-1", "https://mybox.example.com:443")

	target, err := r.ResolveBackend(context.Background(), "acct-selfhost-1", ServiceMail)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.Kind != KindSelfHostFabric {
		t.Errorf("expected Kind=%q, got %q", KindSelfHostFabric, target.Kind)
	}
	if target.FabricRoute != "https://mybox.example.com:443" {
		t.Errorf("unexpected FabricRoute: %q", target.FabricRoute)
	}
	if target.Endpoint != "" {
		t.Errorf("expected empty Endpoint for selfhost-fabric, got %q", target.Endpoint)
	}
	if target.ProxyPath != "/backend/mail" {
		t.Errorf("unexpected ProxyPath: %q", target.ProxyPath)
	}
}

// --- Test 3: unknown account → ErrUnknownAccount ---

func TestResolveBackend_UnknownAccount(t *testing.T) {
	r := newTestResolver()
	// No account registered in storage.

	_, err := r.ResolveBackend(context.Background(), "acct-nobody", ServiceMail)
	if err == nil {
		t.Fatal("expected error for unknown account, got nil")
	}
	if !errors.Is(err, ErrUnknownAccount) {
		t.Errorf("expected ErrUnknownAccount, got: %v", err)
	}
}

// --- Test 4: empty accountID → ErrUnknownAccount ---

func TestResolveBackend_EmptyAccountID(t *testing.T) {
	r := newTestResolver()

	_, err := r.ResolveBackend(context.Background(), "", ServiceMail)
	if err == nil {
		t.Fatal("expected error for empty account ID, got nil")
	}
	if !errors.Is(err, ErrUnknownAccount) {
		t.Errorf("expected ErrUnknownAccount, got: %v", err)
	}
}

// --- Test 5: invalid service → ErrInvalidService ---

func TestResolveBackend_InvalidService(t *testing.T) {
	r := newTestResolver()
	st := r.Storage.(*MemStorageSource)
	st.Set("acct-valid", false)

	_, err := r.ResolveBackend(context.Background(), "acct-valid", Service("invalid-svc"))
	if err == nil {
		t.Fatal("expected error for invalid service, got nil")
	}
	if !errors.Is(err, ErrInvalidService) {
		t.Errorf("expected ErrInvalidService, got: %v", err)
	}
}

// --- Test 6: all valid service enum values resolve correctly ---

func TestResolveBackend_AllServices(t *testing.T) {
	r := newTestResolver()
	st := r.Storage.(*MemStorageSource)
	st.Set("acct-all", false)

	services := []Service{ServiceMail, ServiceOffice, ServiceCalendar}
	for _, svc := range services {
		t.Run(string(svc), func(t *testing.T) {
			target, err := r.ResolveBackend(context.Background(), "acct-all", svc)
			if err != nil {
				t.Fatalf("[%s] unexpected error: %v", svc, err)
			}
			if target.Kind != KindHosted {
				t.Errorf("[%s] expected KindHosted", svc)
			}
			wantEndpoint := "https://mail.vulos.org/jmap/" + string(svc)
			if target.Endpoint != wantEndpoint {
				t.Errorf("[%s] endpoint: want %q, got %q", svc, wantEndpoint, target.Endpoint)
			}
			wantProxy := "/backend/" + string(svc)
			if target.ProxyPath != wantProxy {
				t.Errorf("[%s] proxy path: want %q, got %q", svc, wantProxy, target.ProxyPath)
			}
		})
	}
}

// --- Test 7: selfhost flagged but no enrollment → falls back to hosted ---

func TestResolveBackend_SelfHostNoEnrollmentFallback(t *testing.T) {
	r := newTestResolver()
	st := r.Storage.(*MemStorageSource)
	// Account is flagged as self-host but no enrollment record exists.
	st.Set("acct-noenroll", true)
	// MemEnrollmentSource has nothing for this account.

	target, err := r.ResolveBackend(context.Background(), "acct-noenroll", ServiceOffice)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Graceful fallback to hosted.
	if target.Kind != KindHosted {
		t.Errorf("expected fallback to KindHosted, got %q", target.Kind)
	}
}

// --- Test 8: HostedEndpointBase default ---

func TestResolveBackend_DefaultHostedBase(t *testing.T) {
	// Resolver with empty HostedEndpointBase.
	r := &Resolver{
		Storage:    NewMemStorageSource(),
		Enrollment: NewMemEnrollmentSource(),
	}
	r.Storage.(*MemStorageSource).Set("acct-default", false)

	target, err := r.ResolveBackend(context.Background(), "acct-default", ServiceMail)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.Endpoint != "https://mail.vulos.org/jmap/mail" {
		t.Errorf("unexpected default endpoint: %q", target.Endpoint)
	}
}

// --- Test 9: self-host with different service still gets correct fabric route ---

func TestResolveBackend_SelfHostAllServices(t *testing.T) {
	r := newTestResolver()
	st := r.Storage.(*MemStorageSource)
	en := r.Enrollment.(*MemEnrollmentSource)

	st.Set("acct-sh-all", true)
	en.Set("acct-sh-all", "https://home.example.net:8443")

	for _, svc := range []Service{ServiceMail, ServiceOffice, ServiceCalendar} {
		t.Run(string(svc), func(t *testing.T) {
			target, err := r.ResolveBackend(context.Background(), "acct-sh-all", svc)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if target.Kind != KindSelfHostFabric {
				t.Errorf("expected KindSelfHostFabric, got %q", target.Kind)
			}
			if target.FabricRoute != "https://home.example.net:8443" {
				t.Errorf("unexpected FabricRoute: %q", target.FabricRoute)
			}
			wantProxy := "/backend/" + string(svc)
			if target.ProxyPath != wantProxy {
				t.Errorf("ProxyPath: want %q, got %q", wantProxy, target.ProxyPath)
			}
		})
	}
}

// ─── RESOLVE-LAN-01: LAN-candidate failover tests ──────────────────────────

// LAN candidate is attached when a self-host account has a registered box.
func TestResolveBackend_LANCandidate_SelfHost(t *testing.T) {
	r := newTestResolver()
	st := r.Storage.(*MemStorageSource)
	en := r.Enrollment.(*MemEnrollmentSource)

	st.Set("acct-lan-sh", true)
	en.Set("acct-lan-sh", "https://mybox.example.com:443") // also sets boxID = accountID

	target, err := r.ResolveBackend(context.Background(), "acct-lan-sh", ServiceMail)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.Kind != KindSelfHostFabric {
		t.Fatalf("expected KindSelfHostFabric, got %q", target.Kind)
	}
	if target.LANCandidate == nil {
		t.Fatal("expected LANCandidate to be set, got nil")
	}
	if target.LANCandidate.BoxID != "acct-lan-sh" {
		t.Errorf("LANCandidate.BoxID: want %q, got %q", "acct-lan-sh", target.LANCandidate.BoxID)
	}
	want := "https://box.acct-lan-sh.lan.vulos.org/jmap/mail"
	if target.LANCandidate.Endpoint != want {
		t.Errorf("LANCandidate.Endpoint: want %q, got %q", want, target.LANCandidate.Endpoint)
	}

	// Backward-compat: fabric route still populated.
	if target.FabricRoute != "https://mybox.example.com:443" {
		t.Errorf("FabricRoute not preserved: %q", target.FabricRoute)
	}
}

// LAN candidate is attached for a hosted account that has a co-located box
// registered (e.g. user keeps hosted JMAP for failover but also runs a local
// box for low-latency LAN access).
func TestResolveBackend_LANCandidate_HostedWithBox(t *testing.T) {
	r := newTestResolver()
	st := r.Storage.(*MemStorageSource)
	en := r.Enrollment.(*MemEnrollmentSource)

	st.Set("acct-hosted-box", false) // hosted
	en.SetBoxID("acct-hosted-box", "box-xyz123")

	target, err := r.ResolveBackend(context.Background(), "acct-hosted-box", ServiceOffice)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.Kind != KindHosted {
		t.Fatalf("expected KindHosted, got %q", target.Kind)
	}
	// Backward-compat: Endpoint still populated.
	if target.Endpoint != "https://mail.vulos.org/jmap/office" {
		t.Errorf("unexpected Endpoint: %q", target.Endpoint)
	}
	if target.LANCandidate == nil {
		t.Fatal("expected LANCandidate to be set for hosted-with-box account")
	}
	if target.LANCandidate.BoxID != "box-xyz123" {
		t.Errorf("LANCandidate.BoxID: want %q, got %q", "box-xyz123", target.LANCandidate.BoxID)
	}
	want := "https://box.box-xyz123.lan.vulos.org/jmap/office"
	if target.LANCandidate.Endpoint != want {
		t.Errorf("LANCandidate.Endpoint: want %q, got %q", want, target.LANCandidate.Endpoint)
	}
}

// LAN candidate is omitted for a hosted account with no registered box.
func TestResolveBackend_LANCandidate_OmittedNoBox(t *testing.T) {
	r := newTestResolver()
	st := r.Storage.(*MemStorageSource)
	st.Set("acct-no-box", false)
	// EnrollmentSource has nothing for this account.

	target, err := r.ResolveBackend(context.Background(), "acct-no-box", ServiceMail)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.LANCandidate != nil {
		t.Errorf("expected nil LANCandidate when no box registered, got %+v", target.LANCandidate)
	}
	// Backward-compat: Endpoint still populated.
	if target.Endpoint == "" {
		t.Error("expected Endpoint to be populated for hosted account")
	}
}

// LAN candidate is also omitted in the self-host-flagged-but-no-enrollment
// fallback case (because no box-id exists).
func TestResolveBackend_LANCandidate_OmittedSelfHostNoEnrollment(t *testing.T) {
	r := newTestResolver()
	st := r.Storage.(*MemStorageSource)
	st.Set("acct-sh-noenroll", true)
	// No enrollment record.

	target, err := r.ResolveBackend(context.Background(), "acct-sh-noenroll", ServiceMail)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.Kind != KindHosted {
		t.Errorf("expected fallback to KindHosted, got %q", target.Kind)
	}
	if target.LANCandidate != nil {
		t.Errorf("expected nil LANCandidate, got %+v", target.LANCandidate)
	}
}

// LAN candidate honours a custom LANEndpointDomain (so test/staging envs can
// override) — covers the default-vs-custom branch.
func TestResolveBackend_LANCandidate_CustomDomain(t *testing.T) {
	r := newTestResolver()
	r.LANEndpointDomain = "lan.staging.example"
	en := r.Enrollment.(*MemEnrollmentSource)
	st := r.Storage.(*MemStorageSource)
	st.Set("acct-custom", false)
	en.SetBoxID("acct-custom", "boxA")

	target, err := r.ResolveBackend(context.Background(), "acct-custom", ServiceCalendar)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.LANCandidate == nil {
		t.Fatal("expected LANCandidate to be set")
	}
	want := "https://box.boxA.lan.staging.example/jmap/calendar"
	if target.LANCandidate.Endpoint != want {
		t.Errorf("LANCandidate.Endpoint: want %q, got %q", want, target.LANCandidate.Endpoint)
	}
}

// JSON shape: lan_candidate is omitted entirely (omitempty) when nil, so old
// clients that don't know about the field see the same JSON they always have.
func TestResolveBackend_LANCandidate_JSONOmitEmpty(t *testing.T) {
	r := newTestResolver()
	st := r.Storage.(*MemStorageSource)
	st.Set("acct-json", false) // hosted, no box

	target, err := r.ResolveBackend(context.Background(), "acct-json", ServiceMail)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := json.Marshal(target)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "lan_candidate") {
		t.Errorf("expected lan_candidate field to be omitted, got: %s", string(b))
	}

	// And when present, it serialises with the expected key.
	en := r.Enrollment.(*MemEnrollmentSource)
	en.SetBoxID("acct-json", "acct-json")
	target2, _ := r.ResolveBackend(context.Background(), "acct-json", ServiceMail)
	b2, _ := json.Marshal(target2)
	if !strings.Contains(string(b2), `"lan_candidate"`) {
		t.Errorf("expected lan_candidate field, got: %s", string(b2))
	}
	if !strings.Contains(string(b2), "box.acct-json.lan.vulos.org") {
		t.Errorf("expected LAN hostname in JSON, got: %s", string(b2))
	}
}
