package customdomain

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/env"
)

// ──────────────────────────────────────────────────────────────────────────
// Test doubles
// ──────────────────────────────────────────────────────────────────────────

// fakeDNS is a controllable DNSResolver. txt maps a record name to the TXT
// values returned; lookupErr (if set) is returned for every lookup.
type fakeDNS struct {
	txt       map[string][]string
	lookupErr error
}

func (f *fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	return f.txt[name], nil
}

// fakeNodeResolver returns a fixed BundleNodeRef for any tenant.
type fakeNodeResolver struct {
	ref BundleNodeRef
	err error
}

func (f fakeNodeResolver) BundleNodeForTenant(_ context.Context, _ string) (BundleNodeRef, error) {
	if f.err != nil {
		return BundleNodeRef{}, f.err
	}
	return f.ref, nil
}

func newTestService(dns DNSResolver) *Service {
	node := fakeNodeResolver{ref: BundleNodeRef{Provider: "fly", App: "vulos-node-1", ID: "svc-1"}}
	return NewService(NewMemStore(), NewStaticAttacher(), node, dns)
}

// ──────────────────────────────────────────────────────────────────────────
// RequestVerification
// ──────────────────────────────────────────────────────────────────────────

func TestRequestVerification(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		tenant  string
		domain  string
		wantErr error
	}{
		{"happy", "tenant-a", "mail.acme.example", nil},
		{"empty domain", "tenant-a", "", ErrInvalidDomain},
		{"no dot", "tenant-a", "localhost", ErrInvalidDomain},
		{"reserved suffix", "tenant-a", "foo.vulos.org", ErrInvalidDomain},
		{"empty tenant", "", "mail.acme.example", ErrTenantMismatch},
		{"whitespace", "tenant-a", "mail acme.example", ErrInvalidDomain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(&fakeDNS{})
			instr, err := svc.RequestVerification(ctx, tt.tenant, tt.domain)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if instr.Token == "" || instr.RecordType != "TXT" {
				t.Fatalf("bad instruction: %+v", instr)
			}
			if instr.RecordName != VerificationRecordName(tt.domain) {
				t.Fatalf("record name = %q, want %q", instr.RecordName, VerificationRecordName(tt.domain))
			}
			// Row must exist in pending state.
			row, gerr := svc.Store.Get(ctx, tt.domain)
			if gerr != nil {
				t.Fatalf("Get after RequestVerification: %v", gerr)
			}
			if row.VerifyState != StatePending {
				t.Fatalf("state = %q, want pending", row.VerifyState)
			}
		})
	}
}

