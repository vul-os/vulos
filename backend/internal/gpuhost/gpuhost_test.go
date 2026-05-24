// gpuhost_test.go — end-to-end Service tests + status endpoint + Enabled gate.

package gpuhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// readFile reads path and returns its contents as a string.
func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// contains reports whether haystack contains needle.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// stubFabric is an in-memory fabricClient that records calls.
type stubFabric struct {
	registerN  atomic.Int32
	heartbeatN atomic.Int32
	deregN     atomic.Int32
	failOn     atomic.Pointer[string] // "register"|"heartbeat"|"deregister" → fail that call
}

func (s *stubFabric) Register(ctx context.Context, r HostRegistration) error {
	s.registerN.Add(1)
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

func (s *stubFabric) Deregister(ctx context.Context, id string) error {
	s.deregN.Add(1)
	return nil
}

func baseConfig(t *testing.T, bin string, fabric fabricClient) Config {
	t.Helper()
	return Config{
		Identity:           sampleIdentity(),
		RelayBaseURL:       "https://relay.invalid",
		StreamerBinary:     bin,
		AdvertiseHostname:  "1.2.3.4",
		AdvertisePort:      47989,
		Codec:              "h264",
		MaxBitrateKbps:     20000,
		GPUVendor:          "nvidia",
		HasHardwareEncoder: true,
		HeartbeatInterval:  50 * time.Millisecond,
		RegisterTimeout:    1 * time.Second,
		RestartBackoff:     20 * time.Millisecond,
		P2PProbeTimeout:    50 * time.Millisecond,
		ConfigDir:          t.TempDir(),
		fabricOverride:     fabric,
	}
}

func TestService_New_RejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"missing host id", func(c *Config) { c.Identity.HostID = "" }},
		{"missing pubkey", func(c *Config) { c.Identity.PublicKeyB64 = "" }},
		{"missing domain", func(c *Config) { c.Identity.Domain = "" }},
		{"missing binary", func(c *Config) { c.StreamerBinary = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig(t, "/bin/true", &stubFabric{})
			tc.mut(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestService_StartStop_HappyPath(t *testing.T) {
	bin := writeFakeStreamer(t, "sleep")
	stub := &stubFabric{}
	svc, err := New(baseConfig(t, bin, stub))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = svc.Stop(stopCtx)
	})

	if stub.registerN.Load() != 1 {
		t.Errorf("expected 1 register call, got %d", stub.registerN.Load())
	}
	if svc.State() != StateReady {
		t.Errorf("state = %q, want %q", svc.State(), StateReady)
	}
	if !svc.Ready() {
		t.Error("Ready() should be true after successful Start")
	}

	// Heartbeat must fire within ~2 intervals.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if stub.heartbeatN.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stub.heartbeatN.Load() < 1 {
		t.Errorf("expected ≥1 heartbeat, got %d", stub.heartbeatN.Load())
	}
}

func TestService_StartWithFailingFabric_GoesUnregistered(t *testing.T) {
	bin := writeFakeStreamer(t, "sleep")
	stub := &stubFabric{}
	mode := "register"
	stub.failOn.Store(&mode)

	svc, err := New(baseConfig(t, bin, stub))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		// Start tolerates fabric failures — surface them through State, not error.
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = svc.Stop(stopCtx)
	})

	if svc.State() != StateUnregistered {
		t.Errorf("state = %q, want %q", svc.State(), StateUnregistered)
	}
	if svc.Ready() {
		t.Error("Ready() should be false when fabric registration failed")
	}
	if svc.LastError() == nil {
		t.Error("LastError() should be non-nil after register failure")
	}
}

func TestService_Stop_Deregisters(t *testing.T) {
	bin := writeFakeStreamer(t, "sleep")
	stub := &stubFabric{}
	svc, err := New(baseConfig(t, bin, stub))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	stopCtx, sc := context.WithTimeout(context.Background(), 3*time.Second)
	defer sc()
	if err := svc.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if stub.deregN.Load() != 1 {
		t.Errorf("expected 1 deregister call, got %d", stub.deregN.Load())
	}
}

func TestService_StartTwiceIsError(t *testing.T) {
	bin := writeFakeStreamer(t, "sleep")
	svc, err := New(baseConfig(t, bin, &stubFabric{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = svc.Stop(ctx)
	})
	if err := svc.Start(context.Background()); err == nil {
		t.Fatal("second Start should return error")
	}
}

func TestStatusHandler_JSONShape(t *testing.T) {
	bin := writeFakeStreamer(t, "sleep")
	svc, err := New(baseConfig(t, bin, &stubFabric{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = svc.Stop(ctx)
	})

	srv := httptest.NewServer(svc.StatusHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status code = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Ready {
		t.Errorf("Ready=false, want true")
	}
	if body.State != StateReady {
		t.Errorf("State=%q, want %q", body.State, StateReady)
	}
	if body.HostID != sampleIdentity().HostID {
		t.Errorf("HostID=%q", body.HostID)
	}
	if body.Codec != "h264" {
		t.Errorf("Codec=%q", body.Codec)
	}
	if !body.Supervisor.Alive {
		t.Errorf("Supervisor.Alive=false, want true")
	}
}

func TestStatusHandler_WrongMethod(t *testing.T) {
	bin := writeFakeStreamer(t, "sleep")
	svc, err := New(baseConfig(t, bin, &stubFabric{}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(svc.StatusHandler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestEnabled_RespectsEnv(t *testing.T) {
	t.Setenv("VULOS_GPU_HOST", "")
	if Enabled() {
		t.Error("Enabled should be false when VULOS_GPU_HOST is empty")
	}
	for _, v := range []string{"1", "true", "yes", "TRUE"} {
		t.Setenv("VULOS_GPU_HOST", v)
		if !Enabled() {
			t.Errorf("Enabled should be true for VULOS_GPU_HOST=%q", v)
		}
	}
	for _, v := range []string{"0", "false", "no", "off"} {
		t.Setenv("VULOS_GPU_HOST", v)
		if Enabled() {
			t.Errorf("Enabled should be false for VULOS_GPU_HOST=%q", v)
		}
	}
}

func TestConfig_WriteStreamerConfig_Renders(t *testing.T) {
	cfg := baseConfig(t, "/bin/true", &stubFabric{})
	cfg.applyDefaults()
	path, err := cfg.writeStreamerConfig()
	if err != nil {
		t.Fatalf("writeStreamerConfig: %v", err)
	}
	// File must exist and contain the configured host id + codec.
	data, err := readFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !contains(data, sampleIdentity().HostID) {
		t.Errorf("config missing host_id; got:\n%s", data)
	}
	if !contains(data, "codec=h264") {
		t.Errorf("config missing codec; got:\n%s", data)
	}
}
