package appsplatform

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

// These tests exercise the open-core registry seam: the data plane (handler,
// dispatcher, token auth) depends ONLY on the Registry interface, so a cloud
// control-plane implementation can be substituted for the standalone default
// without touching the core. Here a thin instrumented registry stands in for the
// "cloud" implementation.

// cloudFakeRegistry simulates a Vulos Cloud control-plane Registry. It embeds the
// standalone default for behavior but counts brokered calls, proving the handler
// reaches storage only through the interface.
type cloudFakeRegistry struct {
	*StandaloneRegistry
	creates  atomic.Int64
	tokenHit atomic.Int64
}

func (c *cloudFakeRegistry) Create(p CreateParams) (*Created, error) {
	c.creates.Add(1)
	return c.StandaloneRegistry.Create(p)
}

func (c *cloudFakeRegistry) GetByTokenHash(h string) (*App, error) {
	c.tokenHit.Add(1)
	return c.StandaloneRegistry.GetByTokenHash(h)
}

// Compile-time proof the substitute satisfies the seam.
var _ Registry = (*cloudFakeRegistry)(nil)

func TestHandlerWorksOverSubstituteRegistry(t *testing.T) {
	cloud := &cloudFakeRegistry{StandaloneRegistry: NewMemoryRegistry()}
	h, err := NewHandler(MountConfig{
		Adapter:    &fakeAdapter{},
		Registry:   cloud, // <- the seam: a non-standalone implementation
		Dispatcher: NewDispatcher(cloud, ProductOffice),
		Admin:      headerAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	alice := map[string]string{"X-User": "alice", "Content-Type": "application/json"}

	// Install via the brokered registry.
	w := do(h, "POST", "/api/apps", `{"name":"Echo","scopes":["apps:write"]}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("create over substitute registry: %d %s", w.Code, w.Body)
	}
	if cloud.creates.Load() != 1 {
		t.Fatalf("handler did not route Create through the seam: %d", cloud.creates.Load())
	}

	// Runtime token auth must also flow through the interface.
	var created struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if wt := do(h, "GET", "/api/apps/v1/auth.test", "", bearerH(created.Token)); wt.Code != http.StatusOK {
		t.Fatalf("auth.test over substitute registry: %d", wt.Code)
	}
	if cloud.tokenHit.Load() == 0 {
		t.Fatal("token auth did not route GetByTokenHash through the seam")
	}
}

// TestRegistryFactorySeam documents the composition-root contract: a product's
// main.go selects an implementation behind the RegistryFactory type, and the
// rest of the platform consumes only the interface it returns.
func TestRegistryFactorySeam(t *testing.T) {
	var useCloud bool

	factory := func() (Registry, error) {
		if useCloud {
			return &cloudFakeRegistry{StandaloneRegistry: NewMemoryRegistry()}, nil
		}
		return NewMemoryRegistry(), nil
	}

	var f RegistryFactory = factory
	standalone, err := f()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := standalone.(*StandaloneRegistry); !ok {
		t.Fatalf("default factory should yield the standalone registry, got %T", standalone)
	}

	useCloud = true
	cloud, err := f()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cloud.(*cloudFakeRegistry); !ok {
		t.Fatalf("cloud-selected factory should yield the substitute, got %T", cloud)
	}
}

// TestStandaloneSatisfiesInterface is a compile-time + runtime sanity check that
// the in-repo default is a valid Registry (the seam's reference implementation).
func TestStandaloneSatisfiesInterface(t *testing.T) {
	var r Registry = NewMemoryRegistry()
	if _, err := r.Create(CreateParams{Name: "x", Products: []string{ProductOffice}}); err != nil {
		t.Fatal(err)
	}
}