func TestRequestVerification_CrossTenantRejected(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(&fakeDNS{})
	if _, err := svc.RequestVerification(ctx, "tenant-a", "mail.acme.example"); err != nil {
		t.Fatalf("first request: %v", err)
	}
	// A different tenant must not be able to re-request the same domain.
	if _, err := svc.RequestVerification(ctx, "tenant-b", "mail.acme.example"); !errors.Is(err, ErrTenantMismatch) {
		t.Fatalf("cross-tenant request err = %v, want ErrTenantMismatch", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// VerifyDNS
// ──────────────────────────────────────────────────────────────────────────

func TestVerifyDNS(t *testing.T) {
	ctx := context.Background()
	const tenant, domain = "tenant-a", "mail.acme.example"

	t.Run("matching TXT marks verified", func(t *testing.T) {
		dns := &fakeDNS{txt: map[string][]string{}}
		svc := newTestService(dns)
		instr, err := svc.RequestVerification(ctx, tenant, domain)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		dns.txt[VerificationRecordName(domain)] = []string{"some-other-txt", instr.Token}
		row, err := svc.VerifyDNS(ctx, domain)
		if err != nil {
			t.Fatalf("VerifyDNS: %v", err)
		}
		if row.VerifyState != StateVerified {
			t.Fatalf("state = %q, want verified", row.VerifyState)
		}
		if row.VerifiedAt == nil {
			t.Fatal("VerifiedAt not stamped")
		}
	})

	t.Run("missing TXT stays pending", func(t *testing.T) {
		dns := &fakeDNS{txt: map[string][]string{}}
		svc := newTestService(dns)
		if _, err := svc.RequestVerification(ctx, tenant, domain); err != nil {
			t.Fatalf("request: %v", err)
		}
		// No TXT published.
		_, err := svc.VerifyDNS(ctx, domain)
		if !errors.Is(err, ErrVerificationFailed) {
			t.Fatalf("err = %v, want ErrVerificationFailed", err)
		}
		row, _ := svc.Store.Get(ctx, domain)
		if row.VerifyState != StatePending {
			t.Fatalf("state = %q, want pending", row.VerifyState)
		}
	})

	t.Run("dns error stays pending", func(t *testing.T) {
		dns := &fakeDNS{lookupErr: errors.New("nxdomain")}
		svc := newTestService(dns)
		if _, err := svc.RequestVerification(ctx, tenant, domain); err != nil {
			t.Fatalf("request: %v", err)
		}
		_, err := svc.VerifyDNS(ctx, domain)
		if !errors.Is(err, ErrVerificationFailed) {
			t.Fatalf("err = %v, want ErrVerificationFailed", err)
		}
		row, _ := svc.Store.Get(ctx, domain)
		if row.VerifyState != StatePending {
			t.Fatalf("state = %q, want pending", row.VerifyState)
		}
	})

	t.Run("unknown domain returns not-found", func(t *testing.T) {
		svc := newTestService(&fakeDNS{})
		if _, err := svc.VerifyDNS(ctx, "nope.example"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────
// Attach
// ──────────────────────────────────────────────────────────────────────────

func TestAttach(t *testing.T) {
	ctx := context.Background()
	const tenant, domain = "tenant-a", "mail.acme.example"

	t.Run("only works post-verify", func(t *testing.T) {
		svc := newTestService(&fakeDNS{})
		if _, err := svc.RequestVerification(ctx, tenant, domain); err != nil {
			t.Fatalf("request: %v", err)
		}
		// Pending → ErrNotVerified.
		if _, err := svc.Attach(ctx, tenant, domain); !errors.Is(err, ErrNotVerified) {
			t.Fatalf("attach-pending err = %v, want ErrNotVerified", err)
		}
	})

	t.Run("happy path attaches", func(t *testing.T) {
		dns := &fakeDNS{txt: map[string][]string{}}
		svc := newTestService(dns)
		instr, _ := svc.RequestVerification(ctx, tenant, domain)
		dns.txt[VerificationRecordName(domain)] = []string{instr.Token}
		if _, err := svc.VerifyDNS(ctx, domain); err != nil {
			t.Fatalf("verify: %v", err)
		}
		row, err := svc.Attach(ctx, tenant, domain)
		if err != nil {
			t.Fatalf("attach: %v", err)
		}
		if row.VerifyState != StateAttached {
			t.Fatalf("state = %q, want attached", row.VerifyState)
		}
		if row.ACMEStatus != "pending" {
			t.Fatalf("acme = %q, want pending (static attacher)", row.ACMEStatus)
		}
		if row.BundleNode != "vulos-node-1" {
			t.Fatalf("bundle node = %q, want vulos-node-1", row.BundleNode)
		}
		// Second attach → ErrAlreadyAttached.
		if _, err := svc.Attach(ctx, tenant, domain); !errors.Is(err, ErrAlreadyAttached) {
			t.Fatalf("re-attach err = %v, want ErrAlreadyAttached", err)
		}
	})

	t.Run("cross-tenant attach rejected", func(t *testing.T) {
		dns := &fakeDNS{txt: map[string][]string{}}
		svc := newTestService(dns)
		instr, _ := svc.RequestVerification(ctx, tenant, domain)
		dns.txt[VerificationRecordName(domain)] = []string{instr.Token}
		if _, err := svc.VerifyDNS(ctx, domain); err != nil {
			t.Fatalf("verify: %v", err)
		}
		if _, err := svc.Attach(ctx, "tenant-b", domain); !errors.Is(err, ErrTenantMismatch) {
			t.Fatalf("cross-tenant attach err = %v, want ErrTenantMismatch", err)
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────
// RouteFor + Detach + List
// ──────────────────────────────────────────────────────────────────────────

func attachDomain(t *testing.T, svc *Service, tenant, domain string, dns *fakeDNS) {
	t.Helper()
	ctx := context.Background()
	instr, err := svc.RequestVerification(ctx, tenant, domain)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	dns.txt[VerificationRecordName(domain)] = []string{instr.Token}
	if _, err := svc.VerifyDNS(ctx, domain); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := svc.Attach(ctx, tenant, domain); err != nil {
		t.Fatalf("attach: %v", err)
	}
}

// TestLean2RecordModel_MailAndAppBothRouteUnderOneTenant proves out
// CUSTOM-DOMAIN-LEAN-2-RECORD-01: a customer needs to attach only the two
// RecommendedCustomDomainLabels (mail. and app.) under their own domain, and
// each routes independently to the SAME tenant/bundle-node — no per-app
// (office./meet./talk./board./files.) record is required, since those are all
// embedded inside the Workspace hub served at app.<domain>.
func TestLean2RecordModel_MailAndAppBothRouteUnderOneTenant(t *testing.T) {
	if got := RecommendedCustomDomainLabels; len(got) != 2 || got[0] != "mail" || got[1] != "app" {
		t.Fatalf("RecommendedCustomDomainLabels = %v, want [mail app]", got)
	}

	ctx := context.Background()
	const tenant = "tenant-lean"
	dns := &fakeDNS{txt: map[string][]string{}}
	svc := newTestService(dns)

	for _, label := range RecommendedCustomDomainLabels {
		domain := label + ".acme.example"
		attachDomain(t, svc, tenant, domain, dns)
		gotTenant, ref, err := svc.RouteFor(ctx, domain)
		if err != nil {
			t.Fatalf("RouteFor(%s): %v", domain, err)
		}
		if gotTenant != tenant {
			t.Errorf("RouteFor(%s) tenant = %q, want %q", domain, gotTenant, tenant)
		}
		if ref.App != "vulos-node-1" {
			t.Errorf("RouteFor(%s) ref.App = %q, want vulos-node-1", domain, ref.App)
		}
	}

	// Neither record depends on the other — detaching one leaves the other live.
	if err := svc.Detach(ctx, tenant, "mail.acme.example"); err != nil {
		t.Fatalf("detach mail: %v", err)
	}
	if _, _, err := svc.RouteFor(ctx, "app.acme.example"); err != nil {
		t.Fatalf("app.acme.example must still route after detaching mail: %v", err)
	}
	if _, _, err := svc.RouteFor(ctx, "mail.acme.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mail.acme.example after detach: err = %v, want ErrNotFound", err)
	}
}

func TestRouteFor(t *testing.T) {
	ctx := context.Background()
	const tenant, domain = "tenant-a", "mail.acme.example"

	t.Run("attached resolves tenant+node", func(t *testing.T) {
		dns := &fakeDNS{txt: map[string][]string{}}
		svc := newTestService(dns)
		attachDomain(t, svc, tenant, domain, dns)
		gotTenant, ref, err := svc.RouteFor(ctx, "MAIL.Acme.Example") // case + normalisation
		if err != nil {
			t.Fatalf("RouteFor: %v", err)
		}
		if gotTenant != tenant {
			t.Fatalf("tenant = %q, want %q", gotTenant, tenant)
		}
		if ref.App != "vulos-node-1" {
			t.Fatalf("ref.App = %q, want vulos-node-1", ref.App)
		}
	})

	t.Run("unknown domain errors", func(t *testing.T) {
		svc := newTestService(&fakeDNS{})
		if _, _, err := svc.RouteFor(ctx, "ghost.example"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("pending domain not routable", func(t *testing.T) {
		svc := newTestService(&fakeDNS{})
		if _, err := svc.RequestVerification(ctx, tenant, domain); err != nil {
			t.Fatalf("request: %v", err)
		}
		if _, _, err := svc.RouteFor(ctx, domain); !errors.Is(err, ErrNotFound) {
			t.Fatalf("pending RouteFor err = %v, want ErrNotFound", err)
		}
	})
}

func TestDetach(t *testing.T) {
	ctx := context.Background()
	const tenant, domain = "tenant-a", "mail.acme.example"
	dns := &fakeDNS{txt: map[string][]string{}}
	svc := newTestService(dns)
	attachDomain(t, svc, tenant, domain, dns)

	// Cross-tenant detach rejected.
	if err := svc.Detach(ctx, "tenant-b", domain); !errors.Is(err, ErrTenantMismatch) {
		t.Fatalf("cross-tenant detach err = %v, want ErrTenantMismatch", err)
	}

	if err := svc.Detach(ctx, tenant, domain); err != nil {
		t.Fatalf("detach: %v", err)
	}
	row, _ := svc.Store.Get(ctx, domain)
	if row.VerifyState != StateDetached {
		t.Fatalf("state = %q, want detached", row.VerifyState)
	}
	// Idempotent: second detach is a no-op success.
	if err := svc.Detach(ctx, tenant, domain); err != nil {
		t.Fatalf("idempotent detach: %v", err)
	}
	// Detached domain no longer routable.
	if _, _, err := svc.RouteFor(ctx, domain); !errors.Is(err, ErrNotFound) {
		t.Fatalf("detached RouteFor err = %v, want ErrNotFound", err)
	}
}

func TestList(t *testing.T) {
	ctx := context.Background()
	dns := &fakeDNS{txt: map[string][]string{}}
	svc := newTestService(dns)
	attachDomain(t, svc, "tenant-a", "one.acme.example", dns)
	attachDomain(t, svc, "tenant-a", "two.acme.example", dns)
	attachDomain(t, svc, "tenant-b", "three.beta.example", dns)

	a, err := svc.List(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(a) != 2 {
		t.Fatalf("tenant-a domains = %d, want 2", len(a))
	}
	b, _ := svc.List(ctx, "tenant-b")
	if len(b) != 1 {
		t.Fatalf("tenant-b domains = %d, want 1", len(b))
	}
}

// TestStaticAttacher_WarnsInProd is the WAVE34-4 regression: the static attacher
// reports "pending" but provisions no cert. In prod it must WARN loudly on every
// AttachDomain call (defence in depth behind main.go's prod fatal); in non-prod
// it stays silent.
func TestStaticAttacher_WarnsInProd(t *testing.T) {
	att := NewStaticAttacher()

	t.Run("prod warns", func(t *testing.T) {
		env.Init("prod")
		t.Cleanup(func() { env.Init("prod") })
		var buf bytes.Buffer
		orig := log.Writer()
		log.SetOutput(&buf)
		defer log.SetOutput(orig)

		status, target, err := att.AttachDomain(context.Background(), "tenant-1", "example.test")
		if err != nil || status != "pending" || target != "" {
			t.Fatalf("AttachDomain = (%q,%q,%v), want (pending,\"\",nil)", status, target, err)
		}
		if !strings.Contains(buf.String(), "no-op-in-prod") {
			t.Errorf("expected prod no-op warning, got %q", buf.String())
		}
	})

	t.Run("dev silent", func(t *testing.T) {
		env.Init("dev")
		t.Cleanup(func() { env.Init("prod") })
		var buf bytes.Buffer
		orig := log.Writer()
		log.SetOutput(&buf)
		defer log.SetOutput(orig)

		if _, _, err := att.AttachDomain(context.Background(), "tenant-1", "example.test"); err != nil {
			t.Fatalf("AttachDomain dev: %v", err)
		}
		if strings.Contains(buf.String(), "no-op-in-prod") {
			t.Errorf("did not expect prod warning in dev, got %q", buf.String())
		}
	})
}
