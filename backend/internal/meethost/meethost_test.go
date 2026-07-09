// meethost_test.go — Service lifecycle + fabric-contract + status + Enabled gate.

package meethost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubFabric is an in-memory fabricClient that records calls.
type stubFabric struct {
	registerN  atomic.Int32
	heartbeatN atomic.Int32
	deregN     atomic.Int32
	failOn     atomic.Pointer[string] // "register"|"heartbeat"|"deregister" → fail that call

	mu      sync.Mutex
	lastReg HostRegistration
}

func (s *stubFabric) Register(ctx context.Context, r HostRegistration) error {
	s.registerN.Add(1)
	s.mu.Lock()
	s.lastReg = r
	s.mu.Unlock()
	if p := s.failOn.Load(); p != nil && *p == "register" {
		return context.DeadlineExceeded
	}
	return nil
}

func (s *stubFabric) Heartbeat(ctx context.Context, r HostRegistration) error {
	s.heartbeatN.Add(1)
	if p := s.failOn.Load(); p != nil && *p == "heartbeat" {
		return context.DeadlineExceeded
	}
	return nil
}

func (s *stubFabric) Deregister(ctx context.Context, r HostRegistration) error {
	s.deregN.Add(1)
	return nil
}

func baseConfig(fabric fabricClient) Config {
	return Config{
		Identity:          FabricIdentity{HostID: "vula:box1", PublicKeyB64: "pk", Domain: "box1.vulos.org"},
		RelayBaseURL:      "https://relay.invalid",
		Token:             "tok",
		Name:              "box1",
		Endpoint:          "https://box1.vulos.org",
		Capabilities:      HostCapabilities{MaxParticipants: 50, Region: "eu"},
		HeartbeatInterval: 30 * time.Millisecond,
		RegisterTimeout:   time.Second,
		fabricOverride:    fabric,
	}
}

func TestEnabledGate(t *testing.T) {
	t.Setenv("VULOS_SFU_HOST", "")
	if Enabled() {
		t.Fatal("meethost must be OFF by default")
	}
	t.Setenv("VULOS_SFU_HOST", "1")
	if !Enabled() {
		t.Fatal("VULOS_SFU_HOST=1 must enable meethost")
	}
}

func TestValidate_RequiresEndpointAndName(t *testing.T) {
	// Missing endpoint.
	c := baseConfig(&stubFabric{})
	c.Endpoint = ""
	if _, err := New(c); err == nil {
		t.Fatal("New must reject a config with no Endpoint")
	}
	// Missing name.
	c = baseConfig(&stubFabric{})
	c.Name = ""
	if _, err := New(c); err == nil {
		t.Fatal("New must reject a config with no Name")
	}
}

func TestStart_InProcess_RegistersAndHeartbeats(t *testing.T) {
	fab := &stubFabric{}
	svc, err := New(baseConfig(fab))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer svc.Stop(context.Background())

	if fab.registerN.Load() != 1 {
		t.Fatalf("expected 1 register on Start, got %d", fab.registerN.Load())
	}
	// In-process SFU ⇒ Ready as soon as registered (no worker to check).
	if !svc.Ready() {
		t.Fatalf("in-process host should be Ready after a successful register; state=%s", svc.State())
	}

	// Heartbeats should tick.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fab.heartbeatN.Load() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if fab.heartbeatN.Load() < 2 {
		t.Fatalf("expected >=2 heartbeats, got %d", fab.heartbeatN.Load())
	}

	// The registration advertises the SFU endpoint + caps, not a streamer.
	fab.mu.Lock()
	reg := fab.lastReg
	fab.mu.Unlock()
	if reg.Endpoint != "https://box1.vulos.org" || reg.Name != "box1" || reg.Capabilities.MaxParticipants != 50 {
		t.Fatalf("registration payload wrong: %+v", reg)
	}
}

func TestStart_RegisterFailure_IsNonFatal(t *testing.T) {
	fab := &stubFabric{}
	fail := "register"
	fab.failOn.Store(&fail)
	svc, err := New(baseConfig(fab))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// A failed initial register must NOT fail Start (the co-located SFU still serves).
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start should be non-fatal on register failure, got %v", err)
	}
	defer svc.Stop(context.Background())
	if svc.State() != StateUnregistered {
		t.Fatalf("state after failed register = %s, want unregistered", svc.State())
	}
	if svc.Ready() {
		t.Fatal("must not be Ready when unregistered")
	}
}

func TestStop_Deregisters(t *testing.T) {
	fab := &stubFabric{}
	svc, _ := New(baseConfig(fab))
	_ = svc.Start(context.Background())
	if err := svc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if fab.deregN.Load() != 1 {
		t.Fatalf("expected 1 deregister on Stop, got %d", fab.deregN.Load())
	}
	if svc.State() != StateStopped {
		t.Fatalf("state after Stop = %s, want stopped", svc.State())
	}
}

// TestFabricContract_RealHTTP proves the HTTPS wire shape against a mock relay
// that mirrors /api/meet/host/register: it checks the bearer token, decodes the
// registration JSON, and asserts the endpoint/name/caps are carried.
func TestFabricContract_RealHTTP(t *testing.T) {
	var gotAuth, gotEndpoint, gotName string
	var gotMax int
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/meet/host/register" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		var reg HostRegistration
		_ = json.NewDecoder(r.Body).Decode(&reg)
		gotEndpoint, gotName, gotMax = reg.Endpoint, reg.Name, reg.Capabilities.MaxParticipants
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()

	client := newHTTPFabricClient(relay.URL, "sekret", relay.Client())
	err := client.Register(context.Background(), HostRegistration{
		HostID: "vula:box1", Name: "box1", Endpoint: "https://box1.example",
		Capabilities: HostCapabilities{MaxParticipants: 50},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if gotAuth != "Bearer sekret" {
		t.Fatalf("bearer not sent: %q", gotAuth)
	}
	if gotEndpoint != "https://box1.example" || gotName != "box1" || gotMax != 50 {
		t.Fatalf("payload wrong: endpoint=%q name=%q max=%d", gotEndpoint, gotName, gotMax)
	}
}

func TestFabricContract_Non2xxIsError(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway) // simulate a verification failure
	}))
	defer relay.Close()
	client := newHTTPFabricClient(relay.URL, "tok", relay.Client())
	if err := client.Register(context.Background(), HostRegistration{HostID: "x", Name: "n", Endpoint: "https://e"}); err == nil {
		t.Fatal("a non-2xx relay response must be an error")
	}
}

func TestStatusHandler(t *testing.T) {
	fab := &stubFabric{}
	svc, _ := New(baseConfig(fab))
	_ = svc.Start(context.Background())
	defer svc.Stop(context.Background())

	rr := httptest.NewRecorder()
	svc.StatusHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/meethost/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d", rr.Code)
	}
	var out statusResponse
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !out.InProcess || out.MaxParticipants != 50 || out.Endpoint != "https://box1.vulos.org" {
		t.Fatalf("status body wrong: %+v", out)
	}
	if out.HasE2EE {
		t.Fatal("self-host host must advertise has_e2ee=false (SFU.md §4 LOCKED)")
	}
}

func TestMain(m *testing.M) {
	// Ensure the env gate does not leak between test binaries.
	os.Unsetenv("VULOS_SFU_HOST")
	os.Exit(m.Run())
}
