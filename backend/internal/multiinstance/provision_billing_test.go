package multiinstance_test

// Billing-gate tests for compute provisioning (surface 3).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"vulos/backend/internal/cpbilling"
	"vulos/backend/internal/multiinstance"
)

// provisionCPStub serves entitlements + usage and captures usage events.
type provisionCPStub struct {
	srv   *httptest.Server
	mu    sync.Mutex
	usage []cpbilling.UsageEvent
	got   chan struct{}
}

func newProvisionCPStub(ent cpbilling.Entitlement) *provisionCPStub {
	cs := &provisionCPStub{got: make(chan struct{}, 1)}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/entitlements":
			_ = json.NewEncoder(w).Encode(ent)
		case "/api/usage":
			var ev cpbilling.UsageEvent
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &ev)
			cs.mu.Lock()
			cs.usage = append(cs.usage, ev)
			cs.mu.Unlock()
			select {
			case cs.got <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	return cs
}

func (cs *provisionCPStub) client() *cpbilling.Client {
	return cpbilling.New(cpbilling.Config{BaseURL: cs.srv.URL, SharedSecret: "s", CacheTTL: time.Hour})
}

func TestProvision_AllowedWhenEntitled_AndMeters(t *testing.T) {
	reg := openTempRegistry(t)
	cloudResp := map[string]interface{}{
		"request_id":    "req-bill-1",
		"instance_ulid": "01HWZPROV0000000000000099",
		"status":        "provisioning",
	}
	url, cleanup := startProvisionStub(t, cloudResp, nil)
	defer cleanup()
	t.Setenv("VULOS_CLOUD_URL", url)

	// Under-cap: empty registry → 0 boxes < cap 5.
	cp := newProvisionCPStub(cpbilling.Entitlement{Tier: "pro", ComputeEnabled: true, ComputeBoxCap: 5})
	defer cp.srv.Close()

	p := multiinstance.NewProvisioner(reg, "dev").WithBilling(cp.client())
	if _, err := p.Provision(context.Background(), "acct@x.com", "ams", "small"); err != nil {
		t.Fatalf("entitled provision should succeed: %v", err)
	}
	select {
	case <-cp.got:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for compute usage meter")
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.usage) != 1 {
		t.Fatalf("got %d usage events, want 1", len(cp.usage))
	}
	ev := cp.usage[0]
	if ev.Product != cpbilling.ProductCompute || ev.Kind != cpbilling.KindComputeMachine || ev.Count != 1 {
		t.Errorf("compute usage = %+v", ev)
	}
	if ev.AccountID != "acct@x.com" {
		t.Errorf("usage account = %q", ev.AccountID)
	}
}

func TestProvision_RefusedWhenSuspended(t *testing.T) {
	reg := openTempRegistry(t)
	// The cloud provision endpoint must NOT be hit when suspended; point at a
	// server that fails the test if called.
	provURL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("cloud provision endpoint must not be called when suspended")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provURL.Close()
	t.Setenv("VULOS_CLOUD_URL", provURL.URL)

	cp := newProvisionCPStub(cpbilling.Entitlement{Tier: "pro", Suspended: true, ComputeEnabled: true, ComputeBoxCap: 10})
	defer cp.srv.Close()

	p := multiinstance.NewProvisioner(reg, "dev").WithBilling(cp.client())
	_, err := p.Provision(context.Background(), "acct@x.com", "ams", "small")
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("suspended provision must be refused, got err=%v", err)
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.usage) != 0 {
		t.Errorf("suspended provision must not meter, got %d", len(cp.usage))
	}
}

func TestProvision_RefusedWhenComputeDisabled(t *testing.T) {
	reg := openTempRegistry(t)
	notCalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("cloud provision endpoint must not be called when compute disabled")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer notCalled.Close()
	t.Setenv("VULOS_CLOUD_URL", notCalled.URL)

	cp := newProvisionCPStub(cpbilling.Entitlement{Tier: "free", ComputeEnabled: false, ComputeBoxCap: 0})
	defer cp.srv.Close()

	p := multiinstance.NewProvisioner(reg, "dev").WithBilling(cp.client())
	_, err := p.Provision(context.Background(), "acct@x.com", "ams", "small")
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("compute-disabled provision must be refused, got err=%v", err)
	}
}

func TestProvision_RefusedAtBoxCap(t *testing.T) {
	reg := openTempRegistry(t)
	// Pre-populate the registry so activeBoxes == cap.
	// Insert one instance so the count hits the cap of 1.
	inst := multiinstance.Instance{
		ULID:        "01HWZPROV0000000000000CAP",
		DisplayName: "pre-existing",
		Kind:        multiinstance.KindCloud,
		Role:        multiinstance.RolePeer,
		Status:      multiinstance.StatusUnknown,
	}
	if err := reg.Upsert(inst); err != nil {
		t.Fatalf("pre-populate registry: %v", err)
	}

	notCalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("cloud provision endpoint must not be called at box cap")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer notCalled.Close()
	t.Setenv("VULOS_CLOUD_URL", notCalled.URL)

	// cap=1, activeBoxes=1 → refuse.
	cp := newProvisionCPStub(cpbilling.Entitlement{Tier: "starter", ComputeEnabled: true, ComputeBoxCap: 1})
	defer cp.srv.Close()

	p := multiinstance.NewProvisioner(reg, "dev").WithBilling(cp.client())
	_, err := p.Provision(context.Background(), "acct@x.com", "ams", "small")
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("at-box-cap provision must be refused, got err=%v", err)
	}
}

func TestProvision_DisabledBillingUnchanged(t *testing.T) {
	reg := openTempRegistry(t)
	cloudResp := map[string]interface{}{
		"request_id":    "req-nobill",
		"instance_ulid": "01HWZPROV0000000000000077",
		"status":        "provisioning",
	}
	url, cleanup := startProvisionStub(t, cloudResp, nil)
	defer cleanup()
	t.Setenv("VULOS_CLOUD_URL", url)

	// Disabled billing client (no CP_URL) → standalone OS: must succeed.
	p := multiinstance.NewProvisioner(reg, "dev").WithBilling(cpbilling.New(cpbilling.Config{}))
	if _, err := p.Provision(context.Background(), "", "ams", "small"); err != nil {
		t.Fatalf("standalone provision should succeed: %v", err)
	}
}
